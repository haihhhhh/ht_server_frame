package observability

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"net/http/pprof"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// DiagnosticsServer 在回环地址提供 Prometheus 指标和可选的 pprof，访问时须提供 Bearer 凭据。
type DiagnosticsServer struct {
	httpServer *http.Server
	listener   net.Listener
}

// NewDiagnosticsServer 配置诊断接口，宿主调用 Init 时才开始监听。
func NewDiagnosticsServer(address, token string, profiles bool, collectors ...prometheus.Collector) (*DiagnosticsServer, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil || !net.ParseIP(host).IsLoopback() || len(token) < 32 || len(token) > 512 {
		return nil, errors.New("diagnostics: loopback address and 32-512 byte token required")
	}
	registry := prometheus.NewRegistry()
	for _, c := range append([]prometheus.Collector{
		prometheus.NewGoCollector(),
		prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
		requestCount,
		requestDuration,
	}, collectors...) {
		if err := registry.Register(c); err != nil {
			return nil, err
		}
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		MaxRequestsInFlight: 2,
		Timeout:             2 * time.Second,
	}))
	if profiles {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	}
	expected := sha256.Sum256([]byte(token))
	slots := make(chan struct{}, 2)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := sha256.Sum256([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")))
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || subtle.ConstantTimeCompare(expected[:], got[:]) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() {
				<-slots
			}()
		default:
			http.Error(w, "busy", http.StatusTooManyRequests)
			return
		}
		if r.URL.Query().Has("seconds") || r.URL.Path == "/debug/pprof/profile" || r.URL.Path == "/debug/pprof/trace" {
			q := r.URL.Query()
			seconds, err := strconv.Atoi(q.Get("seconds"))
			if q.Get("seconds") == "" {
				seconds = 5
			}
			if err != nil && q.Get("seconds") != "" || seconds < 1 || seconds > 5 {
				http.Error(w, "seconds must be 1..5", http.StatusBadRequest)
				return
			}
			q.Set("seconds", strconv.Itoa(seconds))
			r.URL.RawQuery = q.Encode()
		}
		mux.ServeHTTP(w, r)
	})
	return &DiagnosticsServer{
		httpServer: &http.Server{
			Addr:              address,
			Handler:           handler,
			ReadHeaderTimeout: 2 * time.Second,
			ReadTimeout:       3 * time.Second,
			WriteTimeout:      10 * time.Second,
			IdleTimeout:       15 * time.Second,
			MaxHeaderBytes:    8192,
		},
	}, nil
}

func (server *DiagnosticsServer) Init(context.Context) error {
	listener, err := net.Listen("tcp", server.httpServer.Addr)
	if err != nil {
		return err
	}
	server.listener = listener
	metricsEnabled.Store(true)
	go func() {
		if err := server.httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			Logger(context.Background()).Error("诊断监听停止")
		}
	}()
	return nil
}

func (server *DiagnosticsServer) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	return server.httpServer.Shutdown(ctx)
}

func (server *DiagnosticsServer) Address() string {
	if server.listener == nil {
		return ""
	}
	return server.listener.Addr().String()
}
