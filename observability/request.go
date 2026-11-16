package observability

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"time"

	"github.com/haihhhhh/ht_server_frame/internal/requestlog"
	"github.com/topfreegames/pitaya/v2/constants"
	pcontext "github.com/topfreegames/pitaya/v2/context"
	perror "github.com/topfreegames/pitaya/v2/errors"
)

// 指标仅区分成功与失败，日志还会记录具体原因或项目的业务错误码。
const (
	resultField    = "result"
	resultOK       = "ok"
	resultError    = "error"
	resultInternal = "internal"
	resultTimeout  = "timeout"
	resultCanceled = "canceled"
	resultSlow     = "slow"
)

// RequestContext 复用 Pitaya 传播的请求标识；缺失时生成，并复制传播数据避免并发修改。
func RequestContext(ctx context.Context) context.Context {
	if requestlog.ValidID(requestlog.ID(ctx)) {
		return ctx
	}
	values := map[string]interface{}{}
	for k, v := range pcontext.ToMap(ctx) {
		values[k] = v
	}
	values[constants.RequestIDKey] = rand.Text()
	return context.WithValue(ctx, constants.PropagateCtxKey, values)
}

func RequestID(ctx context.Context) string {
	return requestlog.ID(ctx)
}

// Detached 仅保留请求标识，不保留认证数据或上游取消；保存期限由队列单独管理。
func Detached(ctx context.Context) context.Context {
	ctx = RequestContext(ctx)
	return context.WithValue(context.Background(), constants.PropagateCtxKey, map[string]interface{}{constants.RequestIDKey: RequestID(ctx)})
}

// Logger 不附加凭据、用户 ID 或消息载荷；项目可继续添加安全的业务字段。
func Logger(ctx context.Context) *slog.Logger {
	return slog.Default().With(requestlog.RequestIDField, RequestID(ctx))
}

// Begin 返回本次操作的完成记录器，复用同一标识关联入口、RPC 和异步保存。
func Begin(ctx context.Context, server, route string) (context.Context, func(error)) {
	ctx = RequestContext(ctx)
	start := time.Now()
	return ctx, func(err error) {
		elapsed := time.Since(start)
		if metricsEnabled.Load() {
			status := resultOK
			if err != nil {
				status = resultError
			}
			requestCount.WithLabelValues(status).Inc()
			requestDuration.Observe(elapsed.Seconds())
		}
		level := slog.LevelDebug
		code := resultOK
		if err != nil {
			level = slog.LevelWarn
			code = resultInternal
			var business *perror.Error
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				code = resultTimeout
			case errors.Is(err, context.Canceled):
				code = resultCanceled
			case errors.As(err, &business) && business != nil:
				code = business.Code
			}
		} else if elapsed >= 500*time.Millisecond {
			level = slog.LevelWarn
			code = resultSlow
		}
		Logger(ctx).Log(ctx, level, "请求结束", "server", server, "route", route, "duration_ms", elapsed.Milliseconds(), resultField, code)
	}
}
