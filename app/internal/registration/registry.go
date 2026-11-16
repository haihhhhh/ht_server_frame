package registration

import (
	"errors"
	"reflect"

	"github.com/golang/protobuf/proto"
	"github.com/haihhhhh/ht_server_frame/execution"
	pitaya "github.com/topfreegames/pitaya/v2"
	"github.com/topfreegames/pitaya/v2/component"
)

const (
	// EndpointRouteMethod 是队列入口登记在 Pitaya 路由中的方法名。
	EndpointRouteMethod = "handle"
	// 反射按 Go 方法的导出名称查找签名。
	endpointGoMethod = "Handle"
)

// Registry 保存启动前登记的原生组件和队列入口，由 App 在启动前串行修改。
type Registry struct {
	components []nativeComponent
	endpoints  map[string]component.Component
}

type nativeComponent struct {
	name      string
	component component.Component
	options   []component.Option
	remote    bool
}

// AddComponent 核对原生组件的方法和重名情况。Handler 与 Remote 各自占用自己的名称。
func (registry *Registry) AddComponent(registeredComponent component.Component, remote, backend bool, opts []component.Option) error {
	componentValue := reflect.ValueOf(registeredComponent)
	if !componentValue.IsValid() || componentValue.Kind() != reflect.Ptr || componentValue.IsNil() || componentValue.Elem().Kind() != reflect.Struct {
		return execution.ErrInvalid
	}
	if !remote && backend {
		return errors.New("app: backend has no client acceptor; use RegisterRemote")
	}
	options := append([]component.Option(nil), opts...)
	nativeService := component.NewService(registeredComponent, options)
	if !ValidName(nativeService.Name) {
		return execution.ErrInvalid
	}
	var err error
	if remote {
		err = nativeService.ExtractRemote()
	} else {
		err = nativeService.ExtractHandler()
	}
	if err != nil {
		return err
	}
	for name := range nativeService.Handlers {
		if !ValidName(name) {
			return execution.ErrInvalid
		}
	}
	for name := range nativeService.Remotes {
		if !ValidName(name) {
			return execution.ErrInvalid
		}
	}
	for _, entry := range registry.components {
		if entry.name == nativeService.Name && entry.remote == remote {
			return errors.New("app: duplicate component")
		}
	}
	if _, exists := registry.endpoints[nativeService.Name]; exists && remote == backend {
		return errors.New("app: duplicate component")
	}
	registry.components = append(registry.components, nativeComponent{
		name:      nativeService.Name,
		component: registeredComponent,
		options:   options,
		remote:    remote,
	})
	return nil
}

// AddEndpoint 登记固定的 name.handle 路由，后端通过 Remote 接收同一个队列入口。
func AddEndpoint[Q proto.Message, R proto.Message](registry *Registry, name string, endpoint component.Component, backend bool) error {
	if !ValidName(name) {
		return execution.ErrInvalid
	}
	for _, entry := range registry.components {
		if entry.name == name && entry.remote == backend {
			return errors.New("app: duplicate component")
		}
	}
	if _, exists := registry.endpoints[name]; exists {
		return errors.New("app: duplicate endpoint")
	}
	if reflect.TypeFor[Q]().Kind() != reflect.Ptr || reflect.TypeFor[R]().Kind() != reflect.Ptr {
		return execution.ErrInvalid
	}
	nativeService := component.NewService(endpoint, []component.Option{component.WithName(name)})
	if err := nativeService.ExtractHandler(); err != nil {
		return err
	}
	if err := nativeService.ExtractRemote(); err != nil {
		return err
	}
	if registry.endpoints == nil {
		registry.endpoints = make(map[string]component.Component)
	}
	registry.endpoints[name] = endpoint
	return nil
}

// Install 在 Pitaya 启动前注册组件，保留手写组件的登记顺序和自定义选项。
func (registry *Registry) Install(native pitaya.Pitaya, frontend bool) {
	for _, entry := range registry.components {
		if entry.remote {
			native.RegisterRemote(entry.component, entry.options...)
		} else {
			native.Register(entry.component, entry.options...)
		}
	}
	for name, endpoint := range registry.endpoints {
		opts := []component.Option{
			component.WithName(name),
			component.WithNameFunc(func(string) string {
				return EndpointRouteMethod
			}),
		}
		if frontend {
			native.Register(endpoint, opts...)
		} else {
			native.RegisterRemote(endpoint, opts...)
		}
	}
}

// ValidName 沿用区分大小写的名称规则，允许字母、数字和下划线。
func ValidName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}
