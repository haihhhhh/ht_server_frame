// tables 包在启动时加载完整且带版本的数据包。
// 它不限定导表工具、数据格式或业务表类型。
package tables

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sync"
	"sync/atomic"
)

var (
	ErrNotReady  = errors.New("tables: snapshot unavailable")
	ErrVersion   = errors.New("tables: incompatible version")
	ErrLimit     = errors.New("tables: bundle exceeds limits")
	ErrIntegrity = errors.New("tables: invalid or incomplete bundle")
	ErrReloading = errors.New("tables: reload already in progress")
)

type File struct{ Name, SHA256 string }

// Manifest 描述不可变的发布产物，不用于监视文件变化。
// CodeVersion 是由项目选择、按精确匹配校验的兼容性标识。
type Manifest struct {
	DataVersion, SchemaVersion, ToolVersion, CodeVersion string
	Files                                                []File
}

type Version struct{ Data, Schema, Tool, Code string }

type Options[T any] struct {
	SchemaVersion, CodeVersion  string
	RequiredFiles               []string
	MaxFiles                    int
	MaxFileBytes, MaxTotalBytes int64
	// Decode 仅接收清单中已校验的文件，可以保留
	// 输入引用；发布前由 Clone 隔离已验证的结果。
	Decode   func(context.Context, map[string][]byte) (T, error)
	Validate func(context.Context, T) error
	// Clone 必须深度复制可变数据，并支持安全的并发读取。
	// 回调属于受信任的项目代码，不受资源沙箱约束。
	Clone func(T) T
}

type Snapshot[T any] struct {
	version Version
	value   T
	clone   func(T) T
}

func (s *Snapshot[T]) Version() Version {
	return s.version
}

// Value 通过项目的 Clone 提供数据。可变字段须独立复制，只读数据可共享。
func (s *Snapshot[T]) Value() T {
	return s.clone(s.value)
}

// Module 实现宿主模块契约，仅初始化一次，
// 所有文件、解码器和校验器均成功后才发布。Close 移除
// 模块持有的引用；已持有快照的操作继续使用原版本。
type Module[T any] struct {
	mu                sync.Mutex
	source            fs.FS
	manifest          Manifest
	opts              Options[T]
	attempted, closed bool
	reloading         bool
	snapshot          atomic.Pointer[Snapshot[T]]
}

func New[T any](source fs.FS, manifest Manifest, opts Options[T]) (*Module[T], error) {
	if source == nil || opts.Decode == nil || opts.Validate == nil || opts.Clone == nil ||
		opts.SchemaVersion == "" || opts.CodeVersion == "" || opts.MaxFiles < 1 ||
		opts.MaxFileBytes < 1 || opts.MaxTotalBytes < 1 {
		return nil, fmt.Errorf("tables: invalid loader options")
	}
	manifest.Files = append([]File(nil), manifest.Files...)
	opts.RequiredFiles = append([]string(nil), opts.RequiredFiles...)
	return &Module[T]{
		source:   source,
		manifest: manifest,
		opts:     opts,
	}, nil
}

// Init 同步执行。context 约束支持协作取消的阶段，但无法强制
// 中断任意 fs.File.Read 或忽略取消的项目回调。
func (tableModule *Module[T]) Init(ctx context.Context) error {
	tableModule.mu.Lock()
	if tableModule.attempted || tableModule.closed {
		tableModule.mu.Unlock()
		return fmt.Errorf("tables: loader already used or closed")
	}
	tableModule.attempted = true
	tableModule.mu.Unlock()
	snapshot, err := load(ctx, tableModule.source, tableModule.manifest, tableModule.opts)
	if err != nil {
		return err
	}
	tableModule.mu.Lock()
	defer tableModule.mu.Unlock()
	if tableModule.closed {
		return ErrNotReady
	}
	tableModule.snapshot.Store(snapshot)
	return nil
}

// Snapshot 一次取出已发布的完整版本。初始化完成前和关闭后均返回 ErrNotReady。
func (tableModule *Module[T]) Snapshot() (*Snapshot[T], error) {
	snapshot := tableModule.snapshot.Load()
	if snapshot == nil {
		return nil, ErrNotReady
	}
	return snapshot, nil
}

// Reload 先完整读取、解码、校验和复制，再一次性替换快照；失败保留旧版本。
// Decode 应同时构造纯配置派生索引。运行中的业务对象是否更新由项目决定。
// 同时只准备一份新包；调用者应提供独立且不再修改的发布目录。
func (tableModule *Module[T]) Reload(ctx context.Context, source fs.FS, manifest Manifest) error {
	if source == nil {
		return ErrIntegrity
	}
	tableModule.mu.Lock()
	if tableModule.snapshot.Load() == nil {
		tableModule.mu.Unlock()
		return ErrNotReady
	}
	if tableModule.reloading {
		tableModule.mu.Unlock()
		return ErrReloading
	}
	tableModule.reloading = true
	tableModule.mu.Unlock()
	defer func() {
		tableModule.mu.Lock()
		tableModule.reloading = false
		tableModule.mu.Unlock()
	}()
	manifest.Files = append([]File(nil), manifest.Files...)
	snapshot, err := load(ctx, source, manifest, tableModule.opts)
	if err != nil {
		return err
	}
	tableModule.mu.Lock()
	defer tableModule.mu.Unlock()
	if tableModule.closed {
		return ErrNotReady
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tableModule.snapshot.Store(snapshot)
	return nil
}

func (tableModule *Module[T]) Close() error {
	tableModule.mu.Lock()
	defer tableModule.mu.Unlock()
	tableModule.closed = true
	tableModule.snapshot.Store(nil)
	return nil
}
