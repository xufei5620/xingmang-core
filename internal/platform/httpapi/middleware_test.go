package httpapi

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"request_id": RequestIDFrom(r.Context())})
	})
}

func TestRequestIDGeneratesWhenAbsent(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	RequestID(okHandler()).ServeHTTP(rec, req)

	got := rec.Header().Get("X-Request-ID")
	if got == "" {
		t.Fatal("应生成并回写 X-Request-ID")
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["request_id"] != got {
		t.Fatalf("ctx 中的 request_id=%q 与响应头 %q 不一致", body["request_id"], got)
	}
}

func TestRequestIDHonorsInbound(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("X-Request-ID", "caller-supplied-123")
	RequestID(okHandler()).ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-ID"); got != "caller-supplied-123" {
		t.Fatalf("应沿用调用方的 request id, got %q", got)
	}
}

func TestRequestIDRejectsOversizedOrUnsafeInbound(t *testing.T) {
	// 调用方可控的值会进日志与响应头，必须限长与限字符集，防日志注入
	for name, v := range map[string]string{
		"超长":   strings.Repeat("a", 200),
		"含换行":  "abc\ndef",
		"含控制符": "abc\x00def",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("X-Request-ID", v)
		RequestID(okHandler()).ServeHTTP(rec, req)

		got := rec.Header().Get("X-Request-ID")
		if got == v {
			t.Fatalf("%s：不安全的 request id 被原样接受: %q", name, got)
		}
		if got == "" {
			t.Fatalf("%s：应回退为生成的 id", name)
		}
	}
}

func TestAccessLogFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := RequestID(AccessLog(logger, "platform-api", "development")(okHandler()))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	h.ServeHTTP(rec, req)

	line := buf.String()
	for _, want := range []string{
		`"service":"platform-api"`, `"module":"httpapi"`,
		`"environment":"development"`, `"request_id":"`,
		`"method":"GET"`, `"path":"/api/v1/services"`, `"status":200`,
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("日志缺少 %s: %s", want, line)
		}
	}
}

func TestAccessLogDoesNotLogSensitiveHeaders(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := RequestID(AccessLog(logger, "platform-api", "development")(okHandler()))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Authorization", "Bearer super-secret-token")
	req.Header.Set("Cookie", "session=abc123")
	h.ServeHTTP(rec, req)

	// 规格 §18.8：日志禁止记录 Token（宪法 7 条）
	if strings.Contains(buf.String(), "super-secret-token") || strings.Contains(buf.String(), "abc123") {
		t.Fatalf("日志泄漏凭据: %s", buf.String())
	}
}

func TestRecoverTurnsPanicInto500WithoutLeakingStack(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	panicky := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom-secret-detail")
	})
	h := RequestID(Recover(logger)(panicky))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "boom-secret-detail") {
		t.Fatalf("响应泄漏 panic 细节: %s", rec.Body.String())
	}
	// 但服务端日志必须留有痕迹，否则事故无法排查
	if !strings.Contains(buf.String(), "boom-secret-detail") {
		t.Fatalf("日志应记录 panic 细节: %s", buf.String())
	}
}

func TestTimeoutCancelsSlowHandler(t *testing.T) {
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
			WriteJSON(w, http.StatusOK, map[string]string{"slow": "done"})
		case <-r.Context().Done():
		}
	})
	h := Timeout(50 * time.Millisecond)(slow)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	done := make(chan struct{})
	go func() { h.ServeHTTP(rec, req); close(done) }()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("超时中间件未在期限内返回")
	}
}
