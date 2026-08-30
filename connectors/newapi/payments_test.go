package newapi_test

import (
	"context"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/newapi"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// ---------------------------------------------------------------------------
// Fake：ListOrders / DailyPaymentSummary
// ---------------------------------------------------------------------------

func TestFakeListOrdersRejectsInvalidFilter(t *testing.T) {
	c := newapi.NewFake(newapi.FakeOptions{})
	ctx := context.Background()
	now := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)

	cases := []newapi.OrderFilter{
		{},
		{From: now, To: time.Time{}},
		{From: now, To: now.AddDate(0, 0, -1)},
		{From: now.AddDate(0, 0, -1), To: now, Status: "made-up"},
	}
	for i, filter := range cases {
		if _, err := c.ListOrders(ctx, filter); err == nil {
			t.Errorf("case %d: 非法 filter 应被拒绝", i)
		} else if connector.KindOf(err) != connector.KindBadResponse {
			t.Errorf("case %d: 错误分类 = %s, want bad_response", i, connector.KindOf(err))
		}
	}
}

func TestFakeListOrdersHonorsWindowAndStatus(t *testing.T) {
	c := newapi.NewFake(newapi.FakeOptions{})
	ctx := context.Background()
	from := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)

	page, err := c.ListOrders(ctx, newapi.OrderFilter{From: from, To: to})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) == 0 {
		t.Fatal("宽窗口应返回若干笔固定数据")
	}
	for _, item := range page.Items {
		if item.CreatedAt.Before(from) || item.CreatedAt.After(to) {
			t.Errorf("订单 %s 的 CreatedAt=%v 落在窗口 [%v,%v] 之外", item.OrderID, item.CreatedAt, from, to)
		}
		if item.Currency == "" || item.UserRef == "" || item.Status == "" {
			t.Errorf("订单 %s 缺少必填展示字段: %+v", item.OrderID, item)
		}
	}

	filtered, err := c.ListOrders(ctx, newapi.OrderFilter{From: from, To: to, Status: "success"})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Items) == 0 {
		t.Fatal("固定数据覆盖 KnownOrderStatuses 全集，success 至少应有一笔")
	}
	for _, item := range filtered.Items {
		if item.Status != "success" {
			t.Errorf("status=success 过滤后出现了 %s", item.Status)
		}
	}
}

func TestFakeDailyPaymentSummaryNeverHasRefundedBucket(t *testing.T) {
	c := newapi.NewFake(newapi.FakeOptions{})
	summary, err := c.DailyPaymentSummary(context.Background(), "2026-08-27")
	if err != nil {
		t.Fatal(err)
	}
	if summary.ObservedAt.IsZero() || summary.Watermark == "" {
		t.Fatal("DailyPaymentSummary 也必须带新鲜度快照（规格 §9.1）")
	}
	if _, ok := summary.ByStatus[newapi.PaymentStatusRefunded]; ok {
		t.Fatal("NewAPI 没有退款概念，refunded 键永远不该出现")
	}
	if summary.FeeMinorUnits != nil || summary.NetMinorUnits != nil {
		t.Fatalf("NewAPI 的 TopUp 模型没有手续费/净现金流字段，两者必须恒为 nil: %+v", summary)
	}
	if _, err := c.DailyPaymentSummary(context.Background(), "not-a-day"); err == nil {
		t.Fatal("非法业务日应被拒绝")
	}
}

func TestFakeListOrdersAndSummaryPropagateFailAndPartial(t *testing.T) {
	ctx := context.Background()
	from := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)

	failing := newapi.NewFake(newapi.FakeOptions{FailWith: connector.KindUnavailable})
	if _, err := failing.ListOrders(ctx, newapi.OrderFilter{From: from, To: to}); connector.KindOf(err) != connector.KindUnavailable {
		t.Fatalf("ListOrders 应透传 FailWith, got %v", err)
	}
	if _, err := failing.DailyPaymentSummary(ctx, "2026-08-27"); connector.KindOf(err) != connector.KindUnavailable {
		t.Fatalf("DailyPaymentSummary 应透传 FailWith, got %v", err)
	}

	partial := newapi.NewFake(newapi.FakeOptions{Partial: true})
	page, err := partial.ListOrders(ctx, newapi.OrderFilter{From: from, To: to})
	if err != nil {
		t.Fatal(err)
	}
	if !page.IsPartial {
		t.Fatal("Partial 选项应体现在 OrderPage.IsPartial 上")
	}
}

// ---------------------------------------------------------------------------
// 真实客户端：上游形状映射（复用 client_contract_test.go 的 fakeTopupItems，
// 该固定数据本就带 id/user_id/trade_no，供本文件的逐笔明细断言使用）。
// ---------------------------------------------------------------------------

func TestRealClientListOrdersMapsFieldsAcrossProviderSemantics(t *testing.T) {
	upstream := startFakeUpstream(t, newapi.FakeOptions{})
	client := upstream.newClient(t)

	from := time.Unix(fakeDayStart, 0).UTC()
	to := time.Unix(fakeDayStart+86399, 0).UTC()
	page, err := client.ListOrders(context.Background(), newapi.OrderFilter{From: from, To: to})
	if err != nil {
		t.Fatalf("ListOrders: %v", err)
	}
	// 107/106/105/104/103/102 都落在今天窗口内；101 是"昨天到账"，不在窗口。
	if len(page.Items) != 6 {
		t.Fatalf("窗口内应有 6 笔（不含订单 101）, got %d: %+v", len(page.Items), page.Items)
	}
	byID := make(map[string]newapi.Order, len(page.Items))
	for _, item := range page.Items {
		byID[item.OrderID] = item
	}
	if _, ok := byID["101"]; ok {
		t.Fatal("订单 101（昨天到账）不该出现在今天的窗口里")
	}

	// 102：epay，Amount=10 → quota=10×500000=5,000,000 → $10.00
	if got := byID["102"]; got.AmountMinorUnits != 1000 || got.Currency != "USD" || got.Method != "epay" {
		t.Errorf("订单 102 = %+v, want AmountMinorUnits=1000 Currency=USD Method=epay", got)
	}
	// 103：stripe，Money=20.5 → quota=20.5×500000=10,250,000 → $20.50
	if got := byID["103"]; got.AmountMinorUnits != 2050 || got.Method != "stripe" {
		t.Errorf("订单 103 = %+v, want AmountMinorUnits=2050 Method=stripe", got)
	}
	// 104：creem，Amount 本身就是 quota=3,000,000 → $6.00
	if got := byID["104"]; got.AmountMinorUnits != 600 || got.Method != "creem" {
		t.Errorf("订单 104 = %+v, want AmountMinorUnits=600 Method=creem", got)
	}
	// 106：balance（内部划转），固定报 0——不是"这笔钱值 0"，是"不重新解释语义"
	if got := byID["106"]; got.AmountMinorUnits != 0 || got.Method != "balance" {
		t.Errorf("订单 106（内部划转） = %+v, want AmountMinorUnits=0 Method=balance", got)
	}
	// 107：mystery_pay（未识别 provider），同样报 0，但整页应标记部分数据
	if got := byID["107"]; got.AmountMinorUnits != 0 {
		t.Errorf("订单 107（provider 未识别） = %+v, want AmountMinorUnits=0", got)
	}
	if !page.IsPartial {
		t.Fatal("窗口内出现 provider 未识别的订单，整页应标记为部分数据")
	}
	// 105：pending，epay，Amount=99 → quota=99×500000=49,500,000 → $99.00
	// —— 即便不是 success，逐笔明细也照样给出金额（与既有 fetchRechargeDay
	// 只处理 success 不同，这里所有状态一视同仁，见 payments.go 顶部说明）。
	if got := byID["105"]; got.AmountMinorUnits != 9900 || got.Status != "pending" {
		t.Errorf("订单 105 = %+v, want AmountMinorUnits=9900 Status=pending", got)
	}

	for _, id := range []string{"102", "103", "104", "105", "106", "107"} {
		if byID[id].UserRef == "" {
			t.Errorf("订单 %s 缺少 UserRef", id)
		}
		if byID[id].UpstreamOrderRef == "" {
			t.Errorf("订单 %s 缺少 UpstreamOrderRef（应为 trade_no）", id)
		}
	}
}

func TestRealClientListOrdersFiltersClientSideByStatus(t *testing.T) {
	upstream := startFakeUpstream(t, newapi.FakeOptions{})
	client := upstream.newClient(t)

	from := time.Unix(fakeDayStart, 0).UTC()
	to := time.Unix(fakeDayStart+86399, 0).UTC()
	page, err := client.ListOrders(context.Background(), newapi.OrderFilter{From: from, To: to, Status: "pending"})
	if err != nil {
		t.Fatalf("ListOrders: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].OrderID != "105" {
		t.Fatalf("status=pending 应只剩订单 105, got %+v", page.Items)
	}
	// 上游列表端点不支持服务端 status 过滤，客户端必须自己筛——用请求记录
	// 确认没有把 status 当查询参数发出去（发了也没用，但发了说明理解错了协议）。
	for _, req := range upstream.recorded() {
		if req.path == upstreamTopups && containsSubstr(req.query, "status=") {
			t.Errorf("newapi 的充值列表端点不支持服务端 status 过滤，不该发送 status 查询参数: %q", req.query)
		}
	}
}

func containsSubstr(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func TestRealClientDailyPaymentSummaryAggregatesQuotaThenConverts(t *testing.T) {
	upstream := startFakeUpstream(t, newapi.FakeOptions{})
	client := upstream.newClient(t)

	summary, err := client.DailyPaymentSummary(context.Background(), "2026-08-27")
	if err != nil {
		t.Fatalf("DailyPaymentSummary: %v", err)
	}
	// succeeded = 107(quota 0,unknown) + 106(quota 0,internal) + 104(3,000,000)
	//           + 103(10,250,000) + 102(5,000,000) = 18,250,000 quota → $36.50；
	// 笔数 5（107/106 也计数，只是不计金额，与既有 fetchRechargeDay 同一条纪律）。
	succeeded, ok := summary.ByStatus[newapi.PaymentStatusSucceeded]
	if !ok || succeeded.Count != 5 || succeeded.AmountMinorUnits != 3650 {
		t.Errorf("succeeded 桶 = %+v, want {Count:5 AmountMinorUnits:3650}", succeeded)
	}
	pending, ok := summary.ByStatus[newapi.PaymentStatusPending]
	if !ok || pending.Count != 1 || pending.AmountMinorUnits != 9900 {
		t.Errorf("pending 桶 = %+v, want {Count:1 AmountMinorUnits:9900}", pending)
	}
	if _, ok := summary.ByStatus[newapi.PaymentStatusFailed]; ok {
		t.Error("固定数据里没有 failed/expired 订单，这个键不该出现")
	}
	if _, ok := summary.ByStatus[newapi.PaymentStatusRefunded]; ok {
		t.Error("NewAPI 没有退款概念，refunded 键永远不该出现")
	}
	if !summary.IsPartial {
		t.Fatal("窗口内有 provider 未识别的订单（107），应标记为部分数据")
	}
	if summary.FeeMinorUnits != nil || summary.NetMinorUnits != nil {
		t.Fatalf("NewAPI 没有手续费/净现金流字段，两者必须恒为 nil: %+v", summary)
	}
}
