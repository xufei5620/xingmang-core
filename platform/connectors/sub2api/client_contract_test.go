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
	"sort"
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
	upstreamHealth        = "/health"
	upstreamVersion       = "/api/v1/admin/system/version"
	upstreamStats         = "/api/v1/admin/dashboard/stats"
	upstreamUsers         = "/api/v1/admin/users"
	upstreamPayment       = "/api/v1/admin/payment/dashboard"
	upstreamPaymentOrders = "/api/v1/admin/payment/orders"
	upstreamTrend         = "/api/v1/admin/dashboard/trend"
	upstreamAccounts      = "/api/v1/admin/accounts"
	// upstreamAccountTodayStatsPattern 是单账号今日统计的 mux 注册形态
	// （Go 1.22+ ServeMux 路径参数）；upstreamAccountTodayStatsPrefix/Suffix
	// 供测试自己拼具体账号的请求路径断言用。
	upstreamAccountTodayStatsPattern = "/api/v1/admin/accounts/{id}/today-stats"
	upstreamAccountTodayStatsPrefix  = "/api/v1/admin/accounts/"
	upstreamAccountTodayStatsSuffix  = "/today-stats"

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
//
// XM-CHAN-FIELDS0：三个账号刻意覆盖三种组合——
//
//	id 1（订阅型 oauth）：load_factor 未配置（回落到 concurrency）、
//	  有 session_window/window_cost_limit（可以算出 usage_window）、
//	  有 proxy、extra 里有上游计费探测结果（upstream_multiplier 有值）。
//	id 2（密钥型 apikey）：load_factor 配置了且 >0（覆盖 concurrency）、
//	  schedulable=false（调度关闭）、没有 proxy/session_window/extra
//	  （usage_window 因 kind!=subscription 恒为 nil，与是否配置无关）。
//	id 3（未登记的 type 取值）：kind 必须解成 nil，不是猜一个桶；
//	  没有 last_used_at（*time.Time 的 null 分支）。
//
// 既有断言（渠道余额小节）只读 quota_*/platform/status/name，新增字段不影响它们。
const fakeAccountItems = `[
  {"id":1,"name":"upstream-a","platform":"anthropic","status":"active",
   "quota_limit":500.00,"quota_used":44.00,
   "type":"oauth","concurrency":5,"load_factor":null,"priority":10,
   "rate_multiplier":1.5,"schedulable":true,
   "last_used_at":"2026-08-27T11:55:00Z","created_at":"2026-01-01T00:00:00Z",
   "expires_at":1798761600,"session_window_end":"2026-08-27T15:00:00Z",
   "window_cost_limit":50.00,"current_concurrency":2,"current_window_cost":21.00,
   "proxy":{"name":"hk-01","host":"should-not-be-read.example","username":"should-not-be-read"},
   "extra":{"upstream_billing_probe":{"status":"ok","data":{"resolved_rate_multiplier":1.1}}}},
  {"id":2,"name":"upstream-b","platform":"openai","status":"error",
   "error_message":"upstream said no","quota_limit":20,"quota_used":8,
   "type":"apikey","concurrency":10,"load_factor":3,"priority":20,
   "rate_multiplier":1.0,"schedulable":false,
   "last_used_at":null,"created_at":"2026-02-01T00:00:00Z",
   "expires_at":null,"session_window_end":null,
   "window_cost_limit":null,"current_concurrency":1,"current_window_cost":null,
   "proxy":null,"extra":{}},
  {"id":3,"name":"upstream-c","platform":"gemini","status":"active",
   "quota_limit":null,"quota_used":null,"quota_daily_limit":10,"quota_daily_used":10,
   "type":"future-unknown-type","concurrency":2,"priority":30,
   "rate_multiplier":0.8,"schedulable":true,
   "last_used_at":null,"created_at":"2026-03-01T00:00:00Z",
   "current_concurrency":0}
]`

const fakeAccountCount = 3

// fakePaymentAmount 是"今天"那条 daily_series 的 amount 字段。
//
// **形状是 0.1.183 的币种 map，不是标量。** 上游把 DailyStats.Amount 从
// float64 改成了 CurrencyAmounts=map[币种码]float64，币种码经
// NormalizePaymentCurrency 规范化成 3 位大写 ISO 4217。
// 假上游必须跟着真实形状走，否则这套测试守的是一个已经不存在的上游。
const fakePaymentAmount = `{"USD":12345.60}`

// fakePaymentEmptyAmount 是"那天没有已支付订单"的形状。
//
// 上游的 buildDailySeries 对没有订单的日子给的是 `make(CurrencyAmounts)`，
// 序列化出来就是空对象——**不是 0，也不是 null**。
// 这个形状正是旧代码能一路全绿的原因：空对象被旧的 rawAmount 收成空串、
// 按 0 处理，于是空实例联调怎么测都不会红。
const fakePaymentEmptyAmount = `{}`

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
	accountTotal int64
	// paymentAmount 允许单个测试替换"今天"那条 daily_series 的金额形状
	// （多币种、缺合约币种、旧版本的标量……）。
	paymentAmount string
	// paymentOrders 允许单个测试替换 /admin/payment/orders 的逐笔订单固定数据
	// （XM-PAY0）；nil 时用 fakePaymentOrders。
	paymentOrders []fakePaymentOrder
	// todayStats 是账号 ID（字符串）到 today-stats 响应 data 的映射
	// （XM-CHAN-FIELDS0）；未登记的 ID 返回一个空统计（requests=0,cost=0），
	// 与"今天没有请求"是真实的同一种形状，不是缺数据。
	todayStats map[string]string
	// todayStatsUnsupported 让 today-stats 路由整体不注册（= 404 = 客户端
	// 归类为 not_supported），模拟一个还没升级到这个端点的旧版本上游。
	todayStatsUnsupported bool

	server *httptest.Server

	mu       sync.Mutex
	requests []fakeRequest
}

func startFakeUpstream(t *testing.T, opts sub2api.FakeOptions, tweak ...func(*fakeUpstream)) *fakeUpstream {
	t.Helper()
	u := &fakeUpstream{
		t: t, opts: opts,
		accountItems:  fakeAccountItems,
		accountTotal:  fakeAccountCount,
		paymentAmount: fakePaymentAmount,
	}
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
	register(upstreamPaymentOrders, u.dataRoute(u.handlePaymentOrders))
	register(upstreamTrend, u.dataRoute(u.handleTrend))
	register(upstreamAccounts, u.dataRoute(u.handleAccounts))
	if !u.todayStatsUnsupported {
		mux.HandleFunc("GET "+upstreamAccountTodayStatsPattern, u.dataRoute(u.handleAccountTodayStats))
	}

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
		// 上游 VERSION 文件里的形态：裸的 x.y.z，没有 v 前缀。
		// 0.1.183 = XM-R013 逐字段核对过的那一版（源码 @ efb46db）。
		version = "0.1.183"
	}
	if u.opts.UnsupportedVersion {
		version = "9.9.9"
	}
	// 只发 version 一个字段——上游这条 handler 从 0.1.133 到 0.1.183 一直是
	// `response.Success(c, gin.H{"version": info.CurrentVersion})`。
	// 假上游以前还发 commit/build_type，那是**凭空发明的字段**，
	// 让"客户端解析了两个不存在的字段"这件事在测试里看不出来（XM-R013 清掉）。
	writeRaw(w, http.StatusOK, envelope(fmt.Sprintf(`{"version":%q}`, version)))
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
		amount, count := fakePaymentEmptyAmount, 0
		if i == 0 {
			amount, count = u.paymentAmount, 168
		}
		series = append(series, fmt.Sprintf(`{"date":%q,"amount":%s,"count":%d}`, date, amount, count))
	}
	// 顶层的 today_amount/total_amount/avg_amount 在 0.1.183 里同样是币种 map。
	// 客户端一个都不读（收入只走 daily_series），但假上游照抄真实形状——
	// 哪天有人想读它们，看到的必须是上游真会发的东西。
	writeRaw(w, http.StatusOK, envelope(fmt.Sprintf(
		`{"today_amount":{"USD":12345.60},"total_amount":{"USD":99999.99},"today_count":168,
		  "total_count":2100,"avg_amount":{"USD":47.6},"pending_orders":2,
		  "daily_series":[%s]}`, strings.Join(series, ","))))
}

// fakePaymentOrder 是 /admin/payment/orders 固定数据的一行（XM-PAY0）。
type fakePaymentOrder struct {
	ID           int64
	UserID       int64
	UserEmail    string
	Amount       string // 十进制文本，与真实上游一致地不经过 float
	PayAmount    string
	Currency     string
	Status       string
	PaymentType  string
	OutTradeNo   string
	RefundAmount string
	CreatedAt    time.Time
}

// fakePaymentOrders 是逐笔订单的默认固定数据，覆盖：多个状态（含退款生命周期
// 里的 REFUNDED）、一笔非合约币种（9005，用于验证 currency gap 处理）、
// 一笔明显更早的订单（9006，用于验证翻页早停在 from 边界生效）。
//
// 时刻都早于 fakeClockNow（2026-08-27T12:00:00Z），与其余测试的固定时钟一致。
var fakePaymentOrders = []fakePaymentOrder{
	{ID: 9001, UserID: 1, UserEmail: "a@example.test", Amount: "100.00", PayAmount: "101.00",
		Currency: "USD", Status: "PAID", PaymentType: "alipay", OutTradeNo: "OUT-9001",
		RefundAmount: "0.00", CreatedAt: time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)},
	{ID: 9002, UserID: 2, UserEmail: "b@example.test", Amount: "50.00", PayAmount: "50.50",
		Currency: "USD", Status: "PENDING", PaymentType: "wxpay", OutTradeNo: "OUT-9002",
		RefundAmount: "0.00", CreatedAt: time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)},
	{ID: 9003, UserID: 3, UserEmail: "c@example.test", Amount: "200.00", PayAmount: "202.00",
		Currency: "USD", Status: "REFUNDED", PaymentType: "stripe", OutTradeNo: "OUT-9003",
		RefundAmount: "202.00", CreatedAt: time.Date(2026, 8, 26, 23, 0, 0, 0, time.UTC)},
	{ID: 9004, UserID: 4, UserEmail: "d@example.test", Amount: "30.00", PayAmount: "30.30",
		Currency: "USD", Status: "FAILED", PaymentType: "card", OutTradeNo: "OUT-9004",
		RefundAmount: "0.00", CreatedAt: time.Date(2026, 8, 26, 8, 0, 0, 0, time.UTC)},
	{ID: 9005, UserID: 5, UserEmail: "e@example.test", Amount: "75.00", PayAmount: "75.75",
		Currency: "CNY", Status: "PAID", PaymentType: "alipay", OutTradeNo: "OUT-9005",
		RefundAmount: "0.00", CreatedAt: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)},
	{ID: 9006, UserID: 6, UserEmail: "f@example.test", Amount: "10.00", PayAmount: "10.10",
		Currency: "USD", Status: "COMPLETED", PaymentType: "easypay", OutTradeNo: "OUT-9006",
		RefundAmount: "0.00", CreatedAt: time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)},
}

// handlePaymentOrders 模拟 GET /api/v1/admin/payment/orders：按 created_at
// DESC 排序、可选 status 服务端过滤、page/page_size 分页——与上游
// response.ParsePagination + PaginatedData 同一个外壳（见 upstream.go
// routePaymentOrders 的注释）。
func (u *fakeUpstream) handlePaymentOrders(w http.ResponseWriter, r *http.Request) {
	page := queryInt(r, "page", 1)
	pageSize := queryInt(r, "page_size", 20)
	status := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("status")))

	source := u.paymentOrders
	if source == nil {
		source = fakePaymentOrders
	}
	filtered := make([]fakePaymentOrder, 0, len(source))
	for _, o := range source {
		if status != "" && o.Status != status {
			continue
		}
		filtered = append(filtered, o)
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].CreatedAt.After(filtered[j].CreatedAt) })

	start := (page - 1) * pageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	pageItems := filtered[start:end]

	rows := make([]string, 0, len(pageItems))
	for _, o := range pageItems {
		rows = append(rows, fmt.Sprintf(
			`{"id":%d,"user_id":%d,"user_email":%q,"amount":%s,"pay_amount":%s,"currency":%q,`+
				`"status":%q,"payment_type":%q,"out_trade_no":%q,"refund_amount":%s,"created_at":%q}`,
			o.ID, o.UserID, o.UserEmail, o.Amount, o.PayAmount, o.Currency,
			o.Status, o.PaymentType, o.OutTradeNo, o.RefundAmount, o.CreatedAt.Format(time.RFC3339)))
	}
	writeRaw(w, http.StatusOK, envelope(fmt.Sprintf(
		`{"items":[%s],"total":%d,"page":%d,"page_size":%d,"pages":1}`,
		strings.Join(rows, ","), len(filtered), page, pageSize)))
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
	total := u.accountTotal
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

// handleAccountTodayStats 模拟 GET /api/v1/admin/accounts/:id/today-stats
// （XM-CHAN-FIELDS0）。未登记的账号 ID 返回一个真实的空统计（requests=0），
// 与"今天没有请求"是同一种形状，不是缺数据。
func (u *fakeUpstream) handleAccountTodayStats(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	data, ok := u.todayStats[id]
	if !ok {
		data = `{"requests":0,"tokens":0,"cost":0,"standard_cost":0,"user_cost":0}`
	}
	writeRaw(w, http.StatusOK, envelope(data))
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

func (u *fakeUpstream) newClient(t *testing.T, extra ...sub2api.Option) sub2api.PaymentsReadClient {
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
		if v.Detected != "0.1.183" {
			t.Fatalf("Detected = %q, want 0.1.183", v.Detected)
		}
		if !v.Supported {
			t.Fatalf("0.1.183 应在兼容矩阵内: %+v", v)
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

// ---------------------------------------------------------------------------
// XM-R013 回归：支付看板金额按币种分桶（上游 0.1.183 的破坏性变更）
//
// **为什么空实例联调测不出这个 bug。** 上游对没有已支付订单的日子给的是
// `"amount":{}`——空对象。旧代码把 amount 解进单个 rawAmount，空对象被收成
// 空串、按 0 处理，于是没有充值记录的实例上怎么跑都全绿。桶里有东西只发生在
// **真的有人付过钱**的那天，那时旧代码拿到的是 `{"CNY":123.45}` 的字面量文本，
// decimalToMinorUnits 直接报错 → 整条 DailyOrders 变 bad_response。
//
// 所以下面每个用例都刻意让"那天有已支付订单"，这正是空实例覆盖不到的那一格。
// ---------------------------------------------------------------------------

// paymentDayFixture 起一个假上游，让 2026-08-27 那天有已支付订单，
// 金额形状由 amountJSON 决定。
func paymentDayFixture(t *testing.T, amountJSON string, extra ...sub2api.Option) sub2api.OrderSummary {
	t.Helper()
	upstream := startFakeUpstream(t, sub2api.FakeOptions{}, func(u *fakeUpstream) {
		u.paymentAmount = amountJSON
	})
	orders, err := upstream.newClient(t, extra...).DailyOrders(t.Context(), "2026-08-27")
	if err != nil {
		t.Fatalf("金额形状 %s 不该报错: %v", amountJSON, err)
	}
	return orders
}

// TestRealClientReadsCurrencyBucketedRevenue 是这次修复的主回归：
// 有真实收入的一天，金额是币种 map，必须换算成分。
func TestRealClientReadsCurrencyBucketedRevenue(t *testing.T) {
	orders := paymentDayFixture(t, `{"CNY":123.45}`, sub2api.WithCurrency("CNY"))

	if orders.RevenueMinorUnits != 12345 {
		t.Fatalf("收入 = %d, want 12345（123.45 元 = 12345 分）", orders.RevenueMinorUnits)
	}
	if orders.Currency != "CNY" {
		t.Fatalf("币种 = %q, want CNY", orders.Currency)
	}
	if orders.OrderCount != 168 {
		t.Fatalf("订单数 = %d, want 168", orders.OrderCount)
	}
	if orders.IsPartial {
		t.Fatal("合约币种那一桶取到了、也没有别的币种，不该标记为部分数据")
	}
}

// TestRealClientPicksContractCurrencyBucket：多币种的日子取合约币种那一桶，
// **绝不跨币种相加**（上游源码里同一条纪律："Amounts in different currencies
// must never be added together"）。
//
// 同时断言这种日子必须标记为部分数据：另外两个币种的收入是真实存在的，
// 只是这份单币种契约装不下——丢掉了就得说出来（规格 §9.1）。
func TestRealClientPicksContractCurrencyBucket(t *testing.T) {
	orders := paymentDayFixture(t,
		`{"CNY":100.00,"USD":12345.60,"JPY":700}`, sub2api.WithCurrency("USD"))

	if orders.RevenueMinorUnits != 1234560 {
		t.Fatalf("收入 = %d, want 1234560（只取 USD 那一桶）", orders.RevenueMinorUnits)
	}
	// 12345.60+100.00+700 = 13145.60 → 1314560 分：任何等于它的结果都说明
	// 有人把不同币种加到了一起。
	if orders.RevenueMinorUnits == 1314560 {
		t.Fatal("跨币种相加了")
	}
	if !orders.IsPartial {
		t.Fatal("丢掉了 CNY/JPY 两桶真实收入，必须标记为部分数据")
	}
}

// TestRealClientMarksPartialWhenContractCurrencyMissing：合约币种那一桶不存在。
//
// 这在真实世界里很可能发生：上游的支付币种是**逐订单**的（默认 CNY），
// 与用户余额/用量成本用的美元记账是两回事，而本契约只有一个 Currency 字段。
// 配错币种时的正确行为是"报不出来 + 说出来"，不是拿另一个币种的数字顶上——
// 顶上去得到的是一个看起来完全正常的错数字。
func TestRealClientMarksPartialWhenContractCurrencyMissing(t *testing.T) {
	orders := paymentDayFixture(t, `{"CNY":123.45}`, sub2api.WithCurrency("USD"))

	if orders.RevenueMinorUnits != 0 {
		t.Fatalf("收入 = %d, want 0（USD 那一桶不存在，不能拿 CNY 顶上）",
			orders.RevenueMinorUnits)
	}
	if !orders.IsPartial {
		t.Fatal("取不到合约币种必须标记为部分数据，否则 0 会被当成「那天没收入」")
	}
}

// TestRealClientTreatsEmptyBucketsAsGenuineZero：空桶是"那天没人充值"，
// 不是"数据缺了"——把它标成部分数据会让看板天天挂着一个假的不完整告警。
func TestRealClientTreatsEmptyBucketsAsGenuineZero(t *testing.T) {
	orders := paymentDayFixture(t, `{}`, sub2api.WithCurrency("USD"))

	if orders.RevenueMinorUnits != 0 {
		t.Fatalf("收入 = %d, want 0", orders.RevenueMinorUnits)
	}
	if orders.IsPartial {
		t.Fatal("空桶 = 那天没有已支付订单，是真实的 0，不该标记为部分数据")
	}
}

// TestRealClientStillReadsLegacyScalarAmount：兼容矩阵登记的是整条 0.1 线，
// 而金额改成 map 是这条线中间某个补丁版的事。只认新形状等于把
// 「0.1.183 上炸」换成「0.1.133 上炸」——一个阻塞换另一个阻塞。
func TestRealClientStillReadsLegacyScalarAmount(t *testing.T) {
	orders := paymentDayFixture(t, `12345.60`, sub2api.WithCurrency("USD"))

	if orders.RevenueMinorUnits != 1234560 {
		t.Fatalf("收入 = %d, want 1234560（旧上游的标量形状）", orders.RevenueMinorUnits)
	}
	if orders.IsPartial {
		t.Fatal("标量形状下没有别的币种可丢，不该标记为部分数据")
	}
}

// TestRealClientClassifiesComplianceGateAsAuth 是第二处阻塞的回归。
//
// 上游 0.1.183 起给 admin 组挂了 AdminComplianceGuard：采集凭据对应的账号
// 没做过合规确认时，**所有** admin GET 返 423 Locked。
// 423 以前落进 classifyStatus 的 default → bad_response，等于告诉运维
// 「上游响应格式非法」，把一个一次性的授权动作伪装成上游改版。
func TestRealClientClassifiesComplianceGateAsAuth(t *testing.T) {
	// 上游 423 的真实正文形状（middleware/admin_compliance.go）。
	const complianceBody = `{"code":"ADMIN_COMPLIANCE_ACK_REQUIRED",
	  "message":"administrator compliance acknowledgement is required",
	  "metadata":{"version":"v1","document_path_zh":"/docs/compliance-zh.md"}}`

	upstream := startFakeUpstreamWithHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeRaw(w, http.StatusLocked, complianceBody)
	})
	_, err := upstream.newClient(t).UserStats(t.Context())
	if err == nil {
		t.Fatal("423 必须报错")
	}
	if connector.KindOf(err) != connector.KindAuth {
		t.Fatalf("423 的分类 = %q, want auth（合规门属于「这个账号还没被授权」，err=%v）",
			connector.KindOf(err), err)
	}
	// 分类换了，纪律不能松：上游正文一个字都不许进错误链（宪法 7 条）。
	for _, dump := range []string{err.Error(), fmt.Sprintf("%+v", err), unwrapDump(err)} {
		if strings.Contains(dump, "ADMIN_COMPLIANCE_ACK_REQUIRED") ||
			strings.Contains(dump, "acknowledgement") ||
			strings.Contains(dump, "document_path_zh") {
			t.Fatalf("错误链带上了上游响应正文: %s", dump)
		}
	}
	if got := err.Error(); got != "auth: sub2api.users.read" {
		t.Fatalf("对外错误文本 = %q, want \"auth: sub2api.users.read\"", got)
	}
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
		u.accountTotal = 2
	})
	client := upstream.newClient(t)
	directory, err := client.ChannelDirectory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(directory.Items) != 2 || directory.Items[1].BalanceMinorUnits != nil {
		t.Fatalf("v2 目录必须保留无余额概念账号: %+v", directory)
	}
	if !directory.Completeness.Complete || !directory.CoveragePartial {
		t.Fatalf("目录完整性与余额覆盖率必须分开: %+v", directory)
	}
	balances, err := client.ChannelBalances(t.Context())
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

// ---------------------------------------------------------------------------
// XM-CHAN-FIELDS0：渠道目录 v3 字段映射
// ---------------------------------------------------------------------------

// TestRealClientMapsChannelCatalogFields 逐字段核对三个账号的 v3 目录字段，
// 覆盖：kind 分桶（含未登记 type → nil）、capacity 的 load_factor 覆盖判据、
// scheduling、usage_window 只对订阅账号计算、proxy 只暴露 name（host/username
// 即使上游给了也不会进 ManagedChannel，因为解码结构体里根本没有那两个字段）、
// rate_multiplier 精确解析、upstream_multiplier 从 extra 里按已知子键读取、
// 以及 today 统计（逐账号预算探测，见 fetchAccountTodayStats）。
func TestRealClientMapsChannelCatalogFields(t *testing.T) {
	upstream := startFakeUpstream(t, sub2api.FakeOptions{}, func(u *fakeUpstream) {
		u.todayStats = map[string]string{
			"1": `{"requests":842,"tokens":9000,"cost":15.60,"standard_cost":14.00,"user_cost":18.00}`,
			"3": `{"requests":5,"tokens":100,"cost":0.10,"standard_cost":0.10,"user_cost":0.12}`,
			// "2" 故意不登记：走默认的真实零统计，验证「查过但是 0」而不是
			// 「没查」。
		}
	})
	directory, err := upstream.newClient(t).ChannelDirectory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(directory.Items) != 3 {
		t.Fatalf("目录条数 = %d, want 3: %+v", len(directory.Items), directory.Items)
	}
	byID := make(map[string]sub2api.ManagedChannel, 3)
	for _, item := range directory.Items {
		byID[item.ChannelID] = item
		contracttest.AssertManagedChannelCatalogInvariants(t, item)
	}

	t.Run("订阅型账号(id 1)", func(t *testing.T) {
		got := byID["1"]
		if got.Kind == nil || *got.Kind != "subscription" {
			t.Fatalf("Kind = %v, want subscription（type=oauth）", got.Kind)
		}
		if got.Vendor == nil || *got.Vendor != "anthropic" {
			t.Fatalf("Vendor = %v, want anthropic", got.Vendor)
		}
		if got.CapacityUsed == nil || *got.CapacityUsed != 2 {
			t.Fatalf("CapacityUsed = %v, want 2（current_concurrency）", got.CapacityUsed)
		}
		if got.CapacityLimit == nil || *got.CapacityLimit != 5 {
			t.Fatalf("CapacityLimit = %v, want 5（load_factor 未配置，回落到 concurrency）", got.CapacityLimit)
		}
		if got.SchedulingEnabled == nil || !*got.SchedulingEnabled {
			t.Fatalf("SchedulingEnabled = %v, want true", got.SchedulingEnabled)
		}
		if got.SchedulingPriority == nil || *got.SchedulingPriority != 10 {
			t.Fatalf("SchedulingPriority = %v, want 10", got.SchedulingPriority)
		}
		if got.TodayRequests == nil || *got.TodayRequests != 842 {
			t.Fatalf("TodayRequests = %v, want 842", got.TodayRequests)
		}
		if got.TodaySuccessRatePPM != nil {
			t.Fatalf("TodaySuccessRatePPM = %v, want nil（WindowStats 无此字段）", got.TodaySuccessRatePPM)
		}
		if got.TodayCostMinorUnits == nil || *got.TodayCostMinorUnits != 1560 {
			t.Fatalf("TodayCostMinorUnits = %v, want 1560（15.60 美元，取 cost 不取 standard_cost/user_cost）",
				got.TodayCostMinorUnits)
		}
		if got.TodayCurrency == nil || *got.TodayCurrency != "USD" || got.TodayScale == nil || *got.TodayScale != 2 {
			t.Fatalf("TodayCurrency/Scale = %v/%v, want USD/2", got.TodayCurrency, got.TodayScale)
		}
		if got.UsageWindowUsedRatioPPM == nil || *got.UsageWindowUsedRatioPPM != 420_000 {
			t.Fatalf("UsageWindowUsedRatioPPM = %v, want 420000（21.00/50.00 = 0.42）", got.UsageWindowUsedRatioPPM)
		}
		wantResets := time.Date(2026, 8, 27, 15, 0, 0, 0, time.UTC)
		if got.UsageWindowResetsAt == nil || !got.UsageWindowResetsAt.Equal(wantResets) {
			t.Fatalf("UsageWindowResetsAt = %v, want %v", got.UsageWindowResetsAt, wantResets)
		}
		if got.ProxyLabel == nil || *got.ProxyLabel != "hk-01" {
			t.Fatalf("ProxyLabel = %v, want hk-01（只取 name，即便上游给了 host/username 也不解）", got.ProxyLabel)
		}
		if got.RateMultiplierPPM == nil || *got.RateMultiplierPPM != 1_500_000 {
			t.Fatalf("RateMultiplierPPM = %v, want 1500000（1.5x）", got.RateMultiplierPPM)
		}
		if got.UpstreamMultiplierPPM == nil || *got.UpstreamMultiplierPPM != 1_100_000 {
			t.Fatalf("UpstreamMultiplierPPM = %v, want 1100000（1.1x，extra.upstream_billing_probe.data.resolved_rate_multiplier）",
				got.UpstreamMultiplierPPM)
		}
		wantLastUsed := time.Date(2026, 8, 27, 11, 55, 0, 0, time.UTC)
		if got.LastUsedAt == nil || !got.LastUsedAt.Equal(wantLastUsed) {
			t.Fatalf("LastUsedAt = %v, want %v", got.LastUsedAt, wantLastUsed)
		}
		wantCreated := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		if got.CreatedAt == nil || !got.CreatedAt.Equal(wantCreated) {
			t.Fatalf("CreatedAt = %v, want %v", got.CreatedAt, wantCreated)
		}
		wantExpires := time.Unix(1798761600, 0).UTC()
		if got.ExpiresAt == nil || !got.ExpiresAt.Equal(wantExpires) {
			t.Fatalf("ExpiresAt = %v, want %v（unix 秒换算）", got.ExpiresAt, wantExpires)
		}
	})

	t.Run("密钥型账号(id 2)：load_factor覆盖+调度关闭+usage_window恒null", func(t *testing.T) {
		got := byID["2"]
		if got.Kind == nil || *got.Kind != "upstream" {
			t.Fatalf("Kind = %v, want upstream（type=apikey）", got.Kind)
		}
		if got.CapacityLimit == nil || *got.CapacityLimit != 3 {
			t.Fatalf("CapacityLimit = %v, want 3（load_factor=3 已配置且 >0，覆盖 concurrency=10）", got.CapacityLimit)
		}
		if got.SchedulingEnabled == nil || *got.SchedulingEnabled {
			t.Fatalf("SchedulingEnabled = %v, want false", got.SchedulingEnabled)
		}
		if got.TodayRequests == nil || *got.TodayRequests != 0 {
			t.Fatalf("TodayRequests = %v, want 0（查过，真实的零，不是没查）", got.TodayRequests)
		}
		if got.TodayCostMinorUnits == nil || *got.TodayCostMinorUnits != 0 {
			t.Fatalf("TodayCostMinorUnits = %v, want 0", got.TodayCostMinorUnits)
		}
		if got.UsageWindowUsedRatioPPM != nil || got.UsageWindowResetsAt != nil {
			t.Fatalf("usage_window 必须对密钥账号恒为 nil, got ratio_ppm=%v resets=%v",
				got.UsageWindowUsedRatioPPM, got.UsageWindowResetsAt)
		}
		if got.ProxyLabel != nil {
			t.Fatalf("ProxyLabel = %v, want nil（未配置代理）", got.ProxyLabel)
		}
		if got.UpstreamMultiplierPPM != nil {
			t.Fatalf("UpstreamMultiplierPPM = %v, want nil（extra 为空对象）", got.UpstreamMultiplierPPM)
		}
		if got.LastUsedAt != nil || got.ExpiresAt != nil {
			t.Fatalf("LastUsedAt/ExpiresAt 应为 nil, got %v/%v", got.LastUsedAt, got.ExpiresAt)
		}
	})

	t.Run("未登记的type取值(id 3)：kind必须是nil不是猜一个桶", func(t *testing.T) {
		got := byID["3"]
		if got.Kind != nil {
			t.Fatalf("Kind = %v, want nil（future-unknown-type 不在任何一桶里）", got.Kind)
		}
		if got.Vendor == nil || *got.Vendor != "gemini" {
			t.Fatalf("Vendor = %v, want gemini", got.Vendor)
		}
		if got.CapacityLimit == nil || *got.CapacityLimit != 2 {
			t.Fatalf("CapacityLimit = %v, want 2（没有 load_factor 字段，回落到 concurrency）", got.CapacityLimit)
		}
		if got.TodayRequests == nil || *got.TodayRequests != 5 {
			t.Fatalf("TodayRequests = %v, want 5", got.TodayRequests)
		}
		if got.TodayCostMinorUnits == nil || *got.TodayCostMinorUnits != 10 {
			t.Fatalf("TodayCostMinorUnits = %v, want 10（0.10 美元）", got.TodayCostMinorUnits)
		}
		if got.UsageWindowUsedRatioPPM != nil || got.UsageWindowResetsAt != nil {
			t.Fatalf("Kind 为 nil 时 usage_window 也必须是 nil, got ratio_ppm=%v resets=%v",
				got.UsageWindowUsedRatioPPM, got.UsageWindowResetsAt)
		}
		if got.ProxyLabel != nil || got.UpstreamMultiplierPPM != nil {
			t.Fatalf("没有 proxy/extra 字段时应为 nil, got proxy=%v upstream_multiplier_ppm=%v",
				got.ProxyLabel, got.UpstreamMultiplierPPM)
		}
	})
}

// TestFakeChannelCatalogFieldsSatisfyInvariants：Fake 的目录行同样要满足
// contracttest.AssertManagedChannelCatalogInvariants——两个实现共享同一套
// 跨字段一致性检查，不是只测真实客户端那一条路径。
func TestFakeChannelCatalogFieldsSatisfyInvariants(t *testing.T) {
	fake := sub2api.NewFake(sub2api.FakeOptions{})
	directory, err := fake.ChannelDirectory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(directory.Items) == 0 {
		t.Fatal("Fake 目录不应为空")
	}
	sawSubscription, sawUpstream := false, false
	for _, item := range directory.Items {
		contracttest.AssertManagedChannelCatalogInvariants(t, item)
		if item.Kind != nil && *item.Kind == "subscription" {
			sawSubscription = true
		}
		if item.Kind != nil && *item.Kind == "upstream" {
			sawUpstream = true
		}
	}
	if !sawSubscription || !sawUpstream {
		t.Fatalf("Fake 应同时覆盖 subscription 与 upstream 两种 kind，便于前端联调两种展示形态"+
			" (subscription=%v upstream=%v)", sawSubscription, sawUpstream)
	}
}

// TestRealClientDegradesTodayStatsWhenUnsupported：上游还没升级到
// today-stats 端点时（404 = not_supported），今日统计整体降级为 nil 并标记
// CoveragePartial，但**不能**让整次目录读取失败——id/name/status/balance
// 等基础字段与 today-stats 完全无关，不该被这一个可选维度拖累。
func TestRealClientDegradesTodayStatsWhenUnsupported(t *testing.T) {
	upstream := startFakeUpstream(t, sub2api.FakeOptions{}, func(u *fakeUpstream) {
		u.todayStatsUnsupported = true
	})
	directory, err := upstream.newClient(t).ChannelDirectory(t.Context())
	if err != nil {
		t.Fatalf("today-stats 端点缺失不该让整次目录读取失败: %v", err)
	}
	if !directory.CoveragePartial {
		t.Fatal("today-stats 端点缺失必须标记 CoveragePartial")
	}
	for _, item := range directory.Items {
		if item.TodayRequests != nil || item.TodayCostMinorUnits != nil {
			t.Fatalf("today-stats 端点缺失时 Today* 必须是 nil: %+v", item)
		}
		// 基础字段不该受影响
		if item.ChannelID == "" || item.Name == "" {
			t.Fatalf("基础字段被今日统计的降级拖累了: %+v", item)
		}
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
	u := &fakeUpstream{t: t, accountItems: fakeAccountItems, accountTotal: fakeAccountCount, paymentAmount: fakePaymentAmount}
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
