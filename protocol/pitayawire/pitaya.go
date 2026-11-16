package pitayawire

import (
	"context"
	"encoding/binary"

	"github.com/golang/protobuf/proto"
	contracts "github.com/haihhhhh/ht_server_frame/protocol"
	pitaya "github.com/topfreegames/pitaya/v2"
	"github.com/topfreegames/pitaya/v2/protos"
)

// Bindings 保存协议目录和每个组件所在的服务类型，启动时核对路由，收发包时读取。
type Bindings struct {
	Catalog  *contracts.Catalog
	Services map[string]string
}

// Attach 始终为入站消息设置解压上限；配置了协议目录后，再接入数字路由和响应编码。
func Attach(builder *pitaya.Builder, bindings *Bindings, serverType string, maxMessageBytes int64, publicError func(error) error) {
	decoder := &protocolDecoder{
		PacketDecoder:   builder.PacketDecoder,
		maxMessageBytes: maxMessageBytes,
	}
	builder.PacketDecoder = decoder
	if bindings == nil {
		return
	}
	catalog := bindings.Catalog
	routes, pushes := map[uint16]string{}, map[string]uint16{}
	definition := catalog.Definition()
	for _, declaredMessage := range definition.Messages {
		pushes[declaredMessage.Name] = declaredMessage.ID
	}
	for _, method := range definition.Methods {
		for _, declaredMessage := range definition.Messages {
			if declaredMessage.Name == method.Request {
				route := method.Service + "." + method.Name
				if target := bindings.Services[method.Service]; target != serverType {
					route = target + "." + route
				}
				routes[declaredMessage.ID] = route
			}
		}
	}
	decoder.routes = routes
	builder.PacketEncoder = &protocolEncoder{
		PacketEncoder: builder.PacketEncoder,
		pushes:        pushes,
	}
	builder.HandlerHooks.AfterHandler.PushBack(func(ctx context.Context, result interface{}, err error) (interface{}, error) {
		if err != nil {
			return nil, publicError(err)
		}
		if problem, ok := result.(*protos.Error); ok {
			body, marshalErr := proto.Marshal(problem)
			return append(binary.BigEndian.AppendUint16(nil, contracts.ErrorID), body...), marshalErr
		}
		value, ok := result.(proto.Message)
		if !ok {
			return result, nil
		}
		// 未声明的旧样板接口保持原载荷；正式协议方法已在启动前核对响应类型。
		if _, ok := catalog.MessageID(value); !ok {
			return result, nil
		}
		return catalog.EncodeResponse(value)
	})
}
