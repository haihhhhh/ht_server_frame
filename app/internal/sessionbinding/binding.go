// Package sessionbinding 将框架校验过的身份与本网关的 Pitaya 连接对应起来。
package sessionbinding

import (
	"context"

	"github.com/haihhhhh/ht_server_frame/access"
	"github.com/topfreegames/pitaya/v2/constants"
	"github.com/topfreegames/pitaya/v2/session"
)

// Login 验证凭据并绑定当前连接。慢认证期间连接可能已关闭，绑定前后都要查原生连接池。
func Login(ctx context.Context, manager *access.Manager, pool session.SessionPool, client session.Session, credential string) (access.Identity, error) {
	if client == nil || !client.GetIsFrontend() {
		return access.Identity{}, access.ErrUnauthenticated
	}
	id, err := manager.AuthenticateAndBind(ctx, client.ID(), credential, func(uid string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if pool.GetSessionByID(client.ID()) == nil {
			return access.ErrUnauthenticated
		}
		// 同一连接重复登录同一账号时，原生 Bind 只能调用一次。
		if client.UID() == uid {
			return nil
		}
		if client.UID() != "" {
			return access.ErrForbidden
		}
		if err := client.Bind(ctx, uid); err != nil {
			client.Close()
			return err
		}
		return nil
	})
	if err != nil {
		return access.Identity{}, err
	}
	if pool.GetSessionByID(client.ID()) == nil {
		manager.Unbind(id)
		// 连接若在 Bind 中途关闭，还需清理随后发布的原生 UID 索引。
		client.Close()
		return access.Identity{}, access.ErrUnauthenticated
	}
	return id, nil
}

// CloseReplaced 先撤销指定代次的绑定，再踢掉旧连接。关闭回调因此不会触发真正下线。
func CloseReplaced(ctx context.Context, manager *access.Manager, pool session.SessionPool, id access.Identity) error {
	if manager == nil || pool == nil {
		return access.ErrUnauthenticated
	}
	if !manager.Retire(id) {
		return nil
	}
	if client := pool.GetSessionByID(id.ConnectionID); client != nil {
		if err := client.Kick(ctx); err != nil {
			client.Close()
			return err
		}
	}
	return nil
}

// ContextForPush 核对绑定代次和原生连接的账号，让推送沿这条连接发送。
func ContextForPush(ctx context.Context, manager *access.Manager, pool session.SessionPool, id access.Identity) (context.Context, error) {
	if manager == nil || pool == nil {
		return ctx, access.ErrUnauthenticated
	}
	local, err := manager.Require(ctx, id.ConnectionID)
	if err != nil {
		return ctx, err
	}
	current, err := access.FromContext(local)
	if err != nil || current != id {
		return ctx, access.ErrUnauthenticated
	}
	client := pool.GetSessionByID(id.ConnectionID)
	if client == nil || client.UID() != id.UserID {
		return ctx, access.ErrUnauthenticated
	}
	return context.WithValue(local, constants.SessionCtxKey, client), nil
}
