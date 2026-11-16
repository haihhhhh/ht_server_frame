package rpccontext

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/haihhhhh/ht_server_frame/access"
	"github.com/haihhhhh/ht_server_frame/execution"
	"github.com/topfreegames/pitaya/v2/constants"
	pcontext "github.com/topfreegames/pitaya/v2/context"
)

const (
	identityKey = "ht.business_identity"
	deadlineKey = "ht.deadline"
	// 旧版网关的签名字段仅用于出站清理，不能随下一跳转发。
	legacySignedUserKey = "ht.signed_user"
)

type closedSessionKey struct{}

type rpcIdentity struct {
	Identity access.Identity
	Closed   bool
}

// WithClosedSession 让下一跳携带已断开连接的身份，供后端核对下线通知。
func WithClosedSession(ctx context.Context, id access.Identity) context.Context {
	return context.WithValue(ctx, closedSessionKey{}, id)
}

// Outgoing 复制 Pitaya 传播字段，重新写入本地核对的身份和本次调用剩余时间。
func Outgoing(ctx context.Context) (context.Context, error) {
	deadline, _ := ctx.Deadline()
	values := make(map[string]interface{})
	for k, v := range pcontext.ToMap(ctx) {
		values[k] = v
	}
	// 旧签名和上一跳的身份都不能直接转发。
	delete(values, legacySignedUserKey)
	delete(values, identityKey)
	identity, err := outgoingIdentity(ctx)
	if err != nil {
		return ctx, err
	}
	if identity != "" {
		values[identityKey] = identity
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return ctx, context.DeadlineExceeded
	}
	values[constants.RequestTimeout] = remaining.String()
	values[deadlineKey] = strconv.FormatInt(deadline.UnixNano(), 10)
	return context.WithValue(ctx, constants.PropagateCtxKey, values), nil
}

// 未登录调用不携带玩家身份；已有本地身份失效则拒绝，不能降级成匿名调用。
func outgoingIdentity(ctx context.Context) (string, error) {
	id, closed := ctx.Value(closedSessionKey{}).(access.Identity)
	if !closed {
		var err error
		id, err = access.FromContext(ctx)
		if err != nil {
			// 区分未登录与已失效身份；后者不能改成匿名请求。
			return "", access.CheckContext(ctx)
		}
	}
	if _, err := access.WithRemoteIdentity(ctx, id); err != nil {
		return "", err
	}
	raw, err := json.Marshal(rpcIdentity{
		Identity: id,
		Closed:   closed,
	})
	return string(raw), err
}

// WithIncomingDeadline 在接收端沿用发送方的绝对截止时间。
func WithIncomingDeadline(ctx context.Context) (context.Context, context.CancelFunc, error) {
	raw, ok := pcontext.GetFromPropagateCtx(ctx, deadlineKey).(string)
	if !ok {
		return ctx, func() {}, execution.ErrInvalid
	}
	nanos, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return ctx, func() {}, execution.ErrInvalid
	}
	ctx, cancel := context.WithDeadline(ctx, time.Unix(0, nanos))
	return ctx, cancel, ctx.Err()
}

// WithIncomingIdentity 读取受信服务传来的玩家身份，并核对当前接口是否用于断线通知。
func WithIncomingIdentity(ctx context.Context, closed bool) (context.Context, error) {
	raw, _ := pcontext.GetFromPropagateCtx(ctx, identityKey).(string)
	var value rpcIdentity
	if len(raw) == 0 || len(raw) > 2048 || json.Unmarshal([]byte(raw), &value) != nil {
		return ctx, access.ErrUnauthenticated
	}
	if value.Closed != closed {
		return ctx, access.ErrForbidden
	}
	return access.WithRemoteIdentity(ctx, value.Identity)
}
