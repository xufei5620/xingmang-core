package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// 看板供数两个端点（XM-0037d，§8.5 + UI 交接 §13）。
//
// 这一层要验的是**形状与诚实**，不是算术（算术在 finance 包里）：
// 金额可空、毛利率给不出时是 null、可用天数给不出时带得出原因、
// 覆盖率与阈值原样回报、凭据只出引用。

type fakeSummaryLister struct {
	items []finance.UpstreamSummary
	err   error
	got   finance.SummaryQuery
}

type fakeSummaryThresholdProvider struct {
	snapshot finance.RunwayThresholdSnapshot
	calls    int
	err      error
}

func (f *fakeSummaryThresholdProvider) Current(_ context.Context, _ string) (finance.RunwayThresholdSnapshot, error) {
	f.calls++
	if f.err != nil {
		return finance.RunwayThresholdSnapshot{}, f.err
	}
	return f.snapshot, nil
}

func (f *fakeSummaryLister) UpstreamSummaries(
	_ context.Context, q finance.SummaryQuery,
) ([]finance.UpstreamSummary, error) {
	f.got = q
	return f.items, f.err
}

func summaryRouter(
	t *testing.T, lister FinanceSummaryLister, thresholds finance.RunwayThresholds,
) http.Handler {
	t.Helper()
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger:                  discardLogger(),
		Service:                 "platform-api",
		Environment:             "development",
		DB:                      fakePinger{},
		Resolver:                res,
		ActionRegistry:          action.NewRegistry(),
		FinanceSummaries:        lister,
		FinanceRunwayThresholds: thresholds,
	})
}

func summaryProviderRouter(t *testing.T, lister FinanceSummaryLister, provider RunwayThresholdProvider) http.Handler {
	t.Helper()
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger: discardLogger(), Service: "platform-api", Environment: "development", DB: fakePinger{}, Resolver: res,
		ActionRegistry: action.NewRegistry(), FinanceSummaries: lister, FinanceRunwayConfig: provider,
	})
}

func getSummary(
	t *testing.T, lister FinanceSummaryLister, path, scopes, query string,
) *httptest.ResponseRecorder {
	t.Helper()
	// 零值阈值 = 装配层没注入，端点回落默认档（见 runwayThresholdsOrDefault）
	return getSummaryWith(t, lister, finance.RunwayThresholds{}, path, scopes, query)
}

func getSummaryWith(
	t *testing.T, lister FinanceSummaryLister, thresholds finance.RunwayThresholds,
	path, scopes, query string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path+query, nil)
	devHeaders(req, scopes)
	rec := httptest.NewRecorder()
	summaryRouter(t, lister, thresholds).ServeHTTP(rec, req)
	return rec
}

// summaryCredentialRef 抽成常量：`credential_ref: "secret://…"` 这个形状会被
// gitleaks 的 generic-api-key 规则当成泄露的密钥（同 finance_test.go 的说明）。
const summaryCredentialRef = "secret://finance/summary-a"

func meteredSummary() finance.UpstreamSummary {
	return finance.UpstreamSummary{
		Account: finance.UpstreamAccount{
			ID:            uuid.New(),
			SystemType:    finance.SystemSub2API,
			AccessMethod:  finance.AccessUpstreamKey,
			BaseURL:       "https://upstream.example.test",
			CredentialRef: summaryCredentialRef,
			RechargeRatio: money.MustParseRatio("1.5"),
			Currency:      "USD",
			BusinessDayTZ: "+08:00",
			PlatformID:    "sub2api-prod",
			Status:        finance.StatusActive,
			Environment:   "development",
		},
		TokenCount: 3,
		// 与仓储交出来的形状一致：ComputeRunway 保证「要么有天数、要么有原因」
		// （finance 包里有一条穷举用例钉住它），所以这里也不能留零值。
		Runway: finance.Runway{
			Reason: finance.RunwayReasonNoBalance, WindowDays: finance.RunwayWindowDays,
		},
	}
}

// TestSummaryEndpointsRequireFinanceRead：两个端点都复用 finance.read。
func TestSummaryEndpointsRequireFinanceRead(t *testing.T) {
	for _, path := range []string{
		"/api/v1/finance/channels/summary",
		"/api/v1/finance/upstreams/summary",
	} {
		rec := getSummary(t, &fakeSummaryLister{}, path, "ops.read", "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s 缺 finance.read 应 403, got %d", path, rec.Code)
		}
		rec = getSummary(t, &fakeSummaryLister{}, path, "finance.read", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 带 finance.read 应 200, got %d (%s)", path, rec.Code, rec.Body.String())
		}
	}
}

func TestSummaryProviderIsReadOnceAndEchoed(t *testing.T) {
	provider := &fakeSummaryThresholdProvider{snapshot: finance.RunwayThresholdSnapshot{
		Thresholds: finance.RunwayThresholds{CriticalDays: 4, WarningDays: 9, SeriousDays: 18}, Revision: 8, Source: "database", UpdatedAt: time.Date(2026, 8, 29, 1, 0, 0, 0, time.UTC),
	}}
	lister := &fakeSummaryLister{items: []finance.UpstreamSummary{meteredSummary()}}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/finance/upstreams/summary", nil)
	devHeaders(req, finance.ScopeRead)
	rec := httptest.NewRecorder()
	summaryProviderRouter(t, lister, provider).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls=%d want 1", provider.calls)
	}
	if lister.got.Thresholds != provider.snapshot.Thresholds {
		t.Fatalf("store thresholds=%+v want %+v", lister.got.Thresholds, provider.snapshot.Thresholds)
	}
	if !strings.Contains(rec.Body.String(), `"revision":8`) || !strings.Contains(rec.Body.String(), `"critical_days":4`) {
		t.Fatalf("response missing same snapshot: %s", rec.Body.String())
	}
}

func TestSummaryProviderFailureDoesNotSynthesizeDefaults(t *testing.T) {
	provider := &fakeSummaryThresholdProvider{err: finance.ErrRunwayConfigUnavailable}
	lister := &fakeSummaryLister{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/finance/channels/summary", nil)
	devHeaders(req, finance.ScopeRead)
	rec := httptest.NewRecorder()
	summaryProviderRouter(t, lister, provider).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "critical_days") {
		t.Fatalf("unexpected unavailable response: %d %s", rec.Code, rec.Body.String())
	}
	if lister.got.Environment != "" {
		t.Fatal("store must not be called when threshold snapshot is unavailable")
	}
}

// TestSummaryDefaultsToToday 钉住默认窗口。
//
// 这两个端点喂的是「今日毛利 / 今日供给成本」那几张卡，默认给 7 天合计
// 会让卡上的数字是卡片标题的七倍——一个不报错的错数字。
func TestSummaryDefaultsToToday(t *testing.T) {
	lister := &fakeSummaryLister{}
	if rec := getSummary(t, lister,
		"/api/v1/finance/channels/summary", "finance.read", ""); rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	today := finance.BusinessDayAt(time.Now().UTC(), finance.DefaultBusinessDayLocation())
	if !lister.got.From.Equal(today) || !lister.got.To.Equal(today) {
		t.Fatalf("默认窗口应只有今天, got %s..%s", lister.got.From, lister.got.To)
	}
}

// TestSummaryRejectsHalfWindow：只给一半区间要报错而不是替调用方补另一半。
//
// 「from=2026-08-01」到底是想要一天还是想要从那天到今天，猜错了就是一张
// 跨度完全不同的图。
func TestSummaryRejectsHalfWindow(t *testing.T) {
	rec := getSummary(t, &fakeSummaryLister{},
		"/api/v1/finance/channels/summary", "finance.read", "?from=2026-08-01")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("只给 from 应 400, got %d", rec.Code)
	}
	rec = getSummary(t, &fakeSummaryLister{},
		"/api/v1/finance/channels/summary", "finance.read",
		"?from=2026-08-01&to=2026-12-31")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("跨度超上限应 400, got %d", rec.Code)
	}
}

// TestChannelSummaryShapeIsHonest 钉住「给不出就给 null」这条贯穿全片的纪律。
//
// 覆盖不全的窗口不能给出金额：偏低的和长得和完整的一模一样。
func TestChannelSummaryShapeIsHonest(t *testing.T) {
	item := meteredSummary()
	lister := &fakeSummaryLister{items: []finance.UpstreamSummary{item}}

	rec := getSummary(t, lister, "/api/v1/finance/channels/summary", "finance.read", "")
	var page struct {
		Items []struct {
			ID               string          `json:"id"`
			Name             string          `json:"name"`
			Metered          bool            `json:"metered"`
			PlatformID       string          `json:"platform_id"`
			CredentialRef    string          `json:"credential_ref"`
			RechargeRatio    string          `json:"recharge_ratio"`
			RechargeCostRate string          `json:"recharge_cost_rate"`
			TokenCount       int             `json:"token_count"`
			UsageRevenue     *map[string]any `json:"usage_revenue"`
			SupplyCost       *map[string]any `json:"supply_cost"`
			GrossProfit      *map[string]any `json:"gross_profit"`
			GrossMargin      *string         `json:"gross_margin"`
			Coverage         struct {
				RowCount int64 `json:"row_count"`
				Complete bool  `json:"complete"`
			} `json:"coverage"`
			Runway struct {
				Days   *int   `json:"days"`
				Reason string `json:"reason"`
			} `json:"runway"`
		} `json:"items"`
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("应有一条, got %d", len(page.Items))
	}
	got := page.Items[0]

	// 空窗口：三个金额与毛利率都必须是 null，不是 0
	if got.UsageRevenue != nil || got.SupplyCost != nil || got.GrossProfit != nil {
		t.Fatal("空窗口给不出金额——那是未知，不是 0")
	}
	if got.GrossMargin != nil {
		t.Fatalf("没有收入时毛利率必须是 null, got %v", *got.GrossMargin)
	}
	if got.Coverage.Complete {
		t.Fatal("空窗口不该标成 complete")
	}
	// 可用天数给不出，但**给得出原因**
	if got.Runway.Days != nil {
		t.Fatal("没有余额时不该给出天数")
	}
	if got.Runway.Reason == "" {
		t.Fatal("给不出天数时必须给得出原因——没有解释的空位会被读成 bug")
	}

	// 后端拼好的展示字段
	if got.Name != "sub2api · https://upstream.example.test" {
		t.Fatalf("渠道名 = %q", got.Name)
	}
	if !got.Metered {
		t.Fatal("upstream_key 应标成计量型")
	}
	if got.PlatformID != "sub2api-prod" {
		t.Fatalf("platform_id = %q", got.PlatformID)
	}
	if got.TokenCount != 3 {
		t.Fatalf("token_count = %d", got.TokenCount)
	}
	// 倍率与充值成本率都是**字符串**：JSON 数字一路解成 double，
	// 1.15 到了页面上就变成 1.1499999999999999（宪法 13 条）
	if got.RechargeRatio != "1.5" {
		t.Fatalf("recharge_ratio = %q", got.RechargeRatio)
	}
	if got.RechargeCostRate != "0.666667" {
		t.Fatalf("recharge_cost_rate = %q, want 0.666667（1/1.5 的展示投影）",
			got.RechargeCostRate)
	}
	// 凭据只出引用（ADR-014、UI 交接 §14.2）
	if got.CredentialRef != summaryCredentialRef {
		t.Fatalf("credential_ref = %q", got.CredentialRef)
	}
	if page.From == "" || page.To == "" {
		t.Fatal("区间要回显——一个没有日期的「今日毛利」是个裸数字")
	}
}

// TestUpstreamSummaryCarriesRunwayCoverage 钉住 §12 拍板要的「标注覆盖率边界」。
//
// 两个真实驱动的余额读取都还没接通（§7），所以这个比值今天多半是 0/N。
// 不显式说出来，看板上就只是一排「—」，看起来像坏了。
func TestUpstreamSummaryCarriesRunwayCoverage(t *testing.T) {
	metered := meteredSummary()
	metered.Runway = finance.Runway{
		Reason: finance.RunwayReasonNoBalance, WindowDays: finance.RunwayWindowDays,
	}
	subscription := meteredSummary()
	subscription.Account.AccessMethod = finance.AccessSubscriptionAccount
	subscription.Account.BaseURL = ""
	subscription.Runway = finance.Runway{Reason: finance.RunwayReasonNotApplicable}

	lister := &fakeSummaryLister{
		items: []finance.UpstreamSummary{metered, subscription},
	}
	rec := getSummary(t, lister, "/api/v1/finance/upstreams/summary", "finance.read", "")
	var page struct {
		Items []struct {
			SupplierKey string `json:"supplier_key"`
			BaseURL     string `json:"base_url"`
		} `json:"items"`
		Coverage struct {
			Total   int            `json:"total"`
			Known   int            `json:"known"`
			Reasons map[string]int `json:"reasons"`
		} `json:"runway_coverage"`
		Thresholds struct {
			CriticalDays int `json:"critical_days"`
			WarningDays  int `json:"warning_days"`
			SeriousDays  int `json:"serious_days"`
		} `json:"runway_thresholds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	// 订阅型不进分母：把「没有这个概念」算成「没覆盖到」，
	// 会让覆盖率随订阅渠道数量下降，而那与采集能力毫无关系
	if page.Coverage.Total != 1 || page.Coverage.Known != 0 {
		t.Fatalf("覆盖率应是 0/1（订阅型不进分母）: %+v", page.Coverage)
	}
	if page.Coverage.Reasons[string(finance.RunwayReasonNoBalance)] != 1 {
		t.Fatalf("原因分布要说得出「为什么是 0/1」: %+v", page.Coverage.Reasons)
	}
	// 阈值原样回报，前端不硬编码
	want := finance.DefaultRunwayThresholds()
	if page.Thresholds.CriticalDays != want.CriticalDays ||
		page.Thresholds.WarningDays != want.WarningDays ||
		page.Thresholds.SeriousDays != want.SeriousDays {
		t.Fatalf("阈值应原样回报: %+v", page.Thresholds)
	}
}

// TestSupplierKeyIsUniquePerAccountToday 钉住一条**会过期的不变量**。
//
// 登记簿的唯一索引 `(environment, system_type, base_url)` 让「一个上游供应商
// 挂多个账号」在有 base_url 的账号上不可能发生，没有 base_url 的账号则各自
// 成一个供应商。所以 /finance/upstreams/summary 一行一个账号——
// 那**就是**按供应商聚合的结果。
//
// 哪天那条唯一索引放宽了，这条用例会红，那时才需要在端点里做真正的分组。
// 用例存在的意义就是让那一天不会被悄悄错过。
func TestSupplierKeyIsUniquePerAccountToday(t *testing.T) {
	withURL := meteredSummary()
	sameURLDifferentSystem := meteredSummary()
	sameURLDifferentSystem.Account.SystemType = finance.SystemNewAPI
	noURL := meteredSummary()
	noURL.Account.BaseURL = ""
	anotherNoURL := meteredSummary()
	anotherNoURL.Account.BaseURL = ""

	seen := map[string]int{}
	for _, item := range []finance.UpstreamSummary{
		withURL, sameURLDifferentSystem, noURL, anotherNoURL,
	} {
		seen[SupplierKeyOf(item.Account)]++
	}
	if len(seen) != 4 {
		t.Fatalf("四个账号应给出四个不同的供应商键（今天供应商≡账号）: %+v", seen)
	}
	// 同一个账号两次必须给出同一个键——否则前端的 groupBy 会把它拆开
	if SupplierKeyOf(withURL.Account) != SupplierKeyOf(withURL.Account) {
		t.Fatal("供应商键必须稳定")
	}
}

// TestGroupRateOmittedWhenUnset 钉住「没配就不出这个字段」（XM-0049）。
//
// 分组倍率对绝大多数渠道本就不存在。出一个空串会让前端多写一次
// 「这个空串是什么意思」的判断，而那个判断迟早有一处会写成
// 「空串当 1」——那正是 §10.2 禁止的重复乘算。
func TestGroupRateOmittedWhenUnset(t *testing.T) {
	item := meteredSummary() // 没配分组倍率
	lister := &fakeSummaryLister{items: []finance.UpstreamSummary{item}}

	// 两个端点各自恒出的那个倍率字段（渠道项出规范存储量，上游项出展示投影）
	alwaysPresent := map[string]string{
		"/api/v1/finance/channels/summary":  "recharge_ratio",
		"/api/v1/finance/upstreams/summary": "recharge_cost_rate",
	}
	for path, ratioField := range alwaysPresent {
		rec := getSummary(t, lister, path, "finance.read", "")
		if strings.Contains(rec.Body.String(), "group_rate") {
			t.Fatalf("%s 未配分组倍率时不该出这个字段: %s", path, rec.Body.String())
		}
		// 对照：充值侧的倍率字段**恒出**——空串本身就是「这条渠道没有倍率」
		// 的信息，而分组倍率对多数渠道本就不存在，缺席才是它的正常状态
		if !strings.Contains(rec.Body.String(), ratioField) {
			t.Fatalf("%s 应恒出 %s", path, ratioField)
		}
	}
}

// TestGroupRatePresentWhenSet：配了就原样出，且是**字符串**。
//
// JSON 数字一路解成 double，1.15 到了页面上就变成 1.1499999999999999
// （宪法 13 条：比例用 Decimal）。
func TestGroupRatePresentWhenSet(t *testing.T) {
	item := meteredSummary()
	item.Account.GroupRate = money.MustParseRatio("1.15")
	lister := &fakeSummaryLister{items: []finance.UpstreamSummary{item}}

	rec := getSummary(t, lister, "/api/v1/finance/channels/summary", "finance.read", "")
	var page struct {
		Items []struct {
			GroupRate     string `json:"group_rate"`
			RechargeRatio string `json:"recharge_ratio"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if page.Items[0].GroupRate != "1.15" {
		t.Fatalf("group_rate = %q, want 1.15", page.Items[0].GroupRate)
	}
	// 与充值倍率是两个独立的量，互不影响
	if page.Items[0].RechargeRatio != "1.5" {
		t.Fatalf("recharge_ratio 不该被分组倍率动过: %q", page.Items[0].RechargeRatio)
	}
}

// TestRunwayThresholdsComeFromAssembly 钉住「告警与看板共用一份阈值」在
// HTTP 这一侧的落点：端点回报的是**装配层注入的那份**，不是就地取的默认。
//
// 就地取默认的话，platform-worker 按环境变量判档、platform-api 按默认值回报，
// 「看板说还有 11 天」与「告警说已经低于阈值」会同时出现在一个人面前。
func TestRunwayThresholdsComeFromAssembly(t *testing.T) {
	custom, err := finance.ParseRunwayThresholds("30", "15")
	if err != nil {
		t.Fatalf("解析阈值: %v", err)
	}
	lister := &fakeSummaryLister{}
	rec := getSummaryWith(t, lister, custom,
		"/api/v1/finance/upstreams/summary", "finance.read", "")

	var page struct {
		Thresholds struct {
			CriticalDays int `json:"critical_days"`
			WarningDays  int `json:"warning_days"`
			SeriousDays  int `json:"serious_days"`
		} `json:"runway_thresholds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if page.Thresholds.WarningDays != 30 || page.Thresholds.CriticalDays != 15 {
		t.Fatalf("端点应回报注入的阈值, got %+v", page.Thresholds)
	}
	// 仓储也要收到同一份——否则 Level 与回报的档会分叉
	if lister.got.Thresholds != custom {
		t.Fatalf("传给仓储的阈值 = %+v, want %+v", lister.got.Thresholds, custom)
	}
}
