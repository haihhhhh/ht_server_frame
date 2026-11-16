package host

import (
	"context"
	"fmt"
)

// Call 执行类型化操作，使用与 Do 相同的准入
// 与生命周期边界，不创建 goroutine。遇到任何错误，包括捕获的
// panic，均丢弃响应，避免把部分结果作为成功结果返回。
// 成功返回的可变响应必须已经是独立快照。
// 传输适配器负责将错误映射为对外错误码。
func Call[T any](ctx context.Context, lifecycleHost *Host, fn func(context.Context) (T, error)) (T, error) {
	var result, zero T
	if lifecycleHost == nil || fn == nil {
		return zero, fmt.Errorf("host: nil host or operation")
	}
	err := lifecycleHost.Do(ctx, func(runCtx context.Context) error {
		var err error
		result, err = fn(runCtx)
		return err
	})
	if err != nil {
		return zero, err
	}
	return result, nil
}
