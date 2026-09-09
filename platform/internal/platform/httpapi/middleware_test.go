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
		// 归责字段恒在：无身份时是空串，不是缺字段（XM-0031）
		`"principal_id":""`,
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("日志缺少 %s: %s", want, line)
		}
	}
}

// TestAccessLogRecordsPrincipalID 回归 Codex 冷审 PR #47 第 4 条 /
// PR #43 head `0a0642c` 第 4 条：「高敏读取不可归责……访问日志又不记录
// principal_id，事后无法回答谁拉取过审计数据」。
func TestAccessLogRecordsPrincipalID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	// 与真实路由同一顺序：AccessLog 在外，RequirePrincipal 在内。
	// 这正是需要显式信道的原因——内层的 context 派生对外层不可见。
	h := RequestID(AccessLog(logger, "platform-api", "development")(
		RequirePrincipal(res)(okHandler())))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/events", nil)
	devHeaders(req, "audit.read")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(buf.String(), `"principal_id":"staff_alice"`) {
		t.Fatalf("访问日志必须记录 principal_id: %s", buf.String())
	}
}

// TestAccessLogPrincipalIDEmptyWhenUnauthenticated：没有身份时字段仍在，
// 值为空串。
//
// 两条都要成立：字段恒在（日志检索不必区分「没有这个字段」与「值为空」）；
// 身份解析失败时**不记调用方声称的 ID**——那不是身份，记它等于让伪造者往
// 可归责记录里塞任意值。
func TestAccessLogPrincipalIDEmptyWhenUnauthenticated(t *testing.T) {
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}

	for name, setup := range map[string]func(*http.Request){
		"完全没有身份头": func(*http.Request) {},
		"身份类型非法": func(r *http.Request) {
			r.Header.Set("X-Dev-Principal-ID", "forged_attacker")
			r.Header.Set("X-Dev-Principal-Type", "SUPERUSER")
		},
	} {
		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, nil))
		h := RequestID(AccessLog(logger, "platform-api", "development")(
			RequirePrincipal(res)(okHandler())))

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/events", nil)
		setup(req)
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 403", name, rec.Code)
		}
		if !strings.Contains(buf.String(), `"principal_id":""`) {
			t.Fatalf("%s: 无身份时 principal_id 应为空串且字段仍在: %s", name, buf.String())
		}
		if strings.Contains(buf.String(), "forged_attacker") {
			t.Fatalf("%s: 解析失败的自称身份不得进访问日志: %s", name, buf.String())
		}
	}
}

// TestNoStoreSetsCacheControl：中间件在 handler 之前就设好响应头，
// 无论 handler 走哪条写出路径（正常 JSON、错误体）都覆盖得到。
func TestNoStoreSetsCacheControl(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	NoStore(okHandler()).ServeHTTP(rec, req)
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}

	// 错误响应同样带上：错误体里也可能有 request_id 与安全文案
	failing := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusInternalServerError, map[string]string{"e": "x"})
	})
	rec2 := httptest.NewRecorder()
	NoStore(failing).ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/x", nil))
	if got := rec2.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("错误响应 Cache-Control = %q, want no-store", got)
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
