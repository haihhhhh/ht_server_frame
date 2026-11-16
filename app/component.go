package app

import (
	"context"

	"github.com/golang/protobuf/proto"
	"github.com/haihhhhh/ht_server_frame/access"
	"github.com/haihhhhh/ht_server_frame/app/internal/registration"
	"github.com/haihhhhh/ht_server_frame/execution"
	"github.com/haihhhhh/ht_server_frame/host"
	"github.com/topfreegames/pitaya/v2/component"
)

// Register 在启动前登记原生客户端组件，保留方法名大小写，不自动添加执行队列。
// 业务入口使用 Call 接入准入、身份和错误处理；原生注册本身不自动鉴权。
func (application *App) Register(registeredComponent component.Component, opts ...component.Option) error {
	return application.registerComponent(registeredComponent, false, opts)
}

// RegisterRemote 在启动前登记原生 RPC 组件，可与同名客户端组件并存。
func (application *App) RegisterRemote(registeredComponent component.Component, opts ...component.Option) error {
	return application.registerComponent(registeredComponent, true, opts)
}

func (application *App) registerComponent(registeredComponent component.Component, remote bool, opts []component.Option) error {
	application.mu.Lock()
	defer application.mu.Unlock()
	if application.started {
		return host.ErrStarted
	}
	backend := application.cluster != nil && !application.cluster.Frontend
	return application.registry.AddComponent(registeredComponent, remote, backend, opts)
}

// RegisterPool 注册 name.handle；校验和业务均在有界执行通道内运行。
func RegisterPool[Q proto.Message, R proto.Message](application *App, name string, pool *Pool, fn func(context.Context, Q) (R, error)) (*Endpoint[Q, R], error) {
	if pool == nil || pool.app != application || fn == nil {
		return nil, execution.ErrInvalid
	}
	return register[Q, R](application, name, func(ctx context.Context, q Q) (any, error) {
		return pool.executor.Call(ctx, payloadSize(q), func(ctx context.Context) (any, error) {
			if err := access.CheckContext(ctx); err != nil {
				return nil, err
			}
			return fn(ctx, q)
		})
	})
}

// RegisterKeyed 的 key 函数须校验身份及访问权限；不能直接信任客户端声明的身份。
// 业务响应必须是独立快照，不得返回执行域私有状态的可变别名。
func RegisterKeyed[Q proto.Message, R proto.Message, S any](application *App, name string, g *Group[S], key func(context.Context, Q) (string, error), fn func(context.Context, S, Q) (R, error)) (*Endpoint[Q, R], error) {
	if g == nil || g.app != application || key == nil || fn == nil {
		return nil, execution.ErrInvalid
	}
	return register[Q, R](application, name, func(ctx context.Context, q Q) (any, error) {
		k, err := key(ctx, q)
		if err != nil {
			return nil, err
		}
		return g.executor.Call(ctx, k, payloadSize(q), func(ctx context.Context, s S) (any, error) {
			if err := access.CheckContext(ctx); err != nil {
				return nil, err
			}
			return fn(ctx, s, q)
		})
	})
}

func payloadSize(q proto.Message) int64 {
	return int64(max(1, proto.Size(q)))
}

func register[Q proto.Message, R proto.Message](application *App, name string, run func(context.Context, Q) (any, error)) (*Endpoint[Q, R], error) {
	application.mu.Lock()
	defer application.mu.Unlock()
	if application.started {
		return nil, host.ErrStarted
	}
	endpoint := &Endpoint[Q, R]{
		app:   application,
		run:   run,
		route: application.cfg.ServerType + "." + name + "." + registration.EndpointRouteMethod,
	}
	backend := application.cluster != nil && !application.cluster.Frontend
	if err := registration.AddEndpoint[Q, R](&application.registry, name, endpoint, backend); err != nil {
		return nil, err
	}
	return endpoint, nil
}
