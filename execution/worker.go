package execution

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/haihhhhh/ht_server_frame/internal/requestlog"
)

type domain[T any] struct {
	key   string
	queue chan request[T]
	bytes int64 // 由 Executor.mu 保护
	err   error // 由 Executor.mu 保护
}
type scopeKey struct{}
type scope struct {
	executor any
	key      string
	parent   *scope
}

func (e *Executor[T]) run(d *domain[T]) {
	defer e.wg.Done()
	domainCtx := context.WithValue(e.ctx, scopeKey{}, &scope{
		executor: e,
		key:      d.key,
	})
	initCtx, cancel := context.WithTimeout(domainCtx, e.opts.InitTimeout)
	var state T
	_, initErr := e.guarded(initCtx, func() (any, error) {
		var err error
		state, err = e.opts.Init(initCtx, d.key)
		return nil, err
	})
	if initErr == nil {
		initErr = initCtx.Err()
	}
	cancel()
	if initErr != nil {
		e.failed.Add(1)
		e.failDomain(d, fmt.Errorf("execution: initialization failed: %w", initErr))
	}
	var ticks <-chan time.Time
	if e.opts.Update != nil && initErr == nil {
		ticker := time.NewTicker(e.opts.UpdateInterval)
		defer ticker.Stop()
		ticks = ticker.C // 定时器合并时钟事件，不为每次触发创建 goroutine
	}
	for {
		select {
		case r, ok := <-d.queue:
			if !ok {
				return
			}
			e.mu.Lock()
			d.bytes -= r.bytes
			domainErr := d.err
			e.mu.Unlock()
			observeDuration(&e.queueWaitTotal, &e.queueWaitMax, time.Since(r.queuedAt))
			rctx, rcancel := context.WithCancel(r.ctx)
			unlink := context.AfterFunc(e.ctx, rcancel)
			if e.ctx.Err() != nil {
				rcancel()
			}
			parent, _ := r.ctx.Value(scopeKey{}).(*scope)
			rctx = context.WithValue(rctx, scopeKey{}, &scope{e, d.key, parent})
			out := result{err: domainErr}
			if out.err == nil {
				out.err = rctx.Err()
			}
			if out.err == nil {
				e.executed.Add(1)
				started := time.Now()
				e.inflight.Add(1)
				out.value, out.err = e.guarded(rctx, func() (any, error) {
					return r.fn(rctx, state)
				})
				observeDuration(&e.executionTotal, &e.executionMax, time.Since(started))
				e.inflight.Add(-1)
				if errors.Is(out.err, ErrFaulted) {
					e.failDomain(d, ErrFaulted)
				}
			} else {
				e.skipped.Add(1)
			}
			unlink()
			rcancel()
			if out.err != nil {
				e.failed.Add(1)
			}
			if r.reply != nil {
				e.finished.Add(1)
				r.reply <- out
			} else {
				e.finished.Add(1)
			}
		case scheduled := <-ticks:
			e.mu.Lock()
			enabled := !e.stopping && d.err == nil
			e.mu.Unlock()
			if !enabled {
				ticks = nil
				continue
			}
			e.updates.Add(1)
			delay := int64(time.Since(scheduled))
			for old := e.updateDelay.Load(); delay > old; old = e.updateDelay.Load() {
				if e.updateDelay.CompareAndSwap(old, delay) {
					break
				}
			}
			e.inflight.Add(1)
			_, err := e.guarded(domainCtx, func() (any, error) {
				return nil, e.opts.Update(domainCtx, state)
			})
			e.inflight.Add(-1)
			if err != nil {
				e.failed.Add(1)
			}
			if errors.Is(err, ErrFaulted) {
				e.failDomain(d, ErrFaulted)
			}
		}
	}
}

func (executor *Executor[T]) guarded(ctx context.Context, fn func() (any, error)) (value any, err error) {
	defer func() {
		if recover() != nil {
			requestlog.Panic(ctx, "execution")
			executor.panics.Add(1)
			err = ErrFaulted // 不包含可能泄露敏感信息的 panic 数据
		}
	}()
	return fn()
}

func (executor *Executor[T]) failDomain(d *domain[T], err error) {
	executor.mu.Lock()
	d.err = err
	executor.mu.Unlock()
}

// 耗时累计达到上限后保持最大值，避免长时间运行后溢出回绕。
func observeDuration(total, maximum *atomic.Int64, duration time.Duration) {
	n := int64(duration)
	if n < 0 {
		return
	}
	const maxInt64 = int64(1<<63 - 1)
	for old := total.Load(); ; old = total.Load() {
		next := maxInt64
		if n <= maxInt64-old {
			next = old + n
		}
		if total.CompareAndSwap(old, next) {
			break
		}
	}
	for old := maximum.Load(); n > old; old = maximum.Load() {
		if maximum.CompareAndSwap(old, n) {
			break
		}
	}
}
