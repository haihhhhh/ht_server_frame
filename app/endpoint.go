package app

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/golang/protobuf/proto"
	"github.com/haihhhhh/ht_server_frame/access"
	"github.com/haihhhhh/ht_server_frame/errorcode"
	"github.com/haihhhhh/ht_server_frame/execution"
	"github.com/haihhhhh/ht_server_frame/host"
	"github.com/haihhhhh/ht_server_frame/internal/rpccontext"
	"github.com/haihhhhh/ht_server_frame/observability"
	"github.com/topfreegames/pitaya/v2/component"
)

// Endpoint 的 Handle 同时符合官方 Handler 与 Remote 签名；两者共用准入与错误映射。
// 前端注册 Handler，后端注册 Remote。
type Endpoint[Q proto.Message, R proto.Message] struct {
	component.Base
	app           *App
	run           func(context.Context, Q) (any, error)
	protected     bool
	closedSession bool
	remote        bool
	route         string
}

// Call 让原生方法复用框架准入、超时、可选认证及错误脱敏，不入队、不另开 goroutine。
// route 为实际注册的组件名.方法名；protected 为真时，后端读取受信 RPC 的业务身份并校验用途。
// 同一状态的互斥仍由项目决定，fn 返回的响应必须与可变状态分离。
func Call[Q proto.Message, R proto.Message](ctx context.Context, application *App, route string, protected bool, req Q, fn func(context.Context, Q) (R, error)) (R, error) {
	var zero R
	parts := strings.Split(route, ".")
	if application == nil || fn == nil || len(parts) != 2 || !validName(parts[0]) || !validName(parts[1]) {
		return zero, errorcode.Public(execution.ErrInvalid)
	}
	if reflect.TypeOf((*Q)(nil)).Elem().Kind() != reflect.Ptr || reflect.TypeOf((*R)(nil)).Elem().Kind() != reflect.Ptr {
		return zero, errorcode.Public(execution.ErrInvalid)
	}
	endpoint := Endpoint[Q, R]{
		app:       application,
		protected: protected,
		route:     application.cfg.ServerType + "." + route,
		run: func(ctx context.Context, q Q) (any, error) {
			return fn(ctx, q)
		},
	}
	return endpoint.Handle(ctx, req)
}

// CallRemote 为原生 Remote 方法读取受信服务传递的业务身份，网关上的内部 RPC 也可使用。
// closed 仅用于断线通知；普通在线身份不能调用此类入口。
func CallRemote[Q proto.Message, R proto.Message](ctx context.Context, application *App, route string, closed bool, req Q, fn func(context.Context, Q) (R, error)) (R, error) {
	var zero R
	parts := strings.Split(route, ".")
	if application == nil || application.cluster == nil || fn == nil || len(parts) != 2 || !validName(parts[0]) || !validName(parts[1]) {
		return zero, errorcode.Public(execution.ErrInvalid)
	}
	if reflect.TypeOf((*Q)(nil)).Elem().Kind() != reflect.Ptr || reflect.TypeOf((*R)(nil)).Elem().Kind() != reflect.Ptr {
		return zero, errorcode.Public(execution.ErrInvalid)
	}
	endpoint := Endpoint[Q, R]{
		app:           application,
		protected:     true,
		remote:        true,
		closedSession: closed,
		route:         application.cfg.ServerType + "." + route,
		run: func(ctx context.Context, q Q) (any, error) {
			return fn(ctx, q)
		},
	}
	return endpoint.Handle(ctx, req)
}

// RequireSession 必须在 Start 前调用；未启用认证时配置失败。
func (endpoint *Endpoint[Q, R]) RequireSession() error {
	endpoint.app.mu.Lock()
	defer endpoint.app.mu.Unlock()
	if endpoint.app.started {
		return host.ErrStarted
	}
	if endpoint.app.sessions == nil && (endpoint.app.cluster == nil || endpoint.app.cluster.Frontend) {
		return errors.New("app: authentication not configured")
	}
	endpoint.protected = true
	endpoint.closedSession = false
	return nil
}

// RequireSessionClosed 将后端接口限制为受信服务发送的断线通知，不能用于普通玩家操作。
func (endpoint *Endpoint[Q, R]) RequireSessionClosed() error {
	endpoint.app.mu.Lock()
	defer endpoint.app.mu.Unlock()
	if endpoint.app.started {
		return host.ErrStarted
	}
	if endpoint.app.cluster == nil || endpoint.app.cluster.Frontend {
		return errors.New("app: session-close receiver requires an authenticated backend")
	}
	endpoint.protected, endpoint.closedSession = true, true
	return nil
}

func (endpoint *Endpoint[Q, R]) Handle(ctx context.Context, req Q) (response R, failure error) {
	ctx, finished := observability.Begin(ctx, endpoint.app.cfg.ServerType, endpoint.route)
	defer func() {
		finished(failure)
	}()
	var zero R
	if endpoint.remote || endpoint.app.cluster != nil && !endpoint.app.cluster.Frontend {
		remote, cancel, err := rpccontext.WithIncomingDeadline(ctx)
		defer cancel()
		if err != nil {
			return zero, errorcode.Public(err)
		}
		ctx = remote
	}
	lifecycleHost := endpoint.app.loadHost()
	if lifecycleHost == nil {
		return zero, errorcode.Public(host.ErrNotReady)
	}
	ctx, cancel := context.WithTimeout(ctx, endpoint.app.cfg.RequestTimeout)
	defer cancel()
	result, err := host.Call(ctx, lifecycleHost, func(ctx context.Context) (R, error) {
		if reflect.ValueOf(req).IsNil() {
			return zero, execution.ErrInvalid
		}
		if endpoint.protected {
			var identityErr error
			if endpoint.remote || endpoint.app.cluster != nil && !endpoint.app.cluster.Frontend {
				if endpoint.app.cluster == nil {
					return zero, access.ErrUnauthenticated
				}
				ctx, identityErr = rpccontext.WithIncomingIdentity(ctx, endpoint.closedSession)
			} else {
				ctx, identityErr = endpoint.app.SessionContext(ctx)
			}
			if identityErr != nil {
				return zero, identityErr
			}
		}
		v, err := endpoint.run(ctx, req)
		if err != nil {
			return zero, err
		}
		value, ok := v.(R)
		if !ok || reflect.ValueOf(value).IsNil() {
			return zero, host.ErrPanic
		}
		return value, nil
	})
	if err != nil {
		return zero, errorcode.Public(err)
	}
	return result, nil
}
