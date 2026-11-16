package pitayawire

import (
	"encoding/binary"
	"fmt"

	contracts "github.com/haihhhhh/ht_server_frame/protocol"
	"github.com/topfreegames/pitaya/v2/conn/codec"
	"github.com/topfreegames/pitaya/v2/conn/message"
	"github.com/topfreegames/pitaya/v2/conn/packet"
)

type protocolEncoder struct {
	codec.PacketEncoder
	pushes map[string]uint16
}

func (encoder *protocolEncoder) Encode(packetType packet.Type, data []byte) ([]byte, error) {
	if packetType == packet.Data {
		messageType, bodyOffset, err := contracts.Header(data)
		if err != nil {
			return nil, err
		}
		if messageType == message.Response {
			if data[0]&contracts.FlagCompressedBody != 0 {
				return nil, fmt.Errorf("app: numeric protocol requires uncompressed server messages")
			}
			if data[0]&contracts.FlagError != 0 {
				// 上游在反序列化、路由和业务错误时绕过成功响应回调。
				buf := append([]byte(nil), data[:bodyOffset]...)
				data = append(binary.BigEndian.AppendUint16(buf, contracts.ErrorID), data[bodyOffset:]...)
			} else if len(data)-bodyOffset >= 2 && binary.BigEndian.Uint16(data[bodyOffset:]) == contracts.ErrorID {
				data = append([]byte(nil), data...)
				data[0] |= contracts.FlagError
			}
		} else if messageType == message.Push {
			if data[0]&contracts.FlagNumericRoute != 0 {
				return nil, fmt.Errorf("app: native route dictionary must remain empty")
			}
			route := string(data[bodyOffset+1 : bodyOffset+1+int(data[bodyOffset])])
			id, ok := encoder.pushes[route]
			if !ok {
				return nil, fmt.Errorf("app: push message is not in protocol catalog")
			}
			data, err = contracts.ReplaceRoute(data, "", id, true)
			if err != nil {
				return nil, err
			}
		}
	}
	return encoder.PacketEncoder.Encode(packetType, data)
}
