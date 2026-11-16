package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang/protobuf/ptypes/wrappers"
	"github.com/haihhhhh/ht_server_frame/errorcode"
	"github.com/haihhhhh/ht_server_frame/execution"
	perror "github.com/topfreegames/pitaya/v2/errors"
)

// 此验证需要脚本托管的本机 NATS/etcd；普通单元测试不启动外部服务。
func TestRPCFaultSubprocess(t *testing.T) {
	if os.Getenv("HT_CLUSTER_FAULT") != "1" {
		t.Skip("requires local services")
	}
	for _, mode := range []string{"delay", "crash"} {
		t.Run(mode, func(t *testing.T) { testRPCFault(t, mode) })
	}
}

func testRPCFault(t *testing.T, mode string) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	prefix := fmt.Sprintf("ht_fault_%d", time.Now().UnixNano())
	backend := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRPCFaultChild$")
	backend.Env = append(os.Environ(), "HT_FAULT_ROLE=backend", "HT_FAULT_PREFIX="+prefix, "HT_FAULT_MODE="+mode)
	input, err := backend.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := backend.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	backend.Stderr = os.Stderr
	if err = backend.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backend.Process.Kill(); _ = backend.Wait() }()
	scanner := bufio.NewScanner(output)
	ready := false
	for scanner.Scan() {
		if scanner.Text() == "FAULT READY" {
			ready = true
			break
		}
	}
	if !ready {
		t.Fatalf("backend not ready: %v", scanner.Err())
	}
	caller := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRPCFaultChild$")
	caller.Env = append(os.Environ(), "HT_FAULT_ROLE=caller", "HT_FAULT_PREFIX="+prefix, "HT_FAULT_MODE="+mode)
	out, err := caller.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "FAULT VERIFIED") {
		t.Fatalf("caller: %v\n%s", err, out)
	}
	if mode == "delay" {
		if _, err = input.Write([]byte("stop\n")); err != nil {
			t.Fatal(err)
		}
	}
	commits := 0
	for scanner.Scan() {
		if scanner.Text() == "FAULT COMMITTED 1" {
			commits++
		}
	}
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if mode == "crash" && commits != 1 {
		t.Fatalf("missing or repeated side effect evidence: %d", commits)
	}
	if err = backend.Wait(); err != nil {
		t.Fatal(err)
	}
	t.Log("已执行计数为 1，响应超时为 HT-OUTCOME-UNKNOWN，无自动重试")
}

func TestRPCFaultChild(t *testing.T) {
	role := os.Getenv("HT_FAULT_ROLE")
	if role == "" {
		t.Skip("subprocess entry")
	}
	cfg := DefaultConfig()
	cfg.Address = ""
	cfg.ServerType = role
	a, err := NewLocalCluster(cfg, LocalClusterConfig{NATS: "nats://127.0.0.1:14222", Etcd: "http://127.0.0.1:12379", Prefix: os.Getenv("HT_FAULT_PREFIX")})
	if err != nil {
		t.Fatal(err)
	}
	var count atomic.Int64
	release := make(chan struct{})
	if role == "backend" {
		p, err := NewPool(a, "fault", 2, 8, 4096)
		if err != nil {
			t.Fatal(err)
		}
		_, err = RegisterPool(a, "probe", p, func(_ context.Context, q *wrappers.StringValue) (*wrappers.Int64Value, error) {
			switch q.Value {
			case "commit":
				count.Add(1)
				if os.Getenv("HT_FAULT_MODE") == "recovery" {
					break
				}
				if os.Getenv("HT_FAULT_MODE") == "crash" {
					// 在响应进入传输层之前退出进程；父进程通过管道核对副作用证据。
					fmt.Printf("FAULT COMMITTED %d\n", count.Load())
					os.Exit(0)
				}
				// 模拟副作用已完成但响应迟迟未返回；停止指令才释放，避免依赖固定睡眠。
				<-release
			case "count":
			default:
				return nil, execution.ErrInvalid
			}
			return &wrappers.Int64Value{Value: count.Load()}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.Stop(ctx); err != nil {
			t.Error(err)
		}
	}()
	if role == "backend" {
		fmt.Println("FAULT READY")
		bufio.NewScanner(os.Stdin).Scan()
		close(release)
		return
	}
	if os.Getenv("HT_FAULT_MODE") == "recovery" {
		checkRPCRecovery(t, a)
		return
	}
	reply := new(wrappers.Int64Value)
	err = a.RPC(context.Background(), "backend.probe.handle", reply, &wrappers.StringValue{Value: "invalid"})
	mapped := errorcode.Public(err)
	if perror.CodeFromError(mapped) != errorcode.Invalid || mapped.Error() != "request failed" {
		t.Fatalf("remote rejection: %v", mapped)
	}
	short, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err = a.RPC(short, "backend.probe.handle", reply, &wrappers.StringValue{Value: "commit"})
	if errorcode.Of(err) != errorcode.RPCOutcomeUnknown {
		t.Fatalf("timeout: %v", err)
	}
	if os.Getenv("HT_FAULT_MODE") == "crash" {
		fmt.Println("FAULT VERIFIED")
		return
	}
	// 空闲工作槽查询已提交副作用；另一个槽仍等待响应释放。
	if err = a.RPC(context.Background(), "backend.probe.handle", reply, &wrappers.StringValue{Value: "count"}); err != nil || reply.Value != 1 {
		t.Fatalf("side effect count=%d err=%v", reply.Value, err)
	}
	fmt.Println("FAULT VERIFIED")
}

// 脚本仅停止它启动的 NATS，并在已观察到断连后恢复；不模拟发送瞬间的竞争。
func checkRPCRecovery(t *testing.T, a *App) {
	t.Helper()
	query := func() (int64, error) {
		reply := new(wrappers.Int64Value)
		err := a.RPC(context.Background(), "backend.probe.handle", reply, &wrappers.StringValue{Value: "count"})
		return reply.Value, err
	}
	wait := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("dependency recovery condition timed out")
	}
	wait(func() bool { _, err := query(); return err == nil })
	fmt.Println("RECOVERY READY")
	input := bufio.NewScanner(os.Stdin)
	if !input.Scan() || input.Text() != "down" {
		t.Fatal("missing down command")
	}
	wait(func() bool {
		_, err := query()
		return errors.Is(err, errorcode.ErrRPCNotSent)
	})
	err := a.RPC(context.Background(), "backend.probe.handle", new(wrappers.Int64Value), &wrappers.StringValue{Value: "commit"})
	if !errors.Is(err, errorcode.ErrRPCNotSent) || errors.Is(err, errorcode.ErrRPCOutcomeUnknown) || errorcode.Of(err) != errorcode.RPCNotSent {
		t.Fatalf("disconnected call: %v", err)
	}
	fmt.Println("RECOVERY REJECTED")
	if !input.Scan() || input.Text() != "up" {
		t.Fatal("missing up command")
	}
	wait(func() bool { _, err := query(); return err == nil })
	if count, err := query(); err != nil || count != 0 {
		t.Fatalf("rejected call replayed: count=%d err=%v", count, err)
	}
	reply := new(wrappers.Int64Value)
	if err := a.RPC(context.Background(), "backend.probe.handle", reply, &wrappers.StringValue{Value: "commit"}); err != nil || reply.Value != 1 {
		t.Fatalf("new call after recovery: count=%d err=%v", reply.Value, err)
	}
	fmt.Println("RECOVERY VERIFIED")
}
