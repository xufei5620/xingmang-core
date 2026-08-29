package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

const contentTypeJSON = "application/json; charset=utf-8"

// ErrorBody 是统一错误体（规格 §18.4）。
type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// ErrorResponse 是错误响应的外层包装。
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// StatusForCode 把 Action 错误码映射为 HTTP 状态码。
//
// ADVANCED_CONTROLS_REQUIRED → 501：这不是调用方的错，是平台尚未实现该风险
// 等级的控制（Foundation-B / XM-0030）。前端据此显示「功能待上线」而非「无权限」。
func StatusForCode(c action.Code) int {
	switch c {
	case action.CodeInvalidParams:
		return http.StatusBadRequest
	case action.CodePermissionDenied, action.CodePrincipalTypeNotAllowed:
		return http.StatusForbidden
	case action.CodeEnvironmentMismatch, action.CodeConflict:
		return http.StatusConflict
	case action.CodePreconditionFailed:
		return http.StatusPreconditionFailed
	case action.CodeNotRegistered:
		return http.StatusNotFound
	case action.CodeAdvancedControlsRequired:
		return http.StatusNotImplemented
	case action.CodeExecutionFailed:
		return http.StatusBadGateway
	case action.CodeRunwayConfigUnavailable:
		return http.StatusServiceUnavailable
	case action.CodeRevisionConflict:
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// safeMessage 返回可以给调用方看的文案。
//
// 只有 Action 错误的 Message 是设计过的安全文案；其他错误一律用固定兜底文案，
// 避免把内网地址、SQL 约束名、供应商错误泄漏出去（规格 §18.4）。
func safeMessage(err error) (action.Code, string) {
	var ae *action.Error
	if errors.As(err, &ae) {
		return ae.Code, ae.Message
	}
	return action.CodeInternal, "服务内部错误"
}

// WriteJSON 写出 JSON 响应。
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", contentTypeJSON)
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// 响应已开始写出，无法改状态码；只能记日志。
		slog.Error("响应编码失败", slog.String("module", "httpapi"),
			slog.String("error_code", "response_encode_failed"), slog.Any("err", err))
	}
}

// WriteError 按 Action 错误码映射状态码并写出安全的错误体。
// 完整根因（含底层错误）只写进服务端日志，不进响应。
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	code, msg := safeMessage(err)
	status := StatusForCode(code)
	requestID := RequestIDFrom(r.Context())

	slog.ErrorContext(r.Context(), "请求失败",
		slog.String("module", "httpapi"),
		slog.String("request_id", requestID),
		slog.String("path", r.URL.Path),
		slog.String("method", r.Method),
		slog.Int("status", status),
		slog.String("error_code", string(code)),
		slog.Any("err", err), // 根因只在这里
	)

	WriteJSON(w, status, ErrorResponse{Error: ErrorBody{
		Code:      string(code),
		Message:   msg,
		RequestID: requestID,
	}})
}

type requestIDKey struct{}

// WithRequestID 把请求 ID 放入上下文（由 RequestID 中间件调用）。
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestIDFrom 取出请求 ID；未设置返回空串。
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}
