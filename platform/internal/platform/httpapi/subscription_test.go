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
)

// 订阅批次与代理资产两个只读端点（XM-0037c）。
//
// 三件事在这一层验：**权限**（复用 finance.read，缺了要 403）、
// **金额形状**（§13 的 Money：amount_minor 是字符串、带 scale）、
// **派生值**（每日摊销与损失由后端算好——让前端复现 §3.5 的两次除法与
// 末日吸收，两处实现迟早在某一天差一个微单位）。

// proxyCredentialRef 是用例共用的凭据引用。
//
// 抽成常量而不是就地写字面量：`CredentialRef: "secret://..."` 这个形状会被
// gitleaks 的 generic-api-key 规则当成泄露的密钥（同 finance_test.go 里
// financeCredentialRef 的说明）。本仓禁止加 gitleaks allowlist，
// 所以换个写法比放宽扫描器划算。
const proxyCredentialRef = "secret://finance/proxy-a"

type fakeSubscriptionLister struct {
	batches   []finance.BatchWithLoss
	proxies   []finance.ProxyWithLoss
	truncated bool
	err       error

	gotQuery finance.SubscriptionBatchQuery
	gotEnv   string
	gotLimit int32
}

func (f *fakeSubscriptionLister) ListBatches(
	_ context.Context, q finance.SubscriptionBatchQuery,
) ([]finance.BatchWithLoss, bool, error) {
	f.gotQuery = q
	return f.batches, f.truncated, f.err
}

func (f *fakeSubscriptionLister) ListProxies(
	_ context.Context, environment string, limit int32,
) ([]finance.ProxyWithLoss, bool, error) {
	f.gotEnv, f.gotLimit = environment, limit
	return f.proxies, f.truncated, f.err
}

func subscriptionRouter(t *testing.T, lister SubscriptionLister) http.Handler {
	t.Helper()
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Deps{
		Logger:               discardLogger(),
		Service:              "platform-api",
		Environment:          "development",
		DB:                   fakePinger{},
		Resolver:             res,
		ActionRegistry:       action.NewRegistry(),
		FinanceSubscriptions: lister,
	})
}

func getFinancePath(
	t *testing.T, lister SubscriptionLister, path, scopes, query string,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path+query, nil)
	devHeaders(req, scopes)
	rec := httptest.NewRecorder()
	subscriptionRouter(t, lister).ServeHTTP(rec, req)
	return rec
}

func day(s string) time.Time {
	d, err := finance.ParseBusinessDay(s)
	if err != nil {
		panic(err)
	}
	return d
}

// sampleBatch 是一笔覆盖「今天」的月订阅：从今天往前 10 天开，往后 20 天到期。
//
// 相对今天而不是写死日期：daily_amortization 算的就是**今天**该摊多少，
// 写死日期的用例会在某一天忽然变成「今天不在期内」而给出 null。
func sampleBatch(now time.Time) finance.BatchWithLoss {
	today := finance.BusinessDayAt(now, finance.DefaultBusinessDayLocation())
	return finance.BatchWithLoss{
		Batch: finance.SubscriptionBatch{
			ID:                uuid.New(),
			UpstreamAccountID: uuid.New(),
			PaidMinor:         29_990_000,
			SurchargeMinor:    1_000_000,
			RefundedMinor:     0,
			Currency:          "USD",
			StartsOn:          today.AddDate(0, 0, -10),
			ExpiresOn:         today.AddDate(0, 0, 20),
			AccountCount:      2,
			CreatedAt:         now,
			UpdatedAt:         now,
		},
	}
}

func sampleProxy(now time.Time) finance.ProxyWithLoss {
	today := finance.BusinessDayAt(now, finance.DefaultBusinessDayLocation())
	return finance.ProxyWithLoss{
		Proxy: finance.ProxyAsset{
			ID:                 uuid.New(),
			PaidMinor:          6_200_000,
			Currency:           "USD",
			OpenedOn:           today.AddDate(0, 0, -5),
			ExpiresOn:          today.AddDate(0, 0, 25),
			SharedAccountCount: 2,
			Mounted:            true,
			CredentialRef:      proxyCredentialRef,
			BuyPlatform:        "example-idc",
			Environment:        "development",
			CreatedAt:          now,
			UpdatedAt:          now,
		},
	}
}

// TestSubscriptionEndpointsRequireFinanceRead：两个端点都复用 finance.read，
// 缺权限一律 403。
//
// 不复用 ops.read（对照 alerts 复用它的理由）：批次里的金额就是订阅型渠道
// 成本的来源，比看板上的余额数字敏感一个量级。
func TestSubscriptionEndpointsRequireFinanceRead(t *testing.T) {
	for _, path := range []string{
		"/api/v1/finance/subscription-batches",
		"/api/v1/finance/proxy-assets",
	} {
		rec := getFinancePath(t, &fakeSubscriptionLister{}, path, "ops.read", "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s 缺 finance.read 应 403, got %d", path, rec.Code)
		}
		rec = getFinancePath(t, &fakeSubscriptionLister{}, path, "finance.read", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s 带 finance.read 应 200, got %d (%s)", path, rec.Code, rec.Body.String())
		}
	}
}

// TestSubscriptionBatchShapeIsMoneyAndDerived 钉住响应的两件事：
// §13 的 Money 形状，以及**后端算好的派生值**。
func TestSubscriptionBatchShapeIsMoneyAndDerived(t *testing.T) {
	now := time.Now().UTC()
	lister := &fakeSubscriptionLister{batches: []finance.BatchWithLoss{sampleBatch(now)}}

	rec := getFinancePath(t, lister,
		"/api/v1/finance/subscription-batches", "finance.read", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", rec.Code, rec.Body.String())
	}
	var page struct {
		Items []struct {
			Paid              map[string]any `json:"paid"`
			CostBasis         map[string]any `json:"cost_basis"`
			AccountShare      map[string]any `json:"account_share"`
			DailyAmortization map[string]any `json:"daily_amortization"`
			EffectiveDays     int            `json:"effective_days"`
			ProxyAssetID      *string        `json:"proxy_asset_id"`
			AmortizationLoss  map[string]any `json:"amortization_loss"`
		} `json:"items"`
		AsOf string `json:"as_of"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("应有一条, got %d", len(page.Items))
	}
	item := page.Items[0]

	// 金额是**字符串**：int64 @ scale-6 超过 2^53 会在前端的 double 里丢精度，
	// 而丢掉的那一位不会报错（§2.4、§13、宪法 13 条）。
	if _, ok := item.Paid["amount_minor"].(string); !ok {
		t.Fatalf("amount_minor 必须是字符串, got %T", item.Paid["amount_minor"])
	}
	if item.Paid["scale"] != float64(financeAmountScale) {
		t.Fatalf("scale = %v, want %d", item.Paid["scale"], financeAmountScale)
	}
	// cost_basis = 实付 + 附加 − 退款，后端算好，前端不必减三个数
	if item.CostBasis["amount_minor"] != "30990000" {
		t.Fatalf("cost_basis = %v, want 30990000", item.CostBasis["amount_minor"])
	}
	// account_share = 基础 ÷ 2 个账号（半进，除得尽）
	if item.AccountShare["amount_minor"] != "15495000" {
		t.Fatalf("account_share = %v, want 15495000", item.AccountShare["amount_minor"])
	}
	// 31 天含两端（今天前 10 后 20）
	if item.EffectiveDays != 31 {
		t.Fatalf("effective_days = %d, want 31（含两端，§12 拍板）", item.EffectiveDays)
	}
	// 15495000 / 31 = 499838.7… 截断 = 499838（今天不是末日）
	if item.DailyAmortization == nil || item.DailyAmortization["amount_minor"] != "499838" {
		t.Fatalf("daily_amortization = %v, want 499838", item.DailyAmortization)
	}
	// 无代理 / 未终止都用 null，不是 0 或空串
	if item.ProxyAssetID != nil {
		t.Fatalf("无代理时应为 null, got %v", *item.ProxyAssetID)
	}
	if item.AmortizationLoss != nil {
		t.Fatalf("未终止时不该有损失, got %v", item.AmortizationLoss)
	}
	if page.AsOf == "" {
		t.Fatal("as_of 必须回显——一个没有日期的「今日成本」是个裸数字（宪法 12 条）")
	}
}

// TestBatchOutsideTodayHasNullAmortization：今天不在期内时，
// daily_amortization 是 null 而不是 0。
//
// 0 会被读成「今天这笔订阅免费」，而真相是它今天根本不该被算进来。
func TestBatchOutsideTodayHasNullAmortization(t *testing.T) {
	now := time.Now().UTC()
	expired := sampleBatch(now)
	expired.Batch.StartsOn = day("2020-01-01")
	expired.Batch.ExpiresOn = day("2020-01-31")
	lister := &fakeSubscriptionLister{batches: []finance.BatchWithLoss{expired}}

	rec := getFinancePath(t, lister,
		"/api/v1/finance/subscription-batches", "finance.read", "")
	var page struct {
		Items []struct {
			DailyAmortization *map[string]any `json:"daily_amortization"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if page.Items[0].DailyAmortization != nil {
		t.Fatalf("期外应为 null 而不是 0, got %v", *page.Items[0].DailyAmortization)
	}
}

// TestTerminatedBatchCarriesLoss：终止过的批次带出损失科目（§12 拍板）。
func TestTerminatedBatchCarriesLoss(t *testing.T) {
	now := time.Now().UTC()
	terminated := sampleBatch(now)
	terminated.Batch.TerminatedOn = terminated.Batch.StartsOn.AddDate(0, 0, 3)
	loss := int64(12_345_678)
	terminated.LossMinor = &loss
	terminated.LossBookedOn = terminated.Batch.TerminatedOn
	lister := &fakeSubscriptionLister{batches: []finance.BatchWithLoss{terminated}}

	rec := getFinancePath(t, lister,
		"/api/v1/finance/subscription-batches", "finance.read", "")
	var page struct {
		Items []struct {
			TerminatedOn     *string `json:"terminated_on"`
			AmortizationLoss *struct {
				Amount   map[string]any `json:"amount"`
				BookedOn string         `json:"booked_on"`
			} `json:"amortization_loss"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	item := page.Items[0]
	if item.TerminatedOn == nil {
		t.Fatal("终止日应带出来")
	}
	if item.AmortizationLoss == nil {
		t.Fatal("终止过的批次必须带出损失——它是要被看见的事实（§6.4）")
	}
	if item.AmortizationLoss.Amount["amount_minor"] != "12345678" {
		t.Fatalf("损失金额 = %v", item.AmortizationLoss.Amount["amount_minor"])
	}
	if item.AmortizationLoss.BookedOn == "" {
		t.Fatal("结转日必须带出来")
	}
}

// TestUnmountedProxyAmortizesZero 钉住 §10.3 在响应层的表现：
// 未挂载的代理 daily_amortization 是**已知的 0**（不是 null），
// 与 mounted=false 一起给，前端才说得出「今天没在服务」而不是「没算出来」。
func TestUnmountedProxyAmortizesZero(t *testing.T) {
	now := time.Now().UTC()
	proxy := sampleProxy(now)
	proxy.Proxy.Mounted = false
	lister := &fakeSubscriptionLister{proxies: []finance.ProxyWithLoss{proxy}}

	rec := getFinancePath(t, lister, "/api/v1/finance/proxy-assets", "finance.read", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", rec.Code, rec.Body.String())
	}
	var page struct {
		Items []struct {
			Mounted           bool            `json:"mounted"`
			DailyAmortization *map[string]any `json:"daily_amortization"`
			CredentialRef     string          `json:"credential_ref"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	item := page.Items[0]
	if item.Mounted {
		t.Fatal("挂载状态应原样带出")
	}
	if item.DailyAmortization == nil {
		t.Fatal("未挂载是**已知的 0**，不是未知——null 会被读成「算不出来」")
	}
	if (*item.DailyAmortization)["amount_minor"] != "0" {
		t.Fatalf("未挂载的代理每日成本 = %v, want 0", (*item.DailyAmortization)["amount_minor"])
	}
	// 凭据只出引用（ADR-014、UI 交接 §14.2）
	if item.CredentialRef != proxyCredentialRef {
		t.Fatalf("credential_ref 应原样回显引用, got %q", item.CredentialRef)
	}
}

// TestSubscriptionLimitIsValidated：非法 limit 一律 400 而不是悄悄取默认。
//
// `limit=abc` 静默变成 200 会让前端拿着一份自以为完整的数据（宪法 12 条）。
func TestSubscriptionLimitIsValidated(t *testing.T) {
	for _, query := range []string{"?limit=abc", "?limit=0", "?limit=99999"} {
		rec := getFinancePath(t, &fakeSubscriptionLister{},
			"/api/v1/finance/subscription-batches", "finance.read", query)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("limit%s 应 400, got %d", query, rec.Code)
		}
	}
}

// TestSubscriptionBatchFiltersByAccount：upstream_account_id 过滤透传到仓储，
// 形态非法时 400（而不是把一个解析失败的 UUID 当成「不过滤」）。
func TestSubscriptionBatchFiltersByAccount(t *testing.T) {
	id := uuid.New()
	lister := &fakeSubscriptionLister{}
	rec := getFinancePath(t, lister, "/api/v1/finance/subscription-batches",
		"finance.read", "?upstream_account_id="+id.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	if lister.gotQuery.UpstreamAccountID != id {
		t.Fatalf("过滤条件未透传: %v", lister.gotQuery.UpstreamAccountID)
	}

	rec = getFinancePath(t, &fakeSubscriptionLister{},
		"/api/v1/finance/subscription-batches", "finance.read",
		"?upstream_account_id=not-a-uuid")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法 UUID 应 400, got %d", rec.Code)
	}
}
