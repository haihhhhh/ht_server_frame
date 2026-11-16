# 宿主接入

`host.Create(host.Options{MaxInFlight: 128, Modules: entries})` 创建宿主，128 是这个例子的并发上限。每个 `host.Entry` 提供唯一的 `Name`、所依赖模块的 `DependsOn` 和 `Module`。重复名称、缺少依赖、依赖环和无效容量都会返回错误。

## 模块启动

模块实现 `Init(context.Context) error` 和 `Close() error`。调用 `Start` 后，宿主按依赖顺序初始化，全部成功才进入 Ready。初始化失败时，宿主逆序关闭已成功初始化的模块；失败模块负责清理自身的部分初始化资源。启动上下文随 Start 返回而取消，常驻 goroutine 需要独立生命周期。

## 请求准入

`Host.Do(ctx, fn)` 在当前 goroutine 运行回调，达到上限返回 `host.ErrOverloaded`，尚未就绪或正在停止返回 `host.ErrNotReady`。它没有额外的等待队列。需要有类型返回值时用 `host.Call(ctx, h, fn)`，错误时得到零值与错误。宿主会捕获该回调的 panic；业务内存是否已被部分修改由项目处理。

出站 RPC 通过 `DoOutbound` 尝试借用同一宿主里当前入口的名额，一个入口同时只能借给一个 RPC。后台下线通知可用 `MaxReservedOutbound` 的独立名额。远端服务仍需通过它自己的宿主准入。在途调用实际退出后才归还其名额。

## 停止与依赖

`Stop(ctx)` 关闭准入并等待在途调用实际完成，随后逆序关闭模块。若队列后台任务仍在访问数据库，把队列登记为依赖数据库的模块，在它的 `Close` 中等待队列工作结束。这样宿主会先关队列再关数据库。用 `app.NewPool` 或 `app.NewGroup` 装配的执行组则由 App 自动纳入生命周期。

等待超时会请求协作取消并返回上下文错误，后台排空仍可继续。调用方可再次调用 Stop 等待最终结果。模块关闭出错或 panic 时宿主进入 `Faulted`，保留该模块及剩余依赖，后续 Stop 返回原错误。通过 `Status().PendingModules` 查看尚未确认关闭的数量。

宿主回调内同步等待本宿主 Stop 会等待自己退出。收到停止信号时，应从独立的运行入口发起关闭。[App 停止顺序](../architecture/05-lifecycle-failures.md)
