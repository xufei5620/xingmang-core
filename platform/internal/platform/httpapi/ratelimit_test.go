package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// XM-R011 读端点限流（Codex 冷审 #47/#48，Issue #75）。

// fixedClock 让令牌补充可控：跟真实时钟走的话，「攒回一个令牌」这类断言
// 要么靠 sleep（慢且不稳），要么根本写不出来。
type fixedClock struct{ t time.Time }

func (c *fixedClock) now() time.Time      { return c.t }
func (c *fixedClock) add(d time.Duration) { c.t = c.t.Add(d) }

func newTestLimiter(perMinute, burst int) (*RateLimiter, *fixedClock) {
	clock := &fixedClock{t: time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)}
	return NewRateLimiter(RateLimitConfig{
		PerMinute: perMinute, Burst: burst, Now: clock.now,
	}), clock
}

func TestLimiterSpendsBurstThenRefuses(t *testing.T) {
	limiter, _ := newTestLimiter(60, 3)
	for i := range 3 {
		if ok, _ := limiter.Allow("k"); !ok {
			t.Fatalf("第 %d 次应当放行（burst=3）", i+1)
		}
	}
	ok, retryAfter := limiter.Allow("k")
	if ok {
		t.Fatal("burst 用尽后应当拒绝")
	}
	// Retry-After 必须是**算出来的**、且至少 1 秒：返回 0 会让客户端立刻重试、
	// 再次被拒，双方空转。
	if retryAfter < time.Second {
		t.Fatalf("Retry-After = %v，至少应为 1 秒", retryAfter)
	}
}

func TestLimiterRefillsOverTime(t *testing.T) {
	limiter, clock := newTestLimiter(60, 2) // 每秒补 1 个
	limiter.Allow("k")
	limiter.Allow("k")
	if ok, _ := limiter.Allow("k"); ok {
		t.Fatal("桶应当已空")
	}

	clock.add(time.Second)
	if ok, _ := limiter.Allow("k"); !ok {
		t.Fatal("过了 1 秒应当补回 1 个令牌")
	}

	// 补充有上限：闲置很久也不该攒出超过 burst 的额度，
	// 否则一个每天只调一次的脚本能在某天一次性打满。
	clock.add(time.Hour)
	for i := range 2 {
		if ok, _ := limiter.Allow("k"); !ok {
			t.Fatalf("闲置后应当恢复到满桶，第 %d 次却被拒", i+1)
		}
	}
	if ok, _ := limiter.Allow("k"); ok {
		t.Fatal("闲置再久也不该超过 burst=2")
	}
}

// TestLimiterBucketsAreIndependent：不同 (身份, 路由) 各计各的。
//
// 合成一个桶的两种坏法都在这里挡住：一个身份吃光所有人的额度（只按路由分），
// 以及一个身份拉审计堵死自己的看板（只按身份分）。
func TestLimiterBucketsAreIndependent(t *testing.T) {
	limiter, _ := newTestLimiter(60, 1)
	if ok, _ := limiter.Allow("alice\x00/api/v1/audit/events"); !ok {
		t.Fatal("alice 的第一次应当放行")
	}
	if ok, _ := limiter.Allow("alice\x00/api/v1/audit/events"); ok {
		t.Fatal("alice 在同一路由上应当被拒")
	}
	if ok, _ := limiter.Allow("bob\x00/api/v1/audit/events"); !ok {
		t.Fatal("bob 不该被 alice 的用量牵连")
	}
	if ok, _ := limiter.Allow("alice\x00/api/v1/metrics"); !ok {
		t.Fatal("alice 换个路由应当有独立额度")
	}
}

// TestRateLimitKeyUsesRoutePatternNotPath 是这条限流会不会形同虚设的关键。
//
// 按实际路径分桶的话，攻击者遍历 {platform} 就能拿到无限额度——每换一个值
// 就是一个新桶。
func TestRateLimitKeyUsesRoutePatternNotPath(t *testing.T) {
	keys := map[string]bool{}
	for _, platform := range []string{"sub2api", "newapi", "cpa"} {
		rc := chi.NewRouteContext()
		rc.RoutePatterns = []string{"/api/v1/platforms/{platform}/requests"}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/platforms/"+platform+"/requests", nil)
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
		ctx = principal.WithPrincipal(ctx, principal.Principal{ID: "staff:operator-01"})
		keys[rateLimitKey(req.WithContext(ctx))] = true
	}
	if len(keys) != 1 {
		t.Fatalf("三个 platform 应当共用同一个桶，实际分成了 %d 个：%v", len(keys), keys)
	}
}

// TestRateLimitKeySeparatorIsUnambiguous：桶键的分隔符不能被身份 ID 伪造。
//
// principal_id 本身就含冒号（`staff:operator-01`），用冒号连接会让两组不同的
// (身份, 路由) 拼出同一个键——与 XM-R009 修的是同一类错。
func TestRateLimitKeySeparatorIsUnambiguous(t *testing.T) {
	makeKey := func(id, pattern string) string {
		rc := chi.NewRouteContext()
		rc.RoutePatterns = []string{pattern}
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rc)
		ctx = principal.WithPrincipal(ctx, principal.Principal{ID: id})
		return rateLimitKey(req.WithContext(ctx))
	}
	a := makeKey("staff:a", "/api/v1/audit/events")
	b := makeKey("staff", "a/api/v1/audit/events")
	if a == b {
		t.Fatalf("两组不同的 (身份, 路由) 拼出了同一个桶键：%q", a)
	}
}

// TestRateLimitMiddlewareReturns429WithRetryAfter 走完整的中间件路径。
func TestRateLimitMiddlewareReturns429WithRetryAfter(t *testing.T) {
	limiter, _ := newTestLimiter(60, 1)
	handler := RateLimit(limiter, discardLogger())(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/audit/events", nil)
		ctx := principal.WithPrincipal(req.Context(), principal.Principal{ID: "staff:operator-01"})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req.WithContext(ctx))
		return rec
	}

	if got := call().Code; got != http.StatusOK {
		t.Fatalf("第一次 = %d, want 200", got)
	}

	rec := call()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("第二次 = %d, want 429", rec.Code)
	}
	ra := rec.Header().Get("Retry-After")
	if ra == "" {
		t.Fatal("429 必须带 Retry-After——不给的话客户端只能瞎猜什么时候重试")
	}
	secs, err := strconv.Atoi(ra)
	if err != nil || secs < 1 {
		t.Fatalf("Retry-After = %q，应为正整数秒", ra)
	}

	// 错误体沿用全站统一形状，前端不必为 429 单开一条解析分支
	var body ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("429 响应体应当是标准错误形状: %v", err)
	}
	if body.Error.Code != "RATE_LIMITED" {
		t.Fatalf("error.code = %q, want RATE_LIMITED", body.Error.Code)
	}
}

// TestProbesAreNotRateLimited 钉住「探针豁免」这条。
//
// 探针不是靠豁免名单放行的，是靠**装配位置**：中间件只装在 /api/v1 组上，
// 而 /healthz、/readyz 挂在路由根上。这条测试走真实路由，于是「有人把限流
// 挪到 r.Use 上」会当场变红——反代与看门狗每几秒探一次，给它们限流等于
// 自己造一次假故障。
func TestProbesAreNotRateLimited(t *testing.T) {
	router := NewRouter(Deps{
		Logger:      discardLogger(),
		Service:     "test",
		Environment: "development",
		DB:          fakePinger{},
		// 配额压到 1，任何第二次调用都会被限
		RateLimit: RateLimitConfig{PerMinute: 1, Burst: 1},
	})

	for _, path := range []string{"/healthz", "/readyz"} {
		for i := range 5 {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code == http.StatusTooManyRequests {
				t.Fatalf("%s 第 %d 次被限流了——探针必须豁免", path, i+1)
			}
		}
	}
}
