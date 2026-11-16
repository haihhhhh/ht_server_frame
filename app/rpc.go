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
	"github.com/haihhhhh/ht_server_frame/internal/discovery"
	"github.com/haihhhhh/ht_server_frame/internal/rpccontext"
	"github.com/haihhhhh/ht_server_frame/observability"
)

type admitRPC func(*host.Host, context.Context, func(context.Context) error) error

type rpcDiscoveryMode uint8

const (
	probeDiscovery rpcDiscoveryMode = iota
	useBoundServer
)

// RPC 执行一次无状态远端调用。请求内的顺序调用复用本进程的入口名额。
// shortcut: 官方 NATS Request 不响应提前取消，最多等剩余预算；跨主机版本需补充时钟偏差策略。
func (application *App) RPC(ctx context.Context, route string, reply, request proto.Message) error {
	return application.rpc(ctx, "", route, reply, request, (*host.Host).DoOutbound, probeDiscovery)
}

// RPCTo 只向指定节点调用一次；目标消失或失败时不改派到其他节点。
// serverID 必须由服务端的归属或配置决定，不直接接受客户端指定。
func (application *App) RPCTo(ctx context.Context, serverID, route string, reply, request proto.Message) error {
	return application.rpcTo(ctx, serverID, route, reply, request, (*host.Host).DoOutbound, probeDiscovery)
}

// RPCToBoundServer 供已进入角色的普通请求调用。调用方必须先确认当前连接已绑定这个节点。
// 只查 Pitaya 本地节点表，不主动探测 etcd；仍检查 NATS 连接，目标消失后不改派。
func (application *App) RPCToBoundServer(ctx context.Context, serverID, route string, reply, request proto.Message) error {
	return application.rpcTo(ctx, serverID, route, reply, request, (*host.Host).DoOutbound, useBoundServer)
}

// RPCSessionClosed 由可信网关通知指定后端，登录换绑时沿用当前请求的名额。
// 接收端必须用 RequireSessionClosed；此通知不能调用普通受保护业务接口。
// 通知失败不自动重试，也不能据此删除归属或断言后端已释放状态。
func (application *App) RPCSessionClosed(ctx context.Context, id access.Identity, target, route string, reply, request proto.Message) error {
	return application.rpcSessionClosed(ctx, id, target, route, reply, request, (*host.Host).DoOutbound)
}

// RPCQueuedSessionClosed 供断线队列使用，只占用为这些队列预留的发送名额。
func (application *App) RPCQueuedSessionClosed(ctx context.Context, id access.Identity, target, route string, reply, request proto.Message) error {
	return application.rpcSessionClosed(ctx, id, target, route, reply, request, (*host.Host).DoReservedOutbound)
}

func (application *App) rpcSessionClosed(ctx context.Context, id access.Identity, target, route string, reply, request proto.Message, admit admitRPC) error {
	if application.sessions == nil || application.cluster == nil || !application.cluster.Frontend {
		return access.ErrForbidden
	}
	return application.rpcTo(rpccontext.WithClosedSession(ctx, id), target, route, reply, request, admit, probeDiscovery)
}

func (application *App) rpcTo(ctx context.Context, serverID, route string, reply, request proto.Message, admit admitRPC, discoveryMode rpcDiscoveryMode) error {
	if serverID == "" || len(serverID) > 256 || strings.TrimSpace(serverID) != serverID {
		return execution.ErrInvalid
	}
	return application.rpc(ctx, serverID, route, reply, request, admit, discoveryMode)
}

func (application *App) rpc(ctx context.Context, target, route string, reply, request proto.Message, admit admitRPC, discoveryMode rpcDiscoveryMode) (failure error) {
	ctx, finished := observability.Begin(ctx, application.cfg.ServerType, "rpc:"+route)
	defer func() {
		finished(failure)
	}()
	parts := strings.Split(route, ".")
	if application.cluster == nil || len(parts) != 3 || !validName(parts[0]) || !validName(parts[1]) || !validName(parts[2]) || (target == "" && parts[0] == application.cfg.ServerType) || nilMessage(reply) || nilMessage(request) {
		return execution.ErrInvalid
	}
	h := application.loadHost()
	if h == nil {
		return host.ErrNotReady
	}
	ctx, cancel := context.WithTimeout(ctx, application.cfg.RequestTimeout)
	defer cancel()
	sent := false
	err := admit(h, ctx, func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		// 仅拒绝已观察到的断连；检查后断连仍按发送后结果未知处理。
		if application.rpcConnection == nil || !application.rpcConnection.IsConnected() {
			return errors.Join(errorcode.ErrRPCNotSent, execution.ErrFaulted)
		}
		if discoveryMode != useBoundServer && (application.discovery == nil || !application.discovery.Check(ctx)) {
			return errors.Join(errorcode.ErrRPCNotSent, execution.ErrFaulted, ctx.Err())
		}
		if target != "" && !discovery.HasServer(application.native, parts[0], target) {
			return errors.Join(errorcode.ErrRPCNotSent, execution.ErrFaulted)
		}
		ctx, err := rpccontext.Outgoing(ctx)
		if err != nil {
			return err
		}
		sent = true
		if target != "" {
			err = application.native.RPCTo(ctx, target, route, reply, request)
		} else {
			err = application.native.RPC(ctx, route, reply, request)
		}
		if err == nil {
			return ctx.Err()
		}
		return errorcode.FromRPC(err)
	})
	if !sent && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return errors.Join(errorcode.ErrRPCNotSent, err)
	}
	if sent && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return errors.Join(errorcode.ErrRPCOutcomeUnknown, err)
	}
	return err
}

func nilMessage(m proto.Message) bool {
	return m == nil || reflect.ValueOf(m).Kind() != reflect.Ptr || reflect.ValueOf(m).IsNil()
}
