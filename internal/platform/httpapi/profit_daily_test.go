package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

type fakeProfitLister struct {
	got       finance.ProfitQuery
	rows      []finance.ProfitRow
	truncated bool
	err       error
}

func (f *fakeProfitLister) ListRows(
	_ context.Context, q finance.ProfitQuery,
) ([]finance.ProfitRow, bool, error) {
	f.got = q
	return f.rows, f.truncated, f.err
}

func profitRouter(t *testing.T, lister ProfitDailyLister) http.Handler {
	t.Helper()
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger:         discardLogger(),
		Service:        "platform-api",
		Environment:    "development",
		DB:             fakePinger{},
		Resolver:       res,
		Kernel:         nil,
		ActionRegistry: action.NewRegistry(),
		FinanceProfit:  lister,
	})
}

func getProfitDaily(
	t *testing.T, lister ProfitDailyLister, scopes, query string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/finance/profit-daily"+query, nil)
	devHeaders(req, scopes)
	rec := httptest.NewRecorder()
	profitRouter(t, lister).ServeHTTP(rec, req)
	return rec
}

func minorPtr(v int64) *int64 { return &v }

func sampleProfitRow() finance.ProfitRow {
	observed := time.Date(2026, 8, 28, 5, 30, 0, 0, time.UTC)
	day, _ := finance.ParseBusinessDay("2026-08-28")
	return finance.ProfitRow{
		UpstreamAccountID: uuid.New(),
		BusinessDay:       day,
		BusinessDayTZ:     "+08:00",
		TokenID:           "tok-1",
		AccountID:         "258",
		PlatformID:        "sub2api-prod",
		RevenueMinor:      minorPtr(12_345_600),
		CostMinor:         minorPtr(3_875_819),
		Currency:          "USD",
		RatioSnapshot:     money.MustParseRatio("1.15"),
		Source:            "finance-collect-staging",
		CostObservedAt:    &observed,
		RevenueObservedAt: &observed,
		UpdatedAt:         observed,
	}
}

func decodeProfitPage(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析响应失败: %v; body=%s", err, rec.Body.String())
	}
	return body
}

// TestListProfitDailyReturnsMoneyAsStrings 钉住 UI 交接 §13 的 Money 形状：
// 金额是**字符串**，不是 JSON 数字。
//
// int64 @ scale-6 超过 2^53 就会在前端的 double 里丢精度，而丢精度的那一位
// 不会报错（§2.4、宪法 13 条）。同时验毛利是**服务端算好的减法**，
// 不让每个前端各减一遍。
func TestListProfitDailyReturnsMoneyAsStrings(t *testing.T) {
	row := sampleProfitRow()
	lister := &fakeProfitLister{rows: []finance.ProfitRow{row}}

	rec := getProfitDaily(t, lister, finance.ScopeRead, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := decodeProfitPage(t, rec)
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("应返回 1 行, got %d", len(items))
	}
	item, _ := items[0].(map[string]any)

	revenue, _ := item["usage_revenue"].(map[string]any)
	if revenue["amount_minor"] != "12345600" {
		t.Fatalf("收入应是字符串大整数, got %#v", revenue["amount_minor"])
	}
	if revenue["currency"] != "USD" {
		t.Fatalf("每个金额必带币种（§10.1）, got %#v", revenue["currency"])
	}
	if revenue["scale"] != float64(money.MicroScale) {
		t.Fatalf("金额必须自带标度, got %#v", revenue["scale"])
	}
	cost, _ := item["supply_cost"].(map[string]any)
	if cost["amount_minor"] != "3875819" {
		t.Fatalf("成本 = %#v", cost["amount_minor"])
	}
	profit, _ := item["gross_profit"].(map[string]any)
	if profit["amount_minor"] != "8469781" {
		t.Fatalf("毛利应由服务端算好, got %#v", profit["amount_minor"])
	}

	// 倍率也是定点字符串：1.15 一路解成 double 会变成 1.1499999999999999
	if item["ratio_snapshot"] != "1.15" {
		t.Fatalf("ratio_snapshot 应是定点字符串, got %#v", item["ratio_snapshot"])
	}
}

// TestListProfitDailyRendersUnknownAsNull 是本端点最重要的一条：
// 「未知」与「已知的 0」在响应里也必须是两件事（§5.1）。
//
// 把未知渲染成 {"amount_minor":"0"} 会让页面显示一个笃定的 $0.00，
// 而真相是我们那天没读到数（宪法 12 条）。
func TestListProfitDailyRendersUnknownAsNull(t *testing.T) {
	unknown := sampleProfitRow()
	unknown.CostMinor = nil
	unknown.CostObservedAt = nil

	knownZero := sampleProfitRow()
	knownZero.TokenID = "tok-2"
	knownZero.CostMinor = minorPtr(0)

	lister := &fakeProfitLister{rows: []finance.ProfitRow{unknown, knownZero}}
	rec := getProfitDaily(t, lister, finance.ScopeRead, "")
	body := decodeProfitPage(t, rec)
	items, _ := body["items"].([]any)

	first, _ := items[0].(map[string]any)
	if first["supply_cost"] != nil {
		t.Fatalf("未知成本必须是 null, got %#v", first["supply_cost"])
	}
	if first["gross_profit"] != nil {
		t.Fatalf("缺一侧时毛利必须是 null（不是「等于另一侧」）, got %#v", first["gross_profit"])
	}
	if first["cost_observed_at"] != nil {
		t.Fatalf("未知的金额不该带观测时刻, got %#v", first["cost_observed_at"])
	}

	second, _ := items[1].(map[string]any)
	cost, _ := second["supply_cost"].(map[string]any)
	if cost == nil || cost["amount_minor"] != "0" {
		t.Fatalf("已知的 0 必须是 0 而不是 null, got %#v", second["supply_cost"])
	}
}

// TestListProfitDailyExposesFreshness：宪法 12 条禁止裸数字——
// 每一行都要说得出「谁读的、什么时候读的」。
//
// 两侧各有一个观测时刻：它们读的是不同上游、在不同时刻、可以各自失败。
func TestListProfitDailyExposesFreshness(t *testing.T) {
	row := sampleProfitRow()
	costObserved := time.Date(2026, 8, 28, 5, 0, 0, 0, time.UTC)
	row.CostObservedAt = &costObserved
	lister := &fakeProfitLister{rows: []finance.ProfitRow{row}}

	body := decodeProfitPage(t, getProfitDaily(t, lister, finance.ScopeRead, ""))
	items, _ := body["items"].([]any)
	item, _ := items[0].(map[string]any)

	if item["source"] != "finance-collect-staging" {
		t.Fatalf("必须回显来源, got %#v", item["source"])
	}
	if item["cost_observed_at"] != "2026-08-28T05:00:00Z" {
		t.Fatalf("成本观测时刻 = %#v", item["cost_observed_at"])
	}
	if item["revenue_observed_at"] != "2026-08-28T05:30:00Z" {
		t.Fatalf("收入观测时刻应与成本分开, got %#v", item["revenue_observed_at"])
	}
	// 业务日是**日历日**，不是带时区的时刻——渲染成 RFC3339 会让前端按浏览器
	// 时区再解释一次，跨零点的用户看到的就是前一天（宪法 14 条）
	if item["business_day"] != "2026-08-28" {
		t.Fatalf("业务日 = %#v, want 2026-08-28", item["business_day"])
	}
	if item["business_day_tz"] != "+08:00" {
		t.Fatalf("切日偏移必须逐行回显, got %#v", item["business_day_tz"])
	}
}

// TestListProfitDailyReportsTruncation：被悄悄截断的区间会被读成
// 「这几天真的没有数据」（宪法 12 条）。
func TestListProfitDailyReportsTruncation(t *testing.T) {
	lister := &fakeProfitLister{rows: []finance.ProfitRow{sampleProfitRow()}, truncated: true}
	body := decodeProfitPage(t, getProfitDaily(t, lister, finance.ScopeRead, "?limit=1"))
	if body["truncated"] != true {
		t.Fatalf("truncated = %#v, want true", body["truncated"])
	}
	if body["limit"] != float64(1) {
		t.Fatalf("limit 必须回显，让 truncated 可解释, got %#v", body["limit"])
	}
}

// TestListProfitDailyDefaultWindowIsSevenBusinessDays：不传日期时的窗口
// 按 CST 固定 +08:00 算（★口径常量 §4），并回显实际生效的区间。
func TestListProfitDailyDefaultWindowIsSevenBusinessDays(t *testing.T) {
	lister := &fakeProfitLister{}
	body := decodeProfitPage(t, getProfitDaily(t, lister, finance.ScopeRead, ""))

	from, to := body["from"].(string), body["to"].(string)
	if from == "" || to == "" {
		t.Fatalf("必须回显实际生效的区间, got from=%q to=%q", from, to)
	}
	fromDay, err := finance.ParseBusinessDay(from)
	if err != nil {
		t.Fatalf("from 应是合法业务日: %v", err)
	}
	toDay, err := finance.ParseBusinessDay(to)
	if err != nil {
		t.Fatalf("to 应是合法业务日: %v", err)
	}
	if days := int(toDay.Sub(fromDay).Hours()/24) + 1; days != 7 {
		t.Fatalf("默认窗口 = %d 天, want 7", days)
	}
	// 「今天」按 CST 算，不按服务器本地时区
	wantToday := finance.BusinessDayAt(time.Now().UTC(), finance.DefaultBusinessDayLocation())
	if !toDay.Equal(wantToday) {
		t.Fatalf("窗口右端 = %s, want %s（CST 今天）",
			to, wantToday.Format(finance.ProfitBusinessDayLayout))
	}
	if !lister.got.From.Equal(fromDay) || !lister.got.To.Equal(toDay) {
		t.Fatalf("传给仓储的区间应与回显一致: %+v", lister.got)
	}
}

// TestListProfitDailyRejectsBadParams：非法参数一律 400 而不是悄悄取默认——
// 静默取默认会让前端拿着一张跨度完全不同的图。
func TestListProfitDailyRejectsBadParams(t *testing.T) {
	cases := map[string]string{
		"只给 from":      "?from=2026-08-01",
		"只给 to":        "?to=2026-08-01",
		"from 格式不严格":   "?from=2026-8-1&to=2026-08-28",
		"起止颠倒":         "?from=2026-08-28&to=2026-08-01",
		"跨度超上限":        "?from=2025-08-01&to=2026-08-28",
		"limit 不是整数":   "?limit=abc",
		"limit 为 0":    "?limit=0",
		"limit 超上限":    "?limit=100000",
		"platform 形态错": "?platform_id=Bad%20Platform",
	}
	for name, query := range cases {
		lister := &fakeProfitLister{}
		if name == "platform 形态错" {
			// 平台形态由领域层判（与写入路径同一条正则），仓储会返回它
			lister.err = finance.ErrInvalidFormat
		}
		rec := getProfitDaily(t, lister, finance.ScopeRead, query)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s 应为 400, got %d body=%s", name, rec.Code, rec.Body.String())
		}
	}
}

// TestListProfitDailyRequiresScope：读也要权限（规格 §2.4）。
//
// 复用 finance.read 而不是另立 scope：台账里的毛利就是「倍率 × 用量」的
// 结果，能看登记簿里那个倍率的人已经能推出毛利的量级。
func TestListProfitDailyRequiresScope(t *testing.T) {
	lister := &fakeProfitLister{rows: []finance.ProfitRow{sampleProfitRow()}}
	if rec := getProfitDaily(t, lister, "ops.read", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("没有 finance.read 应为 403, got %d", rec.Code)
	}
	if rec := getProfitDaily(t, lister, finance.ScopeRead, ""); rec.Code != http.StatusOK {
		t.Fatalf("有 finance.read 应放行, got %d", rec.Code)
	}
}

// TestListProfitDailyRefusesCrossEnvironment：不默认生产，也不允许跨环境读取
// （宪法 15 条）。
func TestListProfitDailyRefusesCrossEnvironment(t *testing.T) {
	lister := &fakeProfitLister{}
	rec := getProfitDaily(t, lister, finance.ScopeRead, "?environment=production")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("跨环境读取应为 403, got %d body=%s", rec.Code, rec.Body.String())
	}

	// 不传时用调用者自己的环境
	if rec := getProfitDaily(t, lister, finance.ScopeRead, ""); rec.Code != http.StatusOK {
		t.Fatalf("同环境应放行, got %d", rec.Code)
	}
	if lister.got.Environment != "development" {
		t.Fatalf("环境应取自调用者身份, got %q", lister.got.Environment)
	}
}

// TestListProfitDailyPassesPlatformFilter：平台过滤要真的传到仓储。
func TestListProfitDailyPassesPlatformFilter(t *testing.T) {
	lister := &fakeProfitLister{}
	if rec := getProfitDaily(t, lister, finance.ScopeRead,
		"?platform_id=sub2api-prod"); rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d body=%s", rec.Code, rec.Body.String())
	}
	if lister.got.PlatformID != "sub2api-prod" {
		t.Fatalf("平台过滤没传到仓储, got %q", lister.got.PlatformID)
	}
}

// TestListProfitDailyHidesInternalErrors：非 Action 错误归 INTERNAL 并隐藏细节。
func TestListProfitDailyHidesInternalErrors(t *testing.T) {
	lister := &fakeProfitLister{err: errors.New("库连接串是 postgres://user:pw@host/db")}
	rec := getProfitDaily(t, lister, finance.ScopeRead, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); contains(body, "postgres://") {
		t.Fatalf("内部错误细节泄漏进响应: %s", body)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
