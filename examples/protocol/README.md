# 样板协议

## 共同编号与接口声明

schema/catalog.proto 用目录消息定义分组，不把目录作为实际网络包装：

```proto
message Messages { BasicMessages basic = 1; }
message BasicMessages {
  HelloRequest hello_request = 1;
  HelloResponse hello_response = 2;
  Notification notification = 3;
}
```

编号按“组字段号 × 256 ＋组内字段号”生成，得到 257、258、259。目录两个层级均使用 1—255 的普通消息字段，不能用 repeated、map 或 oneof；业务消息内部普通字段不受这个 255 限制。

hello.proto 的 `service Greeting { rpc Hello(HelloRequest) returns (HelloResponse); }` 明确配对，不启用 gRPC。消息可用于响应或推送，不加 push 标志；请求类型只对应一个声明接口，响应类型可以复用。

现有 BAT/SH 脚本同时输出 .pb.go、catalog.gen.go（NewProtocolCatalog）及 catalog.json（语言无关的编号/类型/接口清单）。产物均在忽略目录内，不提交 Git。工具用 protoc 解析后的描述数据检查重复编号/类型/路由、请求类型复用、未登记请求/响应及流式接口；不编写第二套 proto 解析器，不生成业务函数。

使用 `-rpc -forward Player` 时，工具还会在 rpc.gen.go 生成 Player 的网关转发组件。注册时使用协议服务名 Player，传入当前 App 和项目的转发函数。生成的方法通过 app.Call 校验登录并调用转发函数；目标节点和具体游戏逻辑由项目处理。

自有生成模板位于 `tools/protocol/templates/`：`catalog.go.tmpl` 定义协议目录，`rpc.go.tmpl` 定义定向 RPC 调用辅助，`forwarder.go.tmpl` 定义网关转发方法。修改产物样式时编辑对应模板，再运行生成命令。工具通过 go:embed 随编译携带模板，项目使用生成脚本时不需要复制模板文件。

已有同名文件没有对应生成标记时停止，不清空目录。带标记的生成文件会被覆盖，业务代码必须放在生成目录外。平级独立协议仓库可使用：

```bat
scripts\Generate-Protocol.bat -schema ..\protocol -out common\protocol\generated -catalog mygame.Messages
```

Linux 使用同参数调用 sh scripts/Generate-Protocol.sh。当前生成入口编译源目录第一层 .proto，protoc 解析其导入；导入文件代码由所属构建流程生成。目录消息的 go_package 决定元数据 Go 包名，使用方需保持输出目录的包一致。工具不选择客户端语言。

在负责问候接口的进程中，项目先注册名为 `Greeting` 的组件，再把协议目录交给 App。下面的 `cfg` 是创建该 App 时使用的 `app.Config`：

```go
catalog, err := protocol.NewProtocolCatalog()
if err != nil {
    return err
}
if err = application.ConfigureProtocol(catalog, map[string]string{"Greeting": cfg.ServerType}); err != nil {
    return err
}
```

`protocol` 是示例生成包。所有使用该目录的进程都应把 `Greeting` 映射到负责它的同一个服务类型。启动时，框架核对分配给本进程的方法是否已注册，并检查大小写及请求、响应类型；远端进程的部署版本要由项目保证。

正式业务示例见平级 ht_server_game_template/servers/gateway/internal/handler；生成与协议接入见模板 docs/development.md。

启用协议目录后，解码器将客户端的数字编号转换为 Pitaya 组件路由，响应编码器为目录中登记的消息加上两字节大端编号。推送由消息类型确定编号；接收端用相同目录解码。内部 RPC 使用自己的消息类型。字符串路由也可以分发，但未登记的响应类型会原样返回，客户端若按数字协议解码，应先将该类型列入目录。

响应为 `Pitaya 响应头（含请求序号）+ uint16 消息编号 + protobuf`；推送为 `Pitaya 推送头 + uint16 数字路由 + protobuf`。框架错误使用编号 0、原生错误位和原生 protos.Error 载荷。Catalog.DecodeServer 返回编号与已去除编号的载荷。不开启服务端载荷压缩，不填 Pitaya 字典、不新增握手信息。

客户端可以用 zlib 压缩业务正文，并在 Pitaya 消息头置位 `0x10`。数字路由和字符串路由适用同一条收包规则。每条 WebSocket 消息只装一个 Pomelo 包，整个包在传输时和正文展开后都须满足 `Config.MaxMessageBytes`（默认 64 KiB）。计入字节数的是 4 字节 Pomelo 包头、客户端实际发送的消息头（含路由）和正文。WebSocket 帧头及网关内部改写的路由名均不计入。正文展开后刚好达到上限可通过，超出时立即停止解压并关闭连接。

对于消息头有效的未知数字编号，框架跳过正文解压。未知 Notify 丢弃，未知 Request 按原请求序号返回错误，连接保持可用。同一解码器的未知编号日志最多每五秒一条。截断或非法消息头按格式错误处理。握手不下发映射或校验协议版本，客户端须随协议变化同步更新自己的目录。

生成文件统一放在 `examples/protocol/generated/`，由 Git 忽略。构建引用示例协议的客户端前先生成。使用框架库本身无需安装 protoc。

## Windows

从 [Protobuf 发布页](https://github.com/protocolbuffers/protobuf/releases/tag/v25.1) 下载 protoc-25.1-win64.zip，解压后将环境变量 `PROTOC_BIN` 指向其中的 protoc.exe。在框架根目录打开 CMD 并执行：

```bat
scripts\Install-ProtocolTools.bat -protoc-source "%PROTOC_BIN%"
scripts\Generate-Protocol.bat
```

默认用 PATH 中的 Go，也可将 `GO_BIN` 指向所需的 Go 可执行文件。

自动化调用时设置 `set PROTOCOL_NO_PAUSE=1` 取消按键等待；BAT 保留失败退出码。在另一个 BAT 中调用时使用 `call`。

## Linux

从同一官方发布页下载匹配本机架构的 protoc 25.1。解压后设置环境变量 `PROTOC_BIN` 指向其中的 protoc，在框架根目录执行：

```sh
chmod +x "$PROTOC_BIN"
sh scripts/Install-ProtocolTools.sh -protoc-source "$PROTOC_BIN"
sh scripts/Generate-Protocol.sh
```

安装入口会在 Linux 本机构建 protoc-gen-go，不使用 Windows 的 .exe。Go 不在 PATH 时可设置 `GO_BIN`。SH 使用 LF 换行；通过 `sh` 调用无需为脚本另设执行权限。

## 共同行为

- 安装入口将 protoc 复制到项目内，并构建项目固定的插件；已有匹配插件时复用。缺失 Go 依赖默认按本机代理设置下载；添加 `-offline` 只使用已缓存模块。不会自动下载 Go 工具链或升级运行库。
- 两个平台使用 tools/protocol 中的同一份 Go 工具处理版本校验和生成。脚本自动切换到项目根目录，不改父终端 PATH。
- 修改 schema/*.proto 后运行 Generate-Protocol。生成说明性注释统一为中文，保留生成标记、版本与 Deprecated 标记。命令行 libprotoc 25.1 与生成头中的 v4.25.1 对应同一编译器。
- 不拉取仓库、不批量删除旧文件。删除协议时自行移除 generated/ 中对应 .pb.go；删除字段时 reserved 旧编号及名称，不复用。只提交 .proto 和生成工具；业务处理函数放在手写文件中，generated/ 中不放手写代码。

## 可选 RPC 辅助

go run ./tools/protocol generate -rpc 另外生成 rpc.gen.go；不加 -rpc 会移除工具先前生成的辅助文件。业务组件始终手写，工具拒绝覆盖无生成标记的文件。

以下示例使用生成的 `GreetingRPC`。它要求目标进程已注册 `Greeting.Hello` Remote；回环集群示例本身只提供 echo 和可选的登录入口。

```go
client, err := protocol.NewGreetingRPC(application, "backend")
if err != nil {
    return nil, err
}
return client.Hello(ctx, backendID, &protocol.HelloRequest{Name: "玩家"})
```

辅助只封装类型和大小写敏感的路由，serverType 在装配时指定，backendID 由项目分配逻辑提供。它不选节点、不改派、不重试，也不代表远端已经注册。
