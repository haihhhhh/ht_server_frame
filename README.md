# ht_server_frame

基于 Pitaya v2.11.24、NATS RPC 和 etcd 的 Go 游戏服务端框架。Go module 为 `github.com/haihhhhh/ht_server_frame`，工具链版本为 Go 1.27.2。

- **先跑通一次请求。** 按[回环集群指南](docs/implementation/cluster-guide.md)启动网关和后端，再用示例客户端发送 echo。构建整个仓库前需先[生成示例协议](examples/protocol/README.md)。
- **从模板开发游戏。** 将 `ht_server_game_template`、`ht_server_frame`、配套协议源和配置表源放在同一父目录。模板维护账号、角色、玩家组件和公共 Boss 示例，生成文件和启动服务的命令见[模板入门指南](https://github.com/haihhhhh/ht_server_game_template/blob/master/docs/getting-started.md)。
- **阅读请求流程。** 从[架构总览](docs/architecture/overview.md)看网关、account 与 game 的调用，再看[登录与 RPC](docs/architecture/03-session-rpc.md)和[状态保存与扩容](docs/architecture/10-state-saving.md)。

## 包和入口

| 包 | 职责 |
| --- | --- |
| [app](app/app.go) | 装配 Pitaya、模块和业务入口，管理启动、停止、RPC 与推送。 |
| [access](access/session.go) | 对接凭据验证函数，维护网关本地的账号与连接绑定。 |
| [errorcode](errorcode/code.go) | 转换可公开的错误，提供业务可识别的框架错误码。 |
| [host](host/host.go) | 按依赖初始化和关闭模块，限制同时执行的调用。 |
| [execution](execution/executor.go) | 提供可选的按 key 串行执行、周期更新和固定通道执行池。 |
| [persistence](persistence/queue.go) | 有界队列执行项目提供的写入函数，分别反馈入队与执行结果。 |
| [placement](placement/registry.go) | 在 etcd 中登记、查询和按分配编号释放业务归属。 |
| [idgen](idgen/generator.go) | 通过 etcd 独占发号节点，在进程内生成 64 位 ID。 |
| [gameclock](gameclock/clock.go) | 读取业务时间，同步环境中的时钟偏移。 |
| [protocol](protocol/wire.go) 与 [tools/protocol](tools/protocol/main.go) | 定义数字消息格式，从共同的 Protobuf 定义生成目录与可选的 RPC 辅助方法。 |
| [tables](tables/tables.go) | 校验整套配置文件，发布单进程只读快照。 |
| [observability](observability/diagnostics_server.go) | 请求日志、运行统计、Prometheus 端点和可选 pprof。 |

## 按调用顺序读源码

1. 从[基础配置](app/config.go)和[集群构造](app/cluster.go)建立 App，[登记组件](app/component.go)，再 Start。前端负责 WebSocket 接入，后端通过 NATS 接收内部 RPC。
2. 网关在[认证入口](app/authentication.go)完成凭据检查与 Pitaya Session 绑定。客户端业务方法通过 [app.Call](app/endpoint.go) 接入准入和身份检查，再按需调用[远端节点](app/rpc.go)。
3. 项目决定哪些数据在一个循环内读写。模板已进入角色的普通请求定向到原 game；新增节点可接收尚无归属的对象。节点失联时不会自动接管其内存状态。
4. 服务通过[推送接口](docs/push-and-groups.md)向客户端发通知，使用[宿主停止流程](docs/architecture/05-lifecycle-failures.md)排空在途工作并关闭依赖。

本项目采用 [MIT 许可证](LICENSE)。
