package discovery

import (
	"errors"
	"sort"

	pitaya "github.com/topfreegames/pitaya/v2"
	"github.com/topfreegames/pitaya/v2/constants"
)

// ServerIDs 从 Pitaya 的本地发现表读取指定类型的节点，按 ID 排序供调用方选点。
func ServerIDs(native pitaya.Pitaya, serverType string) ([]string, error) {
	servers, err := native.GetServersByType(serverType)
	if errors.Is(err, constants.ErrNoServersAvailableOfType) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(servers))
	for id := range servers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// HasServer 按 ID 查本地发现表，确认目标节点属于定向 RPC 要求的服务类型。
func HasServer(native pitaya.Pitaya, serverType, serverID string) bool {
	server, err := native.GetServerByID(serverID)
	return err == nil && server != nil && server.Type == serverType
}
