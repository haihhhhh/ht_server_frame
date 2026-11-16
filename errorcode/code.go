// Package errorcode 定义框架公开的错误码，并将内部错误转换成可返回给调用方的错误。
package errorcode

import (
	"context"
	"errors"
	"strings"

	"github.com/haihhhhh/ht_server_frame/access"
	"github.com/haihhhhh/ht_server_frame/execution"
	"github.com/haihhhhh/ht_server_frame/host"
	perror "github.com/topfreegames/pitaya/v2/errors"
)

const (
	frameworkCodePrefix = "HT-"
	pitayaCodePrefix    = "PIT-"
)

// ErrSlowConnection 表示连接因待发条数或字节数超限而关闭。
var ErrSlowConnection = errors.New("app: connection send capacity exceeded")

// 这些值用于客户端响应和服务间 RPC，也可用于判断 Of 的返回值。
const (
	Internal          = "HT-INTERNAL"
	RPCNotSent        = "HT-NOT-SENT"
	RPCOutcomeUnknown = "HT-OUTCOME-UNKNOWN"
	Unauthenticated   = "HT-UNAUTHENTICATED"
	Forbidden         = "HT-FORBIDDEN"
	DuplicateLogin    = "HT-DUPLICATE-LOGIN"
	Overloaded        = "HT-OVERLOADED"
	NotReady          = "HT-NOT-READY"
	Invalid           = "HT-INVALID"
	Unavailable       = "HT-UNAVAILABLE"
	Reentrant         = "HT-REENTRANT"
	UnknownMessage    = "HT-UNKNOWN-MESSAGE"
)

// Of 返回框架或项目的公开错误码，成功时返回空字符串。
func Of(err error) string {
	if err == nil {
		return ""
	}
	return perror.CodeFromError(Public(err))
}

// Public 用于失败响应。项目声明的业务错误保留原样，框架错误仅公开错误码。
// 无法识别失败原因时（包括 nil），按内部错误处理。
func Public(err error) error {
	code := Internal
	var remote remoteFailure
	if errors.As(err, &remote) {
		return perror.NewError(errors.New("request failed"), remote.code)
	}
	var declared *perror.Error
	if errors.As(err, &declared) && declared != nil && declared.Code != "" {
		if !strings.HasPrefix(declared.Code, frameworkCodePrefix) && !strings.HasPrefix(declared.Code, pitayaCodePrefix) {
			return declared
		}
		return perror.NewError(errors.New("request failed"), declared.Code)
	}
	switch {
	case errors.Is(err, ErrRPCNotSent):
		code = RPCNotSent
	case errors.Is(err, access.ErrUnauthenticated):
		code = Unauthenticated
	case errors.Is(err, access.ErrForbidden):
		code = Forbidden
	case errors.Is(err, access.ErrDuplicate):
		code = DuplicateLogin
	case errors.Is(err, access.ErrCapacity):
		code = Overloaded
	case errors.Is(err, host.ErrNotReady), errors.Is(err, execution.ErrStopped):
		code = NotReady
	case errors.Is(err, host.ErrOverloaded), errors.Is(err, execution.ErrOverloaded):
		code = Overloaded
	case errors.Is(err, ErrRPCOutcomeUnknown), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		code = RPCOutcomeUnknown
	case errors.Is(err, execution.ErrInvalid):
		code = Invalid
	case errors.Is(err, execution.ErrFaulted):
		code = Unavailable
	case errors.Is(err, execution.ErrReentrant):
		code = Reentrant
	}
	return perror.NewError(errors.New("request failed"), code)
}
