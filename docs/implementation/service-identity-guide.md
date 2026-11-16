# NATS 服务认证与业务身份

在 NATS 上为每个环境配置独立的 account，为各服务配置专用凭据。同一环境中的服务互相信任。框架回环示例允许匿名 NATS；运行配套游戏模板时，按模板的 `docs/nats.md` 设置各服务凭据。

| 调用场景 | 用法 |
| --- | --- |
| 客户端首次登录 | 网关用 `ConfigureAuthentication` 设置验证器，再调用 `App.Login` 绑定账号。登录前的认证 RPC 可以没有玩家身份。 |
| 已登录连接调用 | 前端使用 `app.Call(..., true, ...)` 或受 `RequireSession()` 保护的入口。出站 `App.RPC`、`RPCTo` 从当前上下文传递可信业务身份和调用期限。 |
| 普通内部调用 | 需要玩家身份的 Remote 通过 `app.CallRemote(..., false, ...)` 或受保护的后端 Endpoint 读取身份；项目还要核对请求目标、归属与当前连接。 |
| 下线通知 | 登录换绑中用 `RPCSessionClosed`；后台断线队列用 `RPCQueuedSessionClosed`。两者都发送给明确的目标节点。接收方使用 `CallRemote(..., true, ...)` 或 `RequireSessionClosed()`，再检查通知中的绑定。 |

出站代码复制 Pitaya 传播字段，从经过检查的上下文重建 `ht.business_identity`，携带账号、连接 ID、绑定代次和断线用途。接收方还会读取 `ht.deadline` 表示的绝对截止时间，跨机器部署要保持系统时钟同步。

`access.RequireUser` 可以核对当前身份是否对应目标账号；资源访问权限由项目判定。能够以本环境受信服务身份发送 NATS 请求的进程也能构造业务身份，因此要保护服务凭据，并限制 NATS 网络访问。不要将客户端提供的身份直接交给 `access.WithRemoteIdentity`。
