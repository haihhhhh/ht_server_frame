# 在进程内顺序执行任务

使用 `execution.New[T]` 创建一个执行器，并提供 `Options[T]` 中的名称、`Init`、初始化期限、最大 key 数、每个 key 的排队数量与字节上限。需要定时任务时，同时提供 `Update` 与正的 `UpdateInterval`。通过 App 注册网络端点的例子见[模块分工](../architecture/01-modules.md)。

## key、初始化与排队

首次接收某个 key 时，执行器为它建立循环，由 `Init(ctx, key)` 创建状态 T。初始化期间该 key 的请求可以排队，回调要等初始化完成才执行；项目可以在队列外先拒绝尚未就绪的玩家请求。初始化失败或发生 panic 时，这个 key 会保留故障状态，其他 key 继续工作。

- `Call(ctx, key, payloadBytes, fn)` 等待回调结果。排队期间取消会跳过回调；开始执行后取消只会结束调用方的等待，后续任务仍须等当前回调退出。
- `Submit` 只确认接纳，后续失败计入 `Stats().Failed`。需要单次执行结果时使用 `Call`。提交的估算字节数须为正，出队后从排队字节中扣除。
- 同一执行器、同一 key 按接纳顺序执行；不同 key 可并行。带执行器上下文同步调用已经位于本调用链的同一 key 会返回 `ErrReentrant`。
- 周期更新和普通回调共用该 key 的循环。慢回调期间的 tick 会合并。业务回调 panic 时记录堆栈并使这个 key 进入故障状态，已经发生的内存修改无法回滚。
- `Stop(ctx)` 停止接收新任务，停止安排新的周期回调，并排空现有队列。等待超时会请求协作取消；可再次等待。执行器的 key 在实例存活期间不会自动逐出，应按对象寿命确定执行器的创建与停止位置。

无状态工作可用 `execution.NewPool`。它在固定数量的执行通道间轮询；被选通道满时可能拒绝请求，即使其他通道尚有空位。某通道的回调 panic 会隔离该通道。

## 与保存队列配合

在回调内收集已变化的组件数据，复制成后续写库不会再修改的快照，然后交给 `persistence.Queue`。模板玩家实现位于 `servers/game/internal/player`；加载和写入分别在其 `storage/load.go`、`storage/save.go`。账号、玩家、公共 Boss 使用的保存策略见[状态保存与扩容](../architecture/10-state-saving.md)。
