package app

import (
	"context"
	"errors"

	"github.com/haihhhhh/ht_server_frame/access"
	"github.com/haihhhhh/ht_server_frame/app/internal/sessionbinding"
	"github.com/haihhhhh/ht_server_frame/host"
	"github.com/topfreegames/pitaya/v2/session"
)

// ConfigureAuthentication 只能在启动前配置一次，不默认选择重复登录策略。
func (application *App) ConfigureAuthentication(opts access.Options) error {
	application.mu.Lock()
	defer application.mu.Unlock()
	if application.started {
		return host.ErrStarted
	}
	if application.sessions != nil {
		return errors.New("app: authentication already configured")
	}
	if opts.DisconnectWorkers < 0 || (opts.DisconnectWorkers > 0 && opts.OnDisconnected == nil) {
		return errors.New("app: invalid disconnect workers")
	}
	if application.cluster != nil && !application.cluster.Frontend {
		return errors.New("app: local authentication is only supported on frontends")
	}
	m, err := access.New(opts)
	if err != nil {
		return err
	}
	application.sessions = m
	application.disconnectWorkers = opts.DisconnectWorkers
	return nil
}

// Login 由已注册的登录处理器调用，复用该请求的有界执行与超时。
// 复用原生 Bind 关闭同一网关上的旧连接；业务状态由项目单独管理。
func (application *App) Login(ctx context.Context, credential string) (access.Identity, error) {
	if application.Status().Phase != host.Ready {
		return access.Identity{}, host.ErrNotReady
	}
	if application.sessions == nil || application.native == nil {
		return access.Identity{}, access.ErrUnauthenticated
	}
	client := application.native.GetSessionFromCtx(ctx)
	return sessionbinding.Login(ctx, application.sessions, application.sessionPool, client, credential)
}

// SessionContext 供登录成功后的项目逻辑取得当前连接的可信身份，不能自行构造身份。
func (application *App) SessionContext(ctx context.Context) (context.Context, error) {
	if application.sessions == nil || application.native == nil {
		return ctx, access.ErrUnauthenticated
	}
	s := application.native.GetSessionFromCtx(ctx)
	if s == nil || !s.GetIsFrontend() || application.sessionPool.GetSessionByID(s.ID()) == nil {
		return ctx, access.ErrUnauthenticated
	}
	return application.sessions.Require(ctx, s.ID())
}

// CloseClientConnection 关闭当前请求的原生客户端连接，正常触发已绑定身份的下线通知。
// 用于登录后续接线失败，不可用 CloseReplacedConnection 跳过必要的业务清理。
func (application *App) CloseClientConnection(ctx context.Context) error {
	if application.native == nil {
		return access.ErrUnauthenticated
	}
	s := application.native.GetSessionFromCtx(ctx)
	if s == nil || !s.GetIsFrontend() {
		return access.ErrUnauthenticated
	}
	s.Close()
	return nil
}

// CloseReplacedConnection 只关闭指定代次的旧连接，不触发玩家下线。
// 调用者必须先确认后端已接受新绑定；不能直接接受客户端指定的身份。
func (application *App) CloseReplacedConnection(ctx context.Context, id access.Identity) error {
	return sessionbinding.CloseReplaced(ctx, application.sessions, application.sessionPool, id)
}

func (application *App) attachSessions(pool session.SessionPool) {
	application.sessionPool = pool
	if application.sessions != nil {
		pool.OnSessionClose(func(s session.Session) {
			application.sessions.Disconnect(s.ID())
		})
	}
}
