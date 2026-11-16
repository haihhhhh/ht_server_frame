# 登录与当前连接

网关启动前调用 `App.ConfigureAuthentication(access.Options)`，设置 `ScopeLocal`、重复登录策略、容量、凭据长度上限及 `Verify` 函数。验证器根据项目账号系统返回可信 UID。框架同时支持 `RejectNew` 和 `ReplaceOld`；模板使用后者。

1. 客户端登录方法调用 `App.Login(ctx, credential)`。身份验证成功后，框架将本次身份与 Pitaya 前端 Session 绑定。
2. 为已登录接口使用 `app.Call(..., true, ...)` 或调用 Endpoint 的 `RequireSession()`。业务可通过 `access.FromContext` 获取身份，或用 `access.RequireUser` 校验目标账号。
3. `BeforeBind` 用于预留登录后的资源，`OnBound` 和 `OnDisconnected` 用于更新条目或提交后续任务。这些回调可能在管理器持锁时调用，应当只做短小的内存操作或非阻塞提交。
4. 本地绑定按连接 ID 和登录代次区分新旧连接。跨网关替换由游戏模板协调，account 与已关联的 game 分别确认新绑定，再交接下线通知。

在[回环集群示例](cluster-guide.md)中，网关和后端都添加 `-test-auth` 后，客户端可用 `-ticket test-ticket-alice -value alice` 观察认证及后端账号检查。这是该示例硬编码的演示凭据。[服务间身份传递](service-identity-guide.md)
