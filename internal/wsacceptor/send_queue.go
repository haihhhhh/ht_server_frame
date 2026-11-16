package wsacceptor

import (
	"io"
	"log/slog"
	"net"
	"runtime/debug"
	"sync"
	"time"

	"github.com/topfreegames/pitaya/v2/acceptor"
	"github.com/topfreegames/pitaya/v2/logger"
)

// sendQueue 统一限制响应、推送和控制包；只由一个写协程接触底层写接口。
type sendQueue struct {
	acceptor.PlayerConn
	mu     sync.Mutex
	queue  chan []byte
	done   chan struct{}
	closed bool
	count  int
	bytes  int64
	cfg    Config
}

func newSendQueue(conn acceptor.PlayerConn, cfg Config) *sendQueue {
	q := &sendQueue{
		PlayerConn: conn,
		queue:      make(chan []byte, cfg.MaxPendingMessages),
		done:       make(chan struct{}),
		cfg:        cfg,
	}
	go q.writeLoop()
	return q
}

// Write 成功表示进入受限发送队列，不表示客户端已经收到。
func (q *sendQueue) Write(data []byte) (int, error) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return 0, net.ErrClosed
	}
	if q.count >= q.cfg.MaxPendingMessages || int64(len(data)) > q.cfg.MaxPendingBytes-q.bytes {
		q.mu.Unlock()
		logger.Log.Warn("关闭连接：待发条数或字节超过上限")
		_ = q.Close()
		return 0, *q.cfg.SlowConnectionError
	}
	q.count++
	q.bytes += int64(len(data))
	// 上游编码缓冲不归队列所有，入队时复制，避免异步写入共享可变切片。
	q.queue <- append([]byte(nil), data...)
	q.mu.Unlock()
	return len(data), nil
}

// 原生 agent 在入队前设置期限；实际期限由写协程在每次网络写入前设置。
func (q *sendQueue) SetWriteDeadline(time.Time) error {
	return nil
}

func (q *sendQueue) Close() error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return nil
	}
	q.closed = true
	close(q.done)
drain:
	for {
		select {
		case <-q.queue:
		default:
			break drain
		}
	}
	q.count, q.bytes = 0, 0
	q.mu.Unlock()
	return q.PlayerConn.Close()
}

func (q *sendQueue) writeLoop() {
	defer func() {
		if recover() != nil {
			slog.Error("发送循环异常，关闭连接", "component", "send_queue", "kind", "panic", "stack", string(debug.Stack()))
		}
	}()
	defer q.Close()
	for {
		select {
		case <-q.done:
			return
		case data := <-q.queue:
			err := q.PlayerConn.SetWriteDeadline(time.Now().Add(q.cfg.WriteTimeout))
			if err == nil {
				var n int
				n, err = q.PlayerConn.Write(data)
				if err == nil && n != len(data) {
					err = io.ErrShortWrite
				}
			}
			q.mu.Lock()
			if !q.closed {
				q.count--
				q.bytes -= int64(len(data))
			}
			closed := q.closed
			q.mu.Unlock()
			if closed {
				return
			}
			if err != nil {
				logger.Log.Warn("关闭连接：网络写入失败或超时；未送达消息不自动重发")
				return
			}
		}
	}
}
