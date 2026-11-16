package app

import (
	"context"
	"errors"
	"strings"

	"github.com/haihhhhh/ht_server_frame/execution"
	"github.com/haihhhhh/ht_server_frame/host"
)

type stopper interface {
	BeginStop()
	Stop(context.Context) error
	Stats() execution.Stats
}
type workerModule struct{ stopper }

func (*workerModule) Init(context.Context) error {
	return nil
}
func (m *workerModule) Close() error {
	return m.Stop(context.Background())
}

// Group 保留执行器的状态类型，不向业务暴露绕过入口准入的提交方法。
type Group[S any] struct {
	app      *App
	executor *execution.Executor[S]
}
type Pool struct {
	app      *App
	executor *execution.Pool
}

func (application *App) addWorker(name string, w stopper) error {
	application.mu.Lock()
	defer application.mu.Unlock()
	if application.started {
		return host.ErrStarted
	}
	if !validName(name) {
		return execution.ErrInvalid
	}
	name = executionModulePrefix + name
	for _, m := range application.workers {
		if m.Name == name {
			return errors.New("app: duplicate execution group")
		}
	}
	application.workers = append(application.workers, host.Entry{
		Name:      name,
		DependsOn: []string{transportModuleName},
		Module:    &workerModule{w},
	})
	return nil
}

// ExecutionStats 返回已注册执行组的独立统计快照，不包含用户标识或请求内容。
// 各组分别采样，不保证所有计数来自同一时刻；不会触发网络探测。
func (application *App) ExecutionStats() map[string]execution.Stats {
	application.mu.Lock()
	// 注册只会追加新组，已登记的条目可以在锁外逐个采样。
	workers := application.workers
	application.mu.Unlock()
	result := make(map[string]execution.Stats, len(workers))
	for _, entry := range workers {
		result[strings.TrimPrefix(entry.Name, executionModulePrefix)] = entry.Module.(*workerModule).Stats()
	}
	return result
}

func NewGroup[S any](application *App, opts execution.Options[S]) (*Group[S], error) {
	executor, err := execution.New(opts)
	if err != nil {
		return nil, err
	}
	if err = application.addWorker(opts.Name, executor); err != nil {
		_ = executor.Stop(context.Background())
		return nil, err
	}
	return &Group[S]{
		app:      application,
		executor: executor,
	}, nil
}

func NewPool(application *App, name string, workers, queueCapacity int, maxQueueBytes int64) (*Pool, error) {
	executor, err := execution.NewPool(name, workers, queueCapacity, maxQueueBytes)
	if err != nil {
		return nil, err
	}
	if err = application.addWorker(name, executor); err != nil {
		_ = executor.Stop(context.Background())
		return nil, err
	}
	return &Pool{
		app:      application,
		executor: executor,
	}, nil
}
