package app

import (
	"context"
	"fmt"

	"github.com/haihhhhh/ht_server_frame/app/internal/registration"
	"github.com/haihhhhh/ht_server_frame/errorcode"
	"github.com/haihhhhh/ht_server_frame/host"
	contracts "github.com/haihhhhh/ht_server_frame/protocol"
	"github.com/topfreegames/pitaya/v2/component"
	"github.com/topfreegames/pitaya/v2/protos"
)

// UnknownProtocol 处理网关收到的未知编号。Pitaya 注册组件时要求接收类型导出。
type UnknownProtocol struct{ component.Base }

// Unknown 返回可识别的错误对象，由出口设置原生错误标记；避免逐条触发上游错误日志。
func (*UnknownProtocol) Unknown(context.Context, []byte) (*protos.Error, error) {
	return &protos.Error{
		Code: errorcode.UnknownMessage,
		Msg:  "unknown message ID",
	}, nil
}

// ConfigureProtocol 指定共同定义中各组件部署的服务类型，不把部署位置写进 proto。
// Start 前核对本服务的方法和消息类型；不代表远端实例已部署相同定义。
// 启用静态数字协议，不向握手添加字典；客户端须使用相同生成目录。
func (application *App) ConfigureProtocol(catalog *contracts.Catalog, services map[string]string) error {
	application.mu.Lock()
	defer application.mu.Unlock()
	if application.started {
		return host.ErrStarted
	}
	if catalog == nil || application.protocol != nil {
		return fmt.Errorf("app: protocol missing or already configured")
	}
	bindings, err := registration.BindProtocol(catalog, services)
	if err != nil {
		return err
	}
	application.protocol = bindings
	return nil
}
