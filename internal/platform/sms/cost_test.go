package sms

import (
	"context"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/connector"
)

// 成本事件（ADR-022 决策 5，XM-SMS3 #1）。
//
// **在成功那一刻写**，而不是事后从资源表反推：延长、重激活、退款都不改资源
// 单价，只有事件能记下每一分钱的来龙去脉。
//
// 金额未知就是 NULL，**不是 0**：62 买号只回订单 ID（金额要事后从订单列表补），
// Hero 的延长写体不回价格。一个补出来的 0 会让「这个月花了多少」少算一笔，
// 而少算比缺一行更难发现。

func costsOf(t *testing.T, store *memStore) []CostEvent {
	t.Helper()
	events, err := store.ListCostEvents(context.Background(), "", 100)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// Hero 买号：上游逐个报价，一个号一条事件，带服务与国家（统计按供应商 × 币种
// × 服务 × 天聚合，服务不落在事件上就聚合不出来）。
func TestPurchaseWritesOneCostEventPerHeroResource(t *testing.T) {
	store := newMemStore()
	adapter := &fakeAdapter{purchaseOutcome: PurchaseOutcome{Resources: []Resource{
		{ExternalID: "a1", Phone: "79990000001", Service: "go", Country: "12", PriceText: "0.35"},
		{ExternalID: "a2", Phone: "79990000002", Service: "go", Country: "12", PriceText: "0.35"},
	}}}
	svc := newService(t, adapter, store)

	op, err := svc.Purchase(context.Background(), "op-1", ProviderHero, PurchaseInput{Quantity: 2, Service: "go", Country: 12})
	if err != nil {
		t.Fatal(err)
	}
	if op.State != StateSucceeded {
		t.Fatalf("应成功, got %+v", op)
	}
	events := costsOf(t, store)
	if len(events) != 2 {
		t.Fatalf("两个号该有两条成本事件, got %+v", events)
	}
	for _, ev := range events {
		if ev.Kind != CostPurchase || ev.AmountText != "0.35" || ev.Provider != ProviderHero {
			t.Errorf("事件内容不对: %+v", ev)
		}
		if ev.Service != "go" || ev.Country != "12" || ev.OperationID != "op-1" {
			t.Errorf("统计维度没带上: %+v", ev)
		}
		if ev.Subject == "" || ev.AmountSource != CostSourceUpstreamPrice {
			t.Errorf("主体与来源都要有: %+v", ev)
		}
	}
}

// 62 买号只回订单 ID，**上游没说花了多少**：金额留空（NULL），来源标成
// 「等订单金额」，事后由对账补。补一个 0 会让这个月的花费少算一笔。
func TestPurchaseWritesUnknownAmountForSMS62(t *testing.T) {
	store := newMemStore()
	adapter := &fakeAdapter{
		purchaseOutcome: PurchaseOutcome{OrderRef: "order-9"},
		importResources: []Resource{{ExternalID: "t1", Phone: "15550000001", ProviderToken: "tok", Service: "google"}},
	}
	svc := newService(t, adapter, store)

	if _, err := svc.Purchase(context.Background(), "op-2", ProviderSMS62, PurchaseInput{Quantity: 1, GoodsID: "1-2-3"}); err != nil {
		t.Fatal(err)
	}
	events := costsOf(t, store)
	if len(events) != 1 {
		t.Fatalf("应有一条事件, got %+v", events)
	}
	ev := events[0]
	if ev.AmountText != "" || ev.AmountSource != CostSourcePendingOrder {
		t.Fatalf("62 的金额此刻未知: %+v", ev)
	}
	if ev.ProviderRef != "order-9" {
		t.Errorf("要留下订单号，事后才补得回来: %+v", ev)
	}
	if ev.Currency != CurrencyUSD {
		t.Errorf("62 按 USD 记（ADR-022 口径）: %+v", ev)
	}
}

// 失败的操作**一条成本事件都不写**：没花钱。
func TestFailedPurchaseWritesNoCostEvent(t *testing.T) {
	store := newMemStore()
	adapter := &fakeAdapter{purchaseErr: connector.NewError(connector.KindRejected, "余额不足", nil)}
	svc := newService(t, adapter, store)

	if _, err := svc.Purchase(context.Background(), "op-3", ProviderHero, PurchaseInput{Quantity: 1}); err != nil {
		t.Fatal(err)
	}
	if events := costsOf(t, store); len(events) != 0 {
		t.Fatalf("失败不该有成本, got %+v", events)
	}
}

// 结果未知也不写：钱**可能**花了，而一条「可能花了」的成本会让账面凭空多出
// 一笔。这类要靠余额对账（XM-SMS3 #2）发现，不靠猜。
func TestUnknownPurchaseWritesNoCostEvent(t *testing.T) {
	store := newMemStore()
	adapter := &fakeAdapter{purchaseErr: connector.NewError(connector.KindUnavailable, "超时", nil)}
	svc := newService(t, adapter, store)

	op, err := svc.Purchase(context.Background(), "op-4", ProviderHero, PurchaseInput{Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if op.State != StateUnknown {
		t.Fatalf("应为 unknown, got %q", op.State)
	}
	if events := costsOf(t, store); len(events) != 0 {
		t.Fatalf("unknown 不该记成本, got %+v", events)
	}
}

// 同一笔操作重放不会记两次账：成本事件按 (操作, 主体) 唯一。
func TestCostEventsAreIdempotentPerOperationSubject(t *testing.T) {
	store := newMemStore()
	events := []CostEvent{{
		OperationID: "op-x", Provider: ProviderHero, Kind: CostPurchase, Subject: "res-1",
		AmountText: "0.35", Currency: "", OccurredAt: testNow,
	}}
	ctx := context.Background()
	if _, err := store.AppendCostEvents(ctx, events); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendCostEvents(ctx, events); err != nil {
		t.Fatal(err)
	}
	if got := costsOf(t, store); len(got) != 1 {
		t.Fatalf("同一笔只该有一条, got %+v", got)
	}
}

// 租用：与买号同一条路径（都花钱），事件类型是 rent。
func TestRentWritesCostEvent(t *testing.T) {
	store := newMemStore()
	fake := &extrasFake{rentRes: Resource{
		ExternalID: "r1", Phone: "79990000009", Service: "go", Country: "12",
		PriceText: "1.80", Subtype: SubtypeRent,
	}}
	svc := newService(t, fake, store)

	if _, err := svc.Rent(context.Background(), "op-rent", ProviderHero, HeroRentInput{Service: "go", Country: 12, DurationHours: 4}); err != nil {
		t.Fatal(err)
	}
	events := costsOf(t, store)
	if len(events) != 1 || events[0].Kind != CostRent || events[0].AmountText != "1.80" {
		t.Fatalf("租用该记一条 rent 成本, got %+v", events)
	}
}

// Hero 取消会退款：记**负数**。金额取我们当初被收的价（上游取消不回退款额），
// 资源上没有价时留空而不是补 0。
func TestCancelWritesNegativeRefund(t *testing.T) {
	store := newMemStore()
	svc := newService(t, &lifecycleFake{echoState: StateCancelled}, store)
	ctx := context.Background()
	id, _ := store.UpsertResource(ctx, Resource{
		Provider: ProviderHero, ExternalID: "a9", PriceText: "0.35", State: StateWaitingCode,
	})

	if _, err := svc.ExecuteAction(ctx, "op-cancel", KindCancel, id, ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	events := costsOf(t, store)
	if len(events) != 1 {
		t.Fatalf("取消该记一条退款, got %+v", events)
	}
	if events[0].Kind != CostRefund || events[0].AmountText != "-0.35" {
		t.Fatalf("退款要是负数: %+v", events[0])
	}
	if events[0].AmountSource != CostSourceRefundOfPrice {
		t.Errorf("来源要说清是按原价退的: %+v", events[0])
	}
}

// 延长 / 重激活：上游写体**不回价格**，金额留空、来源写明原因。
// 补一个 0 会让这两笔在统计里看起来免费。
func TestProlongWritesCostEventWithUnknownAmount(t *testing.T) {
	store := newMemStore()
	svc := newService(t, &lifecycleFake{echoState: StateWaitingCode}, store)
	ctx := context.Background()
	id, _ := store.UpsertResource(ctx, Resource{
		Provider: ProviderHero, ExternalID: "a10", PriceText: "0.35", State: StateWaitingCode,
	})

	if _, err := svc.ExecuteAction(ctx, "op-prolong", KindProlong, id, ActionOptions{Duration: 4}); err != nil {
		t.Fatal(err)
	}
	events := costsOf(t, store)
	if len(events) != 1 || events[0].Kind != CostProlong {
		t.Fatalf("延长该记一条, got %+v", events)
	}
	if events[0].AmountText != "" || events[0].AmountSource != CostSourceUpstreamSilent {
		t.Fatalf("金额未知要留空并说明: %+v", events[0])
	}
}

// 取码、连接测试这类不花钱的动作**不记成本**。
func TestFetchCodeWritesNoCostEvent(t *testing.T) {
	store := newMemStore()
	adapter := &fakeAdapter{code: Code{Code: "123456"}}
	svc := newService(t, adapter, store)
	ctx := context.Background()
	id, _ := store.UpsertResource(ctx, Resource{Provider: ProviderHero, ExternalID: "a11"})

	if _, err := svc.FetchCode(ctx, id); err != nil {
		t.Fatal(err)
	}
	if events := costsOf(t, store); len(events) != 0 {
		t.Fatalf("取码不花钱, got %+v", events)
	}
}

// 金额是十进制文本，一路不过 float：0.1 + 0.2 那类误差乘以几千笔就是真金白银。
func TestCostAmountKeepsDecimalText(t *testing.T) {
	store := newMemStore()
	adapter := &fakeAdapter{purchaseOutcome: PurchaseOutcome{Resources: []Resource{
		{ExternalID: "a1", Phone: "7999", PriceText: "0.100000"},
	}}}
	svc := newService(t, adapter, store)

	if _, err := svc.Purchase(context.Background(), "op-dec", ProviderHero, PurchaseInput{Quantity: 1}); err != nil {
		t.Fatal(err)
	}
	events := costsOf(t, store)
	if len(events) != 1 || events[0].AmountText != "0.100000" {
		t.Fatalf("金额要原样保留: %+v", events)
	}
	if !events[0].OccurredAt.Equal(testNow) {
		t.Errorf("发生时间该是本次操作的时钟: %+v", events[0])
	}
}

var _ = time.Now
