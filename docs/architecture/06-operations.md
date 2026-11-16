# 运行日志、指标与诊断

| 要观察什么 | 使用入口 |
| --- | --- |
| 某次请求经过哪些服务 | `app.Call`、`app.CallRemote` 和出站 RPC 使用 `observability.Begin`。同一请求复用 Pitaya 传播的 `request_id`，结束日志带服务、接口、用时和结果。 |
| 正在关闭哪些资源 | 用 `App.Status()` 查看阶段、在途调用及尚未确认关闭的模块数。 |
| 请求是否在排队 | 用 `App.ExecutionStats()` 或执行器的 `Stats()` 查看数量、字节、拒绝数和耗时。保存队列另有 `persistence.Queue.Stats()`。 |
| 进程和请求耗时指标 | 显式装配 `observability.NewDiagnosticsServer`，通过受保护的 `/metrics` 抓取。进程指标、Go 指标、操作计数和耗时直方图在这个端点提供。 |
| CPU、内存等 Go 诊断 | 构造同一诊断服务时打开 profiles。回环监听和 Bearer 令牌同时适用于 pprof。 |

日志使用请求标识关联一次调用；指标使用固定标签，避免为账号或请求号各建一条时间序列。诊断服务最多同时接收两个已认证请求，达到上限返回 HTTP 429。见[观测接口用法](../implementation/observability-guide.md)。

配套模板提供控制台管理命令、监控抓取配置、告警规则和看板文件。凭据由运行环境注入。开放服务前，先配置 NATS 环境隔离，并限制 etcd、数据库与诊断端口的访问。[运行与发布](07-delivery-validation.md)
