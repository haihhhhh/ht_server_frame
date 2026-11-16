// host 包提供进程内模块生命周期与请求准入管理。
// 它不注册信号处理器，也不终止调用方进程。
package host

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/haihhhhh/ht_server_frame/internal/requestlog"
)

var (
	ErrNotReady   = errors.New("host: not ready")
	ErrOverloaded = errors.New("host: capacity exceeded")
	ErrStarted    = errors.New("host: already started or stopped")
	ErrPanic      = errors.New("host: callback panic")
)

// Module.Init 返回错误时，必须自行清理部分初始化产生的资源。
// Close 仅在所属操作实际停止后才能成功返回。发生错误
// 或 panic 时保留依赖，因为尚不能确认释放依赖是安全的。
type Module interface {
	Init(context.Context) error
	Close() error
}
type Entry struct {
	Name      string
	DependsOn []string
	Module    Module
}
type Options struct {
	MaxInFlight int
	// MaxReservedOutbound 为后台出站调用预留名额；零表示不启用。
	MaxReservedOutbound int
	Modules             []Entry
}
type Phase string

const (
	New      Phase = "new"
	Starting Phase = "starting"
	Ready    Phase = "ready"
	Stopping Phase = "stopping"
	Stopped  Phase = "stopped"
	Faulted  Phase = "faulted"
)

type Status struct {
	Phase    Phase
	InFlight int
	// PendingModules 包含关闭失败的模块及其保留的依赖。
	PendingModules int
}
type Host struct {
	mu               sync.Mutex
	phase            Phase
	requests         admissionPool
	reservedOutbound admissionPool
	pending          int
	order            []Entry
	initialized      []Entry // 仅由生命周期管理方访问
	ctx              context.Context
	cancel           context.CancelFunc
	stop             chan struct{}
	done             chan struct{}
	wg               sync.WaitGroup
	err              error
}

// Create 校验依赖关系，生成稳定的依赖优先顺序。
func Create(opts Options) (*Host, error) {
	if opts.MaxInFlight < 1 {
		return nil, fmt.Errorf("host: positive MaxInFlight required")
	}
	if opts.MaxReservedOutbound < 0 {
		return nil, fmt.Errorf("host: MaxReservedOutbound must be nonnegative")
	}
	index := make(map[string]Entry, len(opts.Modules))
	for _, entry := range opts.Modules {
		if entry.Name == "" || entry.Module == nil {
			return nil, fmt.Errorf("host: invalid module")
		}
		if _, ok := index[entry.Name]; ok {
			return nil, fmt.Errorf("host: duplicate module %s", entry.Name)
		}
		entry.DependsOn = append([]string(nil), entry.DependsOn...)
		index[entry.Name] = entry
	}
	marks := make(map[string]uint8, len(index))
	order := make([]Entry, 0, len(index))
	var visit func(string) error
	visit = func(name string) error {
		if marks[name] == 2 {
			return nil
		}
		if marks[name] == 1 {
			return fmt.Errorf("host: dependency cycle at %s", name)
		}
		entry, ok := index[name]
		if !ok {
			return fmt.Errorf("host: missing dependency %s", name)
		}
		marks[name] = 1
		for _, dependency := range entry.DependsOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		marks[name] = 2
		order = append(order, entry)
		return nil
	}
	for _, entry := range opts.Modules {
		if err := visit(entry.Name); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Host{
		phase:            New,
		requests:         admissionPool{limit: opts.MaxInFlight},
		reservedOutbound: admissionPool{limit: opts.MaxReservedOutbound},
		order:            order,
		ctx:              ctx,
		cancel:           cancel,
		stop:             make(chan struct{}),
		done:             make(chan struct{}),
	}, nil
}

// Start 同步执行；忽略取消的钩子可能延迟其返回；
// 并发调用 Stop 时，仍遵守该调用方的等待期限。
func (lifecycleHost *Host) Start(ctx context.Context) error {
	lifecycleHost.mu.Lock()
	if lifecycleHost.phase != New {
		lifecycleHost.mu.Unlock()
		return ErrStarted
	}
	lifecycleHost.phase = Starting
	lifecycleHost.mu.Unlock()
	initCtx, cancel := context.WithCancel(ctx)
	unlink := context.AfterFunc(lifecycleHost.ctx, cancel)
	defer func() {
		unlink()
		cancel()
	}()
	var startErr error
	for _, entry := range lifecycleHost.order {
		lifecycleHost.mu.Lock()
		stopping := lifecycleHost.phase == Stopping
		lifecycleHost.mu.Unlock()
		if stopping {
			startErr = ErrNotReady
			break
		}
		if startErr = initCtx.Err(); startErr != nil {
			break
		}
		if err := invoke(initCtx, func() error {
			return entry.Module.Init(initCtx)
		}); err != nil {
			startErr = fmt.Errorf("host: init %s: %w", entry.Name, err)
			break
		}
		lifecycleHost.initialized = append(lifecycleHost.initialized, entry)
		lifecycleHost.mu.Lock()
		lifecycleHost.pending = len(lifecycleHost.initialized)
		lifecycleHost.mu.Unlock()
	}
	lifecycleHost.mu.Lock()
	if startErr == nil {
		startErr = initCtx.Err()
	}
	if startErr == nil && lifecycleHost.phase == Stopping {
		startErr = ErrNotReady
	}
	if startErr == nil {
		lifecycleHost.phase = Ready
		lifecycleHost.mu.Unlock()
		go func() {
			<-lifecycleHost.stop
			lifecycleHost.finish(nil)
		}()
		return nil
	}
	lifecycleHost.requestStopLocked()
	lifecycleHost.mu.Unlock()
	lifecycleHost.finish(startErr)
	lifecycleHost.mu.Lock()
	defer lifecycleHost.mu.Unlock()
	return lifecycleHost.err
}

func (lifecycleHost *Host) requestStopLocked() {
	if lifecycleHost.phase != Stopping && lifecycleHost.phase != Stopped && lifecycleHost.phase != Faulted {
		lifecycleHost.phase = Stopping
		close(lifecycleHost.stop)
	}
}

// BeginStop 关闭准入并启动排空，不等待，也不取消在途操作。
func (lifecycleHost *Host) BeginStop() {
	lifecycleHost.mu.Lock()
	if lifecycleHost.phase == New {
		lifecycleHost.requestStopLocked()
		lifecycleHost.phase = Stopped
		lifecycleHost.cancel()
		close(lifecycleHost.done)
	} else {
		lifecycleHost.requestStopLocked()
	}
	lifecycleHost.mu.Unlock()
}
func (lifecycleHost *Host) Stop(ctx context.Context) error {
	lifecycleHost.BeginStop()
	select {
	case <-lifecycleHost.done:
		return lifecycleHost.result()
	default:
	}
	select {
	case <-lifecycleHost.done:
		return lifecycleHost.result()
	case <-ctx.Done():
		lifecycleHost.cancel() // 协作取消不代表任务已经退出
		return ctx.Err()
	}
}
func (lifecycleHost *Host) finish(initial error) {
	lifecycleHost.wg.Wait()
	err := initial
	failed := false
	for i := len(lifecycleHost.initialized) - 1; i >= 0; i-- {
		entry := lifecycleHost.initialized[i]
		if closeErr := invoke(context.Background(), entry.Module.Close); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("host: close %s: %w", entry.Name, closeErr))
			failed = true
			break // 尚不能确认模块已停止，因此不释放其依赖
		}
		lifecycleHost.mu.Lock()
		lifecycleHost.pending--
		lifecycleHost.mu.Unlock()
	}
	lifecycleHost.cancel()
	lifecycleHost.mu.Lock()
	lifecycleHost.err = err
	if failed {
		lifecycleHost.phase = Faulted
	} else {
		lifecycleHost.phase = Stopped
	}
	close(lifecycleHost.done)
	lifecycleHost.mu.Unlock()
}
func (lifecycleHost *Host) result() error {
	lifecycleHost.mu.Lock()
	defer lifecycleHost.mu.Unlock()
	return lifecycleHost.err
}
func (lifecycleHost *Host) Status() Status {
	lifecycleHost.mu.Lock()
	defer lifecycleHost.mu.Unlock()
	return Status{
		Phase:          lifecycleHost.phase,
		InFlight:       lifecycleHost.requests.active + lifecycleHost.reservedOutbound.active,
		PendingModules: lifecycleHost.pending,
	}
}
func invoke(ctx context.Context, fn func() error) (err error) {
	defer func() {
		if recover() != nil {
			requestlog.Panic(ctx, "host")
			err = ErrPanic
		}
	}()
	return fn()
}
