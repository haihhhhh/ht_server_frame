package registration

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/golang/protobuf/proto"
	"github.com/haihhhhh/ht_server_frame/protocol"
	"github.com/haihhhhh/ht_server_frame/protocol/pitayawire"
	"github.com/topfreegames/pitaya/v2/component"
)

// BindProtocol 把协议声明中的组件名对应到项目指定的服务类型，保留一份独立的映射。
func BindProtocol(catalog *protocol.Catalog, services map[string]string) (*pitayawire.Bindings, error) {
	methods := catalog.Definition().Methods
	for _, method := range methods {
		if method.Service == pitayawire.ReservedComponent {
			return nil, fmt.Errorf("app: %s is reserved for protocol errors", pitayawire.ReservedComponent)
		}
	}
	bindings := make(map[string]string)
	for _, method := range methods {
		serverType := services[method.Service]
		if !ValidName(method.Service) || !ValidName(method.Name) || !ValidName(serverType) {
			return nil, fmt.Errorf("app: invalid service binding for %s", method.Service)
		}
		bindings[method.Service] = serverType
	}
	if len(bindings) != len(services) {
		return nil, fmt.Errorf("app: unknown protocol service binding")
	}
	return &pitayawire.Bindings{Catalog: catalog, Services: bindings}, nil
}

// ValidateProtocol 在启动前核对本服务登记的方法及其请求、响应类型。
func (registry *Registry) ValidateProtocol(bindings *pitayawire.Bindings, serverType string) error {
	if bindings == nil {
		return nil
	}
	methods, err := registry.methodSignatures()
	if err != nil {
		return err
	}
	for _, declared := range bindings.Catalog.Definition().Methods {
		target := bindings.Services[declared.Service]
		route := declared.Service + "." + declared.Name
		if len(target+"."+route) > protocol.MaxRouteBytes {
			return fmt.Errorf("app: protocol route exceeds %d bytes", protocol.MaxRouteBytes)
		}
		if target != serverType {
			continue
		}
		registered := methods[route]
		if len(registered) == 0 {
			return fmt.Errorf("app: declared route is not registered: %s", route)
		}
		for _, method := range registered {
			if method.NumIn() != 3 || method.In(1) != reflect.TypeFor[context.Context]() || method.NumOut() != 2 || method.Out(1) != reflect.TypeFor[error]() {
				return fmt.Errorf("app: method signature mismatch: %s", route)
			}
			if messageTypeName(method.In(2)) != declared.Request || messageTypeName(method.Out(0)) != declared.Response {
				return fmt.Errorf("app: request/response type mismatch: %s", route)
			}
		}
	}
	for route := range methods {
		if strings.HasPrefix(route, pitayawire.ReservedComponent+".") {
			return fmt.Errorf("app: reserved protocol component")
		}
	}
	return nil
}

// 按原生路由汇总本进程实际登记的方法，供启动时核对协议。
func (registry *Registry) methodSignatures() (map[string][]reflect.Type, error) {
	methods := map[string][]reflect.Type{}
	for _, entry := range registry.components {
		nativeService := component.NewService(entry.component, entry.options)
		if entry.remote {
			if err := nativeService.ExtractRemote(); err != nil {
				return nil, err
			}
			for name, registeredMethod := range nativeService.Remotes {
				methods[nativeService.Name+"."+name] = append(methods[nativeService.Name+"."+name], registeredMethod.Method.Type)
			}
		} else {
			if err := nativeService.ExtractHandler(); err != nil {
				return nil, err
			}
			for name, registeredMethod := range nativeService.Handlers {
				methods[nativeService.Name+"."+name] = append(methods[nativeService.Name+"."+name], registeredMethod.Method.Type)
			}
		}
	}
	for name, registeredComponent := range registry.endpoints {
		method, ok := reflect.TypeOf(registeredComponent).MethodByName(endpointGoMethod)
		if ok {
			route := name + "." + EndpointRouteMethod
			methods[route] = append(methods[route], method.Type)
		}
	}
	return methods, nil
}

func messageTypeName(t reflect.Type) string {
	if t.Kind() != reflect.Ptr || t.Elem().Kind() != reflect.Struct {
		return ""
	}
	msg, ok := reflect.New(t.Elem()).Interface().(proto.Message)
	if !ok {
		return ""
	}
	return proto.MessageName(msg)
}
