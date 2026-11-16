package discovery

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// shortcut: 健康结果最多缓存 500ms；需要更快故障感知时接入经过验证的发现事件。
const checkInterval = 500 * time.Millisecond

type Health struct {
	probe     func(context.Context) bool
	mu        sync.Mutex
	checking  chan struct{}
	until     time.Time
	available bool
}

func NewHealth(probe func(context.Context) bool) *Health {
	return &Health{probe: probe}
}

// Check 合并并发探测并短期缓存，避免请求量直接转化为 etcd 查询量。
func (d *Health) Check(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	d.mu.Lock()
	if time.Now().Before(d.until) {
		ok := d.available
		d.mu.Unlock()
		return ok
	}
	if pending := d.checking; pending != nil {
		d.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-pending:
			return d.Check(ctx)
		}
	}
	d.checking = make(chan struct{})
	d.mu.Unlock()
	probeCtx, cancel := context.WithTimeout(ctx, checkInterval)
	ok := d.probe(probeCtx)
	cancel()
	d.mu.Lock()
	changed := !d.until.IsZero() && d.available != ok && ctx.Err() == nil
	d.available = ok
	d.until = time.Now().Add(checkInterval)
	// 单个调用方的取消不能让其他调用方缓存一次依赖故障。
	if ctx.Err() != nil {
		d.until = time.Time{}
		ok = false
	}
	close(d.checking)
	d.checking = nil
	d.mu.Unlock()
	if changed {
		slog.Warn("服务发现可用性变化", "available", ok)
	}
	return ok
}

// Snapshot 只读最近的探测结果和有效期，不发起新探测。
func (d *Health) Snapshot() (available bool, checkedUntil time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.available && time.Now().Before(d.until), d.until
}
