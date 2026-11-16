package wsacceptor

import (
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/topfreegames/pitaya/v2/acceptor"
)

// Config 包含监听地址、每条消息的读取上限，以及每个连接的发送限制。
type Config struct {
	Address            string
	MaxMessageBytes    int64
	MaxPendingMessages int
	MaxPendingBytes    int64
	WriteTimeout       time.Duration
	// 发送超限时读取 app 对外公开的错误变量，所有连接共用同一个错误值。
	SlowConnectionError *error
}

type limitedWS struct {
	listener    net.Listener
	server      *http.Server
	connections chan acceptor.PlayerConn
	done        chan struct{}
	stop        sync.Once
	running     atomic.Bool
	address     string
}

// New 在升级连接时设置读取上限，并为每个 Pitaya 连接装配独立发送队列。
func New(cfg Config) (acceptor.Acceptor, error) {
	address := cfg.Address
	l, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	w := &limitedWS{
		listener:    l,
		connections: make(chan acceptor.PlayerConn),
		done:        make(chan struct{}),
		address:     address,
	}
	// 保留官方接入器的 Origin 行为；生产跨域策略仍需独立配置。
	u := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin: func(*http.Request) bool {
			return true
		},
	}
	w.server = &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		MaxHeaderBytes:    16 * 1024,
		Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			conn, err := u.Upgrade(rw, r, nil)
			if err != nil {
				return
			}
			conn.SetReadLimit(cfg.MaxMessageBytes)
			player, err := acceptor.NewWSConn(conn)
			if err != nil {
				_ = conn.Close()
				return
			}
			out := newSendQueue(player, cfg)
			select {
			case w.connections <- out:
			case <-w.done:
				_ = out.Close()
			}
		}),
	}
	return w, nil
}

func (w *limitedWS) ListenAndServe() {
	w.running.Store(true)
	defer w.running.Store(false)
	defer w.Stop()
	_ = w.server.Serve(w.listener)
}
func (w *limitedWS) Stop() {
	w.stop.Do(func() {
		close(w.done)
		_ = w.server.Close()
		_ = w.listener.Close()
	})
}
func (w *limitedWS) GetAddr() string {
	return w.listener.Addr().String()
}
func (w *limitedWS) GetConfiguredAddress() string {
	return w.address
}
func (w *limitedWS) GetConnChan() chan acceptor.PlayerConn {
	return w.connections
}
func (w *limitedWS) IsRunning() bool {
	return w.running.Load()
}
func (w *limitedWS) EnableProxyProtocol() {}
