package main

import (
	"embed"
	"fmt"
	"text/template"

	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/pluginpb"
)

//go:embed templates/*.go.tmpl
var templateFiles embed.FS

const rpcFileName = "rpc.gen.go"

type rpcService struct {
	GoName  string
	Methods []rpcMethod
	Forward bool
}

type rpcMethod struct {
	Name   string
	Route  string
	Input  protogen.GoIdent
	Output protogen.GoIdent
}

// 使用官方 Go 名称解析，兼容下划线、嵌套类型和跨包消息，不猜测生成类型名。
func generateRPC(set *descriptorpb.FileDescriptorSet, sources []string, root *descriptorpb.FileDescriptorProto, forward string) ([]byte, error) {
	gen, err := (protogen.Options{}).New(&pluginpb.CodeGeneratorRequest{
		ProtoFile:      set.File,
		FileToGenerate: sources,
	})
	if err != nil {
		return nil, err
	}
	owner := gen.FilesByPath[root.GetName()]
	if owner == nil {
		return nil, fmt.Errorf("找不到协议目录的 Go 文件")
	}
	var services []rpcService
	used := map[string]bool{"RPCCaller": true}
	for _, file := range gen.Files {
		if file.GoImportPath != owner.GoImportPath {
			continue
		}
		for _, m := range file.Messages {
			if m.GoIdent.GoName == "RPCCaller" {
				return nil, fmt.Errorf("消息名称与 RPC 辅助冲突：RPCCaller")
			}
			used[m.GoIdent.GoName] = true
		}
		for _, e := range file.Enums {
			used[e.GoIdent.GoName] = true
		}
	}
	for _, file := range gen.Files {
		if !file.Generate {
			continue
		}
		for _, service := range file.Services {
			entry := rpcService{GoName: service.GoName}
			if forward != "" && string(service.Desc.Name()) == forward {
				for _, method := range service.Methods {
					if method.GoName != string(method.Desc.Name()) {
						return nil, fmt.Errorf("转发路由必须与 Go 方法名一致：%s.%s", forward, method.Desc.Name())
					}
					switch method.GoName {
					case "Base", "Application", "ForwardRequest", "Init", "AfterInit", "BeforeShutdown", "Shutdown":
						return nil, fmt.Errorf("转发方法与组件字段或生命周期方法重名：%s.%s", forward, method.GoName)
					}
				}
				for _, symbol := range []string{service.GoName + "Forwarder", "New" + service.GoName + "ForwardRequest"} {
					if used[symbol] {
						return nil, fmt.Errorf("转发入口名称重复：%s", symbol)
					}
					used[symbol] = true
				}
				entry.Forward = true
				forward = ""
			}
			name := service.GoName + "RPC"
			if used[name] || used["New"+name] {
				return nil, fmt.Errorf("RPC 辅助名称重复：%s", name)
			}
			used[name] = true
			used["New"+name] = true
			for _, method := range service.Methods {
				entry.Methods = append(entry.Methods, rpcMethod{
					Name:   method.GoName,
					Route:  string(service.Desc.Name()) + "." + string(method.Desc.Name()),
					Input:  method.Input.GoIdent,
					Output: method.Output.GoIdent,
				})
			}
			services = append(services, entry)
		}
	}
	if forward != "" {
		return nil, fmt.Errorf("未找到要转发的协议服务：%s", forward)
	}
	output := gen.NewGeneratedFile(rpcFileName, owner.GoImportPath)
	functions := template.FuncMap{
		"ident": output.QualifiedGoIdent,
		"qual": func(path, name string) string {
			return output.QualifiedGoIdent(protogen.GoImportPath(path).Ident(name))
		},
	}
	layout, err := template.New("rpc.go.tmpl").Funcs(functions).ParseFS(templateFiles, "templates/rpc.go.tmpl", "templates/forwarder.go.tmpl")
	if err != nil {
		return nil, err
	}
	data := struct {
		Header   string
		Package  protogen.GoPackageName
		Services []rpcService
	}{
		Header:   generatedGoHeader,
		Package:  owner.GoPackageName,
		Services: services,
	}
	if err = layout.Execute(output, data); err != nil {
		return nil, err
	}
	return output.Content()
}
