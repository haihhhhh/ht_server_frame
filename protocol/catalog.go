// protocol 保存从共同定义生成的消息与接口关系，不负责节点分配或网络握手。
package protocol

import (
	"fmt"
	"strings"

	"github.com/golang/protobuf/proto"
)

type Message struct {
	ID   uint16 `json:"id"`
	Name string `json:"name"`
}

type Method struct {
	Service  string `json:"service"`
	Name     string `json:"name"`
	Request  string `json:"request"`
	Response string `json:"response"`
}

type Definition struct {
	Messages []Message `json:"messages"`
	Methods  []Method  `json:"methods"`
}

// Catalog 创建后只读；调用者修改原始定义或查询结果不会改变目录。
type Catalog struct {
	definition Definition
	byID       map[uint16]string
	byName     map[string]uint16
}

func New(def Definition) (*Catalog, error) {
	catalog := &Catalog{
		definition: Definition{append([]Message(nil), def.Messages...), append([]Method(nil), def.Methods...)},
		byID:       make(map[uint16]string),
		byName:     make(map[string]uint16),
	}
	for _, m := range def.Messages {
		if m.ID == ErrorID || m.Name == "" || strings.HasPrefix(m.Name, ".") {
			return nil, fmt.Errorf("protocol: invalid message %q", m.Name)
		}
		if catalog.byID[m.ID] != "" || catalog.byName[m.Name] != 0 {
			return nil, fmt.Errorf("protocol: duplicate message ID or type: %s", m.Name)
		}
		catalog.byID[m.ID], catalog.byName[m.Name] = m.Name, m.ID
	}
	routes, requests := map[string]bool{}, map[string]bool{}
	for _, m := range def.Methods {
		route := m.Service + "." + m.Name
		if m.Service == "" || m.Name == "" || strings.Contains(m.Service, ".") || strings.Contains(m.Name, ".") || catalog.byName[m.Request] == 0 || catalog.byName[m.Response] == 0 {
			return nil, fmt.Errorf("protocol: invalid method %s", route)
		}
		if routes[route] || requests[m.Request] {
			return nil, fmt.Errorf("protocol: duplicate route or request type: %s", route)
		}
		routes[route], requests[m.Request] = true, true
	}
	return catalog, nil
}

func (catalog *Catalog) Definition() Definition {
	return Definition{
		append([]Message(nil), catalog.definition.Messages...),
		append([]Method(nil), catalog.definition.Methods...),
	}
}

func (catalog *Catalog) MessageName(id uint16) (string, bool) {
	name, ok := catalog.byID[id]
	return name, ok
}

// MessageID 按 protobuf 完整类型名查询编号，不依赖请求还是主动推送。
func (catalog *Catalog) MessageID(message proto.Message) (uint16, bool) {
	if message == nil {
		return 0, false
	}
	id, ok := catalog.byName[proto.MessageName(message)]
	return id, ok
}
