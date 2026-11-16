// requestlog 读取入口和 RPC 已有的请求标识，供底层组件记录异常。
package requestlog

import (
	"context"
	"log/slog"
	"runtime/debug"

	"github.com/topfreegames/pitaya/v2/constants"
	pcontext "github.com/topfreegames/pitaya/v2/context"
)

// RequestIDField 是结构化日志中的请求标识，入口和异常堆栈共用同一字段。
const RequestIDField = "request_id"

func ID(ctx context.Context) string {
	id, _ := pcontext.GetFromPropagateCtx(ctx, constants.RequestIDKey).(string)
	return id
}

func ValidID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// Panic 只记录堆栈。业务请求沿用原标识，初始化和定时任务不会补造标识。
func Panic(ctx context.Context, component string) {
	logger := slog.Default()
	if id := ID(ctx); ValidID(id) {
		logger = logger.With(RequestIDField, id)
	}
	logger.ErrorContext(ctx, "执行发生异常", "component", component, "kind", "panic", "stack", string(debug.Stack()))
}
