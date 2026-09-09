package sms

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 余额对账（ADR-022 决策 5，XM-SMS3 #2）。
//
// 两次余额快照之间，余额应当**正好**掉了这期间成本事件之和。对不上就是两种
// 情况之一：上游多扣了，或者我们漏记了。两者都要人去看，而没有对账就没人会
// 发现——账面上每一笔都「成功」了。
//
// 只在能下结论时才报：窗口里有金额未知的事件（62 的订单金额还没补、Hero 的
// 延长不回价）时我们自己的和就是不完整的，那时报差额等于报自己的无知。

func reconcileService(t *testing.T, store *memStore) *Service {
	t.Helper()
	store.status[ProviderHero] = ProviderStatus{Provider: ProviderHero, Enabled: true, VerifiedAt: testNow}
	return NewService([]Provider{{ID: ProviderHero, Adapter: &extrasFake{}}}, store, nil,
		func() time.Time { return testNow })
}

func snapshot(t *testing.T, store *memStore, amount string, at time.Time) {
	t.Helper()
	if _, err := store.SaveBalanceSnapshot(context.Background(), BalanceSnapshot{
		Provider: ProviderHero, AmountText: amount, TakenAt: at,
	}); err != nil {
		t.Fatal(err)
	}
}

func cost(t *testing.T, store *memStore, subject, amount string, at time.Time) {
	t.Helper()
	if _, err := store.AppendCostEvents(context.Background(), []CostEvent{{
		OperationID: "op-" + subject, Provider: ProviderHero, Kind: CostPurchase, Subject: subject,
		AmountText: amount, OccurredAt: at,
	}}); err != nil {
		t.Fatal(err)
	}
}

// 对得上就不报：余额掉的正好是这期间花掉的。
func TestReconcileBalanceQuietWhenLedgerMatches(t *testing.T) {
	store := newMemStore()
	svc := reconcileService(t, store)
	base := testNow.Add(-time.Hour)
	snapshot(t, store, "10.00", base)
	cost(t, store, "r1", "0.35", base.Add(10*time.Minute))
	cost(t, store, "r2", "0.35", base.Add(20*time.Minute))
	snapshot(t, store, "9.30", testNow)

	events, err := svc.EvaluateAlerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := eventOfKind(events, AlertBalanceDrift); ok {
		t.Fatalf("对得上不该报, got %+v", events)
	}
}

// 上游多扣：余额掉得比账本多，超过容差就报。
func TestReconcileBalanceReportsUnexplainedCharge(t *testing.T) {
	store := newMemStore()
	svc := reconcileService(t, store)
	base := testNow.Add(-time.Hour)
	snapshot(t, store, "10.00", base)
	cost(t, store, "r1", "0.35", base.Add(10*time.Minute))
	// 账上只花了 0.35，余额却掉了 2.35。
	snapshot(t, store, "7.65", testNow)

	events, err := svc.EvaluateAlerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, ok := eventOfKind(events, AlertBalanceDrift)
	if !ok {
		t.Fatalf("多扣 2.00 应该报, got %+v", events)
	}
	if got.Provider != ProviderHero || got.Severity != SeverityWarning {
		t.Errorf("事件内容不对: %+v", got)
	}
	// 摘要要给出可查的数：差多少、窗口是哪一段。
	if !strings.Contains(got.Summary, "2") {
		t.Errorf("摘要要说清差了多少: %q", got.Summary)
	}
}

// 充值会让余额**变多**，那不是异常。只报「掉得比账本多」这一个方向。
func TestReconcileBalanceIgnoresTopUp(t *testing.T) {
	store := newMemStore()
	svc := reconcileService(t, store)
	base := testNow.Add(-time.Hour)
	snapshot(t, store, "10.00", base)
	cost(t, store, "r1", "0.35", base.Add(10*time.Minute))
	// 中间充了 50。
	snapshot(t, store, "59.65", testNow)

	events, err := svc.EvaluateAlerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := eventOfKind(events, AlertBalanceDrift); ok {
		t.Fatalf("充值不该报警, got %+v", events)
	}
}

// 容差以内不报：一分两分的差多半是四舍五入，不值得半夜叫人。
func TestReconcileBalanceToleratesRounding(t *testing.T) {
	store := newMemStore()
	svc := reconcileService(t, store)
	base := testNow.Add(-time.Hour)
	snapshot(t, store, "10.00", base)
	cost(t, store, "r1", "0.35", base.Add(10*time.Minute))
	snapshot(t, store, "9.63", testNow) // 比账本多掉 0.02

	events, err := svc.EvaluateAlerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := eventOfKind(events, AlertBalanceDrift); ok {
		t.Fatalf("容差内不该报, got %+v", events)
	}
}

// 窗口里有金额未知的事件：**我们自己的和就是不完整的**，这时报差额等于报
// 自己的无知。跳过，等金额补上。
func TestReconcileBalanceSkipsWindowWithUnknownAmounts(t *testing.T) {
	store := newMemStore()
	svc := reconcileService(t, store)
	base := testNow.Add(-time.Hour)
	snapshot(t, store, "10.00", base)
	cost(t, store, "r1", "0.35", base.Add(10*time.Minute))
	// 延长：上游不回价格，金额未知。
	if _, err := store.AppendCostEvents(context.Background(), []CostEvent{{
		OperationID: "op-prolong", Provider: ProviderHero, Kind: CostProlong, Subject: "r1",
		AmountSource: CostSourceUpstreamSilent, OccurredAt: base.Add(20 * time.Minute),
	}}); err != nil {
		t.Fatal(err)
	}
	snapshot(t, store, "5.00", testNow)

	events, err := svc.EvaluateAlerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := eventOfKind(events, AlertBalanceDrift); ok {
		t.Fatalf("金额不全时不该下结论, got %+v", events)
	}
}

// 只有一张快照时无从对账：**没有窗口**，不报也不算异常。
func TestReconcileBalanceNeedsTwoSnapshots(t *testing.T) {
	store := newMemStore()
	svc := reconcileService(t, store)
	snapshot(t, store, "10.00", testNow)

	events, err := svc.EvaluateAlerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := eventOfKind(events, AlertBalanceDrift); ok {
		t.Fatalf("只有一张快照不该报, got %+v", events)
	}
}

// 窗口里混着别的币种（邮箱按 ISO 码计价，活动没有币种）：不可比，跳过。
// 跨币种相减得到的数字看起来像个金额，其实什么都不是。
func TestReconcileBalanceSkipsMixedCurrencies(t *testing.T) {
	store := newMemStore()
	svc := reconcileService(t, store)
	base := testNow.Add(-time.Hour)
	snapshot(t, store, "10.00", base)
	cost(t, store, "r1", "0.35", base.Add(10*time.Minute))
	if _, err := store.AppendCostEvents(context.Background(), []CostEvent{{
		OperationID: "op-mail", Provider: ProviderHero, Kind: CostEmailPurchase, Subject: "mail-1",
		AmountText: "1.00", Currency: "840", OccurredAt: base.Add(20 * time.Minute),
	}}); err != nil {
		t.Fatal(err)
	}
	snapshot(t, store, "5.00", testNow)

	events, err := svc.EvaluateAlerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := eventOfKind(events, AlertBalanceDrift); ok {
		t.Fatalf("跨币种不可比, got %+v", events)
	}
}

// 退款是负数：账本说「净花了 0.35 又退回 0.35」，余额就该没变。
func TestReconcileBalanceCountsRefundsAsNegative(t *testing.T) {
	store := newMemStore()
	svc := reconcileService(t, store)
	base := testNow.Add(-time.Hour)
	snapshot(t, store, "10.00", base)
	cost(t, store, "r1", "0.35", base.Add(10*time.Minute))
	if _, err := store.AppendCostEvents(context.Background(), []CostEvent{{
		OperationID: "op-cancel", Provider: ProviderHero, Kind: CostRefund, Subject: "r1-refund",
		AmountText: "-0.35", AmountSource: CostSourceRefundOfPrice, OccurredAt: base.Add(20 * time.Minute),
	}}); err != nil {
		t.Fatal(err)
	}
	snapshot(t, store, "10.00", testNow)

	events, err := svc.EvaluateAlerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := eventOfKind(events, AlertBalanceDrift); ok {
		t.Fatalf("退款抵消后应当对得上, got %+v", events)
	}
}
