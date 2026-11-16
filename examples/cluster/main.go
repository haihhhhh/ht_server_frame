// cluster 演示本机网关到后端的无状态请求；可选假认证不代表生产账户体系。
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/golang/protobuf/ptypes/wrappers"
	"github.com/haihhhhh/ht_server_frame/access"
	"github.com/haihhhhh/ht_server_frame/app"
	"github.com/haihhhhh/ht_server_frame/execution"
)

func run() error {
	cfg := app.DefaultConfig()
	role := flag.String("role", "gateway", "gateway 或 backend")
	testAuth := flag.Bool("test-auth", false, "启用假认证；服务连接认证由 NATS 负责")
	flag.StringVar(&cfg.Address, "addr", cfg.Address, "网关回环监听地址")
	cc := app.LocalClusterConfig{
		NATS:   "nats://127.0.0.1:14222",
		Etcd:   "http://127.0.0.1:12379",
		Prefix: "ht_local_example",
	}
	flag.StringVar(&cc.NATS, "nats", cc.NATS, "本机 NATS 地址")
	flag.StringVar(&cc.Etcd, "etcd", cc.Etcd, "本机 etcd 地址")
	flag.StringVar(&cc.Prefix, "prefix", cc.Prefix, "发现命名空间")
	flag.Parse()
	if *role != "gateway" && *role != "backend" {
		return fmt.Errorf("role 必须为 gateway 或 backend")
	}
	cc.Frontend = *role == "gateway"
	cfg.ServerType = *role
	if !cc.Frontend {
		cfg.Address = ""
	}
	a, err := app.NewLocalCluster(cfg, cc)
	if err != nil {
		return err
	}
	if *testAuth {
		if cc.Frontend {
			if err = a.ConfigureAuthentication(access.Options{
				Scope:              access.ScopeLocal,
				Policy:             access.ReplaceOld,
				MaxSessions:        128,
				MaxCredentialBytes: 128,
				Verify: func(_ context.Context, ticket string) (string, error) {
					if ticket != "test-ticket-alice" {
						return "", access.ErrUnauthenticated
					}
					return "alice", nil
				},
			}); err != nil {
				return err
			}
		}
		fmt.Println("TEST AUTH: 假账户 alice；NATS 环境内服务互信，后端检查业务身份")
	}
	p, err := app.NewPool(a, "echo", 2, 16, 64*1024)
	if err != nil {
		return err
	}
	if *testAuth && cc.Frontend {
		_, err = app.RegisterPool(a, "login", p, func(ctx context.Context, q *wrappers.StringValue) (*wrappers.StringValue, error) {
			id, err := a.Login(ctx, q.Value)
			if err != nil {
				return nil, err
			}
			return &wrappers.StringValue{Value: id.UserID}, nil
		})
		if err != nil {
			return err
		}
	}
	echo, err := app.RegisterPool(a, "echo", p, func(ctx context.Context, q *wrappers.StringValue) (*wrappers.StringValue, error) {
		if len(q.Value) > 1024 {
			return nil, execution.ErrInvalid
		}
		if cc.Frontend {
			reply := new(wrappers.StringValue)
			if err := a.RPC(ctx, "backend.echo.handle", reply, q); err != nil {
				return nil, err
			}
			return reply, nil
		}
		if *testAuth {
			if _, err := access.RequireUser(ctx, q.Value); err != nil {
				return nil, err
			}
		}
		return &wrappers.StringValue{Value: "backend: " + q.Value}, nil
	})
	if err != nil {
		return err
	}
	if *testAuth {
		if err = echo.RequireSession(); err != nil {
			return err
		}
	}
	if err = a.Start(context.Background()); err != nil {
		return err
	}
	fmt.Printf("READY %s %s\n", *role, a.Address())
	input := make(chan struct{})
	go func() {
		s := bufio.NewScanner(os.Stdin)
		for s.Scan() {
			if s.Text() == "stop" {
				break
			}
		}
		close(input)
	}()
	select {
	case <-input:
	case <-a.Done():
		return fmt.Errorf("Pitaya 直接退出，未完成显式停止")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = a.Stop(ctx); err != nil {
		return err
	}
	fmt.Println("STOPPED")
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
