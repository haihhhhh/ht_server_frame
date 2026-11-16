// 协议脚本共用工具：准备生成器或生成样板协议，不依赖 PowerShell。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const (
	installCommand  = "install"
	generateCommand = "generate"

	schemaFlag  = "schema"
	outFlag     = "out"
	catalogFlag = "catalog"
	rpcFlag     = "rpc"
	forwardFlag = "forward"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 || (os.Args[1] != installCommand && os.Args[1] != generateCommand) {
		return fmt.Errorf("用法：protocol install [-protoc-source 路径] [-offline] 或 protocol generate")
	}
	options := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	source := options.String("protoc-source", "", "复制已有的 protoc 到项目工具目录")
	offline := options.Bool("offline", false, "仅使用已缓存的 Go 依赖")
	var generation generationOptions
	options.StringVar(&generation.schema, schemaFlag, "examples/protocol/schema", "协议源目录")
	options.StringVar(&generation.destination, outFlag, "examples/protocol/generated", "生成产物目录")
	options.StringVar(&generation.catalogName, catalogFlag, "ht.example.v1.Messages", "描述消息分组的完整 proto 消息名")
	options.BoolVar(&generation.rpc, rpcFlag, false, "生成可选的定向 RPC 调用辅助，不生成业务函数")
	options.StringVar(&generation.forward, forwardFlag, "", "为指定协议服务生成已登录客户端的转发入口（配合 -rpc）")
	if err := options.Parse(os.Args[2:]); err != nil {
		return err
	}
	if (generation.forward != "" && !generation.rpc) || options.NArg() != 0 || (os.Args[1] == generateCommand && (*source != "" || *offline)) {
		return fmt.Errorf("参数不正确，请参阅 examples/protocol/README.md")
	}
	if os.Args[1] == installCommand {
		invalid := false
		options.Visit(func(f *flag.Flag) {
			switch f.Name {
			case schemaFlag, outFlag, catalogFlag, rpcFlag, forwardFlag:
				invalid = true
			}
		})
		if invalid {
			return fmt.Errorf("schema、out 和 catalog 仅用于 generate")
		}
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	bin, err := filepath.Abs(".tools/protobuf/bin")
	if err != nil {
		return err
	}
	tools := toolPaths{
		protoc: filepath.Join(bin, "protoc"+suffix),
		plugin: filepath.Join(bin, "protoc-gen-go"+suffix),
	}
	if os.Args[1] == installCommand {
		goBin := filepath.Join(runtime.GOROOT(), "bin", "go"+suffix)
		return installTools(tools, goBin, *source, *offline)
	}
	return generateProtocol(tools, generation)
}
