# 向客户端推送消息

调用前为 App 配置协议目录，确保其中已经登记推送消息的具体类型。按本次发送的目标选择方法：

| 发送目标 | 方法 | 如何找到连接 |
| --- | --- | --- |
| 当前前端请求所在连接 | `App.PushMessage(ctx, msg)` | 使用上下文中的 Pitaya Session，按消息类型确定编号。 |
| 某一条明确的登录连接 | `App.PushToConnection(ctx, identity, msg)` | 仅在目标网关核对本地连接 ID、账号、登录代次和原生 Session。失效时返回错误。 |
| 一组账号 | `App.PushToUsers(ctx, frontendType, users, msg)` | 按账号交给 Pitaya 发送。每批 1 至 128 个唯一且非空的账号字符串。 |

模板的 `Player.Push` 在玩家循环中读取现有绑定，通过定向 RPC 将消息交给指定网关，由该网关调用 `PushToConnection`。顶号或断线交接期间，若原连接已失效，推送不会改投给另一个连接。该链路和当前请求的响应分别处理。

`PushToUsers` 的收件人由可信业务筛选。它依次将账号提交给原生发送流程，返回失败的名单；上下文中途取消时，名单还会包含尚未发送的账号。返回成功只表示已提交发送，不能确认客户端收到。需要跟踪某条连接的玩法通知使用定向推送。

## 使用 Pitaya 组

就绪后可以通过 `App.Native()` 调用原生组 API，示例中的 `uid` 为服务端已经校验的字符串账号：

```go
native := server.Native()
if native == nil {
    return host.ErrNotReady
}
if err := native.GroupCreate(ctx, "announcement"); err != nil {
    return err
}
if err := native.GroupAddMember(ctx, "announcement", uid); err != nil {
    return err
}
```

广播时调用 `GroupMembers` 获取名单，然后分批送给 `PushToUsers` 并处理失败结果。Pitaya 默认构建器使用进程内的组服务；不同网关的本地组没有共享成员表。项目维护成员权限、数量上限及离组规则。
