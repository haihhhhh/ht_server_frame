# 日志、指标和诊断端点

## 跟踪一条请求

框架的请求入口和出站 RPC 调用 `observability.Begin`。它复用有效的 Pitaya 请求标识，缺失时生成，结束日志带 `request_id`、`server`、`route`、`duration_ms` 和 `result`。成功调用默认记 Debug，耗时达到 500 毫秒的成功调用及失败调用记 Warn。项目可用 `observability.Logger(ctx)` 添加同一请求下的业务日志；不要附加凭据或完整载荷。

## 开启指标或 pprof

将 `observability.NewDiagnosticsServer(address, token, profiles, collectors...)` 返回的模块登记到宿主。地址必须是回环 IP 和端口，令牌长度 32 至 512 字节。模块在 `Init` 时开始监听，在 `Close` 时关闭 HTTP 服务。未装配时没有这个端点。

抓取方用 GET 请求 `/metrics`，请求头设置 `Authorization: Bearer <令牌>`。端点提供 Go、进程和业务操作指标。`ht_operations_total` 的 result 只取 `ok` 或 `error`；`ht_operation_duration_seconds` 是耗时直方图。入口、远端 RPC 和模板中的数据库任务可能分别计数，不能据此直接得出独立玩家请求数。

设置 `profiles=true` 后，同一监听器开放 `/debug/pprof/`。CPU profile 和 trace 的采集参数 `seconds` 可取 1 至 5，默认 5。诊断入口拒绝未认证请求，最多同时处理两个认证通过的 GET；繁忙返回 HTTP 429。

## 读取队列与宿主状态

`App.Status()` 返回宿主阶段、在途数和尚未确认关闭的模块数。`App.ExecutionStats()` 按执行组给出当前值；单次采样中的各组可能来自稍有不同的时刻。

| 执行组字段 | 含义 |
| --- | --- |
| Accepted / Rejected | 成功进入队列和准入时被拒绝的次数。 |
| Executed / Skipped / Finished | 已开始执行、出队时跳过、已结束处理的请求数。 |
| Queued / QueueBytes / InFlight | 待执行条数和申报字节数，以及正在执行的请求或周期更新数。 |
| QueueWaitTotal / QueueWaitMax | 从提交排队到出队的累计与最大时长，可能包含初始化等待。 |
| ExecutionTotal / ExecutionMax | 实际运行请求回调的累计与最大时长。 |
| Failed / Panics | 聚合失败数和捕获的 panic 数；失败数也包含初始化和周期更新。 |
| Updates / MaxUpdateDelay | 开始执行的周期回调次数及最大调度延迟。 |

自建 `host.Host` 和执行器时，可以用 `observability.New(handler, h, groups)` 创建 Reporter，从项目自己的诊断循环显式调用 `Sample(ctx)`。它输出 `host_status` 和各组的 `execution_stats`，耗时字段按纳秒计。注册至多 64 个有固定名称的组；并发采样会返回忙，输出端阻塞会占用本次采样调用。队列统计与 Prometheus 操作直方图分别描述排队情况和操作耗时。

模板中 Prometheus 抓取配置、Grafana 看板和示例告警位于 `ops/monitoring/`，接入步骤见模板的 `docs/observability.md`。
