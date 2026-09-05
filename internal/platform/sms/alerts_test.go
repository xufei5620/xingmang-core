package sms

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
)

// 内部告警（ADR-022，XM-SMS2 #8）。
//
// **一律不外发。** 条件只落成事件与页面红条，投递等另一条线的通知规范定稿。
// 三个条件：余额低于阈值、unknown 待核对超时、租用号快到期。
//
// 事件按指纹去重：每轮巡检都会重新评估，一条「余额不足」在被处理之前会被
// 重新算出很多次；每次插一行会让页面变成一串同样的红条，然后人开始忽略它们。

func alertService(t *testing.T, store *memStore) *Service {
	t.Helper()
	store.status[ProviderSMS62] = ProviderStatus{Provider: ProviderSMS62, Enabled: true, VerifiedAt: testNow}
	store.status[ProviderHero] = ProviderStatus{Provider: ProviderHero, Enabled: true, VerifiedAt: testNow}
	return NewService(
		[]Provider{{ID: ProviderSMS62, Adapter: &fakeAdapter{}}, {ID: ProviderHero, Adapter: &extrasFake{}}},
		store, nil, func() time.Time { return testNow },
	)
}

func eventOfKind(events []AlertEvent, kind string) (AlertEvent, bool) {
	for _, e := range events {
		if e.Kind == kind {
			return e, true
		}
	}
	return AlertEvent{}, false
}

// 没配阈值的那家**不判余额**：一个默认阈值等于替运营决定「多少算少」，
// 而两家的币种不同，那个数字没有通用答案。
func TestEvaluateAlertsBalanceOnlyWhenThresholdConfigured(t *testing.T) {
	store := newMemStore()
	svc := alertService(t, store)
	ctx := context.Background()
	if _, err := store.SaveBalanceSnapshot(ctx, BalanceSnapshot{Provider: ProviderHero, AmountText: "1.20", TakenAt: testNow}); err != nil {
		t.Fatal(err)
	}

	events, err := svc.EvaluateAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("没配阈值就不该报余额, got %+v", events)
	}

	if _, err := svc.SetBalanceThreshold(ctx, ProviderHero, "5"); err != nil {
		t.Fatal(err)
	}
	events, err = svc.EvaluateAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := eventOfKind(events, AlertBalanceLow)
	if !ok {
		t.Fatalf("1.20 < 5 应报余额不足, got %+v", events)
	}
	if got.Provider != ProviderHero || got.Severity != SeverityWarning {
		t.Errorf("事件内容不对: %+v", got)
	}
	// 摘要要说清「多少 / 阈值多少」，不能只说「余额不足」。
	if got.Summary == "" || got.Fingerprint == "" {
		t.Errorf("摘要与指纹都不能空: %+v", got)
	}
}

// 阈值按各家自己的币种比较、不折算；等于阈值不算低。
func TestEvaluateAlertsBalanceComparesDecimalNotFloat(t *testing.T) {
	store := newMemStore()
	svc := alertService(t, store)
	ctx := context.Background()
	if _, err := svc.SetBalanceThreshold(ctx, ProviderHero, "5.00"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		amount string
		fires  bool
	}{
		{"4.999999", true},
		{"5.00", false},
		{"5.000001", false},
		{"0", true},
	} {
		store.snapshots = nil
		if _, err := store.SaveBalanceSnapshot(ctx, BalanceSnapshot{Provider: ProviderHero, AmountText: c.amount, TakenAt: testNow}); err != nil {
			t.Fatal(err)
		}
		events, err := svc.EvaluateAlerts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := eventOfKind(events, AlertBalanceLow); ok != c.fires {
			t.Errorf("余额 %s 阈值 5.00: fires=%v, want %v", c.amount, ok, c.fires)
		}
	}
}

// 空阈值 = 清掉，不是「阈值为 0」。
func TestSetBalanceThresholdEmptyClearsIt(t *testing.T) {
	store := newMemStore()
	svc := alertService(t, store)
	ctx := context.Background()
	if _, err := svc.SetBalanceThreshold(ctx, ProviderHero, "5"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetBalanceThreshold(ctx, ProviderHero, "  "); err != nil {
		t.Fatal(err)
	}
	thresholds, err := svc.ListBalanceThresholds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(thresholds) != 0 {
		t.Fatalf("空值应清掉阈值, got %+v", thresholds)
	}
	// 非法值要拒绝，且不能悄悄写进去。
	for _, bad := range []string{"abc", "-1", "$5"} {
		if _, err := svc.SetBalanceThreshold(ctx, ProviderHero, bad); !errors.Is(err, ErrAlertConfigInvalid) {
			t.Errorf("%q 应被拒绝, got %v", bad, err)
		}
	}
	if _, err := svc.SetBalanceThreshold(ctx, "nobody", "5"); !errors.Is(err, ErrAlertConfigInvalid) {
		t.Errorf("未装配的供应商应被拒绝")
	}
}

// unknown 的含义是「不知道钱花没花出去」。超过宽限期还没人核对就要亮红条；
// 宽限期内不吵——刚发生的 unknown 常常几分钟内就被下一轮同步收敛掉了。
func TestEvaluateAlertsStaleUnknownOperations(t *testing.T) {
	store := newMemStore()
	svc := alertService(t, store)
	ctx := context.Background()

	fresh := Operation{
		ID: "op-fresh", Provider: ProviderHero, Kind: KindPurchase, State: StateUnknown,
		NeedsHumanReview: true, UpdatedAt: testNow.Add(-5 * time.Minute),
	}
	stale := Operation{
		ID: "op-stale", Provider: ProviderSMS62, Kind: KindPurchase, State: StateUnknown,
		NeedsHumanReview: true, UpdatedAt: testNow.Add(-2 * time.Hour),
	}
	settled := Operation{
		ID: "op-done", Provider: ProviderHero, Kind: KindPurchase, State: StateReconciledSucceeded,
		UpdatedAt: testNow.Add(-2 * time.Hour),
	}
	// 直接摆进台账：这里要测的是「怎么判超期」，不是台账怎么写进去的。
	for _, op := range []Operation{fresh, stale, settled} {
		store.ops[op.ID] = op
	}

	events, err := svc.EvaluateAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := eventOfKind(events, AlertUnknownStale)
	if !ok {
		t.Fatalf("超期的 unknown 应报警, got %+v", events)
	}
	if got.Subject != "op-stale" || got.Severity != SeverityCritical {
		t.Errorf("应指向那一笔且是最高档: %+v", got)
	}
	if len(events) != 1 {
		t.Fatalf("只有一笔超期, got %+v", events)
	}
}

// 租用号按小时计费、可延长；到期前一小时提醒，人还有时间决定要不要续。
// 普通激活号 20 分钟就过期，那是**常态**，报出来只会变成噪声。
func TestEvaluateAlertsRentExpiringOnlyForRentals(t *testing.T) {
	store := newMemStore()
	svc := alertService(t, store)
	ctx := context.Background()

	soon, _ := store.UpsertResource(ctx, Resource{
		Provider: ProviderHero, ExternalID: "rent-soon", Subtype: SubtypeRent,
		State: StateWaitingCode, ExpiresAt: testNow.Add(30 * time.Minute),
	})
	if _, err := store.UpsertResource(ctx, Resource{
		Provider: ProviderHero, ExternalID: "rent-later", Subtype: SubtypeRent,
		State: StateWaitingCode, ExpiresAt: testNow.Add(6 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertResource(ctx, Resource{
		Provider: ProviderHero, ExternalID: "act-soon", Subtype: SubtypeActivation,
		State: StateWaitingCode, ExpiresAt: testNow.Add(10 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	// 已经取消的租用号不提醒：它已经没用了。
	if _, err := store.UpsertResource(ctx, Resource{
		Provider: ProviderHero, ExternalID: "rent-cancelled", Subtype: SubtypeRent,
		State: StateCancelled, ExpiresAt: testNow.Add(20 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	events, err := svc.EvaluateAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := eventOfKind(events, AlertRentExpiring)
	if !ok {
		t.Fatalf("快到期的租用号应提醒, got %+v", events)
	}
	if got.Subject != soon || len(events) != 1 {
		t.Fatalf("只该提醒那一个租用号: %+v", events)
	}
}

// 同一个条件反复评估只有**一条**事件：每轮都插一行会让页面变成一串同样的
// 红条，然后人开始忽略它们。条件消失后自动收敛，不用人手动关。
func TestEvaluateAlertsDeduplicatesAndResolves(t *testing.T) {
	store := newMemStore()
	svc := alertService(t, store)
	ctx := context.Background()
	if _, err := svc.SetBalanceThreshold(ctx, ProviderHero, "5"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveBalanceSnapshot(ctx, BalanceSnapshot{Provider: ProviderHero, AmountText: "1.00", TakenAt: testNow}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		if _, err := svc.EvaluateAlerts(ctx); err != nil {
			t.Fatal(err)
		}
	}
	open, err := svc.ListOpenAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 {
		t.Fatalf("同一条件只该有一条事件, got %+v", open)
	}
	if !open[0].FirstSeenAt.Equal(testNow) || !open[0].LastSeenAt.Equal(testNow) {
		t.Errorf("首次与最近看见的时间都要有: %+v", open[0])
	}

	// 充值之后条件消失：事件自动收敛，页面上的红条跟着消失。
	if _, err := store.SaveBalanceSnapshot(ctx, BalanceSnapshot{Provider: ProviderHero, AmountText: "50.00", TakenAt: testNow.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EvaluateAlerts(ctx); err != nil {
		t.Fatal(err)
	}
	open, err = svc.ListOpenAlerts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 0 {
		t.Fatalf("条件消失后应收敛, got %+v", open)
	}
}

// **一律不外发**：告警只落库，通知器一次都不该被调用（外发等通知规范定稿）。
func TestEvaluateAlertsNeverNotifies(t *testing.T) {
	store := newMemStore()
	notifier := &countingNotifier{}
	store.status[ProviderHero] = ProviderStatus{Provider: ProviderHero, Enabled: true, VerifiedAt: testNow}
	svc := NewService([]Provider{{ID: ProviderHero, Adapter: &extrasFake{}}}, store, notifier,
		func() time.Time { return testNow })
	ctx := context.Background()
	if _, err := svc.SetBalanceThreshold(ctx, ProviderHero, "5"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveBalanceSnapshot(ctx, BalanceSnapshot{Provider: ProviderHero, AmountText: "0.10", TakenAt: testNow}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.EvaluateAlerts(ctx); err != nil {
		t.Fatal(err)
	}
	if notifier.calls != 0 {
		t.Fatalf("告警不外发，通知器不该被调用, got %d 次", notifier.calls)
	}
}

func TestAlertThresholdActionRegistered(t *testing.T) {
	store := newMemStore()
	svc := alertService(t, store)
	reg := action.NewRegistry()
	if err := RegisterActions(reg, svc); err != nil {
		t.Fatal(err)
	}
	def, handler, ok := reg.Lookup(ActionAlertSetBalanceThreshold, actionVersion)
	if !ok {
		t.Fatalf("%s 未注册", ActionAlertSetBalanceThreshold)
	}
	if def.RiskLevel != action.L1 || def.Permission != PermissionManage {
		t.Errorf("risk=%v perm=%q", def.RiskLevel, def.Permission)
	}
	params := map[string]any{"provider": ProviderHero, "min_amount": "5.5"}
	if err := def.Schema.Validate(params); err != nil {
		t.Fatalf("参数应通过 Schema: %v", err)
	}
	if _, err := handler(context.Background(), params); err != nil {
		t.Fatal(err)
	}
	thresholds, _ := svc.ListBalanceThresholds(context.Background())
	if len(thresholds) != 1 || thresholds[0].MinAmountText != "5.5" {
		t.Fatalf("阈值没写进去, got %+v", thresholds)
	}
	// 非法值翻成 INVALID_PARAMS，不是「执行失败」。
	_, err := handler(context.Background(), map[string]any{"provider": ProviderHero, "min_amount": "abc"})
	if action.ErrorCode(err) != action.CodeInvalidParams {
		t.Fatalf("应为 INVALID_PARAMS, got %v", err)
	}
}
