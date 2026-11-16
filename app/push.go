package app

import (
	"context"
	"reflect"
	"strings"

	"github.com/golang/protobuf/proto"
	"github.com/haihhhhh/ht_server_frame/access"
	"github.com/haihhhhh/ht_server_frame/app/internal/sessionbinding"
	"github.com/haihhhhh/ht_server_frame/execution"
	"github.com/haihhhhh/ht_server_frame/host"
	"github.com/haihhhhh/ht_server_frame/protocol"
)

// Push 向当前已认证请求的本地连接推送；仅适用于单机或网关处理器。
// 不按用户 ID 重新找连接，避免旧请求通知重连后的新会话。调用期间消息不得并发修改。
// 返回成功仅表示进入发送流程；断线不补发，后端按账号推送使用 PushToUsers。
func (application *App) Push(ctx context.Context, route string, msg proto.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if application.Status().Phase != host.Ready {
		return host.ErrNotReady
	}
	if application.sessions == nil || application.native == nil {
		return access.ErrUnauthenticated
	}
	id, err := access.FromContext(ctx)
	if err != nil {
		return err
	}
	s := application.native.GetSessionFromCtx(ctx)
	if s == nil || !s.GetIsFrontend() || s.ID() != id.ConnectionID || application.sessionPool.GetSessionByID(s.ID()) == nil {
		return access.ErrUnauthenticated
	}
	if msg == nil || (reflect.ValueOf(msg).Kind() == reflect.Ptr && reflect.ValueOf(msg).IsNil()) {
		return execution.ErrInvalid
	}
	if application.protocol != nil {
		if _, ok := application.protocol.Catalog.MessageID(msg); !ok {
			return execution.ErrInvalid
		}
		route = proto.MessageName(msg)
	} else {
		parts := strings.Split(route, ".")
		if len(route) > protocol.MaxRouteBytes || len(parts) != 2 || !validName(parts[0]) || !validName(parts[1]) {
			return execution.ErrInvalid
		}
	}
	// 未压缩的推送帧包含包头 4 字节、消息标记与路由长度 2 字节。
	if int64(proto.Size(msg))+int64(len(route))+6 > application.cfg.MaxPendingBytes {
		return execution.ErrInvalid
	}
	data, err := proto.Marshal(msg)
	if err != nil {
		return execution.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := access.CheckContext(ctx); err != nil {
		return err
	}
	return s.Push(route, data)
}

// PushMessage 按消息类型确定唯一编号，业务不填写推送路由。
func (application *App) PushMessage(ctx context.Context, msg proto.Message) error {
	if application.protocol == nil {
		return execution.ErrInvalid
	}
	return application.Push(ctx, "", msg)
}

// PushToConnection 供受信服务入口向指定本地连接发送，不按账号改投新连接。
// 调用者先核对项目绑定；这里再检查本地身份代次，复用原生 Session 和数字协议出口。
func (application *App) PushToConnection(ctx context.Context, id access.Identity, msg proto.Message) error {
	local, err := sessionbinding.ContextForPush(ctx, application.sessions, application.sessionPool, id)
	if err != nil {
		return err
	}
	return application.PushMessage(local, msg)
}

// PushToUsers 供可信服务代码按账号向网关推送，也可向有限用户列表广播。
// 它面向用户当前连接，区别于 PushMessage 的当前请求连接；调用者负责收件人权限。
// 原生 NATS 推送成功不代表客户端在线或已收到，失败名单也不能用于自动重试。
func (application *App) PushToUsers(ctx context.Context, frontendType string, users []string, value proto.Message) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, application.cfg.RequestTimeout)
	defer cancel()
	lifecycleHost := application.loadHost()
	if lifecycleHost == nil || lifecycleHost.Status().Phase != host.Ready {
		return users, host.ErrNotReady
	}
	if application.protocol == nil || !validName(frontendType) || len(users) == 0 || len(users) > 128 || nilMessage(value) {
		return users, execution.ErrInvalid
	}
	if _, ok := application.protocol.Catalog.MessageID(value); !ok {
		return users, execution.ErrInvalid
	}
	if int64(proto.Size(value))+int64(len(proto.MessageName(value)))+6 > application.cfg.MaxPendingBytes {
		return users, execution.ErrInvalid
	}
	seen := make(map[string]bool, len(users))
	for _, uid := range users {
		if uid == "" || len(uid) > 256 || seen[uid] {
			return users, execution.ErrInvalid
		}
		seen[uid] = true
	}
	body, err := proto.Marshal(value)
	if err != nil {
		return users, execution.ErrInvalid
	}
	var failed []string
	completed := 0
	err = lifecycleHost.Do(ctx, func(ctx context.Context) error {
		for _, uid := range users {
			if err := ctx.Err(); err != nil {
				return err
			}
			miss, err := application.native.SendPushToUsers(proto.MessageName(value), body, []string{uid}, frontendType)
			failed = append(failed, miss...)
			if err != nil && len(miss) == 0 {
				failed = append(failed, uid)
			}
			completed++
		}
		return nil
	})
	if err != nil {
		// 保留原生接口报告的失败，再加上尚未取得发送结果的收件人。
		return append(failed, users[completed:]...), err
	}
	if len(failed) > 0 {
		return failed, execution.ErrFaulted
	}
	return nil, nil
}
