package pitayawire

import (
	"bytes"
	"compress/zlib"
	"io"

	contracts "github.com/haihhhhh/ht_server_frame/protocol"
	"github.com/topfreegames/pitaya/v2/conn/codec"
	"github.com/topfreegames/pitaya/v2/conn/message"
)

// 按客户端实际发送的包头计算正文预算，内部路由名可能比线上的两字节编号长。
func (decoder *protocolDecoder) inflateMessage(data []byte, messageType message.Type, routeOffset int) ([]byte, error) {
	bodyOffset := routeOffset
	if messageType != message.Response {
		if data[0]&contracts.FlagNumericRoute != 0 {
			bodyOffset += 2
		} else {
			bodyOffset += 1 + int(data[routeOffset])
		}
	}
	bodyLimit := decoder.maxMessageBytes - codec.HeadLength - int64(bodyOffset)
	if bodyLimit < 0 {
		return nil, codec.ErrPacketSizeExcced
	}

	var expanded bytes.Buffer
	expanded.Write(data[:bodyOffset])
	header := expanded.Bytes()
	header[0] &^= contracts.FlagCompressedBody
	if decoder.routes == nil && messageType != message.Response && header[0]&contracts.FlagNumericRoute != 0 {
		// 沿用 Pitaya 自己的字典时，先让它检查编号，未知路由不展开正文。
		if _, err := message.Decode(header); err != nil {
			return nil, err
		}
	}
	reader, err := zlib.NewReader(bytes.NewReader(data[bodyOffset:]))
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	// 多读一个字节区分恰好达限和超限；正常读到流末尾时 zlib 也会核对校验和。
	size, err := expanded.ReadFrom(io.LimitReader(reader, bodyLimit+1))
	if size > bodyLimit {
		return nil, codec.ErrPacketSizeExcced
	}
	if err != nil {
		return nil, err
	}
	return expanded.Bytes(), nil
}
