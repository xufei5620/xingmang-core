package action

import "errors"

// Code 是对外稳定的错误码（规格 §18.4：统一错误模型）。
type Code string

const (
	CodeInvalidParams            Code = "INVALID_PARAMS"
	CodePermissionDenied         Code = "PERMISSION_DENIED"
	CodeEnvironmentMismatch      Code = "ENVIRONMENT_MISMATCH"
	CodeConflict                 Code = "CONFLICT"
	CodePreconditionFailed       Code = "PRECONDITION_FAILED"
	CodeNotRegistered            Code = "ACTION_NOT_REGISTERED"
	CodePrincipalTypeNotAllowed  Code = "PRINCIPAL_TYPE_NOT_ALLOWED"
	CodeAdvancedControlsRequired Code = "ADVANCED_CONTROLS_REQUIRED"
	// CodeApprovalRequired：调用被接受，但没有执行——内核为它落了一张待批
	// 审批单（XM-0030）。它**不是失败**：HTTP 层映射成 202，ApprovalRequestID
	// 是跟进用的单号。
	//
	// 与 ADVANCED_CONTROLS_REQUIRED 的区别是「有没有审批中心」：没接审批
	// 网关时内核仍然 fail closed 返回后者（Foundation-A 的行为原样保留），
	// 接了才会走到这里。
	CodeApprovalRequired Code = "APPROVAL_REQUIRED"
	CodeExecutionFailed          Code = "EXECUTION_FAILED"
	CodeRunwayConfigUnavailable  Code = "RUNWAY_CONFIG_UNAVAILABLE"
	CodeRevisionConflict         Code = "REVISION_CONFLICT"
	CodeInternal                 Code = "INTERNAL"
)

// Error 是 Action 的对外错误：只暴露稳定 Code 与安全 Message；
// 根因藏在 cause 中，仅经 Unwrap 供服务端日志使用，绝不进入 Error() 文本
// （规格 §18.4：不将供应商/底层错误直接返回）。
type Error struct {
	Code    Code
	Message string
	// ApprovalRequestID 只在 Code == CodeApprovalRequired 时非空：内核落下的
	// 审批单号，调用方据此跟进。放在这里而不是塞进 Message，是为了让 HTTP
	// 层能给出结构化字段而不是让人从文案里抠单号。
	ApprovalRequestID string
	cause             error
}

func newError(code Code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, cause: cause}
}

// NewError 构造一个标准 Action 错误。供 HTTP 层与业务模块在需要显式返回
// 特定错误码时使用；Kernel 内部仍用 newError。
// cause 只进 Unwrap 链供服务端日志，不进对外文本（规格 §18.4）。
func NewError(code Code, message string, cause error) error {
	return newError(code, message, cause)
}

func (e *Error) Error() string { return string(e.Code) + ": " + e.Message }

// Unwrap 暴露根因给 errors.Is/As 与服务端日志，不进入对外文本。
func (e *Error) Unwrap() error { return e.cause }

// ErrorCode 提取错误码；非 Action 错误一律归为 INTERNAL，nil 返回空串。
func ErrorCode(err error) Code {
	if err == nil {
		return ""
	}
	var ae *Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return CodeInternal
}
