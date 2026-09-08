package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// finance.domainError 的两条新映射，从 HTTP 端点打进来验（XM-READONLY-QUERIES）。
//
// 为什么必须走真链路：「规则」和「调用它的地方」是两段代码。
// domainError 在 finance/actions.go，而它要生效得穿过
//
//	仓储返回域错误 → Action Handler 调 domainError → 内核保留 Code/Message
//	→ StatusForCode 映射状态码 → safeMessage 输出文案
//
// 五段里任何一段断掉，调用方拿到的就还是那句「执行失败」。只在 finance 包里
// 断言 domainError 的返回值证明不了这条链——内核曾经就是在中间把它改写掉的
// （XM-KERNEL-ERRCODE0），而那时包内单测全绿。
//
// 用真库而不是假仓储，是因为这两个错误只有仓储读到**当前状态**才产生得出来
// （退款额低于已登记值、批次已经终止过），假仓储里的「已登记值」是编的。

func financeErrPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("XM_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 XM_TEST_DATABASE_URL，跳过集成测试")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("连接测试库失败: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping 失败: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx,
		"TRUNCATE finance.balance_history, finance.amortization_loss, "+
			"finance.subscription_cost_batch, finance.proxy_asset, "+
			"finance.profit_daily, finance.token_map, "+
			"finance.platform_channel_binding, finance.upstream_account"); err != nil {
		t.Fatalf("清空财务表失败: %v", err)
	}
	return pool
}

// financeErrFixture 造「一个订阅型账号 + 一笔批次」，并返回装好真内核的路由。
//
// 身份是 development（NewDevHeaderResolver），所以账号也建在 development
// ——否则被 resolveBatch 的跨环境闸门先拦下，测到的就不是 domainError 了。
func financeErrFixture(t *testing.T) (http.Handler, finance.SubscriptionBatch) {
	t.Helper()
	pool := financeErrPool(t)
	ctx := context.Background()

	accounts := finance.NewStore(pool)
	account, err := accounts.CreateAccount(ctx, finance.UpstreamAccount{
		ID:            uuid.New(),
		SystemType:    finance.SystemOfficial,
		AccessMethod:  finance.AccessSubscriptionAccount,
		CredentialRef: "secret://finance-test/subscription-a",
		Currency:      finance.DefaultCurrency,
		BusinessDayTZ: finance.DefaultBusinessDayTZ,
		Status:        finance.StatusActive,
		Environment:   "development",
		PlatformID:    "solo-dev",
		RechargeRatio: money.Ratio{},
	})
	if err != nil {
		t.Fatalf("登记订阅型账号: %v", err)
	}

	subs := finance.NewSubscriptionStore(pool)
	batch, err := subs.CreateBatch(ctx, finance.SubscriptionBatch{
		UpstreamAccountID: account.ID,
		PaidMinor:         29_990_000,
		Currency:          finance.DefaultCurrency,
		StartsOn:          day("2026-08-01"),
		ExpiresOn:         day("2026-08-31"),
		AccountCount:      2,
	})
	if err != nil {
		t.Fatalf("登记批次: %v", err)
	}

	reg := action.NewRegistry()
	if err := finance.RegisterSubscriptionActions(reg, subs, accounts); err != nil {
		t.Fatalf("注册订阅 Action: %v", err)
	}
	kernel := action.NewKernel(reg, &boundaryRunStore{}, action.WithLogger(discardLogger()))

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
		Kernel:         kernel,
		ActionRegistry: reg,
	}), batch
}

func execFinance(t *testing.T, h http.Handler, actionID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, executePath(actionID, "1"), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-finance-domain")
	devHeaders(req, finance.ScopeSubscriptionManage)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestRefundNotDecreasingReachesHTTPAsInvalidParams：调低累计退款额时，
// 调用方拿到的是 400 + 一句说得清该怎么办的话，而不是 502「执行失败」。
//
// 先跑一次**成功**的退款登记（正向锚点）再跑那次会被拒的：没有前一步的话，
// 「拿到了 400」有可能只是因为参数根本没解析成功，而那证明不了任何映射。
func TestRefundNotDecreasingReachesHTTPAsInvalidParams(t *testing.T) {
	h, batch := financeErrFixture(t)

	ok := execFinance(t, h, finance.ActionSubscriptionBatchRefund, fmt.Sprintf(
		`{"params":{"subscription_batch_id":%q,"refunded_minor":"8000000",`+
			`"refunded_on":"2026-08-20","reason":"上游先退了一部分"}}`, batch.ID))
	if ok.Code != http.StatusOK {
		t.Fatalf("第一次退款登记应成功，实际 %d：%s", ok.Code, ok.Body.String())
	}

	rec := execFinance(t, h, finance.ActionSubscriptionBatchRefund, fmt.Sprintf(
		`{"params":{"subscription_batch_id":%q,"refunded_minor":"5000000",`+
			`"refunded_on":"2026-08-21","reason":"手滑填成了本次新增"}}`, batch.ID))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("调低累计退款额应 400，实际 %d：%s", rec.Code, rec.Body.String())
	}
	got := decodeError(t, rec)
	if got.Code != "INVALID_PARAMS" {
		t.Fatalf("错误码 = %q，期望 INVALID_PARAMS（落兜底时会是 EXECUTION_FAILED）", got.Code)
	}
	// 文案逐字。只断言错误码的话，一句「执行失败」配 400 也会通过，
	// 而调用方要的正是这句话本身。
	const want = "累计退款额只增不减：这一格填的是累计总额，不是本次新增，必须不低于已登记的累计退款额。"
	if got.Message != want {
		t.Fatalf("文案 = %q\n期望 = %q", got.Message, want)
	}
	// 仓储包进错误里的 scale-6 微单位不该出现在对外文案里——那串数字会让
	// 填表的人以为自己少填了三个零。
	for _, leak := range []string{"5000000", "8000000", "finance:"} {
		if strings.Contains(got.Message, leak) {
			t.Fatalf("对外文案里出现了不该有的 %q：%q", leak, got.Message)
		}
	}
	if got.RequestID != "req-finance-domain" {
		t.Fatalf("request_id 没回来：%q", got.RequestID)
	}
}

// TestAlreadyTerminatedReachesHTTPAsConflict：第二次终止拿 409。
//
// 归 CONFLICT 而不是 INVALID_PARAMS 是刻意的：换任何一个终止日都还是这个
// 答案，它与审批中心的「审批单已经执行过」同一个形状。顺带的要紧后果是
// **不可重试**——前端 ApiError.retryable 对 >= 500 判可重试，落兜底时那是
// 502，于是一个永远不会成功的写操作会被反复重发。
//
// 第一次终止必须成功（正向锚点）：否则「第二次拿到 409」可能只是因为这个
// Action 压根跑不通。
func TestAlreadyTerminatedReachesHTTPAsConflict(t *testing.T) {
	h, batch := financeErrFixture(t)
	body := fmt.Sprintf(
		`{"params":{"subscription_batch_id":%q,"terminated_on":"2026-08-11",`+
			`"reason":"客户提前退订"}}`, batch.ID)

	first := execFinance(t, h, finance.ActionSubscriptionBatchTerminate, body)
	if first.Code != http.StatusOK {
		t.Fatalf("第一次终止应成功，实际 %d：%s", first.Code, first.Body.String())
	}
	// 第一次确实结转了损失——锚点要落在「这个 Action 真的做完了事」上，
	// 而不只是「返回了 200」。
	var okBody map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &okBody); err != nil {
		t.Fatalf("成功响应不是 JSON: %v", err)
	}
	result, _ := okBody["result"].(map[string]any)
	if _, ok := result["amortization_loss"]; !ok {
		t.Fatalf("第一次终止没有结转损失：%s", first.Body.String())
	}

	second := execFinance(t, h, finance.ActionSubscriptionBatchTerminate, body)
	if second.Code != http.StatusConflict {
		t.Fatalf("重复终止应 409，实际 %d：%s", second.Code, second.Body.String())
	}
	got := decodeError(t, second)
	if got.Code != "CONFLICT" {
		t.Fatalf("错误码 = %q，期望 CONFLICT（落兜底时会是 EXECUTION_FAILED）", got.Code)
	}
	const want = "该批次或代理已经终止过：终止日决定结转的损失金额，登记之后不可再改，平台也没有撤销终止的 Action。"
	if got.Message != want {
		t.Fatalf("文案 = %q\n期望 = %q", got.Message, want)
	}
	// 换一个终止日照样是 409：这正是「不是参数问题」的判据，
	// 也是它不该归 INVALID_PARAMS 的理由。
	other := execFinance(t, h, finance.ActionSubscriptionBatchTerminate, fmt.Sprintf(
		`{"params":{"subscription_batch_id":%q,"terminated_on":"2026-08-12",`+
			`"reason":"换个日子再试一次"}}`, batch.ID))
	if other.Code != http.StatusConflict {
		t.Fatalf("换终止日仍应 409，实际 %d：%s", other.Code, other.Body.String())
	}
}

// TestProxyTerminateAlsoReachesHTTPAsConflict：代理资产走的是另一个 Action
// 与另一个仓储方法（TerminateProxy），但共用同一个 domainError。
//
// 单独测它，是因为「批次那条通了」不蕴含「代理那条也通」——两个 handler
// 各自调用 domainError，漏掉一个不会有任何东西报错。
func TestProxyTerminateAlsoReachesHTTPAsConflict(t *testing.T) {
	pool := financeErrPool(t)
	subs := finance.NewSubscriptionStore(pool)
	proxy, err := subs.CreateProxy(context.Background(), finance.ProxyAsset{
		PaidMinor:          6_200_000,
		Currency:           finance.DefaultCurrency,
		OpenedOn:           day("2026-08-01"),
		ExpiresOn:          day("2026-08-31"),
		SharedAccountCount: 2,
		Mounted:            true,
		Environment:        "development",
	})
	if err != nil {
		t.Fatalf("登记代理: %v", err)
	}

	reg := action.NewRegistry()
	if err := finance.RegisterSubscriptionActions(reg, subs, finance.NewStore(pool)); err != nil {
		t.Fatalf("注册订阅 Action: %v", err)
	}
	res, err := NewDevHeaderResolver("development")
	if err != nil {
		t.Fatal(err)
	}
	h := NewRouter(Deps{
		Logger: discardLogger(), Service: "platform-api", Environment: "development",
		DB: fakePinger{}, Resolver: res,
		Kernel:         action.NewKernel(reg, &boundaryRunStore{}, action.WithLogger(discardLogger())),
		ActionRegistry: reg,
	})

	body := fmt.Sprintf(
		`{"params":{"proxy_asset_id":%q,"terminated_on":"2026-08-11","reason":"机房下线"}}`,
		proxy.ID)
	if first := execFinance(t, h, finance.ActionProxyAssetTerminate, body); first.Code != http.StatusOK {
		t.Fatalf("第一次终止代理应成功，实际 %d：%s", first.Code, first.Body.String())
	}
	second := execFinance(t, h, finance.ActionProxyAssetTerminate, body)
	if second.Code != http.StatusConflict {
		t.Fatalf("重复终止代理应 409，实际 %d：%s", second.Code, second.Body.String())
	}
	if got := decodeError(t, second); got.Code != "CONFLICT" {
		t.Fatalf("错误码 = %q，期望 CONFLICT", got.Code)
	}
}
