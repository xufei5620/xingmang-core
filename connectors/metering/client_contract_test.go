package metering_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/metering"
	"github.com/xufei5620/xingmang-platform/connectors/metering/contracttest"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// 两个真实驱动的契约合规测试。
//
// 判据只有一条：**真实实现必须与 Fake 通过同一套 contracttest.RunSuite**。
// 为此这里起一个本地假上游（httptest.NewTLSServer），把套件注入的
// FakeOptions 翻译成假上游的行为——FailWith 变成对应的 HTTP 状态码或畸形
// 响应，Latency 变成响应前的等待。
//
// 这样被测的就是**真实的那条路径**：真实的传输层护栏、真实的 HTTP 解析、
// 真实的错误分类、真实的金额换算。假的只有上游本身。
//
// 另有几组不在套件里的测试，锁住这条链路上不能出事的口径与纪律：
// type=2、actual_cost（而非 trend[].cost）、quota_per_unit 运行期可变、
// 每令牌凭据不串号、凭据不泄漏、只读方法限制、目标 allowlist、拒绝重定向。

// ---------------------------------------------------------------------------
// 上游路由：与 sub2api.go / newapi.go 一字不差地写死在这里
//
// 故意重复而不是从生产代码里引用常量：路由是我们与上游之间的**约定**，
// 有人改了生产代码里的路径，这里必须红——引用同一个常量的话，
// 改哪边测试都绿，等于没测。
// ---------------------------------------------------------------------------

const (
	upstreamHealth       = "/health"
	upstreamVersion      = "/api/v1/admin/system/version"
	upstreamUsage        = "/v1/usage"
	upstreamAccountStats = "/api/v1/admin/accounts/"
	upstreamStatus       = "/api/status"
	upstreamLogStat      = "/api/log/self/stat"

	upstreamAdminHeader   = "X-Api-Key"
	upstreamNewAPIUser    = "New-Api-User"
	upstreamSessionCookie = "session"

	// 假上游的凭据。刻意用一句明显是占位符的英文而不是像真凭据的高熵串：
	// 仓库里不该出现任何长得像凭据的东西（宪法 7 条、ADR-014 的 fixture 纪律），
	// 而泄漏测试只需要一个「能在字符串里被找到」的独特值。
	fakeAdminSecret   = "placeholder-admin-placeholder"
	fakeTokenSecret   = "placeholder-token-placeholder"
	fakeSessionSecret = "42:placeholder-session-placeholder"

	// 凭据引用本身不是秘密，可安全出现在代码与日志里（ADR-014）。
	adminRefText   = "secret://metering-test/sub2api-admin"
	tokenRefText   = "secret://metering-test/sub2api-token"
	sessionRefText = "secret://metering-test/newapi-session"

	adminEnvVar   = "XM_TEST_METERING_ADMIN"
	tokenEnvVar   = "XM_TEST_METERING_TOKEN"
	sessionEnvVar = "XM_TEST_METERING_SESSION"
)

// upstreamActualCost 是假上游给的实扣字面量。
//
// 取设计稿 §2.4 worked example 的那个值：它在 float64 里不精确，
// 走一遍浮点解析就会变成 5813728 或 5813730，而整数路径必须给出 5813729。
const upstreamActualCost = "5.813729"

// upstreamQuota 是假上游给的 newapi 消费额度（整数 credits）。
const upstreamQuota = int64(1_234_567)

// fakeProvider 是测试用的 SecretProvider：三条登记，值来自内存而不是真环境变量。
func fakeProvider(t *testing.T) secrets.SecretProvider {
	t.Helper()
	values := map[string]string{
		adminEnvVar:   fakeAdminSecret,
		tokenEnvVar:   fakeTokenSecret,
		sessionEnvVar: fakeSessionSecret,
	}
	p, err := secrets.NewEnvProvider(
		map[string]string{
			adminRefText:   adminEnvVar,
			tokenRefText:   tokenEnvVar,
			sessionRefText: sessionEnvVar,
		},
		secrets.WithLookup(func(name string) (string, bool) {
			v, ok := values[name]
			return v, ok
		}),
	)
	if err != nil {
		t.Fatalf("构造 Provider 失败: %v", err)
	}
	// 审计装饰器包在外面：每次解析都留一条不含明文的记录（规格 §4.5）。
	// 用一个丢弃输出的 logger——本用例不断言日志内容，但要确保这条路径
	// 在测试里也真的跑过。
	return secrets.NewAudited(p, secrets.NewSlogRecorder(
		slog.New(slog.NewTextHandler(io_Discard{}, nil))), "production")
}

type io_Discard struct{}

func (io_Discard) Write(p []byte) (int, error) { return len(p), nil }

// allowedHost 从 httptest 的 URL 里取出**不带端口**的主机名。
//
// ReadOnlyTransport 的 allowlist 集合是按原样存的，查表时却用 hostOnly()
// 把 host:port 削成 host——写成 "127.0.0.1:41234" 会永远匹配不上，
// 于是每个用例都变成 forbidden_target。这个坑值得留一行注释：
// 它的症状（所有请求都被护栏拒绝）看起来像护栏坏了，其实是配置写法不对。
func allowedHost(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("解析测试服务器地址失败: %v", err)
	}
	return u.Hostname()
}

// fakeUpstream 把 FakeOptions 翻译成 HTTP 行为。
type fakeUpstream struct {
	t    *testing.T
	opts metering.FakeOptions

	mu sync.Mutex
	// requests 记录收到的请求，供口径断言检查查询参数与请求头。
	requests []recordedRequest
	// quotaPerUnit 可在测试中途改，用来验证「运行期可变」。
	quotaPerUnit int64
}

type recordedRequest struct {
	method string
	path   string
	query  url.Values
	// authorization / adminKey / newAPIUser / session 记录的是**收到的头**，
	// 用来验证「拼对了头」以及「没串号」。这是测试里唯一允许存在明文的地方，
	// 而这里的明文本来就是测试自己造的占位符。
	authorization string
	adminKey      string
	newAPIUser    string
	session       string
}

func (u *fakeUpstream) record(r *http.Request) {
	sessionValue := ""
	if c, err := r.Cookie(upstreamSessionCookie); err == nil {
		sessionValue = c.Value
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.requests = append(u.requests, recordedRequest{
		method:        r.Method,
		path:          r.URL.Path,
		query:         r.URL.Query(),
		authorization: r.Header.Get("Authorization"),
		adminKey:      r.Header.Get(upstreamAdminHeader),
		newAPIUser:    r.Header.Get(upstreamNewAPIUser),
		session:       sessionValue,
	})
}

func (u *fakeUpstream) seen() []recordedRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]recordedRequest(nil), u.requests...)
}

func (u *fakeUpstream) lastTo(path string) (recordedRequest, bool) {
	for i := len(u.requests) - 1; i >= 0; i-- {
		if u.requests[i].path == path {
			return u.requests[i], true
		}
	}
	return recordedRequest{}, false
}

// missing 报告某条路由是否被 MissingCapabilities 关掉。
func (u *fakeUpstream) missing(capability registry.Capability) bool {
	for _, c := range u.opts.MissingCapabilities {
		if c == capability {
			return true
		}
	}
	return false
}

// failStatus 把 FailWith 翻译成 HTTP 行为；返回 false 表示不注入故障。
func (u *fakeUpstream) failStatus(w http.ResponseWriter) bool {
	switch u.opts.FailWith {
	case "":
		return false
	case connector.KindAuth:
		w.WriteHeader(http.StatusUnauthorized)
	case connector.KindRateLimited:
		w.WriteHeader(http.StatusTooManyRequests)
	case connector.KindUnavailable:
		w.WriteHeader(http.StatusServiceUnavailable)
	case connector.KindBadResponse:
		// 200 + 畸形正文：这是最难查的一类故障，必须被归成 bad_response
		// 而不是伪装成「上游挂了」
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": {`))
	default:
		w.WriteHeader(http.StatusInternalServerError)
	}
	return true
}

func (u *fakeUpstream) delay() {
	if u.opts.Latency > 0 {
		time.Sleep(u.opts.Latency)
	}
}

func (u *fakeUpstream) version() string {
	if u.opts.UnsupportedVersion {
		// 矩阵外的版本：必须被标记为不支持而不是报错
		return "9.9.9"
	}
	if u.opts.Version != "" {
		return u.opts.Version
	}
	return "0.1.183"
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// sub2apiHandler 是 sub2api 形状的假上游。
func (u *fakeUpstream) sub2apiHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc(upstreamHealth, func(w http.ResponseWriter, r *http.Request) {
		u.record(r)
		u.delay()
		if u.opts.Unhealthy {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeJSON(w, map[string]any{"status": "ok"})
	})

	mux.HandleFunc(upstreamVersion, func(w http.ResponseWriter, r *http.Request) {
		u.record(r)
		u.delay()
		// 版本路由被关掉时，收入能力也随之不可用（sub2api 客户端把两者
		// 绑在同一把 admin key 上）——这正是 MissingCapabilities 要模拟的
		if u.missing("metering.account.revenue_read") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"data": map[string]any{"version": u.version()}})
	})

	mux.HandleFunc(upstreamUsage, func(w http.ResponseWriter, r *http.Request) {
		u.record(r)
		u.delay()
		if u.failStatus(w) {
			return
		}
		// 金额以 JSON **数字字面量**发出（上游用 float64 序列化就是这个形状）：
		// 客户端必须原样接住文本再走整数解析，不能中途落进 float64
		_, _ = fmt.Fprintf(w, `{"usage":{"today":{"actual_cost":%s}}}`, upstreamActualCost)
	})

	mux.HandleFunc(upstreamAccountStats, func(w http.ResponseWriter, r *http.Request) {
		u.record(r)
		u.delay()
		if u.failStatus(w) {
			return
		}
		_, _ = fmt.Fprint(w, `{"data":{"summary":{"today":{"user_cost":"12.3456"}}}}`)
	})

	return mux
}

// newapiHandler 是 newapi 形状的假上游。
func (u *fakeUpstream) newapiHandler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc(upstreamStatus, func(w http.ResponseWriter, r *http.Request) {
		u.record(r)
		u.delay()
		if u.opts.Unhealthy {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		u.mu.Lock()
		perUnit := u.quotaPerUnit
		u.mu.Unlock()
		writeJSON(w, map[string]any{"data": map[string]any{
			"version":        u.version(),
			"quota_per_unit": perUnit,
		}})
	})

	mux.HandleFunc(upstreamLogStat, func(w http.ResponseWriter, r *http.Request) {
		u.record(r)
		u.delay()
		if u.failStatus(w) {
			return
		}
		writeJSON(w, map[string]any{"data": map[string]any{"quota": upstreamQuota}})
	})

	return mux
}

type upstreamFixture struct {
	upstream *fakeUpstream
	server   *httptest.Server
	config   connector.Config
}

func newFixture(t *testing.T, opts metering.FakeOptions, sub2api bool) *upstreamFixture {
	t.Helper()
	u := &fakeUpstream{t: t, opts: opts, quotaPerUnit: 500_000}
	if opts.QuotaPerUnit != 0 {
		u.quotaPerUnit = opts.QuotaPerUnit
	}

	handler := u.newapiHandler()
	if sub2api {
		handler = u.sub2apiHandler()
	}
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)

	ref := sessionRefText
	if sub2api {
		ref = adminRefText
	}
	return &upstreamFixture{
		upstream: u,
		server:   srv,
		config: connector.Config{
			ServiceInstanceID: "metering-test",
			Environment:       "production",
			Endpoint:          srv.URL,
			CredentialRef:     ref,
			TargetAllowlist:   []string{allowedHost(t, srv.URL)},
			Timeout:           5 * time.Second,
		},
	}
}

func (f *upstreamFixture) options(opts metering.FakeOptions) []metering.Option {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return contracttest.Today }
	}
	return []metering.Option{
		// 护栏一个都不能少：base 依旧被 ReadOnlyTransport 包在里面，
		// 只读方法限制、allowlist、拒绝重定向在测试路径与生产路径上是同一份代码
		metering.WithBaseTransport(f.server.Client().Transport),
		metering.WithClock(now),
	}
}

// tokenRef 是套件调用 TokenUsage 时用的令牌引用。
//
// 套件本身只传 UpstreamTokenID（它不认识凭据），所以真实驱动的工厂要在
// 这里补上 CredentialRef——sub2api 的成本侧没有它发不出请求。
type tokenPatchingClient struct {
	metering.ReadClient
}

func (c tokenPatchingClient) TokenUsage(
	ctx context.Context, token metering.TokenRef, day string,
) (metering.TokenUsage, error) {
	if token.CredentialRef == "" {
		token.CredentialRef = tokenRefText
	}
	return c.ReadClient.TokenUsage(ctx, token, day)
}

// TestSub2APIClientSatisfiesContract 让真实 sub2api 驱动跑同一套契约。
func TestSub2APIClientSatisfiesContract(t *testing.T) {
	contracttest.RunSuite(t, func(opts metering.FakeOptions) metering.ReadClient {
		f := newFixture(t, opts, true)
		c, err := metering.NewSub2APIClient(f.config, fakeProvider(t), f.options(opts)...)
		if err != nil {
			t.Fatalf("构造 sub2api 客户端失败: %v", err)
		}
		return tokenPatchingClient{ReadClient: c}
	})
}

// TestNewAPIClientSatisfiesContract 让真实 newapi 驱动跑同一套契约。
func TestNewAPIClientSatisfiesContract(t *testing.T) {
	contracttest.RunSuite(t, func(opts metering.FakeOptions) metering.ReadClient {
		f := newFixture(t, opts, false)
		c, err := metering.NewNewAPIClient(f.config, fakeProvider(t), f.options(opts)...)
		if err != nil {
			t.Fatalf("构造 newapi 客户端失败: %v", err)
		}
		return c
	})
}

// ---------------------------------------------------------------------------
// 口径断言：错了不会报错，只会让数字静静地偏
// ---------------------------------------------------------------------------

// TestSub2APIReadsActualCostNotPanelCost 钉住 §3.1 的显著警告。
//
// 成本侧必须打 `/v1/usage` 取 `usage.today.actual_cost`，而不是 admin 面板的
// `trend[].cost`（那是 connectors/sub2api 在读的自营实例口径）。
// 顺带验证金额没经过 float64：5.813729 在 float64 里不精确。
func TestSub2APIReadsActualCostNotPanelCost(t *testing.T) {
	opts := metering.FakeOptions{Now: func() time.Time { return contracttest.Today }}
	f := newFixture(t, opts, true)
	c, err := metering.NewSub2APIClient(f.config, fakeProvider(t), f.options(opts)...)
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}

	usage, err := c.TokenUsage(context.Background(),
		metering.TokenRef{UpstreamTokenID: "tok-1", CredentialRef: tokenRefText},
		contracttest.TodayText)
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	if usage.UsageMinorUnits != 5_813_729 {
		t.Fatalf("实扣 = %d 微单位, want 5813729（%s 经整数路径的精确值）",
			usage.UsageMinorUnits, upstreamActualCost)
	}

	req, ok := f.upstream.lastTo(upstreamUsage)
	if !ok {
		t.Fatalf("没有打到 %s——成本侧走错了端点（§3.1）", upstreamUsage)
	}
	if req.method != http.MethodGet {
		t.Fatalf("成本取数必须是 GET，got %s", req.method)
	}
	// 每令牌明文自鉴权，不是 admin key
	if req.authorization != "Bearer "+fakeTokenSecret {
		t.Fatalf("成本侧应带该令牌自己的 Bearer 凭据，got %q", req.authorization)
	}
	if req.adminKey != "" {
		t.Fatalf("成本侧不该带 admin key（§3.1：apikey 自鉴权），got %q", req.adminKey)
	}
	// 确认没有顺手去打面板端点
	for _, r := range f.upstream.seen() {
		if strings.Contains(r.path, "trend") || strings.Contains(r.path, "dashboard") {
			t.Fatalf("成本侧打了面板端点 %s——那是自营实例口径，不是核算成本（§3.1）", r.path)
		}
	}
}

// TestSub2APITodayNullIsKnownZero 锁住 §5.1 的「已知 0 与未知必须区分」。
//
// 上游今日零流量时给 today:null。那是**已知的 0**（可以写进台账），
// 不是未知（必须落 NULL）。把它当成错误会让台账缺一行，
// 把未知当成 0 会让利润凭空等于收入。
func TestSub2APITodayNullIsKnownZero(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == upstreamUsage {
			_, _ = fmt.Fprint(w, `{"usage":{"today":null}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	cfg := connector.Config{
		ServiceInstanceID: "metering-test", Environment: "production",
		Endpoint: srv.URL, CredentialRef: adminRefText,
		TargetAllowlist: []string{allowedHost(t, srv.URL)},
		Timeout:         5 * time.Second,
	}
	c, err := metering.NewSub2APIClient(cfg, fakeProvider(t),
		metering.WithBaseTransport(srv.Client().Transport),
		metering.WithClock(func() time.Time { return contracttest.Today }))
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}

	usage, err := c.TokenUsage(context.Background(),
		metering.TokenRef{UpstreamTokenID: "tok-1", CredentialRef: tokenRefText},
		contracttest.TodayText)
	if err != nil {
		t.Fatalf("today:null 是已知 0，不该报错: %v", err)
	}
	if usage.UsageMinorUnits != 0 {
		t.Fatalf("today:null 应是 0，got %d", usage.UsageMinorUnits)
	}
	// 已知 0 照样要带新鲜度：它是一条真实的观测
	if usage.ObservedAt.IsZero() || usage.Watermark == "" {
		t.Fatal("已知 0 也必须带 ObservedAt 与 Watermark")
	}
}

// TestNewAPIUsesConsumeLogTypeAndSessionAuth 钉住 §3.1 / §4 的两条 ★ 口径。
//
// type=2 表示「仅 consume」：抄成别的值不会报错，只会把充值混进成本。
// 起止时间戳必须按业务日时区切分（§4：收入与成本共用同一个时间权威）。
func TestNewAPIUsesConsumeLogTypeAndSessionAuth(t *testing.T) {
	opts := metering.FakeOptions{Now: func() time.Time { return contracttest.Today }}
	f := newFixture(t, opts, false)
	c, err := metering.NewNewAPIClient(f.config, fakeProvider(t), f.options(opts)...)
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}

	usage, err := c.TokenUsage(context.Background(),
		metering.TokenRef{UpstreamTokenID: "my-token"}, contracttest.TodayText)
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}

	req, ok := f.upstream.lastTo(upstreamLogStat)
	if !ok {
		t.Fatalf("没有打到 %s", upstreamLogStat)
	}
	if got := req.query.Get("type"); got != "2" {
		t.Fatalf("type = %q, want \"2\"（仅 consume，不含充值——★口径，§4）", got)
	}
	if got := req.query.Get("token_name"); got != "my-token" {
		t.Fatalf("token_name = %q, want my-token", got)
	}

	// 起点必须是业务日（CST）当日零点，不是 UTC 零点
	startUnix, err := strconv.ParseInt(req.query.Get("start_timestamp"), 10, 64)
	if err != nil {
		t.Fatalf("start_timestamp 不是整数: %v", err)
	}
	wantStart := time.Date(2026, 8, 28, 0, 0, 0, 0, metering.DefaultBusinessDayLocation())
	if startUnix != wantStart.Unix() {
		t.Fatalf("start_timestamp = %d（%s），want %d（%s，CST 当日零点）",
			startUnix, time.Unix(startUnix, 0).UTC(), wantStart.Unix(), wantStart)
	}
	// 终点是此刻，与 SoloAI 的 end_timestamp=<now> 一致——写成当日 23:59:59
	// 会把今天剩下的、还没发生的消费一起算进来
	endUnix, err := strconv.ParseInt(req.query.Get("end_timestamp"), 10, 64)
	if err != nil {
		t.Fatalf("end_timestamp 不是整数: %v", err)
	}
	if endUnix != contracttest.Today.Unix() {
		t.Fatalf("end_timestamp = %d, want %d（此刻）", endUnix, contracttest.Today.Unix())
	}

	// 会话鉴权：New-Api-User + Cookie，两段都来自同一个秘密的切分
	if req.newAPIUser != "42" {
		t.Fatalf("%s = %q, want \"42\"", upstreamNewAPIUser, req.newAPIUser)
	}
	if req.session != "placeholder-session-placeholder" {
		t.Fatalf("session cookie = %q，与凭据第二段不符", req.session)
	}
	if req.authorization != "" || req.adminKey != "" {
		t.Fatal("newapi 侧不该出现 Bearer 或 admin key")
	}

	// quota → 微单位：1234567 / 500000 在 scale-6 下恒为 quota×2，精确无舍入
	if usage.UsageMinorUnits != upstreamQuota*2 {
		t.Fatalf("金额 = %d, want %d（quota×2，§2.4 精确无舍入）",
			usage.UsageMinorUnits, upstreamQuota*2)
	}
	// 整数 quota 与刻度必须带出去，调用方才做得了「先 SUM 再除」
	if usage.RawUnits == nil || *usage.RawUnits != upstreamQuota {
		t.Fatalf("RawUnits = %v, want %d", usage.RawUnits, upstreamQuota)
	}
	if usage.UnitsPerWhole == nil || *usage.UnitsPerWhole != 500_000 {
		t.Fatalf("UnitsPerWhole = %v, want 500000", usage.UnitsPerWhole)
	}
}

// TestNewAPIHonorsRuntimeQuotaPerUnit 验证刻度**从上游读**而不是写死。
//
// quota_per_unit 在 New-API 里是运行期可变的站点配置。写死 500000 的后果是
// 上游一改刻度，平台的成本就整体偏一个倍数，而且不会报错（§4 标注：
// 抄错不报错，金额差 50 万倍）。
func TestNewAPIHonorsRuntimeQuotaPerUnit(t *testing.T) {
	opts := metering.FakeOptions{
		Now:          func() time.Time { return contracttest.Today },
		QuotaPerUnit: 1_000_000,
	}
	f := newFixture(t, opts, false)
	c, err := metering.NewNewAPIClient(f.config, fakeProvider(t), f.options(opts)...)
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}

	usage, err := c.TokenUsage(context.Background(),
		metering.TokenRef{UpstreamTokenID: "my-token"}, contracttest.TodayText)
	if err != nil {
		t.Fatalf("取数失败: %v", err)
	}
	// 刻度翻倍 → 同样的 quota 只值一半的钱
	if usage.UsageMinorUnits != upstreamQuota {
		t.Fatalf("刻度 1000000 时金额 = %d, want %d（quota×1）",
			usage.UsageMinorUnits, upstreamQuota)
	}
	if usage.UnitsPerWhole == nil || *usage.UnitsPerWhole != 1_000_000 {
		t.Fatalf("UnitsPerWhole 应如实报告上游的刻度, got %v", usage.UnitsPerWhole)
	}
}

// TestNewAPIRefusesNonPositiveQuotaPerUnit：上游报了个非正刻度说明它状态异常。
// 按默认 500000 折算会得到一个**看起来正常的错数字**（宪法 12 条）。
func TestNewAPIRefusesNonPositiveQuotaPerUnit(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == upstreamStatus {
			writeJSON(w, map[string]any{"data": map[string]any{
				"version": "0.8.1", "quota_per_unit": 0,
			}})
			return
		}
		writeJSON(w, map[string]any{"data": map[string]any{"quota": upstreamQuota}})
	}))
	t.Cleanup(srv.Close)

	cfg := connector.Config{
		ServiceInstanceID: "metering-test", Environment: "production",
		Endpoint: srv.URL, CredentialRef: sessionRefText,
		TargetAllowlist: []string{allowedHost(t, srv.URL)},
		Timeout:         5 * time.Second,
	}
	c, err := metering.NewNewAPIClient(cfg, fakeProvider(t),
		metering.WithBaseTransport(srv.Client().Transport),
		metering.WithClock(func() time.Time { return contracttest.Today }))
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	if _, err := c.TokenUsage(context.Background(),
		metering.TokenRef{UpstreamTokenID: "my-token"}, contracttest.TodayText); err == nil {
		t.Fatal("非正 quota_per_unit 必须拒绝取数，而不是回落到 500000")
	}
}

// TestNewAPIRevenueIsNotSupported：newapi 的收入在自营库里（§3.2），
// 走只读数据库通道。返回一个理直气壮的 0 会让 037b 把它当成
// 「今天没有收入」写进台账，于是毛利凭空等于成本的负数（§5.1 明令禁止）。
func TestNewAPIRevenueIsNotSupported(t *testing.T) {
	opts := metering.FakeOptions{Now: func() time.Time { return contracttest.Today }}
	f := newFixture(t, opts, false)
	c, err := metering.NewNewAPIClient(f.config, fakeProvider(t), f.options(opts)...)
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	_, err = c.AccountRevenue(context.Background(), "42", contracttest.TodayText)
	if got := connector.KindOf(err); got != connector.KindNotSupported {
		t.Fatalf("newapi 收入应归 %s，got %s（%v）", connector.KindNotSupported, got, err)
	}
}

// TestSub2APIRevenueUsesAdminKey 验证收入侧走 admin key 而不是令牌明文。
func TestSub2APIRevenueUsesAdminKey(t *testing.T) {
	opts := metering.FakeOptions{Now: func() time.Time { return contracttest.Today }}
	f := newFixture(t, opts, true)
	c, err := metering.NewSub2APIClient(f.config, fakeProvider(t), f.options(opts)...)
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}

	revenue, err := c.AccountRevenue(context.Background(), "258", contracttest.TodayText)
	if err != nil {
		t.Fatalf("取收入失败: %v", err)
	}
	if revenue.RevenueMinorUnits != 12_345_600 {
		t.Fatalf("收入 = %d 微单位, want 12345600（\"12.3456\"）", revenue.RevenueMinorUnits)
	}

	var found bool
	for _, r := range f.upstream.seen() {
		if !strings.HasPrefix(r.path, upstreamAccountStats) {
			continue
		}
		found = true
		if r.adminKey != fakeAdminSecret {
			t.Fatalf("收入侧应带 admin key，got %q", r.adminKey)
		}
		if r.authorization != "" {
			t.Fatalf("收入侧不该带令牌 Bearer，got %q", r.authorization)
		}
	}
	if !found {
		t.Fatalf("没有打到账号统计端点")
	}
}

// TestPerTokenCredentialsDoNotCrossOver 锁住成本侧「每令牌一把凭据」。
//
// 串号的后果是：A 令牌的成本被记成 B 令牌的（上游按凭据识别是谁在问），
// 两条渠道的毛利一个虚高一个虚低而合计完全正确——最难从总数上看出来的错。
func TestPerTokenCredentialsDoNotCrossOver(t *testing.T) {
	const secondRefText = "secret://metering-test/sub2api-token-two"
	const secondEnvVar = "XM_TEST_METERING_TOKEN_TWO"
	const secondSecret = "placeholder-token-two-placeholder"

	values := map[string]string{
		adminEnvVar:  fakeAdminSecret,
		tokenEnvVar:  fakeTokenSecret,
		secondEnvVar: secondSecret,
	}
	provider, err := secrets.NewEnvProvider(
		map[string]string{
			adminRefText:  adminEnvVar,
			tokenRefText:  tokenEnvVar,
			secondRefText: secondEnvVar,
		},
		secrets.WithLookup(func(name string) (string, bool) {
			v, ok := values[name]
			return v, ok
		}),
	)
	if err != nil {
		t.Fatalf("构造 Provider 失败: %v", err)
	}

	opts := metering.FakeOptions{Now: func() time.Time { return contracttest.Today }}
	f := newFixture(t, opts, true)
	c, err := metering.NewSub2APIClient(f.config, provider, f.options(opts)...)
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}

	ctx := context.Background()
	if _, err := c.TokenUsage(ctx,
		metering.TokenRef{UpstreamTokenID: "tok-1", CredentialRef: tokenRefText},
		contracttest.TodayText); err != nil {
		t.Fatalf("第一次取数失败: %v", err)
	}
	if _, err := c.TokenUsage(ctx,
		metering.TokenRef{UpstreamTokenID: "tok-2", CredentialRef: secondRefText},
		contracttest.TodayText); err != nil {
		t.Fatalf("第二次取数失败: %v", err)
	}

	var seen []string
	for _, r := range f.upstream.seen() {
		if r.path == upstreamUsage {
			seen = append(seen, r.authorization)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("应有 2 次成本取数, got %d", len(seen))
	}
	if seen[0] == seen[1] {
		t.Fatalf("两个令牌用了同一把凭据 %q——凭据被缓存串号了", seen[0])
	}
	if seen[1] != "Bearer "+secondSecret {
		t.Fatalf("第二个令牌应用自己的凭据, got %q", seen[1])
	}
}

// ---------------------------------------------------------------------------
// 三条不能出事的纪律：凭据不泄漏、只读、目标受限
// ---------------------------------------------------------------------------

// TestCredentialsNeverLeakIntoErrors：错误会进日志、进看板、进 AI 上下文
// （宪法 7 条：明文不进日志、不进 AI 上下文）。
func TestCredentialsNeverLeakIntoErrors(t *testing.T) {
	for _, kind := range []connector.ErrorKind{
		connector.KindAuth, connector.KindRateLimited,
		connector.KindUnavailable, connector.KindBadResponse,
	} {
		opts := metering.FakeOptions{
			Now: func() time.Time { return contracttest.Today }, FailWith: kind,
		}
		f := newFixture(t, opts, true)
		c, err := metering.NewSub2APIClient(f.config, fakeProvider(t), f.options(opts)...)
		if err != nil {
			t.Fatalf("构造失败: %v", err)
		}
		_, err = c.TokenUsage(context.Background(),
			metering.TokenRef{UpstreamTokenID: "tok-1", CredentialRef: tokenRefText},
			contracttest.TodayText)
		if err == nil {
			t.Fatalf("FailWith=%s 应报错", kind)
		}
		text := err.Error()
		for _, secret := range []string{fakeAdminSecret, fakeTokenSecret, fakeSessionSecret} {
			if strings.Contains(text, secret) {
				t.Fatalf("错误里出现了凭据明文: %s", text)
			}
		}
	}
}

// TestNewAPIMalformedCredentialDoesNotLeak：凭据形状不对时的报错
// 同样不能带出任何片段。
func TestNewAPIMalformedCredentialDoesNotLeak(t *testing.T) {
	const badRefText = "secret://metering-test/newapi-broken"
	const badEnvVar = "XM_TEST_METERING_BROKEN"
	const badSecret = "placeholder-no-separator-placeholder"

	provider, err := secrets.NewEnvProvider(
		map[string]string{badRefText: badEnvVar},
		secrets.WithLookup(func(name string) (string, bool) {
			if name == badEnvVar {
				return badSecret, true
			}
			return "", false
		}),
	)
	if err != nil {
		t.Fatalf("构造 Provider 失败: %v", err)
	}

	opts := metering.FakeOptions{Now: func() time.Time { return contracttest.Today }}
	f := newFixture(t, opts, false)
	f.config.CredentialRef = badRefText
	c, err := metering.NewNewAPIClient(f.config, provider, f.options(opts)...)
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	_, err = c.TokenUsage(context.Background(),
		metering.TokenRef{UpstreamTokenID: "my-token"}, contracttest.TodayText)
	if err == nil {
		t.Fatal("凭据缺少分隔符应报错")
	}
	if connector.KindOf(err) != connector.KindAuth {
		t.Fatalf("凭据形状不对应归 auth，got %s", connector.KindOf(err))
	}
	if strings.Contains(err.Error(), badSecret) {
		t.Fatalf("错误里出现了凭据明文: %v", err)
	}
}

// TestOnlyGetIsEverSent 是 ADR-018 闸 2/4 的实测：本包只发 GET。
func TestOnlyGetIsEverSent(t *testing.T) {
	opts := metering.FakeOptions{Now: func() time.Time { return contracttest.Today }}
	f := newFixture(t, opts, true)
	c, err := metering.NewSub2APIClient(f.config, fakeProvider(t), f.options(opts)...)
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	ctx := context.Background()
	_, _ = c.Version(ctx)
	_, _ = c.Health(ctx)
	_, _ = c.Capabilities(ctx)
	_, _ = c.TokenUsage(ctx,
		metering.TokenRef{UpstreamTokenID: "tok-1", CredentialRef: tokenRefText},
		contracttest.TodayText)
	_, _ = c.AccountRevenue(ctx, "258", contracttest.TodayText)

	seen := f.upstream.seen()
	if len(seen) == 0 {
		t.Fatal("一个请求都没发出去，本用例什么都没验到")
	}
	for _, r := range seen {
		if r.method != http.MethodGet {
			t.Fatalf("只读通道发出了 %s %s（ADR-018 闸 2）", r.method, r.path)
		}
	}
}

// TestTargetOutsideAllowlistIsRejected 是 ADR-018 闸 3 在 HTTP 侧的落点。
func TestTargetOutsideAllowlistIsRejected(t *testing.T) {
	opts := metering.FakeOptions{Now: func() time.Time { return contracttest.Today }}
	f := newFixture(t, opts, true)
	// 配一个与 endpoint 主机不同的 allowlist：Validate 会当场拒绝，
	// 因为 endpoint 主机必须在自己的 allowlist 内
	f.config.TargetAllowlist = []string{"somewhere-else.example.test"}
	if _, err := metering.NewSub2APIClient(f.config, fakeProvider(t), f.options(opts)...); err == nil {
		t.Fatal("endpoint 主机不在 allowlist 内时必须拒绝构造（ADR-018 闸 1）")
	}
}

// TestRedirectIsRefused：一个 302 就能把请求引到 allowlist 之外。
func TestRedirectIsRefused(t *testing.T) {
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"usage":{"today":{"actual_cost":999999}}}`)
	}))
	t.Cleanup(elsewhere.Close)

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+upstreamUsage, http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	cfg := connector.Config{
		ServiceInstanceID: "metering-test", Environment: "production",
		Endpoint: srv.URL, CredentialRef: adminRefText,
		TargetAllowlist: []string{allowedHost(t, srv.URL)},
		Timeout:         5 * time.Second,
	}
	c, err := metering.NewSub2APIClient(cfg, fakeProvider(t),
		metering.WithBaseTransport(srv.Client().Transport),
		metering.WithClock(func() time.Time { return contracttest.Today }))
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	if _, err := c.TokenUsage(context.Background(),
		metering.TokenRef{UpstreamTokenID: "tok-1", CredentialRef: tokenRefText},
		contracttest.TodayText); err == nil {
		t.Fatal("重定向必须被拒绝——一个 302 就能把请求引到 allowlist 之外")
	}
}

// TestSub2APIRefusesTokenWithoutCredential：sub2api 的成本侧没有每令牌凭据
// 就发不出请求；这个错要在**发请求之前**报出来，而不是打一个必然 401 的请求。
func TestSub2APIRefusesTokenWithoutCredential(t *testing.T) {
	opts := metering.FakeOptions{Now: func() time.Time { return contracttest.Today }}
	f := newFixture(t, opts, true)
	c, err := metering.NewSub2APIClient(f.config, fakeProvider(t), f.options(opts)...)
	if err != nil {
		t.Fatalf("构造失败: %v", err)
	}
	if _, err := c.TokenUsage(context.Background(),
		metering.TokenRef{UpstreamTokenID: "tok-1"}, contracttest.TodayText); err == nil {
		t.Fatal("缺 credential_ref 必须报错")
	}
	for _, r := range f.upstream.seen() {
		if r.path == upstreamUsage {
			t.Fatal("缺凭据时不该发出请求")
		}
	}
}
