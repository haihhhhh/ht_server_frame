// placement 包记录项目定义的状态范围由哪个后端负责，不负责故障接管。
package placement

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

var (
	ErrInvalid = errors.New("placement: invalid argument")
	ErrCorrupt = errors.New("placement: invalid stored assignment")
)

// Assignment 标识一次分配。释放时必须使用当时取得的完整记录。
type Assignment struct {
	Scope        string `json:"scope"`
	ServerID     string `json:"server_id"`
	AllocationID string `json:"allocation_id"`
	Revision     int64  `json:"-"` // etcd 记录版本，仅用于区分先后观察，不是数据库写入权限。
}

// Registry 使用独立键空间，不修改 Pitaya 的服务发现记录。
// 连接由调用方创建和关闭；记录不绑租约，不随节点失联自动消失。
type Registry struct {
	kv      clientv3.KV
	prefix  string
	timeout time.Duration
}

func New(kv clientv3.KV, prefix string, timeout time.Duration) (*Registry, error) {
	prefix = strings.TrimRight(prefix, "/")
	if kv == nil || !strings.HasPrefix(prefix, "/") || len(prefix) < 2 || len(prefix) > 512 || timeout <= 0 {
		return nil, ErrInvalid
	}
	return &Registry{
		kv:      kv,
		prefix:  prefix + "/",
		timeout: timeout,
	}, nil
}

func (registry *Registry) key(scope string) (string, error) {
	if scope == "" || len(scope) > 512 {
		return "", ErrInvalid
	}
	return registry.prefix + base64.RawURLEncoding.EncodeToString([]byte(scope)), nil
}

func decode(scope string, data []byte) (Assignment, error) {
	var assignment Assignment
	if len(data) > 4096 || json.Unmarshal(data, &assignment) != nil || assignment.Scope != scope || assignment.ServerID == "" || len(assignment.ServerID) > 256 || assignment.AllocationID == "" {
		return Assignment{}, ErrCorrupt
	}
	return assignment, nil
}

// Lookup 不参考在线节点列表；原负责人失联仍返回原归属。
func (registry *Registry) Lookup(ctx context.Context, scope string) (Assignment, bool, error) {
	key, err := registry.key(scope)
	if err != nil {
		return Assignment{}, false, err
	}
	ctx, cancel := context.WithTimeout(ctx, registry.timeout)
	defer cancel()
	resp, err := registry.kv.Get(ctx, key)
	if err != nil {
		return Assignment{}, false, err
	}
	if len(resp.Kvs) == 0 {
		return Assignment{}, false, nil
	}
	a, err := decode(scope, resp.Kvs[0].Value)
	a.Revision = resp.Kvs[0].ModRevision
	return a, err == nil, err
}

// NewCandidate 为一次首次分配生成标识，调用方可以在发 RPC 前保存它。此处不写 etcd。
func NewCandidate(scope, serverID string) Assignment {
	return Assignment{Scope: scope, ServerID: serverID, AllocationID: rand.Text()}
}

// Assign 保留原有的直接登记接口。需要接收方先确认容量的服务使用 Claim。
func (registry *Registry) Assign(ctx context.Context, scope, target string) (Assignment, error) {
	return registry.register(ctx, NewCandidate(scope, target), false)
}

// Claim 由接收节点在预留本地位置后调用。竞争输了返回实际归属，不能据此执行业务。
// 返回错误后可用同一候选号 Release；即使登记事务迟到，也不能在撤销后重新建归属。
func (registry *Registry) Claim(ctx context.Context, candidate Assignment) (Assignment, error) {
	return registry.register(ctx, candidate, true)
}

func permitKey(scopeKey, allocationID string) string {
	return scopeKey + "/claims/" + base64.RawURLEncoding.EncodeToString([]byte(allocationID))
}

func (registry *Registry) register(ctx context.Context, assignment Assignment, claim bool) (Assignment, error) {
	key, err := registry.key(assignment.Scope)
	if err != nil {
		return Assignment{}, err
	}
	if assignment.ServerID == "" || len(assignment.ServerID) > 256 || strings.TrimSpace(assignment.ServerID) != assignment.ServerID ||
		assignment.AllocationID == "" || len(assignment.AllocationID) > 128 || assignment.Revision != 0 {
		return Assignment{}, ErrInvalid
	}
	data, err := json.Marshal(assignment)
	if err != nil {
		return Assignment{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, registry.timeout)
	defer cancel()
	conditions := []clientv3.Cmp{clientv3.Compare(clientv3.Version(key), "=", 0)}
	win := []clientv3.Op{clientv3.OpPut(key, string(data))}
	lose := []clientv3.Op{clientv3.OpGet(key)}
	if claim {
		// 临时许可只保护登记这一笔：确认许可已写入才发送登记事务，正式归属永不过期。
		// 若写许可本身超时，迟到的许可会过期，且没有后续登记事务。
		leases, ok := registry.kv.(interface {
			Grant(context.Context, int64) (*clientv3.LeaseGrantResponse, error)
		})
		if !ok {
			return Assignment{}, errors.New("placement: Claim requires an etcd client with leases")
		}
		lease, err := leases.Grant(ctx, int64(registry.timeout/time.Second)+10)
		if err != nil {
			return Assignment{}, err
		}
		permit := permitKey(key, assignment.AllocationID)
		if _, err := registry.kv.Put(ctx, permit, string(data), clientv3.WithLease(lease.ID)); err != nil {
			return Assignment{}, err
		}
		conditions = append(conditions, clientv3.Compare(clientv3.Value(permit), "=", string(data)))
		win = append(win, clientv3.OpDelete(permit))
		lose = append(lose, clientv3.OpDelete(permit))
	}
	resp, err := registry.kv.Txn(ctx).If(conditions...).Then(win...).Else(lose...).Commit()
	if err != nil {
		return Assignment{}, err
	}
	if resp.Succeeded {
		assignment.Revision = resp.Header.Revision
		return assignment, nil
	}
	if len(resp.Responses) == 0 || resp.Responses[0].GetResponseRange() == nil {
		return Assignment{}, ErrCorrupt
	}
	rows := resp.Responses[0].GetResponseRange().Kvs
	if len(rows) == 0 && claim {
		return Assignment{}, errors.New("placement: claim permission expired or was withdrawn")
	}
	if len(rows) != 1 {
		return Assignment{}, ErrCorrupt
	}
	kv := rows[0]
	assignment, err = decode(assignment.Scope, kv.Value)
	assignment.Revision = kv.ModRevision
	return assignment, err
}

// Release 只删除匹配的这次分配；旧退出通知不能删除后来建立的归属。
// 项目应先结束旧业务和保存收尾再调用；本方法不隔离已经发出的数据库写入。
func (registry *Registry) Release(ctx context.Context, assignment Assignment) (bool, error) {
	key, err := registry.key(assignment.Scope)
	if err != nil || assignment.ServerID == "" || assignment.AllocationID == "" {
		return false, ErrInvalid
	}
	data, err := json.Marshal(assignment)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, registry.timeout)
	defer cancel()
	revokePermit := clientv3.OpDelete(permitKey(key, assignment.AllocationID))
	resp, err := registry.kv.Txn(ctx).
		If(clientv3.Compare(clientv3.Value(key), "=", string(data))).
		Then(clientv3.OpDelete(key), revokePermit).
		Else(revokePermit).Commit()
	if err != nil {
		return false, err
	}
	return resp.Succeeded, nil
}
