package access

import (
	"context"
	"time"
)

type Identity struct {
	ConnectionID int64
	UserID       string
	Generation   uint64
}

type identityKey struct{}
type authenticated struct {
	manager  *Manager
	identity Identity
}

// Require 将当前绑定写入仅在进程内使用的可信上下文；后端不能直接信任网络同名字段。
func (sessionManager *Manager) Require(ctx context.Context, connection int64) (context.Context, error) {
	sessionManager.mu.Lock()
	id, ok := sessionManager.connections[connection]
	sessionManager.mu.Unlock()
	if !ok {
		return ctx, ErrUnauthenticated
	}
	return context.WithValue(ctx, identityKey{}, authenticated{sessionManager, id}), nil
}

func FromContext(ctx context.Context) (Identity, error) {
	if remote, ok := ctx.Value(identityKey{}).(verifiedRemote); ok {
		if ctx.Err() != nil || !time.Now().Before(remote.expires) {
			return Identity{}, ErrUnauthenticated
		}
		return remote.identity, nil
	}
	v, ok := ctx.Value(identityKey{}).(authenticated)
	if !ok {
		return Identity{}, ErrUnauthenticated
	}
	v.manager.mu.Lock()
	current, exists := v.manager.connections[v.identity.ConnectionID]
	v.manager.mu.Unlock()
	if !exists || current != v.identity {
		return Identity{}, ErrUnauthenticated
	}
	return v.identity, nil
}

// CheckContext 供队列在执行前重新校验；未要求认证的公共入口不受影响。
func CheckContext(ctx context.Context) error {
	if ctx.Value(identityKey{}) == nil {
		return nil
	}
	_, err := FromContext(ctx)
	return err
}

func RequireUser(ctx context.Context, claimed string) (string, error) {
	id, err := FromContext(ctx)
	if err != nil {
		return "", err
	}
	if claimed != id.UserID {
		return "", ErrForbidden
	}
	return id.UserID, nil
}

type verifiedRemote struct {
	identity Identity
	expires  time.Time
}

// WithRemoteIdentity 仅供受信服务 RPC 入口使用，不用于客户端自报身份。
// 它检查字段和期限，不验证连接凭据，也不代替项目的有效登录与角色归属检查。
func WithRemoteIdentity(ctx context.Context, id Identity) (context.Context, error) {
	deadline, ok := ctx.Deadline()
	if !ok || ctx.Err() != nil || !time.Now().Before(deadline) || id.UserID == "" || len(id.UserID) > 256 || id.ConnectionID <= 0 || id.Generation == 0 {
		return ctx, ErrUnauthenticated
	}
	return context.WithValue(ctx, identityKey{}, verifiedRemote{id, deadline}), nil
}
