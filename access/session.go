// access 包管理进程内认证绑定，不提供账户数据库或跨网关权威会话。
package access

import (
	"context"
	"errors"
	"sync"
)

var (
	ErrUnauthenticated = errors.New("access: unauthenticated")
	ErrForbidden       = errors.New("access: forbidden")
	ErrDuplicate       = errors.New("access: duplicate login")
	ErrCapacity        = errors.New("access: capacity exceeded")
)

type Policy string

// ScopeLocal 表示仅校验本网关的连接绑定。
const ScopeLocal = "local"

const (
	RejectNew  Policy = "reject-new"
	ReplaceOld Policy = "replace-old"
)

type Options struct {
	Scope              string // 当前仅支持显式 local；global 缺少权威适配，必须拒绝。
	Policy             Policy
	MaxSessions        int
	MaxCredentialBytes int
	Verify             func(context.Context, string) (string, error)
	// PublicError 仅将项目明确允许公开的认证错误转换为安全错误；nil 表示统一拒绝。
	PublicError func(error) error
	// 回调在身份锁内按顺序通知，只允许短内存操作或非阻塞提交。
	// 不得等待数据库、网络或重新调用 Manager 方法。
	// BeforeBind 在验证通过、替换旧身份前预留项目资源。返回错误保留旧绑定；
	// 成功后随即发布新身份，底层绑定若失败按正常断线回收资源。
	BeforeBind     func(Identity) error
	OnBound        func(Identity)
	OnDisconnected func(Identity)
	// DisconnectWorkers 是发送下线通知的后台队列数，每条队列最多同时发一次 RPC。零表示没有后台发送名额。
	DisconnectWorkers int
}

type Manager struct {
	bindMu      sync.Mutex
	mu          sync.Mutex
	opts        Options
	next        uint64
	connections map[int64]Identity
	users       map[string]Identity
}

func New(opts Options) (*Manager, error) {
	if opts.Scope != ScopeLocal || (opts.Policy != RejectNew && opts.Policy != ReplaceOld) || opts.MaxSessions < 1 || opts.MaxCredentialBytes < 1 || opts.Verify == nil {
		return nil, errors.New("access: explicit local scope, supported policy, positive limits and verifier required")
	}
	return &Manager{
		opts:        opts,
		connections: make(map[int64]Identity),
		users:       make(map[string]Identity),
	}, nil
}

// Authenticate 仅从验证器取得身份，不接受客户端自报 UID。认证凭据不存入绑定。
func (sessionManager *Manager) Authenticate(ctx context.Context, connection int64, credential string) (Identity, error) {
	return sessionManager.AuthenticateAndBind(ctx, connection, credential, nil)
}

// AuthenticateAndBind 在凭据验证后顺序完成身份发布和底层连接绑定。
// bind 不得再次发起认证；底层关闭旧连接可以正常调用 Disconnect。
func (sessionManager *Manager) AuthenticateAndBind(ctx context.Context, connection int64, credential string, bind func(string) error) (Identity, error) {
	if connection <= 0 || len(credential) == 0 || len(credential) > sessionManager.opts.MaxCredentialBytes {
		return Identity{}, ErrUnauthenticated
	}
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	uid, err := sessionManager.opts.Verify(ctx, credential)
	if ctx.Err() != nil {
		return Identity{}, ctx.Err()
	}
	if err != nil {
		if sessionManager.opts.PublicError != nil {
			if public := sessionManager.opts.PublicError(err); public != nil {
				return Identity{}, public
			}
		}
		return Identity{}, ErrUnauthenticated
	}
	if uid == "" || len(uid) > 256 {
		return Identity{}, ErrUnauthenticated
	}
	// 慢凭据验证不占绑定锁；只串行化发布身份和底层替换连接。
	sessionManager.bindMu.Lock()
	defer sessionManager.bindMu.Unlock()
	id, err := sessionManager.bind(ctx, connection, uid)
	if err != nil {
		return Identity{}, err
	}
	if bind != nil {
		if err = bind(uid); err != nil {
			sessionManager.Unbind(id)
			return Identity{}, err
		}
	}
	return id, nil
}

func (sessionManager *Manager) bind(ctx context.Context, connection int64, uid string) (Identity, error) {
	sessionManager.mu.Lock()
	defer sessionManager.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	// 同连接不能换用户；重新认证同一身份也不能制造新的代次。
	if old, ok := sessionManager.connections[connection]; ok {
		if old.UserID != uid {
			return Identity{}, ErrForbidden
		}
		return old, nil
	}
	old, exists := sessionManager.users[uid]
	if exists && sessionManager.opts.Policy == RejectNew {
		return Identity{}, ErrDuplicate
	}
	if !exists && len(sessionManager.connections) >= sessionManager.opts.MaxSessions {
		return Identity{}, ErrCapacity
	}
	if sessionManager.next == ^uint64(0) {
		return Identity{}, ErrCapacity
	}
	id := Identity{
		ConnectionID: connection,
		UserID:       uid,
		Generation:   sessionManager.next + 1,
	}
	if sessionManager.opts.BeforeBind != nil {
		if err := sessionManager.opts.BeforeBind(id); err != nil {
			return Identity{}, err
		}
	}
	if exists {
		delete(sessionManager.connections, old.ConnectionID)
	}
	sessionManager.next = id.Generation
	sessionManager.connections[connection] = id
	sessionManager.users[uid] = id
	if sessionManager.opts.OnBound != nil {
		sessionManager.opts.OnBound(id)
	}
	return id, nil
}

// Unbind 只移除同一代次，迟到的断连或清理不能影响后来的登录。
func (sessionManager *Manager) Unbind(id Identity) {
	sessionManager.mu.Lock()
	defer sessionManager.mu.Unlock()
	sessionManager.unbindLocked(id)
}

// Retire 仅撤下匹配的旧连接，不通知业务下线；用于已确认的新连接替换。
func (sessionManager *Manager) Retire(id Identity) bool {
	sessionManager.mu.Lock()
	defer sessionManager.mu.Unlock()
	if sessionManager.connections[id.ConnectionID] != id || id.UserID == "" {
		return false
	}
	delete(sessionManager.connections, id.ConnectionID)
	if sessionManager.users[id.UserID] == id {
		delete(sessionManager.users, id.UserID)
	}
	return true
}
func (sessionManager *Manager) unbindLocked(id Identity) {
	if current, ok := sessionManager.connections[id.ConnectionID]; !ok || current != id {
		return
	}
	delete(sessionManager.connections, id.ConnectionID)
	if sessionManager.users[id.UserID] == id {
		delete(sessionManager.users, id.UserID)
		if sessionManager.opts.OnDisconnected != nil {
			sessionManager.opts.OnDisconnected(id)
		}
	}
}
func (sessionManager *Manager) Disconnect(connection int64) {
	sessionManager.mu.Lock()
	defer sessionManager.mu.Unlock()
	if id, ok := sessionManager.connections[connection]; ok {
		sessionManager.unbindLocked(id)
	}
}
