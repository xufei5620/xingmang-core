package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func (f *fakeSummaryLister) UpstreamSummaries(
	_ context.Context, q finance.SummaryQuery,
) ([]finance.UpstreamSummary, error) {
	f.got = q
	return f.items, f.err
}

func summaryRouter(t *testing.T, lister FinanceSummaryLister) http.Handler {
	t.Helper()
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger:           discardLogger(),
		Service:          "platform-api",
		Environment:      "development",
		DB:               fakePinger{},
		Resolver:         res,
		ActionRegistry:   action.NewRegistry(),
		FinanceSummaries: lister,
	})
}

func getSummary(
	t *testing.T, lister FinanceSummaryLister, path, scopes, query string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path+query, nil)
	devHeaders(req, scopes)
	rec := httptest.NewRecorder()
	summaryRouter(t, lister).ServeHTTP(rec, req)
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
