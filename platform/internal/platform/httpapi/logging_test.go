package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

// 这个文件钉的是 XM-LOG-INJECTED：**错误路径要用注入的 logger**。
//
// 修之前：两个进程各建一个 JSON handler 的 logger 注入进 Deps.Logger，
// 但 WriteError 走的是包级 `slog.ErrorContext`，也就是全局默认——文本格式、
// 写 stderr。后果是**最需要被检索的那类日志（错误）与其余日志格式不同、
// 流也不同**，按 JSON 解析的采集会整片漏掉它们。

// captureLogger 返回一个写进 buf 的 JSON logger。
//
// 用 JSON 而不是 Text：本片要证明的正是「错误日志与访问日志走同一个 handler」，
// 而 handler 的种类是那个差别里最刺眼的一半。
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, nil))
}

// logLines 把捕获到的输出解析成一条条 JSON 记录。
//
// 解析失败即失败：一行解析不出来就说明有人往同一个流里写了非 JSON——
// 那正是这一片要消灭的情况。
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("日志不是 JSON：%q", line)
		}
		out = append(out, rec)
	}
	return out
}

func findLog(records []map[string]any, msg string) (map[string]any, bool) {
	for _, rec := range records {
		if rec["msg"] == msg {
			return rec, true
		}
	}
	return nil, false
}

// TestWriteErrorUsesInjectedLogger 是这一片的主张。
//
// 断言的形状是「注入的 logger 里能找到那条错误记录」——它不是恒真的：
// 修之前那条记录会去 slog.Default()，这个 buf 里只有访问日志。
// 底下的 TestErrorLogGoesToTheSameSinkAsAccessLog 用「同一个 buf 里两条都在」
// 把这一点说得更死。
func TestWriteErrorUsesInjectedLogger(t *testing.T) {
	var buf bytes.Buffer
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	h := NewRouter(Deps{
		Logger: captureLogger(&buf), Service: "platform-api", Environment: "development",
		DB: fakePinger{}, Resolver: res, ActionRegistry: action.NewRegistry(),
		Kernel: fixedErrorExecutor{code: action.CodeConflict},
	})

	req := httptest.NewRequest(http.MethodPost,
		executePath("registry.service.create", "1"), strings.NewReader(`{"params":{}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-log-1")
	devHeaders(req, "registry.service.manage")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("状态码 %d：%s", rec.Code, rec.Body.String())
	}
	got, ok := findLog(logLines(t, &buf), "请求失败")
	if !ok {
		t.Fatalf("注入的 logger 里没有那条错误日志——它去了全局默认：%s", buf.String())
	}
	// 字段要齐：报障时靠这几样对上响应与审计。
	for field, want := range map[string]any{
		"request_id": "req-log-1",
		"error_code": "CONFLICT",
		"method":     http.MethodPost,
		"status":     float64(http.StatusConflict), // JSON 解出来是 float64
	} {
		if got[field] != want {
			t.Fatalf("字段 %s = %v，期望 %v（整条：%v）", field, got[field], want, got)
		}
	}
}

// TestErrorLogGoesToTheSameSinkAsAccessLog 是最直白的那条：
// **一次失败的请求，访问日志和错误日志要落在同一个地方、同一种格式。**
//
// 修之前这两条会分家——访问日志在注入的 JSON logger 里，错误日志在全局默认的
// 文本 handler 里。这条断言「同一个 buf 里两条都在」，分家就红。
func TestErrorLogGoesToTheSameSinkAsAccessLog(t *testing.T) {
	var buf bytes.Buffer
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	h := NewRouter(Deps{
		Logger: captureLogger(&buf), Service: "platform-api", Environment: "development",
		DB: fakePinger{}, Resolver: res, ActionRegistry: action.NewRegistry(),
		Kernel: fixedErrorExecutor{code: action.CodePermissionDenied},
	})

	req := httptest.NewRequest(http.MethodPost,
		executePath("registry.service.create", "1"), strings.NewReader(`{"params":{}}`))
	req.Header.Set("Content-Type", "application/json")
	devHeaders(req, "registry.service.manage")
	h.ServeHTTP(httptest.NewRecorder(), req)

	records := logLines(t, &buf)
	if _, ok := findLog(records, "http_request"); !ok {
		t.Fatalf("访问日志不在：%s", buf.String())
	}
	if _, ok := findLog(records, "请求失败"); !ok {
		t.Fatalf("错误日志不在同一个 sink 里：%s", buf.String())
	}
}

// TestReadinessFailureUsesInjectedLogger：探针失败那一处同样要用注入的 logger。
// 它是四个调用点里唯一一个在 /api/v1 之外的，容易被漏掉。
func TestReadinessFailureUsesInjectedLogger(t *testing.T) {
	var buf bytes.Buffer
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	h := NewRouter(Deps{
		Logger: captureLogger(&buf), Service: "platform-api", Environment: "development",
		DB: fakePinger{err: context.DeadlineExceeded}, Resolver: res, ActionRegistry: action.NewRegistry(),
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码 %d", rec.Code)
	}
	if _, ok := findLog(logLines(t, &buf), "readiness_failed"); !ok {
		t.Fatalf("探针失败日志没进注入的 logger：%s", buf.String())
	}
}

// TestLoggerFromFallsBackToDefault：拿不到注入值时回落到 slog.Default()。
//
// 回落必须在：直接构造 handler 的测试、以及 Logging 中间件之外的调用路径
// 都没有那个值，而「日志记不出来」不该让请求失败（更不该 panic）。
func TestLoggerFromFallsBackToDefault(t *testing.T) {
	if LoggerFrom(context.Background()) == nil {
		t.Fatal("回落不能返回 nil——调用方会立刻 panic")
	}
	// 显式塞一个 nil 也要回落，而不是把 nil 存进去再取出来。
	if LoggerFrom(WithLogger(context.Background(), nil)) == nil {
		t.Fatal("WithLogger(nil) 之后仍要能取到一个可用的 logger")
	}
	// 对照：塞了真 logger 就要拿到那一个，否则这条回落等于永远生效。
	var buf bytes.Buffer
	mine := captureLogger(&buf)
	if LoggerFrom(WithLogger(context.Background(), mine)) != mine {
		t.Fatal("注入的 logger 没被取出来")
	}
}
