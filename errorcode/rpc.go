package errorcode

import (
	"context"
	"errors"

	"github.com/haihhhhh/ht_server_frame/execution"
	"github.com/topfreegames/pitaya/v2/constants"
	perror "github.com/topfreegames/pitaya/v2/errors"
)

// ErrRPCNotSent 表示本地调用在交给原生 RPC 前被拒绝。
var ErrRPCNotSent = errors.New("app: RPC not sent")

// ErrRPCOutcomeUnknown 表示无法确认远端最终结果。
var ErrRPCOutcomeUnknown = errors.New("app: RPC outcome unknown")

// FromRPC 将 Pitaya 调用返回的错误转换成框架错误。调用方根据实际发送进度判断超时的含义。
func FromRPC(err error) error {
	if err == nil {
		return nil
	}
	var remote *perror.Error
	if errors.As(err, &remote) && remote != nil {
		switch remote.Code {
		case NotReady, Overloaded, Invalid, Unavailable, Reentrant,
			Unauthenticated, Forbidden, DuplicateLogin, Internal, RPCOutcomeUnknown:
			return NewRemoteFailure(remote.Code)
		case perror.ErrRequestTimeout:
			return context.DeadlineExceeded
		default:
			return Public(remote)
		}
	}
	if errors.Is(err, constants.ErrNoServersAvailableOfType) {
		return execution.ErrFaulted
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	// 传输失败也可能发生在远端处理之后，无法确认执行结果。
	return NewRemoteFailure(RPCOutcomeUnknown)
}

type remoteFailure struct{ code string }

// NewRemoteFailure 只保留调用方已经识别的错误码，丢弃远端文本和元数据。
// 即使远端报告未发送，也不能据此判定本地 RPC 未发送。
func NewRemoteFailure(code string) error {
	return remoteFailure{code: code}
}

func (e remoteFailure) Error() string {
	return "remote request failed"
}

func (e remoteFailure) Is(target error) bool {
	return target == ErrRPCOutcomeUnknown && e.code == RPCOutcomeUnknown
}
