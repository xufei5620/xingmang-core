package sub2api_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/sub2api"
	"github.com/xufei5620/xingmang-platform/connectors/sub2api/contracttest"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// 真实客户端的契约合规测试。
//
// 判据只有一条：**真实实现必须与 Fake 通过同一套 contracttest.RunSuite**。
// 为此这里起一个本地假上游（httptest.NewTLSServer），把套件注入的
// FakeOptions 翻译成假上游的行为——FailWith 变成对应的 HTTP 状态码或畸形
// 响应，Latency 变成响应前的等待，Partial 变成"声称有更多数据却给不出来"。
//
// 这样被测的就是**真实的那条路径**：真实的传输层护栏、真实的 HTTP 解析、
// 真实的错误分类、真实的金额换算。假的只有上游本身。
//
// 另外三组不在套件里的测试，锁住三条不能出事的纪律：
// 凭据不泄漏、目标 allowlist、拒绝重定向。

// ---------------------------------------------------------------------------
// 上游路由：与 upstream.go 一字不差地写死在这里
//
// 故意重复而不是从生产代码里引用常量：路由是我们与上游之间的**约定**，
// 有人改了生产代码里的路径，这里必须红——引用同一个常量的话，
// 改哪边测试都绿，等于没测。
// ---------------------------------------------------------------------------

const (
	upstreamHealth   = "/health"
	upstreamVersion  = "/api/v1/admin/system/version"
	upstreamStats    = "/api/v1/admin/dashboard/stats"
	upstreamUsers    = "/api/v1/admin/users"
	upstreamPayment  = "/api/v1/admin/payment/dashboard"
	upstreamTrend    = "/api/v1/admin/dashboard/trend"
	upstreamAccounts = "/api/v1/admin/accounts"

	// upstreamAuthHeader 是上游的程序化访问头。
	upstreamAuthHeader = "X-Api-Key"

	// fakeUpstreamToken 是假上游的凭据。
	//
	// 刻意用一句明显是占位符的英文而不是像真凭据的高熵串：
	// 仓库里不该出现任何长得像凭据的东西（宪法 7 条），
	// 而泄漏测试只需要一个"能在字符串里被找到"的独特值。
	fakeUpstreamToken = "placeholder-placeholder"

	// fakeCredentialRef 是测试用的凭据引用；引用本身不是秘密。
	fakeCredentialRef = "secret://sub2api/readonly-token"

	// fakeTokenEnvVar 是引用在 env Provider 下的登记落点。
	fakeTokenEnvVar = "XM_TEST_SUB2API_TOKEN"
)

// fakeClockNow 是所有测试共用的固定时钟。
//
// 必须固定：契约套件里的业务日是写死的 "2026-08-27"，而上游的支付看板
// 只认"从今天往回数几天"。跟着真实时钟走的话，这套测试会在某个未来的
// 日子突然变红，而且红得毫无道理——那种失败最消耗人。
var fakeClockNow = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

// capabilityRoutes 是能力到支撑路由的映射，供 MissingCapabilities 使用。
var capabilityRoutes = map[registry.Capability][]string{
	"sub2api.service.version_read":  {upstreamVersion},
	"sub2api.health.read":           {upstreamHealth},
	"sub2api.users.read":            {upstreamStats},
	"sub2api.users.balance_read":    {upstreamUsers},
	"sub2api.orders.read":           {upstreamPayment, upstreamTrend},
	"sub2api.accounts.read":         {upstreamAccounts},
	"sub2api.channels.balance_read": {upstreamAccounts},
}

// fakeUserItems 是用户列表固定数据。
//
// 余额值是挑过的：120.508 拆成 120.50 + 0.004 + 0.004。
// 每个人先四舍五入到分再相加会得到 12050 分，按 8 位小数累加、
// 最后只舍一次才得到 12051 分。测试断言后者——这条断言就是
// "先高精度累加再折算"这个决定的守卫。
const fakeUserItems = `[
  {"id":1,"email":"a@example.test","balance":120.50000000},
  {"id":2,"email":"b@example.test","balance":0.004},
  {"id":3,"email":"c@example.test","balance":0.004},
  {"id":4,"email":"d@example.test","balance":-3.25}
]`

const fakeUserCount = 4

// fakeAccountItems 是上游账号（= 契约里的"渠道"）固定数据。
const fakeAccountItems = `[
  {"id":1,"name":"upstream-a","platform":"anthropic","status":"active",
   "quota_limit":500.00,"quota_used":44.00},
  {"id":2,"name":"upstream-b","platform":"openai","status":"error",
   "error_message":"upstream said no","quota_limit":20,"quota_used":8},
  {"id":3,"name":"upstream-c","platform":"gemini","status":"active",
   "quota_limit":null,"quota_used":null,"quota_daily_limit":10,"quota_daily_used":10}
]`

const fakeAccountCount = 3

// ---------------------------------------------------------------------------
// 假上游
// ---------------------------------------------------------------------------

type fakeRequest struct {
	method string
	path   string
	query  string
	auth   string
}

type fakeUpstream struct {
	t    *testing.T
	opts sub2api.FakeOptions

	// echoCredential 让失败响应把收到的凭据原样回显。
	//
	// 这是在模拟一个**不小心的上游**：真实世界里见过把请求头写进错误正文的
	// 服务。泄漏测试要的就是这种上游——如果我们把响应体拼进错误信息，
	// 凭据就顺着错误链跑到日志里去了。
	echoCredential bool
	// redirectTo 非空时，所有请求都被 302 到该地址。
	redirectTo string
	// accountItems / accountTotal 允许单个测试替换渠道数据。
	accountItems string

	server *httptest.Server

	mu       sync.Mutex
	requests []fakeRequest
}

func startFakeUpstream(t *testing.T, opts sub2api.FakeOptions, tweak ...func(*fakeUpstream)) *fakeUpstream {
	t.Helper()
	u := &fakeUpstream{t: t, opts: opts, accountItems: fakeAccountItems}
	for _, fn := range tweak {
		fn(u)
	}

	mux := http.NewServeMux()
	suppressed := u.suppressedRoutes()
	register := func(path string, h http.HandlerFunc) {
		if suppressed[path] {
			// 不注册 = 404 = 客户端归类为 not_supported = 该能力不可用。
			// 这正是"旧版本上游少几项能力"在 HTTP 上的样子。
			return
		}
		mux.HandleFunc("GET "+path, h)
	}
	register(upstreamHealth, u.handleHealth)
	register(upstreamVersion, u.handleVersion)
	register(upstreamStats, u.dataRoute(u.handleStats))
	register(upstreamUsers, u.dataRoute(u.handleUsers))
	register(upstreamPayment, u.dataRoute(u.handlePayment))
	register(upstreamTrend, u.dataRoute(u.handleTrend))
	register(upstreamAccounts, u.dataRoute(u.handleAccounts))

	var handler http.Handler = mux
	if u.redirectTo != "" {
		target := u.redirectTo
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target, http.StatusFound)
		})
	}

	u.server = httptest.NewTLSServer(u.record(handler))
	t.Cleanup(u.server.Close)
	return u
}

func (u *fakeUpstream) suppressedRoutes() map[string]bool {
	out := make(map[string]bool)
	for _, capability := range u.opts.MissingCapabilities {
		for _, route := range capabilityRoutes[capability] {
			out[route] = true
		}
	}
	return out
}

func (u *fakeUpstream) record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.requests = append(u.requests, fakeRequest{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.RawQuery,
			auth:   r.Header.Get(upstreamAuthHeader),
		})
		u.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}

func (u *fakeUpstream) recorded() []fakeRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]fakeRequest(nil), u.requests...)
}

func (u *fakeUpstream) now() time.Time {
	if u.opts.Now != nil {
		return u.opts.Now().UTC()
	}
	return fakeClockNow
}

func (u *fakeUpstream) observedAt() time.Time { return u.now().Add(-u.opts.ObservedAge) }

// wait 模拟上游延迟，同时尊重客户端断开——否则 httptest 的 Close
// 会一直等到 handler 返回，一个 5 秒的延迟注入会让整个测试慢 5 秒。
func (u *fakeUpstream) wait(r *http.Request) bool {
	if u.opts.Latency <= 0 {
		return true
	}
	select {
	case <-time.After(u.opts.Latency):
		return true
	case <-r.Context().Done():
		return false
	}
}

// dataRoute 把 FailWith 翻译成数据路由上的真实故障。
//
// 只作用于数据路由：Fake 的 FailWith 也不影响 Version/Health
// （它们各有 UnsupportedVersion / Unhealthy 两个开关），
// 两个实现在这一点上必须一致，否则"换实现"会换掉故障语义。
func (u *fakeUpstream) dataRoute(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !u.wait(r) {
			return
		}
		switch u.opts.FailWith {
		case connector.KindAuth:
			u.writeCredentialLeakingError(w, r, http.StatusUnauthorized, "UNAUTHORIZED")
			return
		case connector.KindRateLimited:
			w.Header().Set("Retry-After", "30")
			u.writeCredentialLeakingError(w, r, http.StatusTooManyRequests, "RATE_LIMITED")
			return
		case connector.KindUnavailable:
			u.writeCredentialLeakingError(w, r, http.StatusServiceUnavailable, "UPSTREAM_DOWN")
			return
		case connector.KindBadResponse:
			// 200 但正文是半截 JSON：格式非法必须归 bad_response，
			// 而不是被当成"网络出问题"重试。
			writeRaw(w, http.StatusOK, `{"code":0,"message":"success","data":{"total_users":`)
			return
		}
		h(w, r)
	}
}

// writeCredentialLeakingError 模拟一个把请求头回显进错误正文的上游。
func (u *fakeUpstream) writeCredentialLeakingError(w http.ResponseWriter, r *http.Request, status int, code string) {
	body := fmt.Sprintf(`{"code":%q,"message":"upstream refused the request"}`, code)
	if u.echoCredential {
		body = fmt.Sprintf(`{"code":%q,"message":"rejected key %s for user a@example.test"}`,
			code, r.Header.Get(upstreamAuthHeader))
	}
	writeRaw(w, status, body)
}

func (u *fakeUpstream) handleHealth(w http.ResponseWriter, r *http.Request) {
	if !u.wait(r) {
		return
	}
	if u.opts.Unhealthy {
		writeRaw(w, http.StatusServiceUnavailable, `{"status":"degraded"}`)
		return
	}
	writeRaw(w, http.StatusOK, `{"status":"ok"}`)
}

func (u *fakeUpstream) handleVersion(w http.ResponseWriter, r *http.Request) {
	if !u.wait(r) {
		return
	}
	version := strings.TrimSpace(u.opts.Version)
	if version == "" {
		// 上游 VERSION 文件里的形态：裸的 x.y.z，没有 v 前缀
		version = "0.1.133"
	}
	if u.opts.UnsupportedVersion {
		version = "9.9.9"
	}
	writeRaw(w, http.StatusOK, envelope(fmt.Sprintf(
		`{"version":%q,"commit":"deadbeef","build_type":"docker"}`, version)))
}

func (u *fakeUpstream) handleStats(w http.ResponseWriter, r *http.Request) {
	writeRaw(w, http.StatusOK, envelope(fmt.Sprintf(
		`{"total_users":2482,"today_new_users":5,"active_users":311,
		  "total_cost":123.456789,"today_cost":1.23,
		  "stats_updated_at":%q,"stats_stale":false}`,
		u.observedAt().Format(time.RFC3339))))
}

func (u *fakeUpstream) handleUsers(w http.ResponseWriter, r *http.Request) {
	page := queryInt(r, "page", 1)
	total := fakeUserCount
	if u.opts.Partial {
		// 声称有 99 个用户却只给得出第一页：客户端必须把这件事
		// 标记成部分数据，而不是把少算的余额当成全部余额。
		total = 99
	}
	items := "[]"
	if page == 1 {
		items = fakeUserItems
	}
	writeRaw(w, http.StatusOK, envelope(fmt.Sprintf(
		`{"items":%s,"total":%d,"page":%d,"page_size":%s,"pages":1}`,
		items, total, page, r.URL.Query().Get("page_size"))))
}

func (u *fakeUpstream) handlePayment(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r, "days", 1)
	if days < 1 {
		days = 1
	}
	series := make([]string, 0, days)
	for i := 0; i < days; i++ {
		date := u.now().AddDate(0, 0, -i).Format("2006-01-02")
		amount, count := `0`, 0
		if i == 0 {
			amount, count = `12345.60`, 168
		}
		series = append(series, fmt.Sprintf(`{"date":%q,"amount":%s,"count":%d}`, date, amount, count))
	}
	writeRaw(w, http.StatusOK, envelope(fmt.Sprintf(
		`{"today_amount":12345.60,"total_amount":99999.99,"today_count":168,
		  "total_count":2100,"avg_amount":47.6,"pending_orders":2,
		  "daily_series":[%s]}`, strings.Join(series, ","))))
}

func (u *fakeUpstream) handleTrend(w http.ResponseWriter, r *http.Request) {
	day := strings.TrimSpace(r.URL.Query().Get("start_date"))
	if day == "" {
		day = u.now().Format("2006-01-02")
	}
	writeRaw(w, http.StatusOK, envelope(fmt.Sprintf(
		`{"start_date":%q,"end_date":%q,"granularity":"day",
		  "trend":[{"date":%q,"requests":1500,"input_tokens":120,"output_tokens":340,
		            "cost":7890.10,"actual_cost":7000.00}]}`, day, day, day)))
}

func (u *fakeUpstream) handleAccounts(w http.ResponseWriter, r *http.Request) {
	page := queryInt(r, "page", 1)
	total := fakeAccountCount
	if u.opts.Partial {
		total = 99
	}
	items := "[]"
	if page == 1 {
		items = u.accountItems
	}
	writeRaw(w, http.StatusOK, envelope(fmt.Sprintf(
		`{"items":%s,"total":%d,"page":%d,"page_size":200,"pages":1}`, items, total, page)))
}

func envelope(data string) string {
	return `{"code":0,"message":"success","data":` + data + `}`
}

func writeRaw(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func queryInt(r *http.Request, name string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get(name)))
	if err != nil {
		return fallback
	}
	return v
}

// ---------------------------------------------------------------------------
// 客户端装配
// ---------------------------------------------------------------------------

func (u *fakeUpstream) host() string {
	parsed, err := url.Parse(u.server.URL)
	if err != nil {
		u.t.Fatalf("解析假上游地址: %v", err)
	}
	return parsed.Hostname()
}

func (u *fakeUpstream) config() connector.Config {
	return connector.Config{
		ServiceInstanceID: "sub2api-test",
		Environment:       "test",
		Endpoint:          u.server.URL,
		CredentialRef:     fakeCredentialRef,
		TargetAllowlist:   []string{u.host()},
		Timeout:           10 * time.Second,
	}
}

func fakeSecretProvider(t *testing.T, logger *slog.Logger) secrets.SecretProvider {
	t.Helper()
	provider, err := secrets.NewEnvProvider(
		map[string]string{fakeCredentialRef: fakeTokenEnvVar},
		secrets.WithLookup(func(name string) (string, bool) {
			if name != fakeTokenEnvVar {
				return "", false
			}
			return fakeUpstreamToken, true
		}),
	)
	if err != nil {
		t.Fatalf("装配 env provider: %v", err)
	}
	if logger == nil {
		return provider
	}
	// 审计装饰器：每次解析都留一条不含明文的记录（规格 §4.5）
	return secrets.NewAudited(provider, secrets.NewSlogRecorder(logger), "test")
}

func (u *fakeUpstream) newClient(t *testing.T, extra ...sub2api.Option) sub2api.ReadClient {
	t.Helper()
	opts := append([]sub2api.Option{
		// 只换"怎么连"：让客户端信任 httptest 的自签证书。
		// 只读方法限制、allowlist、拒绝重定向依旧是生产那一份代码。
		sub2api.WithBaseTransport(u.server.Client().Transport),
		sub2api.WithClock(u.now),
	}, extra...)
	client, err := sub2api.NewClient(u.config(), fakeSecretProvider(t, nil), opts...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

// ---------------------------------------------------------------------------
// 合规判据：真实客户端必须通过与 Fake 同一套契约套件
// ---------------------------------------------------------------------------

func TestRealClientSatisfiesContract(t *testing.T) {
	contracttest.RunSuite(t, func(opts sub2api.FakeOptions) sub2api.ReadClient {
		upstream := startFakeUpstream(t, opts)
		return upstream.newClient(t)
	})
}

// ---------------------------------------------------------------------------
// 上游形状映射
// ---------------------------------------------------------------------------

func TestRealClientMapsUpstreamShapes(t *testing.T) {
	upstream := startFakeUpstream(t, sub2api.FakeOptions{})
	client := upstream.newClient(t)
	ctx := t.Context()

	t.Run("版本", func(t *testing.T) {
		v, err := client.Version(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if v.Detected != "0.1.133" {
			t.Fatalf("Detected = %q, want 0.1.133", v.Detected)
		}
		if !v.Supported {
			t.Fatalf("0.1.133 应在兼容矩阵内: %+v", v)
		}
		if !strings.HasPrefix(v.Fingerprint, "sha256:") {
			t.Fatalf("指纹形态不对: %q", v.Fingerprint)
		}
		if v.DetectedAt.IsZero() {
			t.Fatal("缺少 DetectedAt")
		}
	})

	t.Run("健康", func(t *testing.T) {
		h, err := client.Health(ctx)
		if err != nil || !h.Healthy {
			t.Fatalf("Health = %+v, err = %v", h, err)
		}
	})

	t.Run("用户与余额", func(t *testing.T) {
		stats, err := client.UserStats(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stats.TotalUsers != 2482 || stats.ActiveUsers != 311 {
			t.Fatalf("用户数映射错了: %+v", stats)
		}
		// 120.50 + 0.004 + 0.004 = 120.508 → 12051 分。
		// 每人先舍到分再加会得到 12050——这条断言守着"先累加再折算"。
		if stats.BalanceMinorUnits != 12051 {
			t.Fatalf("余额 = %d 分, want 12051（先按 8 位小数累加，最后只舍一次）",
				stats.BalanceMinorUnits)
		}
		if stats.OverdraftMinorUnits != 325 {
			t.Fatalf("透支 = %d 分, want 325（负余额取绝对值单独汇总）",
				stats.OverdraftMinorUnits)
		}
		if stats.Currency != "USD" {
			t.Fatalf("币种 = %q, want USD（上游全线美元记账）", stats.Currency)
		}
		if !stats.ObservedAt.Equal(fakeClockNow) {
			t.Fatalf("ObservedAt = %v, want 上游的 stats_updated_at %v", stats.ObservedAt, fakeClockNow)
		}
		if stats.Watermark != fakeClockNow.Format(time.RFC3339) {
			t.Fatalf("水位 = %q, want 上游给的时间戳", stats.Watermark)
		}
		if stats.IsPartial {
			t.Fatal("拉全了不该标记为部分数据")
		}
	})

	t.Run("业务日收入与成本", func(t *testing.T) {
		orders, err := client.DailyOrders(ctx, "2026-08-27")
		if err != nil {
			t.Fatal(err)
		}
		if orders.Day != "2026-08-27" {
			t.Fatalf("业务日 = %q", orders.Day)
		}
		if orders.RevenueMinorUnits != 1234560 {
			t.Fatalf("收入 = %d, want 1234560（12345.60 美元）", orders.RevenueMinorUnits)
		}
		if orders.CostMinorUnits != 789010 {
			t.Fatalf("成本 = %d, want 789010（取 cost 而不是 actual_cost）", orders.CostMinorUnits)
		}
		if orders.OrderCount != 168 {
			t.Fatalf("订单数 = %d, want 168", orders.OrderCount)
		}
		if orders.IsPartial {
			t.Fatal("两个来源都给出了这一天，不该标记为部分数据")
		}
		if !strings.Contains(orders.Watermark, "2026-08-27") {
			t.Fatalf("水位应带上业务日: %q", orders.Watermark)
		}
	})

	t.Run("渠道余额", func(t *testing.T) {
		balances, err := client.ChannelBalances(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(balances) != 3 {
			t.Fatalf("渠道数 = %d, want 3: %+v", len(balances), balances)
		}
		want := []sub2api.ChannelBalance{
			{ChannelID: "1", ChannelName: "upstream-a", BalanceMinorUnits: 45600, Currency: "USD", TokenValid: true},
			{ChannelID: "2", ChannelName: "upstream-b", BalanceMinorUnits: 1200, Currency: "USD", TokenValid: false},
			// 没配总额度但配了日额度：退到日额度口径，用满了就是 0
			{ChannelID: "3", ChannelName: "upstream-c", BalanceMinorUnits: 0, Currency: "USD", TokenValid: true},
		}
		for i, w := range want {
			got := balances[i]
			if got.ChannelID != w.ChannelID || got.ChannelName != w.ChannelName ||
				got.BalanceMinorUnits != w.BalanceMinorUnits ||
				got.Currency != w.Currency || got.TokenValid != w.TokenValid {
				t.Fatalf("渠道 #%d = %+v, want %+v", i, got, w)
			}
			if got.ObservedAt.IsZero() || got.Watermark == "" {
				t.Fatalf("渠道 #%d 缺少新鲜度: %+v", i, got)
			}
		}
	})

	t.Run("能力清单只报实现了的", func(t *testing.T) {
		caps, err := client.Capabilities(ctx)
		if err != nil {
			t.Fatal(err)
		}
		got := make(map[registry.Capability]bool, len(caps))
		for _, c := range caps {
			got[c] = true
		}
		for _, want := range []registry.Capability{
			"sub2api.health.read", "sub2api.service.version_read",
			"sub2api.users.read", "sub2api.users.balance_read",
			"sub2api.orders.read", "sub2api.accounts.read",
			"sub2api.channels.balance_read",
		} {
			if !got[want] {
				t.Fatalf("缺少能力 %q: %v", want, caps)
			}
		}
		// 没有对应 ReadClient 方法的能力不该被声明——声明一项兑现不了的
		// 能力等于说谎，调用方会据此做出错误的决策。
		for _, unwanted := range []registry.Capability{
			"sub2api.groups.read", "sub2api.models.usage_read",
		} {
			if got[unwanted] {
				t.Fatalf("声明了没有实现的能力 %q", unwanted)
			}
		}
	})
}

// TestRealClientMarksPartialWhenChannelsLackQuota 锁住渠道侧的一条判断：
// 上游账号没配任何额度上限时，"余额"这个概念根本不存在——
// 报成 0 会在看板上变成"这个渠道没钱了"的假警报。
func TestRealClientMarksPartialWhenChannelsLackQuota(t *testing.T) {
	upstream := startFakeUpstream(t, sub2api.FakeOptions{}, func(u *fakeUpstream) {
		u.accountItems = `[
		  {"id":1,"name":"has-quota","status":"active","quota_limit":100,"quota_used":25},
		  {"id":2,"name":"no-quota","status":"active","quota_limit":null,"quota_used":null}
		]`
	})
	balances, err := upstream.newClient(t).ChannelBalances(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(balances) != 1 {
		t.Fatalf("只该报得出配了额度的那个渠道, got %+v", balances)
	}
	if balances[0].BalanceMinorUnits != 7500 {
		t.Fatalf("余额 = %d, want 7500", balances[0].BalanceMinorUnits)
	}
	if !balances[0].IsPartial {
		t.Fatal("少报了渠道就必须标记为部分数据，否则看板会把不全的清单当全的")
	}
}

// TestRealClientRejectsFutureBusinessDayWindow：上游的支付看板只能从今天
// 往回数，回溯不了 90 天以前——那是**能力边界**，不是一次失败的读取。
func TestRealClientRejectsTooOldBusinessDay(t *testing.T) {
	upstream := startFakeUpstream(t, sub2api.FakeOptions{})
	_, err := upstream.newClient(t).DailyOrders(t.Context(), "2020-01-01")
	if connector.KindOf(err) != connector.KindNotSupported {
		t.Fatalf("KindOf = %q, want not_supported（err=%v）", connector.KindOf(err), err)
	}
}

// TestRealClientClassifiesUpstreamStatuses 覆盖 ADR-004 的错误映射表里
// 契约套件没有覆盖到的那几格。
func TestRealClientClassifiesUpstreamStatuses(t *testing.T) {
	// 路由不存在 = 这个上游版本没有这项能力，不是"上游挂了"：
	// 两者的处置完全不同，重试对前者永远没用。
	upstream := startFakeUpstream(t, sub2api.FakeOptions{
		MissingCapabilities: []registry.Capability{"sub2api.users.read"},
	})
	_, err := upstream.newClient(t).UserStats(t.Context())
	if connector.KindOf(err) != connector.KindNotSupported {
		t.Fatalf("404 的分类 = %q, want not_supported（err=%v）", connector.KindOf(err), err)
	}

	// 业务码非 0：HTTP 说成功但上游说没成功，属于响应看不懂
	badCode := startFakeUpstreamWithHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeRaw(w, http.StatusOK, `{"code":40004,"message":"nope"}`)
	})
	if _, err := badCode.newClient(t).UserStats(t.Context()); connector.KindOf(err) != connector.KindBadResponse {
		t.Fatalf("非 0 业务码的分类 = %q, want bad_response（err=%v）", connector.KindOf(err), err)
	}
}

// startFakeUpstreamWithHandler 起一个所有路由都走同一个 handler 的假上游。
func startFakeUpstreamWithHandler(t *testing.T, h http.HandlerFunc) *fakeUpstream {
	t.Helper()
	u := &fakeUpstream{t: t, accountItems: fakeAccountItems}
	u.server = httptest.NewTLSServer(u.record(h))
	t.Cleanup(u.server.Close)
	return u
}

// ---------------------------------------------------------------------------
// 三条不能出事的纪律
// ---------------------------------------------------------------------------

// TestRealClientNeverLeaksCredential：凭据明文不进错误、不进日志（宪法 7 条）。
//
// 假上游在这里会把收到的凭据原样回显进错误正文——真实世界里见过这种上游。
// 只要我们把响应体拼进错误信息，凭据就顺着错误链跑进日志了。
func TestRealClientNeverLeaksCredential(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	upstream := startFakeUpstream(t, sub2api.FakeOptions{FailWith: connector.KindAuth},
		func(u *fakeUpstream) { u.echoCredential = true })

	client, err := sub2api.NewClient(upstream.config(), fakeSecretProvider(t, logger),
		sub2api.WithBaseTransport(upstream.server.Client().Transport),
		sub2api.WithClock(upstream.now))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, readErr := client.UserStats(t.Context())
	if readErr == nil {
		t.Fatal("注入 401 后应报错")
	}
	if connector.KindOf(readErr) != connector.KindAuth {
		t.Fatalf("401 的分类 = %q, want auth", connector.KindOf(readErr))
	}

	// 先证明这个测试不是空转：凭据确实被发出去过，上游也确实回显了它。
	sent := false
	for _, req := range upstream.recorded() {
		if req.auth == fakeUpstreamToken {
			sent = true
		}
	}
	if !sent {
		t.Fatal("客户端根本没发出凭据，这个测试就没有意义了")
	}

	// 把错误链能吐出来的每一种文本都翻一遍
	dumps := map[string]string{
		"Error()": readErr.Error(),
		"%v":      fmt.Sprintf("%v", readErr),
		"%+v":     fmt.Sprintf("%+v", readErr),
		"%#v":     fmt.Sprintf("%#v", readErr),
		"unwrap":  unwrapDump(readErr),
		"日志":      logs.String(),
	}
	for name, dump := range dumps {
		if strings.Contains(dump, fakeUpstreamToken) {
			t.Fatalf("%s 泄漏了凭据明文: %s", name, dump)
		}
		// 上游正文里的用户邮箱同样不该被搬进我们的错误与日志
		if strings.Contains(dump, "a@example.test") {
			t.Fatalf("%s 泄漏了上游响应正文: %s", name, dump)
		}
	}
	// 审计记录必须留下（谁在什么时候用了这个只读账号），但只留引用不留明文
	if !strings.Contains(logs.String(), "secret_access") ||
		!strings.Contains(logs.String(), fakeCredentialRef) {
		t.Fatalf("凭据解析应留审计记录: %s", logs.String())
	}
	// 对外错误文本只有分类 + 操作名
	if got := readErr.Error(); got != "auth: sub2api.users.read" {
		t.Fatalf("对外错误文本 = %q", got)
	}
}

func unwrapDump(err error) string {
	var parts []string
	for e := err; e != nil; e = errors.Unwrap(e) {
		parts = append(parts, fmt.Sprintf("%v|%#v", e, e))
	}
	return strings.Join(parts, " -> ")
}

// TestRealClientEnforcesTargetAllowlist：目标 allowlist 是硬护栏。
func TestRealClientEnforcesTargetAllowlist(t *testing.T) {
	upstream := startFakeUpstream(t, sub2api.FakeOptions{})

	// 端点主机不在自己的 allowlist 里：这种配置能"配得出来"但一个请求都
	// 发不出去，属于最难排查的故障，所以在构造期就拒（闸 1）。
	cfg := upstream.config()
	cfg.TargetAllowlist = []string{"api.somewhere-else.test"}
	if _, err := sub2api.NewClient(cfg, fakeSecretProvider(t, nil)); err == nil {
		t.Fatal("端点不在 allowlist 内的配置必须被拒")
	} else if connector.KindOf(err) != connector.KindInternal {
		t.Fatalf("配置错误的分类 = %q, want internal（是我们的配置问题，不是上游的）",
			connector.KindOf(err))
	}

	// allowlist 为空：fail closed，不是"放行一切"
	cfg = upstream.config()
	cfg.TargetAllowlist = nil
	if _, err := sub2api.NewClient(cfg, fakeSecretProvider(t, nil)); err == nil {
		t.Fatal("空 allowlist 必须被拒（fail closed）")
	}
}

// TestRealClientRefusesRedirects：上游一个 302 就能把请求引到 allowlist
// 之外，所以重定向一律拒绝——而且拒绝的分类必须是 forbidden_target，
// 不能被改判成 unavailable，否则"目标被换了"会伪装成"上游挂了"。
func TestRealClientRefusesRedirects(t *testing.T) {
	upstream := startFakeUpstream(t, sub2api.FakeOptions{}, func(u *fakeUpstream) {
		u.redirectTo = "https://evil.example.test/api/v1/admin/dashboard/stats"
	})
	_, err := upstream.newClient(t).UserStats(t.Context())
	if err == nil {
		t.Fatal("重定向必须被拒绝")
	}
	if connector.KindOf(err) != connector.KindForbiddenTarget {
		t.Fatalf("重定向被拒的分类 = %q, want forbidden_target（err=%v）",
			connector.KindOf(err), err)
	}
	if strings.Contains(err.Error(), "evil.example.test") {
		t.Fatalf("错误信息不该带完整目标 URL: %s", err.Error())
	}
}

// TestRealClientRequiresSecretProvider：没有 Provider 就解析不出凭据，
// 与其带着一个必然失败的客户端上路，不如在构造期就说清楚。
func TestRealClientRequiresSecretProvider(t *testing.T) {
	upstream := startFakeUpstream(t, sub2api.FakeOptions{})
	if _, err := sub2api.NewClient(upstream.config(), nil); err == nil {
		t.Fatal("没有 SecretProvider 必须被拒")
	}
}

// TestRealClientResolvesCredentialLazily：构造期不做任何 I/O，
// 凭据在首次读取时才解析——用的是那次请求的 ctx，取消才管得住它。
func TestRealClientResolvesCredentialLazily(t *testing.T) {
	upstream := startFakeUpstream(t, sub2api.FakeOptions{})
	failing := failingProvider{err: errors.New("provider 暂时不可用")}

	client, err := sub2api.NewClient(upstream.config(), failing,
		sub2api.WithBaseTransport(upstream.server.Client().Transport),
		sub2api.WithClock(upstream.now))
	if err != nil {
		t.Fatalf("构造期不该解析凭据，也就不该在这里失败: %v", err)
	}
	if len(upstream.recorded()) != 0 {
		t.Fatal("构造期不该发出任何请求")
	}
	_, readErr := client.UserStats(t.Context())
	if connector.KindOf(readErr) != connector.KindAuth {
		t.Fatalf("凭据解析失败的分类 = %q, want auth（err=%v）", connector.KindOf(readErr), readErr)
	}
}

type failingProvider struct{ err error }

func (p failingProvider) Resolve(context.Context, secrets.CredentialRef, string) (secrets.SecretValue, error) {
	return secrets.SecretValue{}, p.err
}

func (p failingProvider) Metadata(_ context.Context, ref secrets.CredentialRef) (secrets.SecretMetadata, error) {
	return secrets.SecretMetadata{Ref: ref, Provider: "failing"}, nil
}
