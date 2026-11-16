package discovery

import "github.com/topfreegames/pitaya/v2/cluster"

type readyNotifier struct {
	cluster.ServiceDiscovery
	ready chan<- struct{}
}

// NotifyWhenReady 在原生服务发现完成初始化后通知 App。关闭 Unique 模块时，这是 Pitaya 集群的最后一个初始化钩子。
func NotifyWhenReady(service cluster.ServiceDiscovery, ready chan<- struct{}) cluster.ServiceDiscovery {
	return &readyNotifier{ServiceDiscovery: service, ready: ready}
}

func (notifier *readyNotifier) AfterInit() {
	notifier.ServiceDiscovery.AfterInit()
	close(notifier.ready)
}
