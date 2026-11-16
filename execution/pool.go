package execution

import (
	"context"
	"strconv"
	"sync/atomic"
	"time"
)

// Pool 通过固定数量、轮询分配的工作通道提供有界并发。
// QueueCapacity 和 MaxQueueBytes 分别限制每个通道。繁忙通道可能拒绝任务，
// 即使其他通道空闲；此调度方式不提供负载均衡。
type Pool struct {
	executor *Executor[struct{}]
	workers  uint64
	next     atomic.Uint64
}

func NewPool(name string, workers, queueCapacity int, maxQueueBytes int64) (*Pool, error) {
	if workers < 1 {
		return nil, ErrInvalid
	}
	e, err := New(Options[struct{}]{
		Name:          name,
		MaxDomains:    workers,
		QueueCapacity: queueCapacity,
		MaxQueueBytes: maxQueueBytes,
		InitTimeout:   time.Second,
		Init: func(context.Context, string) (struct{}, error) {
			return struct{}{}, nil
		},
	})
	if err != nil {
		return nil, err
	}
	return &Pool{
		executor: e,
		workers:  uint64(workers),
	}, nil
}

func (pool *Pool) Call(ctx context.Context, payloadBytes int64, fn func(context.Context) (any, error)) (any, error) {
	if fn == nil {
		return nil, ErrInvalid
	}
	key := strconv.FormatUint((pool.next.Add(1)-1)%pool.workers, 10)
	return pool.executor.Call(ctx, key, payloadBytes, func(ctx context.Context, _ struct{}) (any, error) {
		return fn(ctx)
	})
}

func (pool *Pool) Stop(ctx context.Context) error {
	return pool.executor.Stop(ctx)
}
func (pool *Pool) BeginStop() {
	pool.executor.BeginStop()
}
func (pool *Pool) Stats() Stats {
	return pool.executor.Stats()
}
