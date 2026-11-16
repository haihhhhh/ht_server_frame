package app

// app 包装配官方 Pitaya 与框架自管模块。每进程只允许启动一次网络实例。
import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/haihhhhh/ht_server_frame/access"
	"github.com/haihhhhh/ht_server_frame/app/internal/registration"
	"github.com/haihhhhh/ht_server_frame/host"
	"github.com/haihhhhh/ht_server_frame/internal/discovery"
	"github.com/haihhhhh/ht_server_frame/protocol/pitayawire"
	pitaya "github.com/topfreegames/pitaya/v2"
	"github.com/topfreegames/pitaya/v2/acceptor"
	"github.com/topfreegames/pitaya/v2/session"
)

// 宿主用这些名字登记模块、检查依赖，并预留 ht. 命名空间。
const (
	reservedModulePrefix  = "ht."
	transportModuleName   = "ht.transport"
	readyModuleName       = "ht.ready"
	executionModulePrefix = "ht.execution."
)

type App struct {
	mu                sync.Mutex
	cfg               Config
	modules           []host.Entry
	workers           []host.Entry
	registry          registration.Registry
	protocol          *pitayawire.Bindings
	lifecycleHost     atomic.Pointer[host.Host]
	native            pitaya.Pitaya
	rpcConnection     interface{ IsConnected() bool }
	discovery         *discovery.Health
	ws                acceptor.Acceptor
	ready             chan struct{}
	done              chan struct{}
	started           bool
	cluster           *LocalClusterConfig
	sessions          *access.Manager
	disconnectWorkers int
	sessionPool       session.SessionPool
}

// NewStandalone 只装配本地模式；不隐式启动发现、RPC 或管理端口。
func NewStandalone(cfg Config, modules ...host.Entry) (*App, error) {
	return newApp(cfg, nil, modules...)
}

// 单机和集群前端监听客户端连接，集群后端只接受内部调用。
func newApp(cfg Config, cluster *LocalClusterConfig, modules ...host.Entry) (*App, error) {
	if err := cfg.prepare(cluster == nil || cluster.Frontend); err != nil {
		return nil, err
	}
	for _, m := range modules {
		if strings.HasPrefix(m.Name, reservedModulePrefix) {
			return nil, errors.New("app: reserved module name")
		}
	}
	if _, err := host.Create(host.Options{
		MaxInFlight: cfg.MaxInFlight,
		Modules:     modules,
	}); err != nil {
		return nil, err
	}
	return &App{
		cfg:     cfg,
		cluster: cluster,
		modules: append([]host.Entry(nil), modules...),
		ready:   make(chan struct{}),
		done:    make(chan struct{}),
	}, nil
}

func validName(s string) bool {
	return registration.ValidName(s)
}

// Start 等待自管模块及 Pitaya 初始化。忽略取消的初始化钩子仍可能延迟返回。
// 官方 Fatal 路径可能直接退出进程；不支持在此进程重试启动。
func (application *App) Start(ctx context.Context) error {
	application.mu.Lock()
	if application.started {
		application.mu.Unlock()
		return host.ErrStarted
	}
	if err := application.registry.ValidateProtocol(application.protocol, application.cfg.ServerType); err != nil {
		application.mu.Unlock()
		return err
	}
	application.started = true
	deps := make([]string, 0, len(application.modules))
	for _, m := range application.modules {
		deps = append(deps, m.Name)
	}
	entries := append([]host.Entry(nil), application.modules...)
	entries = append(entries, host.Entry{
		Name:      transportModuleName,
		DependsOn: deps,
		Module:    &pitayaTransport{application: application},
	})
	entries = append(entries, application.workers...)
	h, err := host.Create(host.Options{
		MaxInFlight:         application.cfg.MaxInFlight,
		MaxReservedOutbound: application.disconnectWorkers,
		Modules:             entries,
	})
	if err != nil {
		application.mu.Unlock()
		return err
	}
	application.lifecycleHost.Store(h)
	application.mu.Unlock()
	return h.Start(ctx)
}

func (application *App) loadHost() *host.Host {
	if h := application.lifecycleHost.Load(); h != nil {
		return h
	}
	// Start 可能正在装配宿主。等它释放装配锁再判断是否已发布。
	application.mu.Lock()
	defer application.mu.Unlock()
	return application.lifecycleHost.Load()
}

// Stop 的超时只停止等待并协作取消；后台排空仍继续，不提前释放依赖。
// 不得从本实例的业务回调内同步调用 Stop。
func (application *App) Stop(ctx context.Context) error {
	application.mu.Lock()
	h := application.lifecycleHost.Load()
	if h == nil {
		application.started = true
	}
	application.mu.Unlock()
	if h == nil {
		return nil
	}
	h.BeginStop()
	for _, entry := range application.workers {
		entry.Module.(*workerModule).BeginStop()
	}
	return h.Stop(ctx)
}

func (application *App) Status() host.Status {
	application.mu.Lock()
	h := application.lifecycleHost.Load()
	started := application.started
	application.mu.Unlock()
	if h == nil {
		if started {
			return host.Status{Phase: host.Stopped}
		}
		return host.Status{Phase: host.New}
	}
	return h.Status()
}

// Address 只在 Start 成功后读取，支持配置端口 0 的本地测试。
func (application *App) Address() string {
	select {
	case <-application.ready:
		if application.ws == nil {
			return ""
		}
		return application.ws.GetAddr()
	default:
		return ""
	}
}

// Done 表示 Pitaya.Start 已返回，不等于操作系统进程已经退出。
func (application *App) Done() <-chan struct{} {
	return application.done
}

// Native 在启动就绪后提供原生 API。调用者自行承担绕过框架检查的权限和容量责任。
// 不得改变全局字典或重复启动/停止；组件仍应在 Start 前通过 Register 注册。
func (application *App) Native() pitaya.Pitaya {
	if application.Status().Phase != host.Ready {
		return nil
	}
	return application.native
}
