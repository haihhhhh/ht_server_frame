// persistence 包顺序执行项目提交的数据库任务，不定义业务模型或数据库操作。
package persistence

// 【通常无需修改｜数据库执行】顺序执行上层提交的保存或查询函数，并反馈执行结果。
// 本包不理解玩家、SQL 或 GORM；模板的 database.Service 负责分队列，组件负责具体写入内容。

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/haihhhhh/ht_server_frame/internal/requestlog"
	"github.com/haihhhhh/ht_server_frame/observability"
)

var (
	ErrClosed = errors.New("persistence: queue closed")
	ErrFull   = errors.New("persistence: queue full")
	ErrPanic  = errors.New("persistence: task callback panic")
)

type Options struct {
	Capacity int
	MaxBytes int64
	Timeout  time.Duration
}

type Stats struct {
	Pending   int
	Bytes     int64
	Completed uint64
	Failed    uint64
	Rejected  uint64
}

type task struct {
	ctx     context.Context
	execute func(context.Context) error
	size    int64
	done    chan error
}

// Queue 按提交顺序执行加载和保存，容量统计包含正在执行的任务。
// 项目必须提交独立数据副本；队列不推断业务幂等性，不自动重试。
type Queue struct {
	mu     sync.Mutex
	opts   Options
	jobs   chan task
	done   chan struct{}
	closed bool
	stats  Stats
}

func NewQueue(opts Options) (*Queue, error) {
	if opts.Capacity < 1 || opts.MaxBytes < 1 || opts.Timeout <= 0 {
		return nil, errors.New("persistence: positive limits and timeout required")
	}
	queue := &Queue{
		opts: opts,
		jobs: make(chan task, opts.Capacity),
		done: make(chan struct{}),
	}
	go queue.run()
	return queue, nil
}

// Submit 返回入队结果及独立的执行结果通道；接收方不读取也不会阻塞保存。
// size 是项目数据副本的字节估计，必须覆盖变长字段，不能仅用指针大小。
// execute 必须遵守 context；超时不会强行终止忽略取消的函数。
func (queue *Queue) Submit(execute func(context.Context) error, size int64) (<-chan error, error) {
	return queue.SubmitContext(context.Background(), execute, size)
}

// SubmitContext 只关联日志标识；任务执行不会继承请求取消或认证信息。
func (queue *Queue) SubmitContext(ctx context.Context, execute func(context.Context) error, size int64) (<-chan error, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if queue.closed {
		queue.stats.Rejected++
		return nil, ErrClosed
	}
	if execute == nil || size <= 0 || size > queue.opts.MaxBytes-queue.stats.Bytes || queue.stats.Pending >= queue.opts.Capacity {
		queue.stats.Rejected++
		return nil, ErrFull
	}
	done := make(chan error, 1)
	queue.stats.Pending++
	queue.stats.Bytes += size
	queue.jobs <- task{
		ctx:     observability.Detached(ctx),
		execute: execute,
		size:    size,
		done:    done,
	}
	return done, nil
}

func (queue *Queue) Stats() Stats {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return queue.stats
}

// BeginClose 立即停止接单，已提交的任务全部结束后关闭返回的通道。可重复调用。
func (queue *Queue) BeginClose() <-chan struct{} {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if !queue.closed {
		queue.closed = true
		close(queue.jobs)
	}
	return queue.done
}

// Close 停止接收并等待已提交任务结束；超时只结束等待，不提前关闭项目数据库。
func (queue *Queue) Close(ctx context.Context) error {
	drained := queue.BeginClose()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (queue *Queue) run() {
	defer close(queue.done)
	for job := range queue.jobs {
		err := queue.write(job)
		queue.mu.Lock()
		queue.stats.Pending--
		queue.stats.Bytes -= job.size
		queue.stats.Completed++
		if err != nil {
			queue.stats.Failed++
		}
		queue.mu.Unlock()
		job.done <- err
		close(job.done)
	}
}

func (queue *Queue) write(job task) (err error) {
	base, finished := observability.Begin(job.ctx, "persistence", "database_task")
	defer func() {
		finished(err)
	}()
	defer func() {
		if recover() != nil {
			requestlog.Panic(base, "persistence")
			err = ErrPanic
		}
	}()
	ctx, cancel := context.WithTimeout(base, queue.opts.Timeout)
	defer cancel()
	return job.execute(ctx)
}
