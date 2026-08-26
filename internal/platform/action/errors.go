package action

import "errors"

// Code 是对外稳定的错误码（规格 §18.4：统一错误模型）。
type Code string

const (
	CodeInvalidParams            Code = "INVALID_PARAMS"
	CodePermissionDenied         Code = "PERMISSION_DENIED"
	CodeEnvironmentMismatch      Code = "ENVIRONMENT_MISMATCH"
	CodeNotRegistered            Code = "ACTION_NOT_REGISTERED"
	CodePrincipalTypeNotAllowed  Code = "PRINCIPAL_TYPE_NOT_ALLOWED"
	CodeAdvancedControlsRequired Code = "ADVANCED_CONTROLS_REQUIRED"
	CodeExecutionFailed          Code = "EXECUTION_FAILED"
	CodeInternal                 Code = "INTERNAL"
)

// Error 是 Action 的对外错误：只暴露稳定 Code 与安全 Message；
// 根因藏在 cause 中，仅经 Unwrap 供服务端日志使用，绝不进入 Error() 文本
// （规格 §18.4：不将供应商/底层错误直接返回）。
type Error struct {
	Code    Code
	Message string
	cause   error
}

func newError(code Code, message string, cause error) *Error {
	return &Error{Code: code, Message: message, cause: cause}
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
