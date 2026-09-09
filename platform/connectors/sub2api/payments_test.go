package sub2api_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/sub2api"
	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// ---------------------------------------------------------------------------
// Fake：ListOrders / DailyPaymentSummary
// ---------------------------------------------------------------------------

func TestFakeListOrdersRejectsInvalidFilter(t *testing.T) {
	c := sub2api.NewFake(sub2api.FakeOptions{})
	ctx := context.Background()
	now := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)

	cases := []sub2api.OrderFilter{
		{},                                     // From/To 都是零值
		{From: now, To: time.Time{}},           // To 缺失
		{From: now, To: now.AddDate(0, 0, -1)}, // to 早于 from
		{From: now.AddDate(0, 0, -1), To: now, Status: "made-up"}, // 未知状态
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
	c := sub2api.NewFake(sub2api.FakeOptions{})
	ctx := context.Background()
	from := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)

	page, err := c.ListOrders(ctx, sub2api.OrderFilter{From: from, To: to})
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

	filtered, err := c.ListOrders(ctx, sub2api.OrderFilter{From: from, To: to, Status: "PAID"})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range filtered.Items {
		if item.Status != "PAID" {
			t.Errorf("status=PAID 过滤后出现了 %s", item.Status)
		}
	}
	if len(filtered.Items) == 0 {
		t.Fatal("固定数据覆盖 KnownOrderStatuses 全集，PAID 至少应有一笔")
	}
}

// TestFakeListOrdersExposesFeeAndRefundAmountPerOrder（XM-PAY1）：假实现的
// 逐笔 Fee/RefundAmount 门禁必须与真实客户端一致——固定数据覆盖
// KnownOrderStatuses 全集，逐条按桶断言。
func TestFakeListOrdersExposesFeeAndRefundAmountPerOrder(t *testing.T) {
	c := sub2api.NewFake(sub2api.FakeOptions{})
	from := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	page, err := c.ListOrders(context.Background(), sub2api.OrderFilter{From: from, To: to})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		// RefundAmountMinorUnits 对每一笔都必须非 nil：sub2api 侧这个字段永远
		// 可知（未退款订单是已知的 0），与 FeeMinorUnits 的按桶门禁不是同一
		// 条规则。
		if item.RefundAmountMinorUnits == nil {
			t.Errorf("订单 %s(%s).RefundAmountMinorUnits = nil, sub2api 侧应恒非 nil", item.OrderID, item.Status)
		}
		switch item.Status {
		case "PAID", "RECHARGING", "COMPLETED", "REFUND_REQUESTED", "REFUNDING",
			"REFUND_PENDING", "REFUND_FAILED", "PARTIALLY_REFUNDED", "REFUNDED":
			if item.FeeMinorUnits == nil {
				t.Errorf("订单 %s(%s) 落在 succeeded/refunded 桶，FeeMinorUnits 不该是 nil", item.OrderID, item.Status)
			}
		case "PENDING", "EXPIRED", "CANCELLED", "FAILED":
			if item.FeeMinorUnits != nil {
				t.Errorf("订单 %s(%s) 钱没真的动过，FeeMinorUnits 应为 nil, got %d", item.OrderID, item.Status, *item.FeeMinorUnits)
			}
		}
	}
}

func TestFakeDailyPaymentSummaryBucketsAndLeavesNetUnknown(t *testing.T) {
	c := sub2api.NewFake(sub2api.FakeOptions{})
	summary, err := c.DailyPaymentSummary(context.Background(), "2026-08-27")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Day != "2026-08-27" || summary.Currency == "" {
		t.Fatalf("summary 基础字段缺失: %+v", summary)
	}
	if summary.ObservedAt.IsZero() || summary.Watermark == "" {
		t.Fatal("DailyPaymentSummary 也必须带新鲜度快照（规格 §9.1）")
	}
	if len(summary.ByStatus) == 0 {
		t.Fatal("固定数据应至少落进一个分桶")
	}
	for bucket, amount := range summary.ByStatus {
		if amount.Count <= 0 {
			t.Errorf("分桶 %s 的 Count 应为正", bucket)
		}
	}
	if summary.FeeMinorUnits == nil {
		t.Fatal("Fake 应给出手续费（非 nil），验证「未知才给 nil」这条纪律在假实现里也生效")
	}
	if summary.NetMinorUnits != nil {
		t.Fatal("NetMinorUnits 必须恒为 nil：本片没有可核实的净现金流口径，不能冒充已知")
	}

	if _, err := c.DailyPaymentSummary(context.Background(), "not-a-day"); err == nil {
		t.Fatal("非法业务日应被拒绝")
	}
}

func TestFakeListOrdersAndSummaryPropagateFailAndPartial(t *testing.T) {
	ctx := context.Background()
	from := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)

	failing := sub2api.NewFake(sub2api.FakeOptions{FailWith: connector.KindUnavailable})
	if _, err := failing.ListOrders(ctx, sub2api.OrderFilter{From: from, To: to}); connector.KindOf(err) != connector.KindUnavailable {
		t.Fatalf("ListOrders 应透传 FailWith, got %v", err)
	}
	if _, err := failing.DailyPaymentSummary(ctx, "2026-08-27"); connector.KindOf(err) != connector.KindUnavailable {
		t.Fatalf("DailyPaymentSummary 应透传 FailWith, got %v", err)
	}

	partial := sub2api.NewFake(sub2api.FakeOptions{Partial: true})
	page, err := partial.ListOrders(ctx, sub2api.OrderFilter{From: from, To: to})
	if err != nil {
		t.Fatal(err)
	}
	if !page.IsPartial {
		t.Fatal("Partial 选项应体现在 OrderPage.IsPartial 上")
	}
	summary, err := partial.DailyPaymentSummary(ctx, "2026-08-27")
	if err != nil {
		t.Fatal(err)
	}
	if !summary.IsPartial {
		t.Fatal("Partial 选项应体现在 DailyPaymentSummary.IsPartial 上")
	}
}

// ---------------------------------------------------------------------------
// 真实客户端：上游形状映射（httptest 假上游，回放真实字段）
// ---------------------------------------------------------------------------

func TestRealClientListOrdersMapsFieldsAndWindow(t *testing.T) {
	upstream := startFakeUpstream(t, sub2api.FakeOptions{})
	client := upstream.newClient(t)

	from := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 27, 23, 59, 59, 0, time.UTC)
	page, err := client.ListOrders(context.Background(), sub2api.OrderFilter{From: from, To: to})
	if err != nil {
		t.Fatalf("ListOrders: %v", err)
	}
	if page.IsPartial {
		t.Fatalf("这个窗口全是同币种、单页数据，不该标记部分数据: %+v", page)
	}
	if len(page.Items) != 4 {
		t.Fatalf("窗口 [%s,%s] 应命中 9001-9004 共 4 笔, got %d: %+v", from, to, len(page.Items), page.Items)
	}
	// 必须按 CreatedAt 降序：9001(27 10:00) > 9002(27 09:00) > 9003(26 23:00) > 9004(26 08:00)
	wantOrder := []string{"9001", "9002", "9003", "9004"}
	for i, id := range wantOrder {
		if page.Items[i].OrderID != id {
			t.Fatalf("Items[%d].OrderID = %s, want %s（顺序应为 created_at 降序）", i, page.Items[i].OrderID, id)
		}
	}

	first := page.Items[0]
	if first.AmountMinorUnits != 100_00 {
		t.Errorf("9001.AmountMinorUnits = %d, want 10000（100.00 的面值，不是 pay_amount）", first.AmountMinorUnits)
	}
	if first.Currency != "USD" {
		t.Errorf("9001.Currency = %s, want USD", first.Currency)
	}
	if first.Method != "alipay" {
		t.Errorf("9001.Method = %s, want alipay", first.Method)
	}
	if first.UpstreamOrderRef != "OUT-9001" {
		t.Errorf("9001.UpstreamOrderRef = %s, want OUT-9001（out_trade_no，不是网关 trade no）", first.UpstreamOrderRef)
	}
	if first.UserRef == "a@example.test" || first.UserRef == "" {
		t.Errorf("9001.UserRef = %q，明文邮箱不该出现，也不该是空", first.UserRef)
	}

	stats, ok := page.StatsByStatus["PAID"]
	if !ok || stats.Count != 1 || stats.AmountMinorUnits != 100_00 {
		t.Errorf("StatsByStatus[PAID] = %+v, want {Count:1 AmountMinorUnits:10000}", stats)
	}
	refundStats, ok := page.StatsByStatus["REFUNDED"]
	if !ok || refundStats.Count != 1 || refundStats.AmountMinorUnits != 200_00 {
		// 逐状态统计用的是订单面值 Amount（200.00），不是退款额 RefundAmount
		// （202.00）——那个区分只在 DailyPaymentSummary 的归一化"refunded"桶里生效。
		t.Errorf("StatsByStatus[REFUNDED] = %+v, want {Count:1 AmountMinorUnits:20000}（面值，非退款额）", refundStats)
	}
}

// TestRealClientListOrdersExposesFeeAndRefundAmountPerOrder（XM-PAY1）：
// FeeMinorUnits 只在 succeeded/refunded 桶才给出，RefundAmountMinorUnits
// 对每一笔订单都给出（未退款订单是已知的 0，不是 nil）——与
// DailyPaymentSummary 聚合手续费/退款额的门禁规则完全一致，只是落到了
// 逐笔明细上。窗口固定命中 9001(PAID)/9002(PENDING)/9003(REFUNDED)/
// 9004(FAILED) 四种桶，覆盖门禁的两个分支。
func TestRealClientListOrdersExposesFeeAndRefundAmountPerOrder(t *testing.T) {
	upstream := startFakeUpstream(t, sub2api.FakeOptions{})
	client := upstream.newClient(t)

	from := time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 27, 23, 59, 59, 0, time.UTC)
	page, err := client.ListOrders(context.Background(), sub2api.OrderFilter{From: from, To: to})
	if err != nil {
		t.Fatalf("ListOrders: %v", err)
	}
	byID := make(map[string]sub2api.Order, len(page.Items))
	for _, o := range page.Items {
		byID[o.OrderID] = o
	}

	paid := byID["9001"] // amount=100.00 pay_amount=101.00 → fee=1.00；refund_amount=0.00
	if paid.FeeMinorUnits == nil || *paid.FeeMinorUnits != 100 {
		t.Errorf("9001(PAID→succeeded).FeeMinorUnits = %v, want *100（101.00-100.00）", ptrString(paid.FeeMinorUnits))
	}
	if paid.RefundAmountMinorUnits == nil || *paid.RefundAmountMinorUnits != 0 {
		t.Errorf("9001.RefundAmountMinorUnits = %v, want *0（未退款订单是已知的 0，不是 nil）", ptrString(paid.RefundAmountMinorUnits))
	}

	pending := byID["9002"]
	if pending.FeeMinorUnits != nil {
		t.Errorf("9002(PENDING).FeeMinorUnits = %v, want nil（钱没真的动过，手续费不适用）", ptrString(pending.FeeMinorUnits))
	}
	if pending.RefundAmountMinorUnits == nil || *pending.RefundAmountMinorUnits != 0 {
		t.Errorf("9002.RefundAmountMinorUnits = %v, want *0", ptrString(pending.RefundAmountMinorUnits))
	}

	refunded := byID["9003"] // amount=200.00 pay_amount=202.00 → fee=2.00；refund_amount=202.00
	if refunded.FeeMinorUnits == nil || *refunded.FeeMinorUnits != 200 {
		t.Errorf("9003(REFUNDED).FeeMinorUnits = %v, want *200（202.00-200.00）", ptrString(refunded.FeeMinorUnits))
	}
	if refunded.RefundAmountMinorUnits == nil || *refunded.RefundAmountMinorUnits != 20200 {
		t.Errorf("9003.RefundAmountMinorUnits = %v, want *20200（实退金额，不是面值 20000）", ptrString(refunded.RefundAmountMinorUnits))
	}

	failed := byID["9004"]
	if failed.FeeMinorUnits != nil {
		t.Errorf("9004(FAILED).FeeMinorUnits = %v, want nil", ptrString(failed.FeeMinorUnits))
	}
	if failed.RefundAmountMinorUnits == nil || *failed.RefundAmountMinorUnits != 0 {
		t.Errorf("9004.RefundAmountMinorUnits = %v, want *0", ptrString(failed.RefundAmountMinorUnits))
	}
}

func ptrString(v *int64) string {
	if v == nil {
		return "nil"
	}
	return fmt.Sprintf("*%d", *v)
}

func TestRealClientListOrdersForwardsStatusFilterToUpstream(t *testing.T) {
	upstream := startFakeUpstream(t, sub2api.FakeOptions{})
	client := upstream.newClient(t)

	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	page, err := client.ListOrders(context.Background(), sub2api.OrderFilter{From: from, To: to, Status: "PAID"})
	if err != nil {
		t.Fatalf("ListOrders: %v", err)
	}

	found := false
	for _, req := range upstream.recorded() {
		if req.path == upstreamPaymentOrders && req.query != "" {
			if q := req.query; contains(q, "status=PAID") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("status 过滤应作为服务端查询参数转发给上游，而不是拉全量自己筛")
	}

	// 9001（USD）与 9005（CNY）都是 PAID，窗口够宽应该两笔都在，
	// 但 9005 币种不同，金额不计入 StatsByStatus，且整页应标记部分数据。
	if len(page.Items) != 2 {
		t.Fatalf("应返回 2 笔 PAID 订单（9001、9005）, got %d", len(page.Items))
	}
	if !page.IsPartial {
		t.Fatal("窗口内出现非合约币种订单，应标记为部分数据（currency gap）")
	}
	stats := page.StatsByStatus["PAID"]
	if stats.Count != 2 {
		t.Errorf("StatsByStatus[PAID].Count = %d, want 2（跨币种也计数）", stats.Count)
	}
	if stats.AmountMinorUnits != 100_00 {
		t.Errorf("StatsByStatus[PAID].AmountMinorUnits = %d, want 10000（只计合约币种 USD 那一笔）", stats.AmountMinorUnits)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestRealClientListOrdersRejectsUnknownStatusWithoutCallingUpstream(t *testing.T) {
	upstream := startFakeUpstream(t, sub2api.FakeOptions{})
	client := upstream.newClient(t)

	_, err := client.ListOrders(context.Background(), sub2api.OrderFilter{
		From: time.Now().AddDate(0, 0, -1), To: time.Now(), Status: "not-a-real-status",
	})
	if err == nil || connector.KindOf(err) != connector.KindBadResponse {
		t.Fatalf("未知 status 应在发请求前就被拒绝, got %v", err)
	}
	if len(upstream.recorded()) != 0 {
		t.Fatal("非法输入不该变成一次上游读取")
	}
}

func TestRealClientDailyPaymentSummaryBucketsRefundByRefundAmount(t *testing.T) {
	upstream := startFakeUpstream(t, sub2api.FakeOptions{})
	client := upstream.newClient(t)

	summary, err := client.DailyPaymentSummary(context.Background(), "2026-08-26")
	if err != nil {
		t.Fatalf("DailyPaymentSummary: %v", err)
	}
	refunded, ok := summary.ByStatus[sub2api.PaymentStatusRefunded]
	if !ok || refunded.Count != 1 || refunded.AmountMinorUnits != 202_00 {
		t.Errorf("refunded 桶 = %+v, want {Count:1 AmountMinorUnits:20200}（退款额，不是订单面值 20000）", refunded)
	}
	failed, ok := summary.ByStatus[sub2api.PaymentStatusFailed]
	if !ok || failed.Count != 1 || failed.AmountMinorUnits != 30_00 {
		t.Errorf("failed 桶 = %+v, want {Count:1 AmountMinorUnits:3000}", failed)
	}
	if _, ok := summary.ByStatus[sub2api.PaymentStatusSucceeded]; ok {
		t.Error("2026-08-26 没有 succeeded 订单，这个键不该出现（不能冒充 0）")
	}
	if summary.FeeMinorUnits == nil || *summary.FeeMinorUnits != 2_00 {
		t.Errorf("FeeMinorUnits = %v, want 200（只对 refunded 订单 9003 求 pay_amount-amount，failed 订单不计）",
			derefOrNil(summary.FeeMinorUnits))
	}
	if summary.NetMinorUnits != nil {
		t.Error("NetMinorUnits 必须为 nil")
	}
}

func TestRealClientDailyPaymentSummarySucceededBucketAndFee(t *testing.T) {
	upstream := startFakeUpstream(t, sub2api.FakeOptions{})
	client := upstream.newClient(t)

	summary, err := client.DailyPaymentSummary(context.Background(), "2026-08-27")
	if err != nil {
		t.Fatalf("DailyPaymentSummary: %v", err)
	}
	succeeded, ok := summary.ByStatus[sub2api.PaymentStatusSucceeded]
	if !ok || succeeded.Count != 1 || succeeded.AmountMinorUnits != 100_00 {
		t.Errorf("succeeded 桶 = %+v, want {Count:1 AmountMinorUnits:10000}（9001 的面值）", succeeded)
	}
	pending, ok := summary.ByStatus[sub2api.PaymentStatusPending]
	if !ok || pending.Count != 1 || pending.AmountMinorUnits != 50_00 {
		t.Errorf("pending 桶 = %+v, want {Count:1 AmountMinorUnits:5000}", pending)
	}
	if summary.FeeMinorUnits == nil || *summary.FeeMinorUnits != 1_00 {
		t.Errorf("FeeMinorUnits = %v, want 100（只对 succeeded 订单 9001 求手续费，pending 不计）",
			derefOrNil(summary.FeeMinorUnits))
	}
}

func derefOrNil(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
