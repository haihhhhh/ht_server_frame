package app

import (
	"errors"
	"time"

	"github.com/haihhhhh/ht_server_frame/app/internal/clusterconfig"
	"github.com/haihhhhh/ht_server_frame/execution"
	"github.com/haihhhhh/ht_server_frame/host"
	"github.com/haihhhhh/ht_server_frame/internal/discovery"
)

// LocalClusterConfig 配置原生集群；NATS 支持逗号分隔地址、URL 用户名密码及系统信任 TLS。
type LocalClusterConfig struct {
	Frontend bool
	NATS     string
	Etcd     string
	Prefix   string
}

func NewLocalCluster(cfg Config, clusterCfg LocalClusterConfig, modules ...host.Entry) (*App, error) {
	if !clusterconfig.ValidLocalEndpoints(clusterCfg.NATS, clusterCfg.Etcd) || !validName(clusterCfg.Prefix) {
		return nil, errors.New("app: invalid local cluster configuration")
	}
	if clusterCfg.Frontend && !clusterconfig.IsLoopbackListener(cfg.Address) {
		return nil, errors.New("app: local cluster requires loopback listener")
	}
	return newApp(cfg, &clusterCfg, modules...)
}

// NewPrivateCluster 用于可信内网；NATS 认证与 TLS 使用原生连接配置。
// 只接受回环/私有 IP；NATS、etcd 的访问隔离由部署方保证，禁止直接开放公网。
func NewPrivateCluster(cfg Config, clusterCfg LocalClusterConfig, modules ...host.Entry) (*App, error) {
	if err := clusterconfig.ValidatePrivateEndpoints(clusterCfg.NATS, clusterCfg.Etcd); err != nil {
		return nil, err
	}
	if !validName(clusterCfg.Prefix) {
		return nil, errors.New("app: invalid cluster prefix")
	}
	return newApp(cfg, &clusterCfg, modules...)
}

// ServerIDs 返回 Pitaya 当前发现的指定类型节点，按 ID 排序；没有该类型的节点时返回空列表。
// 发现记录不是业务就绪或唯一归属证明；项目只在首次分配时使用此列表。
func (application *App) ServerIDs(serverType string) ([]string, error) {
	if !validName(serverType) || application.cluster == nil {
		return nil, execution.ErrInvalid
	}
	if application.Status().Phase != host.Ready {
		return nil, host.ErrNotReady
	}
	return discovery.ServerIDs(application.native, serverType)
}

// ServerID 返回本次运行的原生实例 ID；实例尚未创建时返回空字符串，停服期间仍可读取。
func (application *App) ServerID() string {
	application.mu.Lock()
	defer application.mu.Unlock()
	if application.native == nil {
		return ""
	}
	return application.native.GetServerID()
}

type DependencyStatus struct {
	NATSConnected         bool
	DiscoveryAvailable    bool
	DiscoveryCheckedUntil time.Time
}

// Dependencies 返回最近观察值，不触发网络探测；缓存过期或尚未探测时发现状态为不可用。
func (application *App) Dependencies() DependencyStatus {
	select {
	case <-application.ready:
	default:
		return DependencyStatus{}
	}
	select {
	case <-application.done:
		return DependencyStatus{}
	default:
	}
	if application.cluster == nil {
		return DependencyStatus{}
	}
	status := DependencyStatus{NATSConnected: application.rpcConnection != nil && application.rpcConnection.IsConnected()}
	if application.discovery != nil {
		status.DiscoveryAvailable, status.DiscoveryCheckedUntil = application.discovery.Snapshot()
	}
	return status
}
