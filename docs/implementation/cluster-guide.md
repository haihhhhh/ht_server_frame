# 回环集群示例

`examples/cluster` 使用 Pitaya、NATS 和 etcd。客户端向网关发送 `echo.handle`，网关再调用后端的 `backend.echo.handle`。

## 脚本运行

在 Windows 上按[本地服务说明](local-services.md)准备 NATS 和 etcd 可执行文件，保持三个服务端口空闲。按[协议示例](../../examples/protocol/README.md)安装生成工具。在框架仓库根目录打开 PowerShell 7，使用 Go 1.27.2 执行：

```powershell
go mod download
.\scripts\Generate-Protocol.bat
.\scripts\Test-Cluster.ps1
```

[Test-Cluster.ps1](../../scripts/Test-Cluster.ps1) 从本地 Go 缓存构建示例，使用 PATH 中的 Go，也可传 `-Go` 指定可执行文件。脚本自行启动 NATS、etcd、后端和网关，选用临时网关端口，客户端收到 `backend: cluster-smoke` 后停止这些进程。示例程序保存在框架父目录的 `.runtime/cluster-check`。

## 手工运行

先按[本地服务说明](local-services.md)启动 NATS 和 etcd。在框架根目录打开三个终端，按顺序分别运行以下三行。

```powershell
go run ./examples/cluster -role backend
go run ./examples/cluster -role gateway -addr 127.0.0.1:3250
go run ./examples/client -url ws://127.0.0.1:3250 -route echo.handle -value hello
```

等后端和网关分别打印 `READY` 再运行客户端，应收到 `backend: hello`。在网关和后端终端输入 `stop` 可停止服务。要体验带身份的请求，两端都加 `-test-auth`，然后运行下面的客户端命令。示例校验票据并要求请求值等于登录用户 ID，见[登录说明](authentication-guide.md)。

```powershell
go run ./examples/client -ticket test-ticket-alice -route echo.handle -value alice
```

## 定向 RPC 与状态归属

`App.RPCTo(ctx, serverID, route, reply, request)` 通过 Pitaya 向指定实例发送一次调用，检查 NATS 连接和服务发现，并传递业务身份和剩余期限。目标已不在对应类型的本地节点表中时返回 `errorcode.ErrRPCNotSent`。发送后超时按结果未知处理，不换节点重试。

已收到成功进入角色回包的网关，可用相同签名的 `App.RPCToBoundServer` 向该连接已绑定的原 game 发送普通玩家请求。它仍检查 NATS 连接和本地节点表，跳过主动 etcd 健康探测。调用方负责核对绑定和原目标，后端负责核对身份和分配编号。登录、换绑、进入、退出使用原有接口。

`App.ServerID()` 返回当前实例 ID，`App.ServerIDs(serverType)` 返回该类型已发现实例的排序列表。接收节点仍需确认本机可接单并登记归属。

`placement.Registry` 使用独立于 Pitaya 服务发现的 etcd 前缀。账号、玩家和 Boss 的首次分配分成两步。

```go
// 调用方查不到既有归属时，选出 target，把候选编号随 RPC 发给它。
candidate := placement.NewCandidate(scope, target)

// 接收方先预留本机名额，再用共享的 etcd 客户端登记。
// 用本机真实实例 ID 核对候选目标，保留调用方传来的分配编号。
owner, err := registry.Claim(ctx, candidate)
if err != nil {
    // 登记结果可能未知，接收方用原 candidate 收尾，无须重发业务请求。
    return err
}
// 仅 owner 与 candidate 的节点和分配编号都一致时，本机才继续加载或绑定。
```

满员时接收方直接返回繁忙。两个候选竞争同一范围时，etcd 只接受第一个登记，另一个退还本地名额并返回实际负责人。已有归属始终定向原节点。模板的网关在收到明确的竞争结果后最多转交一次关联请求。Boss 单独用 `Claim` 准备归属，随后才向负责人发送一次攻击。

`Claim` 使用支持租约的完整 etcd 客户端（`*clientv3.Client`）。带短租约的临时键只保护本次登记，防止撤销后迟到的登记再次生效。实际归属不带租约，节点失联不会自动释放。候选编号每次分配新建，同一编号只发起一次登记。

退出后使用取得的完整 Assignment 调用 `Release(ctx, owner)`，只删除内容完全匹配的当前分配；旧通知不能删掉新一轮归属。先完成业务及保存收尾，再释放归属。释放返回 false 表示当前记录不匹配或已不存在，不代表保存完成。任何写 etcd 的超时都可能已经生效，不可凭超时假定没写入。

`Assign(ctx, scope, target)` 可直接登记指定节点。需要由接收节点判断容量时，使用 `NewCandidate` 和 `Claim`。
