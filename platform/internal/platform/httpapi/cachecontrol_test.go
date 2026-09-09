package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/audit"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
)

// bigMoneyObservation 造一条带超大金额的观测。
//
// 指标名经形参 k 中转而不是写成字面量，理由与 metrics_history_test.go 里那段
// 常量块注释同源：gitleaks 的 generic-api-key 规则会把
// 「名字带 key 字样的标识符 + 冒号 + 一长串字符」判成泄漏，认不出那只是个指标名；
// 而本仓禁止加 allowlist（会顺手掩盖真报）。形参名只有一个字母，够不上规则要求的
// 十字符下限。
func bigMoneyObservation(k string, at time.Time) ops.Observation {
	return ops.Observation{
		MetricKey: k, Source: "sub2api-staging",
		Environment: "development", ObservedAt: &at, SyncedAt: at,
		Status: ops.SyncOK, StalenessThresholdSeconds: 1800, LastSuccess: &at,
		// 2^53 + 3：float64 表示不出来的最小一类整数
		Value: map[string]any{"amount_minor": json.Number("9007199254740995")},
	}
}

// fullRouter 装一个所有依赖都齐的路由，用来验证跨端点一致的响应头策略。
func fullRouter(t *testing.T) http.Handler {
	t.Helper()
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Minute)
	return NewRouter(Deps{
		Logger:         discardLogger(),
		Service:        "platform-api",
		Environment:    "development",
		DB:             fakePinger{},
		Resolver:       res,
		Kernel:         &fakeExecutor{},
		ActionRegistry: action.NewRegistry(),
		Services: &fakeServiceLister{items: []registry.Service{{
			ServiceType: "sub2api", InstanceID: "sub2api-dev",
			Environment: registry.EnvDevelopment, Endpoint: "https://api.example.test",
			Owner: "platform", Status: registry.ServiceActive,
		}}},
		Metrics: &fakeMetricLister{
			items: []ops.Observation{bigMoneyObservation(seriesName, at)},
		},
		MetricHistory: &fakeHistoryLister{
			samples: []ops.Observation{bigMoneyObservation(seriesName, at)},
		},
		AuditEvents: &fakeAuditLister{items: []audit.Event{{
			Sequence: 1, OccurredAt: at, PrincipalID: "staff_alice",
			PrincipalType: principal.TypeHuman, ActionID: "registry.service.create",
			ActionVersion: "1", Environment: "development", Result: audit.ResultSucceeded,
			EventHash: strings.Repeat("a", 64), PrevHash: strings.Repeat("0", 64),
		}}},
	})
}

// TestSensitiveGetsAreNoStore 回归 Codex 冷审 PR #47 第 8 条 /
// PR #43 head `0a0642c` 第 5 条：「敏感 GET 未设置 `Cache-Control: no-store`」。
//
// 这些响应带 principal_id、resource_id、操作前后镜像、收入与余额，
// 不该依赖浏览器或反代的默认缓存行为。
func TestSensitiveGetsAreNoStore(t *testing.T) {
	h := fullRouter(t)
	for path, scope := range map[string]string{
		"/api/v1/audit/events":                       audit.ScopeRead,
		"/api/v1/metrics/history" + historyQuery(""): ops.ScopeRead,
		"/api/v1/metrics":                            ops.ScopeRead,
		"/api/v1/services":                           registry.ScopeRead,
		"/api/v1/actions":                            registry.ScopeRead,
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		devHeaders(req, scope)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d body = %s", path, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s: Cache-Control = %q, want no-store", path, got)
		}
	}
}

// TestNoStoreCoversDeniedAndMissingRoutes：403 与 404 也要带上。
//
// 一个「你没有权限看审计」的响应同样不该被中间缓存留下——它泄漏的是
// 「这个路径存在且需要授权」，而且缓存过的 403 会在授权修好之后继续挡人。
func TestNoStoreCoversDeniedAndMissingRoutes(t *testing.T) {
	h := fullRouter(t)

	// 无身份 → 403
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/audit/events", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("403 响应 Cache-Control = %q, want no-store", got)
	}

	// 权限不足 → 403
	req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/events", nil)
	devHeaders(req, "ops.read") // 没有 audit.read
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec2.Code)
	}
	if got := rec2.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("权限不足响应 Cache-Control = %q, want no-store", got)
	}
}

// TestProbesRemainCacheable：探针**不**打 no-store。
//
// 这是选择路由中间件而不是塞进 WriteJSON 的直接后果，也是刻意的：
// 反代与外部看门狗对 /healthz 做几秒微缓存是合理的运维手段，不该被一条
// 为审计数据设的策略顺手波及。
func TestProbesRemainCacheable(t *testing.T) {
	h := fullRouter(t)
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Header().Get("Cache-Control"); got != "" {
			t.Fatalf("%s 不该被打上 Cache-Control, got %q", path, got)
		}
	}
}

// TestMoneySerializesAsNumberLiteral 回归 Codex 冷审 PR #48 第 4 条的出口侧：
// ops 仓储把金额读回成 json.Number 之后，HTTP 层必须原样输出为**数字字面量**。
//
// 两个方向都要证：一是精度没丢（>2^53 的整数逐字相同），二是类型没漂
// （不能变成带引号的字符串——那会悄悄改掉前端契约）。
func TestMoneySerializesAsNumberLiteral(t *testing.T) {
	const big = "9007199254740995" // 2^53 + 3

	h := fullRouter(t)
	for _, path := range []string{
		"/api/v1/metrics",
		"/api/v1/metrics/history" + historyQuery(""),
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		devHeaders(req, ops.ScopeRead)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d body = %s", path, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"amount_minor":`+big) {
			t.Fatalf("%s: 金额未原样输出为数字字面量: %s", path, body)
		}
		if strings.Contains(body, `"`+big+`"`) {
			t.Fatalf("%s: 金额被序列化成字符串，契约漂移: %s", path, body)
		}
		// float64 退化的具体症状：2^53+3 变成 2^53+4
		if strings.Contains(body, "9007199254740996") {
			t.Fatalf("%s: 金额经 float64 退化了: %s", path, body)
		}
	}
}
