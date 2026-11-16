// observability 包按显式调用输出低基数的运行时采样。
// 它不启动监听器、后台任务或遥测服务。
package observability

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/haihhhhh/ht_server_frame/execution"
	"github.com/haihhhhh/ht_server_frame/host"
)

// Source 的方法必须并发安全且及时返回。内置的
// execution.Executor 和 execution.Pool 实现了此接口。
type Source interface{ Stats() execution.Stats }
type Group struct {
	Name   string
	Source Source
}

// Reporter 使用固定注册表，最多 64 个组，每个名称最多 64 个 ASCII 字节。
// 仅注册静态服务名或组名，不使用用户标识、key 或请求标识。
type Reporter struct {
	mu      sync.Mutex
	handler slog.Handler
	host    *host.Host
	groups  []Group
}

func New(handler slog.Handler, h *host.Host, groups []Group) (*Reporter, error) {
	if handler == nil || h == nil || len(groups) > 64 {
		return nil, fmt.Errorf("observability: invalid configuration")
	}
	seen := make(map[string]bool, len(groups))
	for _, g := range groups {
		if !validName(g.Name) || g.Source == nil || seen[g.Name] {
			return nil, fmt.Errorf("observability: invalid group registry")
		}
		seen[g.Name] = true
	}
	return &Reporter{
		handler: handler,
		host:    h,
		groups:  append([]Group(nil), groups...),
	}, nil
}

func validName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

// Sample 输出聚合计数，不输出载荷、key、panic 值或原始
// 错误。应在自行管理的诊断循环中调用，不在请求回调中调用。
// 不设队列或重试；输出错误返回调用方。阻塞的输出端可能
// 阻塞采样调用方，但不会阻塞独立的任务执行主路径。
// 拒绝并发采样，避免在慢输出端后方积压。
func (r *Reporter) Sample(ctx context.Context) error {
	if !r.mu.TryLock() {
		return fmt.Errorf("observability: sample already in progress")
	}
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	s := r.host.Status()
	if err := r.write(ctx, "host_status", slog.String("phase", string(s.Phase)), slog.Int("in_flight", s.InFlight), slog.Int("pending_modules", s.PendingModules)); err != nil {
		return err
	}
	for _, g := range r.groups {
		if err := ctx.Err(); err != nil {
			return err
		}
		s := g.Source.Stats()
		if err := r.write(ctx, "execution_stats",
			slog.String("group", g.Name), slog.Int("domains", s.Domains), slog.Int("queued", s.Queued), slog.Int64("queue_bytes", s.QueueBytes), slog.Int("in_flight", s.InFlight),
			slog.Uint64("accepted", s.Accepted), slog.Uint64("rejected", s.Rejected), slog.Uint64("finished", s.Finished), slog.Uint64("executed", s.Executed), slog.Uint64("skipped", s.Skipped),
			slog.Uint64("failed", s.Failed), slog.Uint64("panics", s.Panics), slog.Uint64("updates", s.Updates), slog.Int64("update_delay_max_ns", int64(s.MaxUpdateDelay)),
			slog.Int64("queue_wait_total_ns", int64(s.QueueWaitTotal)), slog.Int64("queue_wait_max_ns", int64(s.QueueWaitMax)),
			slog.Int64("execution_total_ns", int64(s.ExecutionTotal)), slog.Int64("execution_max_ns", int64(s.ExecutionMax)),
			slog.Uint64("stop_wait_canceled", s.StopWaitCanceled), slog.Bool("stopping", s.Stopping), slog.Bool("stopped", s.Stopped)); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reporter) write(ctx context.Context, event string, attrs ...slog.Attr) error {
	if !r.handler.Enabled(ctx, slog.LevelInfo) {
		return nil
	}
	record := slog.NewRecord(time.Now(), slog.LevelInfo, event, 0)
	record.AddAttrs(attrs...)
	return r.handler.Handle(ctx, record)
}
