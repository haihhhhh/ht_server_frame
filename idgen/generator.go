// idgen 为持久化业务实体生成雪花 ID。etcd 只参与启动和关闭，发号在本地完成。
package idgen

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/bwmarrin/snowflake"
	clientv3 "go.etcd.io/etcd/client/v3"
)

// 启动认领和停止释放使用同一组 etcd 键。
const (
	nodeOwnerKey  = "owner"
	nodeLastIDKey = "last-id"
)

// Generator 由进程内的业务服务共享；节点占用没有租约，不会因暂时失联而转移。
type Generator struct {
	mu       sync.Mutex
	node     *snowflake.Node
	client   *clientv3.Client
	key      string
	revision int64
	lastID   int64
	closed   bool
}

// Open 在同一 etcd 集群内独占节点号。节点已被占用、时钟落后或 etcd 不可用都会返回错误。
func Open(ctx context.Context, client *clientv3.Client, nodeID int64, instance string) (*Generator, error) {
	if nodeID < 0 || nodeID > 1023 {
		return nil, fmt.Errorf("idgen: id_node 必须在 0 到 1023 之间，当前为 %d", nodeID)
	}
	if client == nil || instance == "" {
		return nil, errors.New("idgen: 必须提供 etcd 客户端和实例名称")
	}
	node, err := snowflake.NewNode(nodeID)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("/ht/idgen/nodes/%d/", nodeID)
	previous, err := client.Get(ctx, key+nodeLastIDKey)
	if err != nil {
		return nil, fmt.Errorf("读取发号节点 %d 的最后 ID: %w", nodeID, err)
	}
	first := node.Generate().Int64()
	if first <= 0 {
		return nil, fmt.Errorf("发号节点 %d 的系统时间不在雪花 ID 的有效范围内", nodeID)
	}
	var previousRevision int64
	if len(previous.Kvs) > 0 {
		record := previous.Kvs[0]
		last, parseErr := strconv.ParseInt(string(record.Value), 10, 64)
		if parseErr != nil || last <= 0 {
			return nil, fmt.Errorf("发号节点 %d 的最后 ID 无效", nodeID)
		}
		if first <= last {
			return nil, fmt.Errorf("发号节点 %d 的时钟未超过上次发号时间，启动被拒绝", nodeID)
		}
		previousRevision = record.ModRevision
	}
	claim, err := client.Txn(ctx).If(
		clientv3.Compare(clientv3.Version(key+nodeOwnerKey), "=", 0),
		clientv3.Compare(clientv3.ModRevision(key+nodeLastIDKey), "=", previousRevision),
	).Then(clientv3.OpPut(key+nodeOwnerKey, instance)).Commit()
	if err != nil {
		return nil, fmt.Errorf("占用发号节点 %d 未确认: %w", nodeID, err)
	}
	if !claim.Succeeded {
		return nil, fmt.Errorf("发号节点 %d 已被占用或刚刚释放，请检查 %s", nodeID, key+nodeOwnerKey)
	}
	return &Generator{
		node:     node,
		client:   client,
		key:      key,
		revision: claim.Header.Revision,
		lastID:   first,
	}, nil
}

// Next 返回 64 位整数 ID，不访问网络。
func (generator *Generator) Next() (int64, error) {
	generator.mu.Lock()
	defer generator.mu.Unlock()
	if generator.closed {
		return 0, errors.New("idgen: 发号器已关闭")
	}
	next := generator.node.Generate().Int64()
	if next <= generator.lastID {
		return 0, errors.New("idgen: 拒绝发出未递增的 ID")
	}
	generator.lastID = next
	return next, nil
}

// NextString 用十进制字符串承载同一种 ID，适用于字符串主键与客户端协议。
func (generator *Generator) NextString() (string, error) {
	id, err := generator.Next()
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(id, 10), nil
}

// Close 在业务停止后使用。停止本地发号，再原子记录最后 ID 并释放自己的占用。
func (generator *Generator) Close(ctx context.Context) error {
	generator.mu.Lock()
	defer generator.mu.Unlock()
	generator.closed = true
	result, err := generator.client.Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(generator.key+nodeOwnerKey), "=", generator.revision),
	).Then(
		clientv3.OpPut(generator.key+nodeLastIDKey, strconv.FormatInt(generator.lastID, 10)),
		clientv3.OpDelete(generator.key+nodeOwnerKey),
	).Commit()
	if err != nil {
		return fmt.Errorf("释放发号节点 %s 未确认: %w", generator.key, err)
	}
	if !result.Succeeded {
		return fmt.Errorf("发号节点 %s 占用已改变，拒绝释放", generator.key)
	}
	return nil
}
