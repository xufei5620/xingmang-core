package httpapi

import (
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"
)

const requestIDHeader = "X-Request-ID"

// safeRequestID：调用方提供的值会进日志与响应头，因此限字符集与长度，
// 防止日志注入（换行伪造日志行）与响应头注入。
var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// RequestID 读取或生成请求 ID，写入响应头与上下文（规格 §5.8）。
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !safeRequestID.MatchString(id) {
			id = uuid.NewString()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(WithRequestID(r.Context(), id)))
	})
}

// statusRecorder 记录写出的状态码供访问日志使用。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// AccessLog 按规格 §18.8 的字段记录访问日志。
//
// 只记录方法、路径、状态、耗时与 request_id —— **不记录任何 Header**，
// 因为 Authorization / Cookie 会带 Token（宪法 7 条）。
func AccessLog(logger *slog.Logger, service, environment string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			logger.InfoContext(r.Context(), "http_request",
				slog.String("service", service),
				slog.String("module", "httpapi"),
				slog.String("environment", environment),
				slog.String("request_id", RequestIDFrom(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			)
		})
	}
}

// Recover 把 panic 转成 500，细节只进日志不进响应。
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					logger.ErrorContext(r.Context(), "http_panic",
						slog.String("module", "httpapi"),
						slog.String("request_id", RequestIDFrom(r.Context())),
						slog.String("path", r.URL.Path),
						slog.String("error_code", "panic"),
						slog.Any("panic", v),
					)
					WriteJSON(w, http.StatusInternalServerError, ErrorResponse{Error: ErrorBody{
						Code:      "INTERNAL",
						Message:   "服务内部错误",
						RequestID: RequestIDFrom(r.Context()),
					}})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// Timeout 给请求上下文加期限（规格 §18.1-4：所有外部 I/O 必须有超时）。
func Timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.TimeoutHandler(next, d, `{"error":{"code":"TIMEOUT","message":"请求超时"}}`)
	}
}
