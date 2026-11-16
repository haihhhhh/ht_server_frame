# Windows 本地 NATS / etcd

[Test-LocalServices.ps1](../../scripts/Test-LocalServices.ps1) 为框架的回环示例启动一个 NATS 和一个 etcd。这里使用无认证的本机 NATS。运行游戏模板时，同一端口只能启动一个 NATS 实例，请改用模板的用户配置。[模板的 NATS 启动说明](https://github.com/haihhhhh/ht_server_game_template/blob/master/docs/nats.md)

## 固定版本与来源

取 Windows amd64 发行包，解压前用 `Get-FileHash` 对照发行方的 SHA-256 校验清单。脚本按下列版本查找可执行文件。[NATS Server 2.15.1](https://github.com/nats-io/nats-server/releases/tag/v2.15.1) · [etcd 3.5.34](https://github.com/etcd-io/etcd/releases/tag/v3.5.34)

以下路径均相对于框架仓库的父目录。将两个官方发行包分别解压到对应的目录。

- `.tools/nats-server-v2.15.1-windows-amd64`：NATS 程序及附带文件。
- `.tools/etcd-v3.5.34-windows-amd64`：etcd、etcdctl、etcdutl 及附带文件。
- `.runtime/local-services`：生成的配置、日志和 etcd-data；不是项目源码，不放入框架远端。

## 一次启动并检查连接

在框架仓库根目录打开 PowerShell 7 并运行：

```powershell
& .\scripts\Test-LocalServices.ps1
```

脚本先检查端口，生成配置，再启动两个进程。它轮询 etcd 的 HTTP 健康接口（就绪期限 20 秒），检查 NATS 的 PING/PONG 应答，并运行 `etcdctl endpoint health`。脚本退出时会终止自己启动的进程，保留配置、日志和 etcd 数据；端口已占用则在启动前报错。

## 手工启动并保持运行

先执行上述脚本生成并验证配置，再在两个终端分别运行：

```powershell
# 终端一，在框架仓库的父目录运行
& .\.tools\nats-server-v2.15.1-windows-amd64\nats-server.exe -c .\.runtime\local-services\nats.conf
# 终端二，同一工作目录
& .\.tools\etcd-v3.5.34-windows-amd64\etcd.exe --config-file .\.runtime\local-services\etcd.yaml
```

测试结束在各自终端停止进程。不要在服务仍运行时再次执行验证脚本；脚本会拒绝使用已占用的端口。

| 用途 | 地址 | 限制 |
| --- | --- | --- |
| NATS Core | nats://127.0.0.1:14222 | 不启用 JetStream、监控端口或集群路由；连接上限 64，消息上限 1 MiB，单连接待发上限 4 MiB |
| etcd 客户端 | http://127.0.0.1:12379 | 单节点，仅回环 |
| etcd 节点通信 | http://127.0.0.1:12380 | 单节点，仅回环 |

两项服务仅供本机测试，未配置生产认证/TLS。同机其他进程仍可能访问回环服务，不用于真实敏感数据。

NATS 日志每份约 1 MiB，最多保留 2 份轮转日志；etcd 日志每份 1 MiB、最多 2 份备份、保留 1 天。etcd 后端配额 128 MiB，WAL 与快照各保留 2 份，每 1000 次提交触发快照，按 1 小时历史周期压缩。WAL 预分配等占用不包含在后端配额中，配额不是目录总大小硬上限；持续使用仍需监控磁盘，不自动删除数据库。日志参数来源：[NATS 配置](https://docs.nats.io/reference/config/)。
