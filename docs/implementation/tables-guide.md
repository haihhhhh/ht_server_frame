# 配置表加载与版本快照

`tables` 是可选的纯标准库模块，服务启动时加载导表产物，不运行 Excel 或 Luban。框架不定义业务表结构、JSON/二进制格式或生成类型；这些由接入项目的 Decode、Validate 和 Clone 提供。

## 输入与约束

`tables.New[T](source fs.FS, manifest tables.Manifest, opts tables.Options[T])` 创建实现 `host.Module` 的加载模块。

- Manifest 标明数据、schema、导表工具和代码兼容版本，以及文件名和 SHA-256。清单来自构建/发布产物；文件校验和用于防止损坏和混包，不是签名，也不能认证不可信发布者。
- SchemaVersion 和 CodeVersion 与项目指定值精确匹配。CodeVersion 是项目定义的读表兼容标识，不要求绑定每一次 Git 提交，也不自动解释 semver 区间。
- RequiredFiles 声明必须出现在清单中的文件。清单中的全部文件均需存在且校验通过；源目录中未列入清单的文件不会传给 Decode。
- MaxFiles、MaxFileBytes、MaxTotalBytes 都必须为正。先检查文件大小，再限制实际读取，避免文件增长越过读取上限。这些约束限制输入字节，不等于解码对象的堆内存上限；Decode/Validate/Clone 是可信项目代码，仍需控制展开规模。
- 文件名必须是合法 fs 路径，不允许 `..` 等路径穿越。fs.FS 必须来自可信来源；路径检查不提供符号链接隔离或操作系统级沙箱。

New 复制清单与必需文件切片；调用方之后修改原切片不影响本模块。运行配置（端口、密钥等）独立处理，不放进表包。本模块不读取环境变量或输出日志；项目回调的错误可带诊断上下文，调用方不得将未经处理的内部错误直接发送客户端。

## 项目提供的三个函数

```go
// T 可以是导表生成的类型，也可以是手写聚合结构。
Decode(ctx context.Context, files map[string][]byte) (T, error)
Validate(ctx context.Context, candidate T) error
Clone(candidate T) T
```

Decode 收到当前清单中已校验的文件，Validate 检查行约束和跨表引用。全部成功后，Clone 提供用于发布的值。会被调用方修改的 map、切片或指针需要复制。若整版数据在发布后保持只读，且查询方法不会暴露可写的内部数据，Clone 可以返回共享该版本的只读入口。

模块只允许初始化一次，失败后不自动重试或继续暴露部分表；需要重试时由新的启动流程创建新模块。context 在阶段间及每次文件读取前检查，不能强行中断忽略取消的回调或阻塞 fs.Read。

## 宿主接入与读取

将模块作为 `host.Entry{Name: "tables", Module: tableModule}` 注册，使用配置表的其他模块声明 `DependsOn: []string{"tables"}`。表包失败会使 Host.Start 失败、准入保持关闭，不向业务暴露半成品。

一次业务操作获取一个快照：

```go
snapshot, err := tableModule.Snapshot()
if err != nil { return err }
version := snapshot.Version()
data := snapshot.Value()
// 在本次操作内始终使用 data 与 version，不重新从不同来源读各张表。
```

每次请求开始时，Snapshot 用一次原子读取得当前版本，随后 Value 调用项目提供的 Clone。骨架的背包和角色在同一个玩家循环内共享本次请求取得的只读配置。即使中途重载，扣道具和加经验也使用同一版。

Close 清除模块持有的快照引用，后续 Snapshot 返回 ErrNotReady；已持有快照的操作仍保留自己的版本，Go 按引用回收内存。并发 Close 不会强杀正在执行的初始化回调，但会阻止其完成后发布快照；宿主正常按初始化/停止顺序调用，不应把 Close 返回当作并发 Init 已结束。

## 重载范围

用 `Reload(ctx, source, manifest)` 重载当前进程的配置，传入独立的新包目录及其清单。准备期间仍可读取原快照；同时发起另一次重载会得到 `ErrReloading`。全部校验成功后一次替换，任何一步失败都保留旧版。

模板的 game 在控制台接收 `reload-tables <完整配置包目录>`。在 `common/gamedata/load.go` 中查看模板的解码、校验和只读 Clone 配置。每个 game 进程独立执行该命令。
