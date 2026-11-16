package observability

import (
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
)

var metricsEnabled atomic.Bool

// 不使用用户、角色、请求标识作为指标标签；只有固定结果分类。
var requestCount = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "ht_operations_total",
	Help: "框架请求、RPC 和数据库任务完成次数",
}, []string{resultField})
var requestDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
	Name:    "ht_operation_duration_seconds",
	Help:    "框架操作耗时",
	Buckets: []float64{.001, .005, .01, .05, .1, .5, 1, 3, 10},
})
