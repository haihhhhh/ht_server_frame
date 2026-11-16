// client 是有截止时间和请求次数上限的样板客户端，不是生产 SDK。
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/golang/protobuf/proto"
	"github.com/golang/protobuf/ptypes/wrappers"
	"github.com/gorilla/websocket"
	"github.com/haihhhhh/ht_server_frame/examples/protocol/generated"
	contracts "github.com/haihhhhh/ht_server_frame/protocol"
	"github.com/topfreegames/pitaya/v2/conn/codec"
	"github.com/topfreegames/pitaya/v2/conn/message"
	"github.com/topfreegames/pitaya/v2/conn/packet"
	"github.com/topfreegames/pitaya/v2/protos"
)

const (
	helloRoute    = "hello.handle"
	greetingRoute = "Greeting.Hello"
	ioTimeout     = 5 * time.Second
)

func run() error {
	catalog, err := protocol.NewProtocolCatalog()
	if err != nil {
		return err
	}
	addr := flag.String("url", "ws://127.0.0.1:3250/", "服务地址")
	route := flag.String("route", "echo.handle", "接口名称；hello.handle 使用独立消息类型，counter.handle 返回整数，其余为通用字符串")
	value := flag.String("value", "hello", "消息文本或测试计数器 key")
	ticket := flag.String("ticket", "", "可选本机测试凭据；先登录再请求")
	count := flag.Int("count", 1, "同一连接上顺序调用目标接口的次数，范围 1 到 100；失败即停止，不重试")
	actions := flag.String("actions", "", "player.handle 同一连接执行的操作，例如 enter,add,current,leave；不能与 count 同用")
	flag.Parse()
	if *count < 1 || *count > 100 {
		return fmt.Errorf("count 必须在 1 到 100 之间")
	}
	values := make([]string, *count)
	for i := range values {
		values[i] = *value
	}
	if *actions != "" {
		if *route != "player.handle" || *count != 1 || len(*actions) > 1024 {
			return fmt.Errorf("actions 仅用于 player.handle，count 必须为 1，长度不超过 1024")
		}
		values = strings.Split(*actions, ",")
		if len(values) > 100 {
			return fmt.Errorf("actions 最多 100 项")
		}
		for _, action := range values {
			switch action {
			case "enter", "add", "current", "leave", "where":
			default:
				return fmt.Errorf("actions 只接受 enter、add、current、leave、where（集群归属查询）")
			}
		}
	}
	d := websocket.Dialer{HandshakeTimeout: 3 * time.Second}
	c, _, err := d.Dial(*addr, nil)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetReadLimit(64 * 1024)
	if err = c.SetReadDeadline(time.Now().Add(ioTimeout)); err != nil {
		return err
	}
	if err = c.SetWriteDeadline(time.Now().Add(ioTimeout)); err != nil {
		return err
	}
	write := func(kind packet.Type, data []byte) error {
		b, err := codec.NewPomeloPacketEncoder().Encode(kind, data)
		if err != nil {
			return err
		}
		return c.WriteMessage(websocket.BinaryMessage, b)
	}
	read := func() (*packet.Packet, error) {
		_, b, err := c.ReadMessage()
		if err != nil {
			return nil, err
		}
		ps, err := codec.NewPomeloPacketDecoder().Decode(b)
		if err != nil {
			return nil, err
		}
		if len(ps) != 1 {
			return nil, fmt.Errorf("预期单个协议包")
		}
		return ps[0], nil
	}
	if err = write(packet.Handshake, []byte(`{"sys":{"type":"go-test","version":"1"},"user":{}}`)); err != nil {
		return err
	}
	p, err := read()
	if err != nil {
		return err
	}
	if p.Type != packet.Handshake {
		return fmt.Errorf("握手类型错误")
	}
	if err = write(packet.HandshakeAck, nil); err != nil {
		return err
	}
	call := func(route, value string, id uint) ([]byte, error) {
		deadline := time.Now().Add(ioTimeout)
		if err := c.SetReadDeadline(deadline); err != nil {
			return nil, err
		}
		if err := c.SetWriteDeadline(deadline); err != nil {
			return nil, err
		}
		var request proto.Message = &wrappers.StringValue{Value: value}
		if route == helloRoute || route == greetingRoute {
			request = &protocol.HelloRequest{Name: value}
		}
		body, err := proto.Marshal(request)
		if err != nil {
			return nil, err
		}
		msg, err := message.NewMessagesEncoder(false).Encode(&message.Message{
			Type:  message.Request,
			ID:    id,
			Route: route,
			Data:  body,
		})
		if route == greetingRoute {
			msg, err = catalog.EncodeRequest(id, request)
		}
		if err != nil {
			return nil, err
		}
		if err = write(packet.Data, msg); err != nil {
			return nil, err
		}
		for {
			p, err = read()
			if err != nil {
				return nil, err
			}
			if p.Type == packet.Heartbeat {
				if err = write(packet.Heartbeat, nil); err != nil {
					return nil, err
				}
				continue
			}
			if p.Type != packet.Data {
				return nil, fmt.Errorf("响应包类型错误")
			}
			var m *message.Message
			kind, _, headerErr := contracts.Header(p.Data)
			if headerErr != nil {
				return nil, headerErr
			}
			if kind == message.Push || p.Data[0]&contracts.FlagError != 0 || route == greetingRoute || route == helloRoute {
				m, _, err = catalog.DecodeServer(p.Data)
			} else {
				m, err = message.Decode(p.Data)
			}
			if err != nil {
				return nil, err
			}
			if m.Type == message.Push {
				if m.Err || m.Route != "ht.example.v1.Notification" {
					return nil, fmt.Errorf("无法处理的推送路由：%s", m.Route)
				}
				var notice protocol.Notification
				if err := proto.Unmarshal(m.Data, &notice); err != nil {
					return nil, err
				}
				fmt.Printf("收到服务端通知：%s\n", notice.Text)
				continue
			}
			if m.Type != message.Response || m.ID != id {
				return nil, fmt.Errorf("响应编号或类型错误")
			}
			if m.Err {
				var problem protos.Error
				if err = proto.Unmarshal(m.Data, &problem); err != nil {
					return nil, err
				}
				return nil, fmt.Errorf("请求失败: %s", problem.Code)
			}
			return m.Data, nil
		}
	}
	if *ticket != "" {
		if _, err = call("login.handle", *ticket, 1); err != nil {
			return err
		}
	}
	for i, value := range values {
		data, err := call(*route, value, uint(i+2))
		if err != nil {
			return fmt.Errorf("第 %d 次请求失败: %w", i+1, err)
		}
		if *route == helloRoute || *route == greetingRoute {
			var out protocol.HelloResponse
			if err = proto.Unmarshal(data, &out); err != nil {
				return err
			}
			fmt.Println(out.Greeting)
		} else if *route == "counter.handle" {
			var out wrappers.Int64Value
			if err = proto.Unmarshal(data, &out); err != nil {
				return err
			}
			fmt.Println(out.Value)
		} else {
			var out wrappers.StringValue
			if err = proto.Unmarshal(data, &out); err != nil {
				return err
			}
			fmt.Println(out.Value)
		}
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
