package newapi_test

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

	"github.com/xufei5620/xingmang-platform/connectors/newapi"
	"github.com/xufei5620/xingmang-platform/connectors/newapi/contracttest"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
	"github.com/xufei5620/xingmang-platform/internal/platform/registry"
	"github.com/xufei5620/xingmang-platform/internal/platform/secrets"
)

// 真实客户端的契约合规测试。
//
// 判据只有一条：**真实实现必须与 Fake 通过同一套 contracttest.RunSuite**。
// 为此这里起一个本地假上游（httptest.NewTLSServer），把套件注入的
// FakeOptions 翻译成假上游的行为——FailWith 变成对应的 HTTP 状态码或畸形
// 响应，Latency 变成响应前的等待，Partial 变成「声称有更多数据却给不出来」。
//
// 这样被测的就是**真实的那条路径**：真实的传输层护栏、真实的写端点黑名单、
// 真实的 HTTP 解析、真实的错误分类、真实的 quota 换算。假的只有上游本身。
//
// 另外几组不在套件里的测试，锁住这条链路上不能出事的纪律：
// 凭据不泄漏、目标 allowlist、拒绝重定向、以及**一次完整同步里一个写端点
// 都没碰过**。

// ---------------------------------------------------------------------------
// 上游路由：与 upstream.go 一字不差地写死在这里
//
// 故意重复而不是从生产代码里引用常量：路由是我们与上游之间的**约定**，
// 有人改了生产代码里的路径（比如手滑删掉一个尾斜杠），这里必须红——
// 引用同一个常量的话，改哪边测试都绿，等于没测。
// ---------------------------------------------------------------------------

const (
	upstreamStatus   = "/api/status"
	upstreamChannels = "/api/channel/"
	upstreamUsers    = "/api/user/"
	upstreamTopups   = "/api/user/topup"
	upstreamLogs     = "/api/log/"
	upstreamData     = "/api/data/"

	// upstreamAuthHeader 是管理员 access token 的携带方式。
	upstreamAuthHeader = "Authorization"
	// upstreamUserHeader 是旧版本 NewAPI 的第二个鉴权头。
	upstreamUserHeader = "New-Api-User"

	// fakeUpstreamToken 是假上游的凭据。
	//
	// 刻意用一句明显是占位符的英文而不是像真凭据的高熵串：
	// 仓库里不该出现任何长得像凭据的东西（宪法 7 条），
	// 而泄漏测试只需要一个「能在字符串里被找到」的独特值。
	fakeUpstreamToken = "placeholder-placeholder"

	// fakeCredentialRef 是测试用的凭据引用；引用本身不是秘密。
	fakeCredentialRef = "secret://newapi/readonly-token"

	// fakeTokenEnvVar 是引用在 env Provider 下的登记落点。
	fakeTokenEnvVar = "XM_TEST_NEWAPI_TOKEN"

	// fakeQuotaPerUnit 是上游的 quota → 美元换算基数（上游默认值）。
	// 500000 quota = 1 美元 = 100 分，所以 5000 quota = 1 分。
	fakeQuotaPerUnit = 500_000

	// fakeVersion 取自 docs/inventory/managed-systems.yaml 的 newapi-prod
	// detected_version：真实实例上报过的形态，带 v 前缀与 rc 后缀。
	fakeVersion = "v1.0.0-rc.25"
)

// fakeClockNow 是所有测试共用的固定时钟。
//
// 必须固定：契约套件里的业务日是写死的 "2026-08-27"，而本客户端要把它换算成
// Unix 时间戳区间去过滤充值订单与用量。跟着真实时钟走的话，这套测试会在某个
// 未来的日子突然变红，而且红得毫无道理——那种失败最消耗人。
var fakeClockNow = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

// fakeDayStart 是 2026-08-27 在 UTC 下的零点（默认业务日结时区）。
var fakeDayStart = time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC).Unix()

// capabilityRoutes 是能力到支撑路由的映射，供 MissingCapabilities 使用。
var capabilityRoutes = map[registry.Capability][]string{
	"newapi.service.version_read": {upstreamStatus},
	"newapi.health.read":          {upstreamStatus},
	"newapi.users.read":           {upstreamUsers},
	"newapi.orders.read":          {upstreamTopups},
	"newapi.channels.read":        {upstreamChannels},
	"newapi.errors.read":          {upstreamLogs},
	"newapi.models.usage_read":    {upstreamData},
}

// ---------------------------------------------------------------------------
// 固定数据
// ---------------------------------------------------------------------------

// fakeUserItems 是用户列表固定数据。
//
// 三处刻意设计：
//
//   - id=4 **被软删除**（DeletedAt 非 null）。上游的用户列表查询是 Unscoped 的，
//     软删除用户混在结果里、也算进 total。它那 999999999 的额度如果被算进去，
//     余额会大出好几个数量级——这条守着「用户数与余额都要跳过软删除」。
//   - 额度是 2500 + 2500 + 10000000。**先 SUM 再除**得 2001 分；
//     每人各自换算再相加会得到 2002 分（两个 0.5 分各自进位）。
//     这条守着 quotaToMinorUnits 的调用纪律。
//   - id=2 的 last_login_at 在 30 天窗口之外，用来验活跃口径真的在筛。
//
// 字段只给本包解得到的那几个：真实上游还会发 username/email/telegram_id
// 一堆个人数据，假上游照发一部分，好证明我们**没有**去解它们。
var fakeUserItems = fmt.Sprintf(`[
  {"id":1,"username":"alice","email":"a@example.test","quota":2500,
   "used_quota":10,"last_login_at":%d,"created_at":%d,"DeletedAt":null},
  {"id":2,"username":"bob","email":"b@example.test","quota":2500,
   "used_quota":20,"last_login_at":%d,"created_at":%d,"DeletedAt":null},
  {"id":3,"username":"carol","email":"c@example.test","quota":10000000,
   "used_quota":30,"last_login_at":%d,"created_at":%d,"DeletedAt":null},
  {"id":4,"username":"deleted","email":"d@example.test","quota":999999999,
   "used_quota":40,"last_login_at":%d,"created_at":%d,"DeletedAt":"2026-01-01T00:00:00Z"}
]`,
	fakeClockNow.AddDate(0, 0, -1).Unix(), fakeClockNow.AddDate(0, -6, 0).Unix(),
	fakeClockNow.AddDate(0, 0, -60).Unix(), fakeClockNow.AddDate(0, -6, 0).Unix(),
	fakeClockNow.AddDate(0, 0, -2).Unix(), fakeClockNow.AddDate(0, -6, 0).Unix(),
	fakeClockNow.AddDate(0, 0, -1).Unix(), fakeClockNow.AddDate(0, -6, 0).Unix())

const fakeUserTotal = 4

// fakeChannelItems 是渠道列表固定数据。
//
// 三条各自钉一个边界：
//
//	id=1  正常启用，余额刷新过 → BalanceMinorUnits = 3150（$31.50）
//	id=2  status=2（手动禁用）且 balance_updated_time=0
//	      → 余额**必须是 nil**：「从来没刷新过」= 这个渠道没有余额这个概念
//	id=3  启用，余额刷新过但**是 0** → BalanceMinorUnits = *0
//	      「配了而且花光了」与 id=2 的「没配」在看板上一个该报警一个该沉默
//
// models 里塞了空段与纯空白段：上游的 GetModels() 不做 TrimSpace，脏数据是
// 真会出现的。key 字段照发空串——上游把它 Omit 出了 SQL，但字段是非指针
// string，JSON 里仍然会有；本包**不解它**。
const fakeChannelItems = `[
  {"id":1,"name":"openai-main","type":1,"status":1,"key":"",
   "balance":31.50,"balance_updated_time":%d,"used_quota":123456,
   "models":"gpt-4o,gpt-4o-mini, ,o3","group":"default","response_time":480,
   "priority":10,"test_time":%d},
  {"id":2,"name":"gemini-backup","type":24,"status":2,"key":"",
   "balance":0,"balance_updated_time":0,"used_quota":0,
   "models":"gemini-2.5-pro","group":"default","response_time":0,
   "priority":5,"test_time":0},
  {"id":3,"name":"relay-cheap","type":8,"status":1,"key":"",
   "balance":0,"balance_updated_time":%d,"used_quota":9999,
   "models":"","group":"default","response_time":2340,
   "priority":1,"test_time":%d}
]`

const fakeChannelTotal = 3

// fakeLogCounts 是逐渠道、逐日志类型的条数（错误率的原料）。
//
// 上游没有错误率端点，只能拿 /api/log/ 的 COUNT 自己算：
// ppm = type5 / (type2 + type5)。
//
//	渠道 1  3/(997+3)   = 3000 ppm  = 0.3%，健康
//	渠道 2  1/(1+1)     = 500000 ppm = 50%，但它**停用了**——按契约一律不算异常
//	渠道 3  100/(100+100) = 500000 ppm = 50%，启用中，这是唯一一条异常渠道
var fakeLogCounts = map[string]int64{
	"1/5": 3, "1/2": 997,
	"2/5": 1, "2/2": 1,
	"3/5": 100, "3/2": 100,
}

// fakeTopupItems 是充值订单固定数据（上游按 id desc 排）。
//
// 七笔覆盖每一条筛选规则：三笔计入、四笔各自因为不同理由被排除或丢弃。
// 金额语义**三种都出现**（epay 取 amount、stripe 取 money、creem 的 amount
// 已经是 quota），抄错任意一支这里都会红。
var fakeTopupItems = fmt.Sprintf(`[
  {"id":107,"user_id":1,"amount":10,"money":72.50,"trade_no":"t-107",
   "payment_method":"alipay","payment_provider":"mystery_pay","status":"success",
   "create_time":%d,"complete_time":%d},
  {"id":106,"user_id":1,"amount":5,"money":5,"trade_no":"t-106",
   "payment_method":"balance","payment_provider":"balance","status":"success",
   "create_time":%d,"complete_time":%d},
  {"id":105,"user_id":2,"amount":99,"money":99,"trade_no":"t-105",
   "payment_method":"alipay","payment_provider":"epay","status":"pending",
   "create_time":%d,"complete_time":0},
  {"id":104,"user_id":2,"amount":3000000,"money":30,"trade_no":"t-104",
   "payment_method":"creem","payment_provider":"creem","status":"success",
   "create_time":%d,"complete_time":%d},
  {"id":103,"user_id":3,"amount":999,"money":20.5,"trade_no":"t-103",
   "payment_method":"stripe","payment_provider":"stripe","status":"success",
   "create_time":%d,"complete_time":%d},
  {"id":102,"user_id":3,"amount":10,"money":72.50,"trade_no":"t-102",
   "payment_method":"alipay","payment_provider":"epay","status":"success",
   "create_time":%d,"complete_time":%d},
  {"id":101,"user_id":4,"amount":99,"money":99,"trade_no":"t-101",
   "payment_method":"alipay","payment_provider":"epay","status":"success",
   "create_time":%d,"complete_time":%d}
]`,
	fakeDayStart+3600, fakeDayStart+3700, // 107 provider 认不出来 → unclassified
	fakeDayStart+3600, fakeDayStart+3700, // 106 内部划转 → 既不计钱也不计单
	fakeDayStart+3600,                    // 105 未支付 → 跳过
	fakeDayStart+3600, fakeDayStart+3700, // 104 creem  → quota 3000000
	fakeDayStart+3600, fakeDayStart+3700, // 103 stripe → 20.5 × 500000 = 10250000
	fakeDayStart+3600, fakeDayStart+3700, // 102 epay   → 10 × 500000 = 5000000
	fakeDayStart-7200, fakeDayStart-100) // 101 昨天到账 → 不属于这一天

const fakeTopupTotal = 7

// fakeQuotaData 是逐模型用量固定数据（/api/data/ 是**裸数组**，不分页）。
//
// gpt-4o 被拆成两个小时桶，各 2500 quota：**先 SUM 再除**得 1 分
// （5000/500000 = $0.01）；每桶各自换算再相加会得到 2 分。
// 这条与用户余额那条一起，把「先 SUM 再除」钉在两个不同的调用点上。
var fakeQuotaData = fmt.Sprintf(`[
  {"model_name":"gpt-4o","count":100,"quota":2500,"token_used":1000,"created_at":%d,
   "id":0,"user_id":0,"username":"","channel_id":0},
  {"model_name":"gpt-4o","count":50,"quota":2500,"token_used":500,"created_at":%d,
   "id":0,"user_id":0,"username":"","channel_id":0},
  {"model_name":"claude-sonnet-4","count":10,"quota":10000000,"token_used":9,"created_at":%d,
   "id":0,"user_id":0,"username":"","channel_id":0}
]`, fakeDayStart+3600, fakeDayStart+7200, fakeDayStart+7200)

// ---------------------------------------------------------------------------
// 假上游
// ---------------------------------------------------------------------------

type fakeRequest struct {
	method string
	path   string
	query  string
	auth   string
	user   string
}

type fakeUpstream struct {
	t    *testing.T
	opts newapi.FakeOptions

	// echoCredential 让失败响应把收到的凭据原样回显。
	//
	// 这是在模拟一个**不小心的上游**：真实世界里见过把请求头写进错误正文的
	// 服务。泄漏测试要的就是这种上游——如果我们把响应体拼进错误信息，
	// 凭据就顺着错误链跑到日志里去了。
	echoCredential bool
	// redirectTo 非空时，所有请求都被 302 到该地址。
	redirectTo string
	// quotaPerUnit 允许单个测试改换算基数（包括改成 0 那种病态值）。
	quotaPerUnit string

	server *httptest.Server

	mu       sync.Mutex
	requests []fakeRequest
}

func startFakeUpstream(t *testing.T, opts newapi.FakeOptions, tweak ...func(*fakeUpstream)) *fakeUpstream {
	t.Helper()
	u := &fakeUpstream{t: t, opts: opts, quotaPerUnit: strconv.Itoa(fakeQuotaPerUnit)}
	for _, fn := range tweak {
		fn(u)
	}

	mux := http.NewServeMux()
	suppressed := u.suppressedRoutes()
	register := func(path string, h http.HandlerFunc) {
		if suppressed[path] {
			// 不注册 = 404 = 客户端归类为 not_supported = 该能力不可用。
			// 这正是「旧版本上游少几项能力」在 HTTP 上的样子。
			return
		}
		mux.HandleFunc("GET "+path, h)
	}
	// /api/status 不受 FailWith 影响：它是版本与健康的来源，那两者各有
	// UnsupportedVersion / Unhealthy 开关。两个实现在这一点上必须一致，
	// 否则「换实现」会换掉故障语义。
	register(upstreamStatus, u.handleStatus)
	register(upstreamChannels, u.dataRoute(u.handleChannels))
	register(upstreamUsers, u.dataRoute(u.handleUsers))
	register(upstreamTopups, u.dataRoute(u.handleTopups))
	register(upstreamLogs, u.dataRoute(u.handleLogs))
	register(upstreamData, u.dataRoute(u.handleQuotaData))

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
			user:   r.Header.Get(upstreamUserHeader),
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
func (u *fakeUpstream) dataRoute(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !u.wait(r) {
			return
		}
		switch u.opts.FailWith {
		case connector.KindAuth:
			u.writeCredentialLeakingError(w, r, http.StatusUnauthorized, "AUTH_UNAUTHORIZED")
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
			// 而不是被当成「网络出问题」重试。
			u.writeRaw(w, http.StatusOK, `{"success":true,"message":"","data":{"total":`)
			return
		}
		h(w, r)
	}
}

// writeCredentialLeakingError 模拟一个把请求头回显进错误正文的上游。
func (u *fakeUpstream) writeCredentialLeakingError(w http.ResponseWriter, r *http.Request, status int, code string) {
	body := fmt.Sprintf(`{"success":false,"message":%q}`, code)
	if u.echoCredential {
		body = fmt.Sprintf(`{"success":false,"message":"rejected %s for a@example.test"}`,
			r.Header.Get(upstreamAuthHeader))
	}
	u.writeRaw(w, status, body)
}

func (u *fakeUpstream) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !u.wait(r) {
		return
	}
	if u.opts.Unhealthy {
		// 上游的业务失败是 **HTTP 200 + success:false**（common.ApiError），
		// 不是 5xx。健康判据必须看得懂这一点。
		u.writeRaw(w, http.StatusOK, `{"success":false,"message":"database unavailable","data":null}`)
		return
	}
	version := strings.TrimSpace(u.opts.Version)
	if version == "" {
		version = fakeVersion
	}
	if u.opts.UnsupportedVersion {
		version = "v9.9.9"
	}
	// data 里塞几个本包**不该去解**的键：真实的 /api/status 有 60 多个，
	// 多解一个就多一处会随上游改版而炸的地方。
	u.writeRaw(w, http.StatusOK, fmt.Sprintf(
		`{"success":true,"message":"","data":{
		  "version":%q,"start_time":%d,"quota_per_unit":%s,
		  "system_name":"New API","theme":"default","register_enabled":true,
		  "display_in_currency":true,"usd_exchange_rate":7.2,
		  "HeaderNavModules":"{}","enable_data_export":true}}`,
		version, u.now().Add(-72*time.Hour).Unix(), u.quotaPerUnit))
}

func (u *fakeUpstream) handleUsers(w http.ResponseWriter, r *http.Request) {
	page := queryInt(r, "p", 1)
	total := fakeUserTotal
	if u.opts.Partial {
		// 声称有 99 个用户却只给得出第一页：客户端必须把这件事标记成部分数据，
		// 而不是把少算的余额当成全部余额。
		total = 99
	}
	items := "[]"
	if page == 1 {
		items = fakeUserItems
	}
	u.writePage(w, items, total, page)
}

func (u *fakeUpstream) handleChannels(w http.ResponseWriter, r *http.Request) {
	page := queryInt(r, "p", 1)
	items := "[]"
	if page == 1 {
		items = fmt.Sprintf(fakeChannelItems,
			u.now().Add(-time.Hour).Unix(), u.now().Add(-time.Hour).Unix(),
			u.now().Add(-2*time.Hour).Unix(), u.now().Add(-2*time.Hour).Unix())
	}
	// 渠道列表是 handler 自己拼的 gin.H，比 PageInfo 多一个 type_counts。
	// 假上游照抄真实形状——哪天有人想读它，看到的必须是上游真会发的东西。
	u.writeRaw(w, http.StatusOK, fmt.Sprintf(
		`{"success":true,"message":"","data":{"items":%s,"total":%d,"page":%d,
		  "page_size":100,"type_counts":{"1":1,"8":1,"24":1}}}`,
		items, fakeChannelTotal, page))
}

func (u *fakeUpstream) handleTopups(w http.ResponseWriter, r *http.Request) {
	page := queryInt(r, "p", 1)
	items := "[]"
	if page == 1 {
		items = fakeTopupItems
	}
	u.writePage(w, items, fakeTopupTotal, page)
}

// handleLogs 只回 total：客户端拿它当 COUNT 用，一条日志正文都不该要。
func (u *fakeUpstream) handleLogs(w http.ResponseWriter, r *http.Request) {
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	logType := strings.TrimSpace(r.URL.Query().Get("type"))
	if logType == "0" || logType == "" {
		// 上游把 type=0 当成「不过滤」。客户端若真发了 0，错误率的分母会
		// 静默变成全量日志数——让假上游当场炸，比让它悄悄算错好。
		u.t.Errorf("客户端不该用 type=%q 查日志：0 在上游表示不过滤", logType)
	}
	u.writePage(w, "[]", int(fakeLogCounts[channel+"/"+logType]), queryInt(r, "p", 1))
}

func (u *fakeUpstream) handleQuotaData(w http.ResponseWriter, r *http.Request) {
	// /api/data/ 的 data 是**裸数组**，不是 {items,total}。
	u.writeRaw(w, http.StatusOK,
		fmt.Sprintf(`{"success":true,"message":"","data":%s}`, fakeQuotaData))
}

// writePage 输出 common.PageInfo 形状的响应。
func (u *fakeUpstream) writePage(w http.ResponseWriter, items string, total, page int) {
	u.writeRaw(w, http.StatusOK, fmt.Sprintf(
		`{"success":true,"message":"","data":{"items":%s,"total":%d,"page":%d,"page_size":100}}`,
		items, total, page))
}

// writeRaw 写响应，并显式给出 Date 头。
//
// 显式设置是有意的：NewAPI 的列表端点**一个都不给数据时间戳**，所以观测时刻
// 必然退到 Date 头。把它钉成固定值，ObservedAt 才是可断言的——顺便也证明了
// 客户端真的在读这个头，而不是拿本地时钟冒充。
func (u *fakeUpstream) writeRaw(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Date", u.observedAt().Format(http.TimeFormat))
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
		ServiceInstanceID: "newapi-test",
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

func (u *fakeUpstream) newClient(t *testing.T, extra ...newapi.Option) newapi.ReadClient {
	t.Helper()
	opts := append([]newapi.Option{
		// 只换「怎么连」：让客户端信任 httptest 的自签证书。
		// 只读方法限制、allowlist、拒绝重定向、写端点黑名单依旧是生产那一份代码。
		newapi.WithBaseTransport(u.server.Client().Transport),
		newapi.WithClock(u.now),
	}, extra...)
	client, err := newapi.NewClient(u.config(), fakeSecretProvider(t, nil), opts...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

// ---------------------------------------------------------------------------
// 合规判据：真实客户端必须通过与 Fake 同一套契约套件
// ---------------------------------------------------------------------------

func TestRealClientSatisfiesContract(t *testing.T) {
	contracttest.RunSuite(t, func(opts newapi.FakeOptions) newapi.ReadClient {
		upstream := startFakeUpstream(t, opts)
		return upstream.newClient(t)
	})
}

// ---------------------------------------------------------------------------
// 上游形状映射
// ---------------------------------------------------------------------------

func TestRealClientMapsUpstreamShapes(t *testing.T) {
	upstream := startFakeUpstream(t, newapi.FakeOptions{})
	client := upstream.newClient(t)
	ctx := t.Context()

	t.Run("版本", func(t *testing.T) {
		v, err := client.Version(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if v.Detected != fakeVersion {
			t.Fatalf("Detected = %q, want %q", v.Detected, fakeVersion)
		}
		if !v.Supported {
			t.Fatalf("%s 应在兼容矩阵内（normalizeVersion 收敛成 1.0.0）: %+v", fakeVersion, v)
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
		// 4 条里有 1 条是软删除的：上游的列表是 Unscoped 的，
		// data.total 也把它算在内，所以这里必须是 3 而不是 4。
		if stats.TotalUsers != 3 {
			t.Fatalf("用户数 = %d, want 3（软删除的那条必须跳过）", stats.TotalUsers)
		}
		// alice(1 天前) 与 carol(2 天前) 在 30 天窗口内，bob(60 天前) 不在，
		// 被删掉的那个虽然「活跃」但根本不该被数。
		if stats.ActiveUsers != 2 {
			t.Fatalf("活跃用户 = %d, want 2（last_login_at 落在 30 天窗口内）", stats.ActiveUsers)
		}
		// (2500 + 2500 + 10000000) / 500000 = $20.01 = 2001 分。
		// 每人先各自换算再相加会得到 2002——这条守着「先 SUM 再除」。
		if stats.BalanceMinorUnits != 2001 {
			t.Fatalf("余额 = %d 分, want 2001（先 SUM 再除 quota_per_unit）", stats.BalanceMinorUnits)
		}
		if stats.Currency != "USD" {
			t.Fatalf("币种 = %q, want USD（quota 的换算口径是美元）", stats.Currency)
		}
		if !stats.ObservedAt.Equal(fakeClockNow) {
			t.Fatalf("ObservedAt = %v, want 上游 Date 头 %v", stats.ObservedAt, fakeClockNow)
		}
		// 活跃口径与换算基数都是「数字之外必须一起给出的判据」，
		// 契约 v1 没有字段装它们，所以走水位。
		if !strings.Contains(stats.Watermark, "active:last_login_30d") ||
			!strings.Contains(stats.Watermark, "qpu:500000") {
			t.Fatalf("水位应带上活跃判据与换算基数: %q", stats.Watermark)
		}
		if stats.IsPartial {
			t.Fatal("拉全了不该标记为部分数据")
		}
	})

	t.Run("渠道", func(t *testing.T) {
		channels, err := client.Channels(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(channels) != 3 {
			t.Fatalf("渠道数 = %d, want 3: %+v", len(channels), channels)
		}

		balance := func(v int64) *int64 { return &v }
		want := []struct {
			id, name, chType string
			enabled          bool
			balance          *int64
			models           int64
			latency          int64
			ppm              int64
		}{
			{"1", "openai-main", "1", true, balance(3150), 3, 480, 3_000},
			// balance_updated_time = 0 → 从来没刷新过 → 没有「余额」这个概念
			{"2", "gemini-backup", "24", false, nil, 1, 0, 500_000},
			// 刷新过、但确实是 0 → 「配了而且花光了」，与上面那条语义相反
			{"3", "relay-cheap", "8", true, balance(0), 0, 2340, 500_000},
		}
		for i, w := range want {
			got := channels[i]
			if got.ChannelID != w.id || got.Name != w.name || got.Type != w.chType {
				t.Fatalf("渠道 #%d 身份 = %s/%s/%s, want %s/%s/%s",
					i, got.ChannelID, got.Name, got.Type, w.id, w.name, w.chType)
			}
			if got.Enabled != w.enabled {
				t.Fatalf("渠道 %s 启停 = %v, want %v（status 1=启用，2=手动禁用）",
					w.id, got.Enabled, w.enabled)
			}
			switch {
			case w.balance == nil && got.BalanceMinorUnits != nil:
				t.Fatalf("渠道 %s 余额应为 nil（balance_updated_time=0 = 从没刷新过），got %d",
					w.id, *got.BalanceMinorUnits)
			case w.balance != nil && got.BalanceMinorUnits == nil:
				t.Fatalf("渠道 %s 余额不该是 nil, want %d", w.id, *w.balance)
			case w.balance != nil && *got.BalanceMinorUnits != *w.balance:
				t.Fatalf("渠道 %s 余额 = %d, want %d", w.id, *got.BalanceMinorUnits, *w.balance)
			}
			if got.ModelCount != w.models {
				t.Fatalf("渠道 %s 模型数 = %d, want %d（逗号分隔，空段不算）",
					w.id, got.ModelCount, w.models)
			}
			if got.LatencyMS != w.latency {
				t.Fatalf("渠道 %s 延迟 = %d, want %d", w.id, got.LatencyMS, w.latency)
			}
			if got.ErrorRatePPM != w.ppm {
				t.Fatalf("渠道 %s 错误率 = %d ppm, want %d（type5/(type2+type5)）",
					w.id, got.ErrorRatePPM, w.ppm)
			}
			if got.Snapshot.IsPartial {
				t.Fatalf("渠道 %s 三条都算过错误率，不该标记为部分数据", w.id)
			}
		}

		// 停用渠道一律不算异常：它没在服务，谈不上出错。
		if channels[1].Unhealthy() {
			t.Fatal("停用渠道不该被计进异常数，哪怕错误率 50%")
		}
		if !channels[2].Unhealthy() {
			t.Fatal("启用且错误率 50% 的渠道应算异常")
		}
		// 余额的新鲜度与渠道状态的新鲜度差得很远（上游默认不自动刷新余额），
		// 契约 v1 没有字段装它，所以走水位。
		if !strings.Contains(channels[0].Watermark, "balance_oldest:") {
			t.Fatalf("水位应带上余额的最旧刷新时刻: %q", channels[0].Watermark)
		}
	})

	t.Run("业务日充值与订阅", func(t *testing.T) {
		orders, err := client.DailyOrders(ctx, "2026-08-27")
		if err != nil {
			t.Fatal(err)
		}
		if orders.Day != "2026-08-27" {
			t.Fatalf("业务日 = %q", orders.Day)
		}
		// epay 10 美元 + stripe 20.5 美元 + creem 3000000 quota
		// = (5000000 + 10250000 + 3000000) quota = 18250000 → $36.50
		if orders.RechargeMinorUnits != 3650 {
			t.Fatalf("充值 = %d 分, want 3650（三种 provider 语义各取各的字段）",
				orders.RechargeMinorUnits)
		}
		// 三笔计入：昨天到账的、未支付的、内部划转的都不算。
		if orders.OrderCount != 3 {
			t.Fatalf("订单数 = %d, want 3", orders.OrderCount)
		}
		// 上游根本没有订阅订单的列表端点，读不到就是读不到。
		if orders.SubscriptionMinorUnits != 0 {
			t.Fatalf("订阅金额 = %d，上游无端点可读，只能是 0", orders.SubscriptionMinorUnits)
		}
		if !orders.IsPartial {
			t.Fatal("订阅金额读不到，这条汇总必须标记为部分数据——" +
				"否则看板会把「读不到」显示成「这个月没人订阅」")
		}
		for _, want := range []string{"day:2026-08-27", "subscription:unavailable_over_http", "unclassified:1"} {
			if !strings.Contains(orders.Watermark, want) {
				t.Fatalf("水位缺少 %q: %q", want, orders.Watermark)
			}
		}
	})

	t.Run("逐模型用量", func(t *testing.T) {
		usages, err := client.ModelUsages(ctx, "2026-08-27")
		if err != nil {
			t.Fatal(err)
		}
		if len(usages) != 2 {
			t.Fatalf("模型数 = %d, want 2（同一模型的多个小时桶要合并）: %+v", len(usages), usages)
		}
		got := map[string]newapi.ModelUsage{}
		for _, u := range usages {
			got[u.ModelName] = u
		}
		gpt := got["gpt-4o"]
		if gpt.RequestCount != 150 {
			t.Fatalf("gpt-4o 请求数 = %d, want 150（100 + 50 两个小时桶）", gpt.RequestCount)
		}
		// 两个桶各 2500 quota：先加成 5000 再除得 1 分；各自除完再加会得到 2 分。
		if gpt.ConsumedMinorUnits != 1 {
			t.Fatalf("gpt-4o 消耗 = %d 分, want 1（先 SUM 再除 quota_per_unit）",
				gpt.ConsumedMinorUnits)
		}
		if claude := got["claude-sonnet-4"]; claude.ConsumedMinorUnits != 2000 {
			t.Fatalf("claude 消耗 = %d 分, want 2000", claude.ConsumedMinorUnits)
		}
		if !strings.Contains(gpt.Watermark, "bucket:") {
			t.Fatalf("水位应带上最新的小时桶: %q", gpt.Watermark)
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
		// 契约的 7 项全部实现了，一项都不该少。
		for _, want := range newapi.ReadCapabilities {
			if !got[want] {
				t.Fatalf("缺少能力 %q: %v", want, caps)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// 第五道闸的端到端证明
// ---------------------------------------------------------------------------

// TestRealClientNeverTouchesWriteEndpoints：跑完一整轮同步（采集任务真正会调
// 的四个读方法 + 能力探测），假上游收到的每一条请求都必须是 GET，
// 而且路径一条都不能落在写端点黑名单里。
//
// 这比 assertReadOnlyRoute 的单元测试强一个量级：那条证明「函数会拦」，
// 这条证明「跑完整条链路之后，事实上一次都没碰过」。将来有人给某个 fetch
// 函数加一条便利路由（比如「顺手 update_balance 一下拿最新余额」），
// 单元测试可能照绿，这条会当场红。
func TestRealClientNeverTouchesWriteEndpoints(t *testing.T) {
	upstream := startFakeUpstream(t, newapi.FakeOptions{})
	client := upstream.newClient(t)
	ctx := t.Context()

	// 采集任务一轮会做的全部事情，外加不在热路径上的版本/健康/能力探测。
	if _, err := client.Version(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Health(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Capabilities(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UserStats(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.DailyOrders(ctx, "2026-08-27"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Channels(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ModelUsages(ctx, "2026-08-27"); err != nil {
		t.Fatal(err)
	}

	// 与 upstream.go 的黑名单一字不差地重写一遍（理由同路由常量）。
	blocked := []string{
		"/api/channel/test", "/api/channel/update_balance", "/api/channel/fetch_models",
		"/api/user/token", "/api/user/aff", "/api/user/epay/notify",
		"/api/subscription/epay/", "/api/oauth/", "/v1/realtime", "/v1/videos",
	}
	requests := upstream.recorded()
	if len(requests) == 0 {
		t.Fatal("一条请求都没发出去，这个测试就没有意义了")
	}
	for _, req := range requests {
		if req.method != http.MethodGet {
			t.Fatalf("只读通道上出现了 %s %s", req.method, req.path)
		}
		for _, bad := range blocked {
			if strings.HasPrefix(req.path, bad) {
				t.Fatalf("碰到了写端点 %s（匹配黑名单项 %s）", req.path, bad)
			}
		}
	}
}

// TestRealClientSendsBearerCredential：凭据按 Bearer 形态发出，
// 而 New-Api-User 只在显式配置时才发。
//
// 后半条是有代价的选择：普查依据的源码里那个头**已经不参与鉴权**，
// 但真实实例跑的补丁版未知，旧版本上没有它就是 401。所以它是可选的——
// 默认不发（新版本不需要），配了就发（旧版本需要）。
func TestRealClientSendsBearerCredential(t *testing.T) {
	upstream := startFakeUpstream(t, newapi.FakeOptions{})
	if _, err := upstream.newClient(t).UserStats(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, req := range upstream.recorded() {
		if req.auth != "Bearer "+fakeUpstreamToken {
			t.Fatalf("%s 的 Authorization = %q, want Bearer 形态", req.path, req.auth)
		}
		if req.user != "" {
			t.Fatalf("没配 user id 时不该发 %s 头, got %q", upstreamUserHeader, req.user)
		}
	}

	withUser := startFakeUpstream(t, newapi.FakeOptions{})
	if _, err := withUser.newClient(t, newapi.WithUserID("1")).UserStats(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, req := range withUser.recorded() {
		if req.user != "1" {
			t.Fatalf("配了 user id 之后 %s 头 = %q, want 1", upstreamUserHeader, req.user)
		}
	}
}

// ---------------------------------------------------------------------------
// 上游的两个坑
// ---------------------------------------------------------------------------

// TestRealClientRejectsBusinessFailure：上游的业务失败是 **HTTP 200 +
// success:false**（common.ApiError），只有鉴权失败才是真 401/403。
//
// 只看状态码的话，一次「查询失败」会被当成一次成功读取，然后把空数据
// 写进看板——数字变成 0，徽章显示「数据新鲜」。
func TestRealClientRejectsBusinessFailure(t *testing.T) {
	upstream := startFakeUpstreamWithHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeBody(w, http.StatusOK, `{"success":false,"message":"查询失败","data":null}`)
	})
	_, err := upstream.newClient(t).Channels(t.Context())
	if connector.KindOf(err) != connector.KindBadResponse {
		t.Fatalf("success=false 的分类 = %q, want bad_response（err=%v）", connector.KindOf(err), err)
	}
}

// TestRealClientRefusesZeroQuotaPerUnit：上游的 option 写入路径把 ParseFloat
// 的 error 丢掉了（model/option.go 的 `_`），非法值会把 QuotaPerUnit 置 0。
//
// 拿 0 做除数是崩溃，拿默认值 500000 顶上是**编数**——那会产出一批
// 看起来完全正常的错数字。所以只能报错，让这一轮的金额指标写成失败观测。
func TestRealClientRefusesZeroQuotaPerUnit(t *testing.T) {
	upstream := startFakeUpstream(t, newapi.FakeOptions{},
		func(u *fakeUpstream) { u.quotaPerUnit = "0" })
	_, err := upstream.newClient(t).UserStats(t.Context())
	if connector.KindOf(err) != connector.KindBadResponse {
		t.Fatalf("quota_per_unit=0 的分类 = %q, want bad_response（err=%v）",
			connector.KindOf(err), err)
	}
}

// TestRealClientClassifiesUpstreamStatuses 覆盖 ADR-004 的错误映射表里
// 契约套件没有覆盖到的那几格。
func TestRealClientClassifiesUpstreamStatuses(t *testing.T) {
	// 路由不存在 = 这个上游版本没有这项能力，不是「上游挂了」：
	// 两者的处置完全不同，重试对前者永远没用。
	upstream := startFakeUpstream(t, newapi.FakeOptions{
		MissingCapabilities: []registry.Capability{"newapi.channels.read"},
	})
	_, err := upstream.newClient(t).Channels(t.Context())
	if connector.KindOf(err) != connector.KindNotSupported {
		t.Fatalf("404 的分类 = %q, want not_supported（err=%v）", connector.KindOf(err), err)
	}

	// 403：角色不够，或者渠道路由上的 casbin 权限缺 ChannelRead。
	// 与 401 同一种处置——都要人去看凭据配置，重试不会自己变好。
	forbidden := startFakeUpstreamWithHandler(t, func(w http.ResponseWriter, r *http.Request) {
		writeBody(w, http.StatusForbidden, `{"success":false,"message":"AUTH_INSUFFICIENT_PRIVILEGE"}`)
	})
	if _, err := forbidden.newClient(t).Channels(t.Context()); connector.KindOf(err) != connector.KindAuth {
		t.Fatalf("403 的分类 = %q, want auth（err=%v）", connector.KindOf(err), err)
	}
}

// startFakeUpstreamWithHandler 起一个所有路由都走同一个 handler 的假上游。
func startFakeUpstreamWithHandler(t *testing.T, h http.HandlerFunc) *fakeUpstream {
	t.Helper()
	u := &fakeUpstream{t: t, quotaPerUnit: strconv.Itoa(fakeQuotaPerUnit)}
	u.server = httptest.NewTLSServer(u.record(h))
	t.Cleanup(u.server.Close)
	return u
}

func writeBody(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
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

	upstream := startFakeUpstream(t, newapi.FakeOptions{FailWith: connector.KindAuth},
		func(u *fakeUpstream) { u.echoCredential = true })

	client, err := newapi.NewClient(upstream.config(), fakeSecretProvider(t, logger),
		newapi.WithBaseTransport(upstream.server.Client().Transport),
		newapi.WithClock(upstream.now))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, readErr := client.Channels(t.Context())
	if readErr == nil {
		t.Fatal("注入 401 后应报错")
	}
	if connector.KindOf(readErr) != connector.KindAuth {
		t.Fatalf("401 的分类 = %q, want auth", connector.KindOf(readErr))
	}

	// 先证明这个测试不是空转：凭据确实被发出去过，上游也确实回显了它。
	sent := false
	for _, req := range upstream.recorded() {
		if strings.Contains(req.auth, fakeUpstreamToken) {
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
	if got := readErr.Error(); got != "auth: newapi.channels.read" {
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
	upstream := startFakeUpstream(t, newapi.FakeOptions{})

	// 端点主机不在自己的 allowlist 里：这种配置能「配得出来」但一个请求都
	// 发不出去，属于最难排查的故障，所以在构造期就拒（闸 1）。
	cfg := upstream.config()
	cfg.TargetAllowlist = []string{"api.somewhere-else.test"}
	if _, err := newapi.NewClient(cfg, fakeSecretProvider(t, nil)); err == nil {
		t.Fatal("端点不在 allowlist 内的配置必须被拒")
	} else if connector.KindOf(err) != connector.KindInternal {
		t.Fatalf("配置错误的分类 = %q, want internal（是我们的配置问题，不是上游的）",
			connector.KindOf(err))
	}

	// allowlist 为空：fail closed，不是「放行一切」
	cfg = upstream.config()
	cfg.TargetAllowlist = nil
	if _, err := newapi.NewClient(cfg, fakeSecretProvider(t, nil)); err == nil {
		t.Fatal("空 allowlist 必须被拒（fail closed）")
	}
}

// TestRealClientRefusesRedirects：上游一个 302 就能把请求引到 allowlist
// 之外，所以重定向一律拒绝——而且拒绝的分类必须是 forbidden_target，
// 不能被改判成 unavailable，否则「目标被换了」会伪装成「上游挂了」。
//
// 这条对 NewAPI 尤其要紧：它的列表路由注册的是 "/"，**少写一个尾斜杠就会
// 触发 301**。症状会是 forbidden_target 而不是 404，运行手册里写明了这一点。
func TestRealClientRefusesRedirects(t *testing.T) {
	upstream := startFakeUpstream(t, newapi.FakeOptions{}, func(u *fakeUpstream) {
		u.redirectTo = "https://evil.example.test/api/channel/"
	})
	_, err := upstream.newClient(t).Channels(t.Context())
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
	upstream := startFakeUpstream(t, newapi.FakeOptions{})
	if _, err := newapi.NewClient(upstream.config(), nil); err == nil {
		t.Fatal("没有 SecretProvider 必须被拒")
	}
}

// TestRealClientResolvesCredentialLazily：构造期不做任何 I/O，
// 凭据在首次读取时才解析——用的是那次请求的 ctx，取消才管得住它。
func TestRealClientResolvesCredentialLazily(t *testing.T) {
	upstream := startFakeUpstream(t, newapi.FakeOptions{})
	failing := failingProvider{err: errors.New("provider 暂时不可用")}

	client, err := newapi.NewClient(upstream.config(), failing,
		newapi.WithBaseTransport(upstream.server.Client().Transport),
		newapi.WithClock(upstream.now))
	if err != nil {
		t.Fatalf("构造期不该解析凭据，也就不该在这里失败: %v", err)
	}
	if len(upstream.recorded()) != 0 {
		t.Fatal("构造期不该发出任何请求")
	}
	_, readErr := client.Channels(t.Context())
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
