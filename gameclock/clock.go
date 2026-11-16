// gameclock 提供可调整的业务时间。超时、定时器和发号器继续使用系统时钟。
package gameclock

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

type setting struct {
	offset   time.Duration
	revision int64
}

// Clock 在本地读取偏移，仅在启动、同步配置和执行管理命令时访问 etcd。
type Clock struct {
	client  *clientv3.Client
	key     string
	current atomic.Pointer[setting]
	cancel  context.CancelFunc
	done    chan struct{}
}

func Open(ctx context.Context, client *clientv3.Client, key string) (*Clock, error) {
	if client == nil || key == "" {
		return nil, errors.New("业务时钟需要 etcd 客户端和配置路径")
	}
	clock := &Clock{client: client, key: key, done: make(chan struct{})}
	revision, err := clock.refresh(ctx)
	if err != nil {
		return nil, fmt.Errorf("读取业务时钟配置: %w", err)
	}
	watchCtx, cancel := context.WithCancel(context.Background())
	clock.cancel = cancel
	go clock.watch(watchCtx, revision+1)
	return clock, nil
}

func (clock *Clock) Offset() time.Duration { return clock.current.Load().offset }

func (clock *Clock) Now() time.Time { return clock.At(time.Now()) }

// At 将同一个真实时刻换算为业务时间，不保留系统单调时钟读数。
func (clock *Clock) At(real time.Time) time.Time { return real.Round(0).Add(clock.Offset()) }

// SetOffset 替换整个环境的偏移。写入成功后本进程立即生效，其他进程通过订阅跟进。
func (clock *Clock) SetOffset(ctx context.Context, offset time.Duration) error {
	reply, err := clock.client.Put(ctx, clock.key, strconv.FormatInt(int64(offset), 10))
	if err != nil {
		return fmt.Errorf("写入业务时间偏移未确认: %w", err)
	}
	clock.apply(reply.Header.Revision, offset)
	return nil
}

func (clock *Clock) Close() { clock.cancel(); <-clock.done }

func (clock *Clock) apply(revision int64, offset time.Duration) {
	next := &setting{offset: offset, revision: revision}
	for {
		old := clock.current.Load()
		if old != nil && old.revision >= revision {
			return
		}
		if clock.current.CompareAndSwap(old, next) {
			return
		}
	}
}

func decode(value []byte) (time.Duration, error) {
	n, err := strconv.ParseInt(string(value), 10, 64)
	if err != nil {
		return 0, errors.New("业务时间偏移不是有效的纳秒数")
	}
	return time.Duration(n), nil
}

func (clock *Clock) refresh(ctx context.Context) (int64, error) {
	reply, err := clock.client.Get(ctx, clock.key)
	if err != nil {
		return 0, err
	}
	var offset time.Duration
	if len(reply.Kvs) > 0 {
		offset, err = decode(reply.Kvs[0].Value)
		if err != nil {
			return 0, err
		}
	}
	clock.apply(reply.Header.Revision, offset)
	return reply.Header.Revision, nil
}

func (clock *Clock) watch(ctx context.Context, revision int64) {
	defer close(clock.done)
	for ctx.Err() == nil {
		for response := range clock.client.Watch(ctx, clock.key, clientv3.WithRev(revision)) {
			if response.Err() != nil {
				break
			}
			for _, event := range response.Events {
				var offset time.Duration
				if event.Type != clientv3.EventTypeDelete {
					value, err := decode(event.Kv.Value)
					if err != nil {
						slog.Error("忽略无效的业务时钟配置", "revision", event.Kv.ModRevision)
						continue
					}
					offset = value
				}
				clock.apply(event.Kv.ModRevision, offset)
			}
		}
		// 订阅中断或历史版本被压缩后，先取最新值，再从紧随其后的版本继续订阅。
		for ctx.Err() == nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			latest, err := clock.refresh(readCtx)
			cancel()
			if err == nil {
				revision = latest + 1
				break
			}
		}
	}
}
