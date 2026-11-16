package host

import (
	"context"
	"fmt"
)

// 两组名额均由所属宿主的 mu 保护。
type admissionPool struct {
	limit  int
	active int
}

// Do 保留在途计数，直到 fn 实际返回。回调不得等待
// 本宿主的 Stop。回调可调用有界执行器，但该执行器必须
// 注册为模块，并在其存储依赖之前关闭。
func (lifecycleHost *Host) Do(ctx context.Context, fn func(context.Context) error) error {
	return lifecycleHost.run(ctx, fn, &lifecycleHost.requests, false)
}

// DoOutbound 供出站 RPC 使用。同一请求同时只能有一个 RPC 借用入口名额。
func (lifecycleHost *Host) DoOutbound(ctx context.Context, fn func(context.Context) error) error {
	return lifecycleHost.run(ctx, fn, &lifecycleHost.requests, true)
}

// DoReservedOutbound 用后台预留名额执行一次出站调用，停服时与普通调用一起排空。
func (lifecycleHost *Host) DoReservedOutbound(ctx context.Context, fn func(context.Context) error) error {
	return lifecycleHost.run(ctx, fn, &lifecycleHost.reservedOutbound, false)
}

type admissionKey struct{}

// 两个标记由 host.mu 保护。入口与借用它的 RPC 都返回后，名额才能释放。
type admission struct {
	pool        *admissionPool
	entryActive bool
	rpcActive   bool
}

func (lifecycleHost *Host) run(ctx context.Context, fn func(context.Context) error, pool *admissionPool, allowBorrow bool) error {
	if fn == nil {
		return fmt.Errorf("host: nil callback")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lifecycleHost.mu.Lock()
	if lifecycleHost.phase != Ready {
		lifecycleHost.mu.Unlock()
		return ErrNotReady
	}
	slot, _ := ctx.Value(admissionKey{}).(*admission)
	borrow := allowBorrow && slot != nil && slot.pool == pool && slot.entryActive && !slot.rpcActive
	if borrow {
		slot.rpcActive = true
	} else {
		if pool.active >= pool.limit {
			lifecycleHost.mu.Unlock()
			return ErrOverloaded
		}
		pool.active++
		lifecycleHost.wg.Add(1)
		slot = &admission{pool: pool, entryActive: true}
		ctx = context.WithValue(ctx, admissionKey{}, slot)
	}
	lifecycleHost.mu.Unlock()
	defer func() {
		lifecycleHost.mu.Lock()
		defer lifecycleHost.mu.Unlock()
		if borrow {
			slot.rpcActive = false
		} else {
			slot.entryActive = false
		}
		if !slot.entryActive && !slot.rpcActive {
			slot.pool.active--
			lifecycleHost.wg.Done()
		}
	}()
	runCtx, cancel := context.WithCancel(ctx)
	unlink := context.AfterFunc(lifecycleHost.ctx, cancel)
	defer func() {
		unlink()
		cancel()
	}()
	if lifecycleHost.ctx.Err() != nil {
		cancel()
	}
	if err := runCtx.Err(); err != nil {
		return err
	}
	return invoke(runCtx, func() error {
		return fn(runCtx)
	})
}
