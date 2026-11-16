package pitayawire

import (
	"encoding/binary"
	"log/slog"
	"sync/atomic"
	"time"

	contracts "github.com/haihhhhh/ht_server_frame/protocol"
	"github.com/topfreegames/pitaya/v2/conn/codec"
	"github.com/topfreegames/pitaya/v2/conn/message"
	"github.com/topfreegames/pitaya/v2/conn/packet"
)

// ReservedComponent 是网关登记未知编号处理方法时使用的名字。
const ReservedComponent = "HTProtocol"

const unknownProtocolRoute = ReservedComponent + ".Unknown"

type protocolDecoder struct {
	codec.PacketDecoder
	routes          map[uint16]string
	maxMessageBytes int64
	lastLog         atomic.Int64
}

// 【通常无需修改｜网络入口】把客户端消息编号转换成内部路由，之后由 Pitaya 解码并调用已注册组件；这里不执行业务。
func (decoder *protocolDecoder) Decode(data []byte) ([]*packet.Packet, error) {
	packets, decodeErr := decoder.PacketDecoder.Decode(data)
	if decodeErr != nil {
		return nil, decodeErr
	}
	decodedPackets := packets[:0]
	for _, currentPacket := range packets {
		if currentPacket.Type == packet.Data {
			payload, err := decoder.decodeMessage(currentPacket.Data)
			if err != nil {
				return nil, err
			}
			if payload == nil {
				continue
			}
			currentPacket.Data = payload
			currentPacket.Length = len(payload)
		}
		decodedPackets = append(decodedPackets, currentPacket)
	}
	return decodedPackets, nil
}

func (decoder *protocolDecoder) decodeMessage(data []byte) ([]byte, error) {
	if decoder.routes == nil && len(data) > 0 && data[0]&contracts.FlagCompressedBody == 0 {
		return data, nil
	}
	messageType, routeOffset, err := contracts.Header(data)
	if err != nil {
		return nil, err
	}
	var route string
	if decoder.routes != nil {
		if messageType != message.Request && messageType != message.Notify {
			return nil, message.ErrWrongMessageType
		}
		if data[0]&contracts.FlagNumericRoute != 0 {
			id := binary.BigEndian.Uint16(data[routeOffset:])
			route = decoder.routes[id]
			if route == "" {
				now, old := time.Now().Unix(), decoder.lastLog.Load()
				if now-old >= 5 && decoder.lastLog.CompareAndSwap(old, now) {
					slog.Warn("未知消息编号", "message_id", id)
				}
				if messageType == message.Notify {
					return nil, nil
				}
				// 未知请求仅保留序号，不读正文，连损坏的压缩流也走原有错误入口。
				route = unknownProtocolRoute
				data = append([]byte(nil), data[:routeOffset+2]...)
				data[0] &^= contracts.FlagCompressedBody
			}
		}
	}
	if data[0]&contracts.FlagCompressedBody != 0 {
		data, err = decoder.inflateMessage(data, messageType, routeOffset)
		if err != nil {
			return nil, err
		}
	}
	if route != "" {
		return contracts.ReplaceRoute(data, route, 0, false)
	}
	return data, nil
}
