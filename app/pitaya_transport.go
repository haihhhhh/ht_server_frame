package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync/atomic"

	"github.com/haihhhhh/ht_server_frame/errorcode"
	"github.com/haihhhhh/ht_server_frame/internal/discovery"
	"github.com/haihhhhh/ht_server_frame/internal/wsacceptor"
	"github.com/haihhhhh/ht_server_frame/protocol/pitayawire"
	pitaya "github.com/topfreegames/pitaya/v2"
	"github.com/topfreegames/pitaya/v2/component"
	"github.com/topfreegames/pitaya/v2/config"
	logruslogger "github.com/topfreegames/pitaya/v2/logger/logrus"
	"github.com/topfreegames/pitaya/v2/serialize"
)

var processStarted atomic.Bool

type pitayaTransport struct{ application *App }

func (transportModule *pitayaTransport) Init(context.Context) error {
	if !processStarted.CompareAndSwap(false, true) {
		return errors.New("app: Pitaya already started in this process")
	}
	application := transportModule.application
	// 官方默认 Debug 会记录原始请求；使用公开接口切换到默认 Info 日志器。
	pitaya.SetLogger(logruslogger.New())
	pitayaConfig := config.NewDefaultPitayaConfig()
	pitayaConfig.SerializerType = uint16(serialize.PROTOBUF)
	pitayaConfig.Handler.Messages.Compression = false
	// 原生转交通道仅保留一条；网络写队列另按条数及字节计量。
	// shortcut: 原生转交槽及编码中的临时包不计入网络写队列，完整发送预算验收前需统一计量。
	pitayaConfig.Buffer.Agent.Messages = 1
	pitayaConfig.Buffer.Agent.WriteTimeout = application.cfg.WriteTimeout
	frontend, mode := true, pitaya.Standalone
	if application.cluster != nil {
		frontend, mode = application.cluster.Frontend, pitaya.Cluster
		pitayaConfig.Session.Unique = false
		pitayaConfig.Cluster.RPC.Client.Nats.Connect = application.cluster.NATS
		pitayaConfig.Cluster.RPC.Server.Nats.Connect = application.cluster.NATS
		pitayaConfig.Cluster.RPC.Client.Nats.RequestTimeout = application.cfg.RequestTimeout
		pitayaConfig.Cluster.SD.Etcd.Endpoints = []string{application.cluster.Etcd}
		pitayaConfig.Cluster.SD.Etcd.Prefix = application.cluster.Prefix
	}
	builder := pitaya.NewDefaultBuilder(frontend, application.cfg.ServerType, mode, nil, *pitayaConfig)
	if application.cluster != nil {
		var ok bool
		application.rpcConnection, ok = builder.RPCClient.(interface{ IsConnected() bool })
		if !ok {
			return errors.New("app: RPC client connection status unavailable")
		}
		builder.ServiceDiscovery = discovery.NotifyWhenReady(builder.ServiceDiscovery, application.ready)
		application.discovery = discovery.NewHealth(builder.ServiceDiscovery.IsConnected)
	}
	application.attachSessions(builder.SessionPool)
	pitayawire.Attach(builder, application.protocol, application.cfg.ServerType, application.cfg.MaxMessageBytes, errorcode.Public)
	if frontend {
		var err error
		application.ws, err = wsacceptor.New(wsacceptor.Config{
			Address:             application.cfg.Address,
			MaxMessageBytes:     application.cfg.MaxMessageBytes,
			MaxPendingMessages:  application.cfg.MaxPendingMessages,
			MaxPendingBytes:     application.cfg.MaxPendingBytes,
			WriteTimeout:        application.cfg.WriteTimeout,
			SlowConnectionError: &errorcode.ErrSlowConnection,
		})
		if err != nil {
			return fmt.Errorf("app: websocket listen: %w", err)
		}
		builder.AddAcceptor(application.ws)
	}
	nativeApp := builder.Build()
	// 与 ServerID 的读取使用同一把锁，避免启动期间并发读取尚未发布的实例。
	application.mu.Lock()
	application.native = nativeApp
	application.mu.Unlock()
	if application.protocol != nil && frontend {
		application.native.Register(&UnknownProtocol{}, component.WithName(pitayawire.ReservedComponent))
	}
	application.registry.Install(nativeApp, frontend)
	if application.cluster == nil {
		if err := application.native.RegisterModule(&startup{ready: application.ready}, readyModuleName); err != nil {
			return fmt.Errorf("app: readiness module: %w", err)
		}
	}
	go func() {
		defer func() {
			if recover() != nil {
				slog.Error("Pitaya运行异常，进程终止", "component", "transport", "kind", "panic", "stack", string(debug.Stack()))
				// 部分启动后的上游资源不能安全回滚；按进程一次生命周期直接失败退出。
				panic("Pitaya lifecycle failed")
			}
		}()
		application.native.Start()
		close(application.done)
		_ = application.Stop(context.Background())
	}()
	<-application.ready
	return nil
}
func (transportModule *pitayaTransport) Close() error {
	transportModule.application.native.Shutdown()
	<-transportModule.application.done
	if transportModule.application.ws != nil {
		transportModule.application.ws.Stop()
	}
	return nil
}

// 此模块最后注册；不读取上游未同步的 IsRunning/IsReady 字段。
type startup struct{ ready chan struct{} }

func (*startup) Init() error {
	return nil
}
func (s *startup) AfterInit() {
	close(s.ready)
}
func (*startup) BeforeShutdown() {}
func (*startup) Shutdown() error {
	return nil
}
