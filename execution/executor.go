package execution

// execution 包提供进程内有界的按 key 串行执行能力。
// 它不建立分布式所有权，也不持久化排队任务。
// 【通常无需修改｜业务执行】把提交的回调放进有容量限制的队列，按所属范围顺序执行。
// 模板为每个在线玩家创建自己的执行器；具体道具、经验与保存字段都不属于本包。

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrStopped    = errors.New("execution: admission closed")
	ErrOverloaded = errors.New("execution: capacity exceeded")
	ErrReentrant  = errors.New("execution: synchronous reentry")
	ErrFaulted    = errors.New("execution: domain faulted")
	ErrInvalid    = errors.New("execution: invalid argument")
)

// Options 描述一个执行组，内部状态不得逃逸到回调之外。
// PayloadBytes 由调用方提供，必须计入保留的请求数据；
// 执行器无法测量闭包内部引用的内存。
type Options[T any] struct {
	Name           string
	MaxDomains     int
	QueueCapacity  int
	MaxQueueBytes  int64
	InitTimeout    time.Duration
	Init           func(context.Context, string) (T, error)
	UpdateInterval time.Duration
	Update         func(context.Context, T) error
}

type Task[T any] func(context.Context, T) (any, error)
type result struct {
	value any
	err   error
}
type request[T any] struct {
	queuedAt time.Time
	ctx      context.Context
	bytes    int64
	fn       Task[T]
	reply    chan result
}

// Stats 包含整个执行组的有界、低基数统计。
type Stats struct {
	Finished, Executed, Skipped, StopWaitCanceled              uint64
	QueueWaitTotal, QueueWaitMax, ExecutionTotal, ExecutionMax time.Duration
	Stopping, Stopped                                          bool
	Domains, Queued, InFlight                                  int
	QueueBytes                                                 int64
	Accepted, Rejected, Failed, Panics                         uint64
	// Updates 统计已开始的周期回调数。MaxUpdateDelay 包含
	// 等待请求或前一次更新完成的时间；Updates 不是时钟触发次数。
	Updates        uint64
	MaxUpdateDelay time.Duration
}

type Executor[T any] struct {
	finished, executed, skipped, stopTimeouts                  atomic.Uint64
	queueWaitTotal, queueWaitMax, executionTotal, executionMax atomic.Int64
	opts                                                       Options[T]
	mu                                                         sync.Mutex
	domains                                                    map[string]*domain[T]
	stopping                                                   bool
	ctx                                                        context.Context
	cancel                                                     context.CancelFunc
	wg                                                         sync.WaitGroup
	done                                                       chan struct{}
	accepted                                                   atomic.Uint64
	rejected                                                   atomic.Uint64
	failed                                                     atomic.Uint64
	panics                                                     atomic.Uint64
	inflight                                                   atomic.Int64
	updates                                                    atomic.Uint64
	updateDelay                                                atomic.Int64
}

func New[T any](opts Options[T]) (*Executor[T], error) {
	if opts.Name == "" || opts.MaxDomains < 1 || opts.QueueCapacity < 1 ||
		opts.MaxQueueBytes < 1 || opts.InitTimeout <= 0 || opts.Init == nil ||
		(opts.Update == nil) != (opts.UpdateInterval == 0) || opts.UpdateInterval < 0 {
		return nil, ErrInvalid
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Executor[T]{
		opts:    opts,
		domains: make(map[string]*domain[T]),
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
	}, nil
}

// Call 等待操作完成。准入后的超时不能证明
// 操作未执行。返回结果不得与执行域持有的状态共享可变引用。
func (e *Executor[T]) Call(ctx context.Context, key string, payloadBytes int64, fn Task[T]) (any, error) {
	for s, _ := ctx.Value(scopeKey{}).(*scope); s != nil; s = s.parent {
		if s.executor == e && s.key == key {
			e.rejected.Add(1)
			return nil, ErrReentrant
		}
	}
	reply := make(chan result, 1)
	if err := e.enqueue(key, request[T]{
		ctx:   ctx,
		bytes: payloadBytes,
		fn:    fn,
		reply: reply,
	}); err != nil {
		return nil, err
	}
	select {
	case r := <-reply:
		return r.value, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Submit 仅报告准入结果，后续失败会增加 Stats.Failed。
// 其 context 控制操作的生命周期，包括排队等待阶段。
func (executor *Executor[T]) Submit(ctx context.Context, key string, payloadBytes int64, fn Task[T]) error {
	return executor.enqueue(key, request[T]{
		ctx:   ctx,
		bytes: payloadBytes,
		fn:    fn,
	})
}

func (executor *Executor[T]) enqueue(key string, r request[T]) (err error) {
	defer func() {
		if err != nil {
			executor.rejected.Add(1)
		}
	}()
	if key == "" || r.fn == nil || r.bytes < 1 || r.bytes > executor.opts.MaxQueueBytes {
		return ErrInvalid
	}
	if err = r.ctx.Err(); err != nil {
		return err
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.stopping {
		return ErrStopped
	}
	d := executor.domains[key]
	if d == nil {
		if len(executor.domains) >= executor.opts.MaxDomains {
			return ErrOverloaded
		}
		d = &domain[T]{
			key:   key,
			queue: make(chan request[T], executor.opts.QueueCapacity),
		}
		executor.domains[key] = d
		executor.wg.Add(1)
		go executor.run(d)
	}
	if d.err != nil {
		return d.err
	}
	if r.bytes > executor.opts.MaxQueueBytes-d.bytes {
		return ErrOverloaded
	}
	r.queuedAt = time.Now()
	select {
	case d.queue <- r:
		d.bytes += r.bytes
		executor.accepted.Add(1)
		return nil
	default:
		return ErrOverloaded
	}
}

// DomainError 返回导致指定循环无法继续工作的错误，Stop 后仍可读取。
// 尚未启动或运行正常的循环返回 nil。
func (executor *Executor[T]) DomainError(key string) error {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if domain := executor.domains[key]; domain != nil {
		return domain.err
	}
	return nil
}

// Stop 原子地关闭准入并排空已接收任务。超时时
// 发起协作取消；后续 Stop 调用可继续等待任务实际退出。
func (executor *Executor[T]) Stop(ctx context.Context) error {
	executor.BeginStop()
	select {
	case <-executor.done:
		return nil
	default:
	}
	select {
	case <-executor.done:
		return nil
	case <-ctx.Done():
		executor.stopTimeouts.Add(1)
		executor.cancel()
		return ctx.Err()
	}
}

// BeginStop 关闭队列并停止产生周期任务，保留已接纳操作的执行上下文。
func (executor *Executor[T]) BeginStop() {
	executor.mu.Lock()
	if !executor.stopping {
		executor.stopping = true
		for _, d := range executor.domains {
			close(d.queue)
		}
		go func() {
			executor.wg.Wait()
			executor.cancel()
			close(executor.done)
		}()
	}
	executor.mu.Unlock()
}

func (executor *Executor[T]) Stats() Stats {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	s := Stats{
		Domains:          len(executor.domains),
		InFlight:         int(executor.inflight.Load()),
		Finished:         executor.finished.Load(),
		Executed:         executor.executed.Load(),
		Skipped:          executor.skipped.Load(),
		StopWaitCanceled: executor.stopTimeouts.Load(),
		QueueWaitTotal:   time.Duration(executor.queueWaitTotal.Load()),
		QueueWaitMax:     time.Duration(executor.queueWaitMax.Load()),
		ExecutionTotal:   time.Duration(executor.executionTotal.Load()),
		ExecutionMax:     time.Duration(executor.executionMax.Load()),
		Stopping:         executor.stopping,
		Accepted:         executor.accepted.Load(),
		Rejected:         executor.rejected.Load(),
		Failed:           executor.failed.Load(),
		Panics:           executor.panics.Load(),
		Updates:          executor.updates.Load(),
		MaxUpdateDelay:   time.Duration(executor.updateDelay.Load()),
	}
	select {
	case <-executor.done:
		s.Stopped = true
	default:
	}
	for _, d := range executor.domains {
		s.Queued += len(d.queue)
		s.QueueBytes += d.bytes
	}
	return s
}
