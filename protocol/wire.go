package protocol

import (
	"encoding/binary"
	"fmt"
	"strconv"

	"github.com/golang/protobuf/proto"
	"github.com/topfreegames/pitaya/v2/conn/message"
)

const (
	// ErrorID 仅表示框架错误载荷，不属于业务消息目录。
	ErrorID uint16 = 0

	// Pitaya 消息头首字节的标记位。
	FlagNumericRoute   = 0x01
	FlagCompressedBody = 0x10
	FlagError          = 0x20

	// MaxRouteBytes 是消息头中单字节长度字段能表示的最长字符串路由。
	MaxRouteBytes = 255
)

// Header 检查原生消息头，返回请求序号后、路由前的位置。
// 先检查再调用上游解码器，避免截断的路由和无限长序号进入上游。
func Header(data []byte) (message.Type, int, error) {
	if len(data) < 2 {
		return 0, 0, message.ErrInvalidMessage
	}
	t := message.Type((data[0] >> 1) & 7)
	if t > message.Push {
		return 0, 0, message.ErrWrongMessageType
	}
	p := 1
	if t == message.Request || t == message.Response {
		for n := 0; ; n++ {
			if p >= len(data) || n >= (strconv.IntSize+6)/7 {
				return 0, 0, message.ErrInvalidMessage
			}
			b := data[p]
			p++
			if n == (strconv.IntSize+6)/7-1 && b >= 1<<uint(strconv.IntSize-n*7) {
				return 0, 0, message.ErrInvalidMessage
			}
			if b < 128 {
				break
			}
		}
	}
	if t != message.Response {
		if p >= len(data) {
			return 0, 0, message.ErrInvalidMessage
		}
		n := 2
		if data[0]&FlagNumericRoute == 0 {
			n = 1 + int(data[p])
		}
		if len(data)-p < n {
			return 0, 0, message.ErrInvalidMessage
		}
	}
	return t, p, nil
}

// ReplaceRoute 只替换路由字节，保留请求序号、错误位、压缩位与载荷。
func ReplaceRoute(data []byte, route string, id uint16, numeric bool) ([]byte, error) {
	t, p, err := Header(data)
	if err != nil {
		return nil, err
	}
	if t == message.Response || (!numeric && (len(route) == 0 || len(route) > MaxRouteBytes)) {
		return nil, message.ErrInvalidMessage
	}
	n := 2
	if data[0]&FlagNumericRoute == 0 {
		n = 1 + int(data[p])
	}
	out := append([]byte(nil), data[:p]...)
	if numeric {
		out[0] |= FlagNumericRoute
		out = binary.BigEndian.AppendUint16(out, id)
	} else {
		out[0] &^= FlagNumericRoute
		out = append(out, byte(len(route)))
		out = append(out, route...)
	}
	return append(out, data[p+n:]...), nil
}

// EncodeRequest 业务只传消息对象；请求序号由客户端网络层传入。
func (catalog *Catalog) EncodeRequest(seq uint, value proto.Message) ([]byte, error) {
	id, ok := catalog.MessageID(value)
	if !ok {
		return nil, fmt.Errorf("protocol: unregistered request")
	}
	name, _ := catalog.MessageName(id)
	declared := false
	for _, m := range catalog.definition.Methods {
		if m.Request == name {
			declared = true
			break
		}
	}
	if !declared {
		return nil, fmt.Errorf("protocol: message has no request method")
	}
	body, err := proto.Marshal(value)
	if err != nil {
		return nil, err
	}
	t := message.Request
	if seq == 0 {
		t = message.Notify
	}
	data, err := message.NewMessagesEncoder(false).Encode(&message.Message{
		Type:  t,
		ID:    seq,
		Route: "_",
		Data:  body,
	})
	if err != nil {
		return nil, err
	}
	return ReplaceRoute(data, "", id, true)
}

// EncodeResponse 在 protobuf 载荷前添加消息编号，不改 Pitaya 请求序号。
func (catalog *Catalog) EncodeResponse(value proto.Message) ([]byte, error) {
	id, ok := catalog.MessageID(value)
	if !ok {
		return nil, fmt.Errorf("protocol: unregistered response")
	}
	body, err := proto.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(binary.BigEndian.AppendUint16(nil, id), body...), nil
}

// DecodeServer 解开响应/推送编号；错误仍交给调用方按原生错误格式处理。
func (catalog *Catalog) DecodeServer(data []byte) (*message.Message, uint16, error) {
	t, p, err := Header(data)
	if err != nil {
		return nil, 0, err
	}
	var id uint16
	if t == message.Push && data[0]&FlagNumericRoute != 0 {
		id = binary.BigEndian.Uint16(data[p:])
		name, ok := catalog.MessageName(id)
		if !ok {
			return nil, id, fmt.Errorf("protocol: unknown push ID %d", id)
		}
		data, err = ReplaceRoute(data, name, 0, false)
		if err != nil {
			return nil, id, err
		}
	}
	m, err := message.Decode(data)
	if err != nil {
		return nil, id, err
	}
	if t == message.Response {
		if len(m.Data) < 2 {
			return nil, 0, message.ErrInvalidMessage
		}
		id = binary.BigEndian.Uint16(m.Data)
		m.Data = m.Data[2:]
		if id == ErrorID && !m.Err {
			return nil, id, fmt.Errorf("protocol: error ID without error flag")
		}
		if id != ErrorID {
			if _, ok := catalog.MessageName(id); !ok {
				return nil, id, fmt.Errorf("protocol: unknown response ID %d", id)
			}
		}
	}
	return m, id, nil
}
