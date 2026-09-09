package finance_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// 本文件跑在真库上。订阅付款有一半不变量在 SQL 里：金额与日期的 CHECK、
// 损失行的部分唯一索引、代理与批次的外键、profit_daily 的账号级哨兵约束。
// 用内存假货复刻只会测到假货（算术那一半在 amortization_test.go）。
//
// 最要紧的两条只有真库测得出来：
//   - **损失行的 UPSERT**（终止后到账的退款要能重算它，而唯一索引是**部分**
//     索引，ON CONFLICT 必须带索引谓词——写错了会在运行时才炸）；
//   - **WriteAmortizedRow 收入未知也建行**，且下一轮不会把已读到的收入抹掉。

// subscriptionIntegrationAccount 造一个订阅型登记簿条目（无倍率、无 base_url）。
func subscriptionIntegrationAccount() finance.UpstreamAccount {
	a := integrationAccount()
	a.SystemType = finance.SystemOfficial
	a.AccessMethod = finance.AccessSubscriptionAccount
	a.BaseURL = ""
	// 零值 = **未配置**（不是「配置成 0」）：订阅型渠道本就不该有倍率，
	// 库层 CHECK 与领域校验都会拒绝给它配一个（§2.0）。
	a.RechargeRatio = money.Ratio{}
	a.PlatformID = "solo-prod"
	return a
}

func newSubscriptionFixture(t *testing.T) (*finance.SubscriptionStore, finance.UpstreamAccount) {
	t.Helper()
	pool := testPool(t)
	registry := finance.NewStore(pool)
	account := mustCreate(t, registry, subscriptionIntegrationAccount())
	return finance.NewSubscriptionStore(pool), account
}

func intBatch(accountID uuid.UUID) finance.SubscriptionBatch {
	return finance.SubscriptionBatch{
		UpstreamAccountID: accountID,
		PaidMinor:         29_990_000,
		SurchargeMinor:    1_000_000,
		Currency:          "USD",
		StartsOn:          day("2026-08-01"),
		ExpiresOn:         day("2026-08-31"),
		AccountCount:      2,
	}
}

func intProxy() finance.ProxyAsset {
	return finance.ProxyAsset{
		PaidMinor:          6_200_000,
		Currency:           "USD",
		OpenedOn:           day("2026-08-01"),
		ExpiresOn:          day("2026-08-31"),
		SharedAccountCount: 2,
		Mounted:            true,
		Environment:        intEnv,
	}
}

// TestBatchRoundTrip 钉住批次经库往返后逐字段不变——尤其是可空的日期与
// 可空的代理外键（sqlc 的可空 uuid 必须映射成指针，否则 uuid.Nil 会被写成
// 一个全零 UUID 而不是 NULL，外键当场炸）。
func TestBatchRoundTrip(t *testing.T) {
	subs, account := newSubscriptionFixture(t)
	ctx := context.Background()

	created, err := subs.CreateBatch(ctx, intBatch(account.ID))
	if err != nil {
		t.Fatalf("登记批次失败: %v", err)
	}
	if created.ProxyAssetID != uuid.Nil {
		t.Fatalf("未关联代理时应落 NULL，读回 %s", created.ProxyAssetID)
	}
	if !created.RefundedOn.IsZero() || !created.TerminatedOn.IsZero() {
		t.Fatal("未退款未终止时两个日期都该是零值")
	}

	got, err := subs.GetBatch(ctx, created.ID)
	if err != nil {
		t.Fatalf("取批次失败: %v", err)
	}
	if !got.StartsOn.Equal(day("2026-08-01")) || !got.ExpiresOn.Equal(day("2026-08-31")) {
		t.Fatalf("有效期往返出错: %s..%s", got.StartsOn, got.ExpiresOn)
	}
	if got.PaidMinor != 29_990_000 || got.SurchargeMinor != 1_000_000 {
		t.Fatalf("金额往返出错: %+v", got)
	}
	if got.AccountCount != 2 {
		t.Fatalf("账号数往返出错: %d", got.AccountCount)
	}
}

// TestBatchWithProxyRoundTrip：关联代理时外键存得进、读得回。
func TestBatchWithProxyRoundTrip(t *testing.T) {
	subs, account := newSubscriptionFixture(t)
	ctx := context.Background()

	proxy, err := subs.CreateProxy(ctx, intProxy())
	if err != nil {
		t.Fatalf("登记代理失败: %v", err)
	}
	batch := intBatch(account.ID)
	batch.ProxyAssetID = proxy.ID
	created, err := subs.CreateBatch(ctx, batch)
	if err != nil {
		t.Fatalf("登记批次失败: %v", err)
	}
	if created.ProxyAssetID != proxy.ID {
		t.Fatalf("代理关联往返出错: %s", created.ProxyAssetID)
	}

	// 取消关联要能落回 NULL
	cleared, err := subs.SetBatchProxy(ctx, created.ID, uuid.Nil)
	if err != nil {
		t.Fatalf("取消代理关联失败: %v", err)
	}
	if cleared.ProxyAssetID != uuid.Nil {
		t.Fatalf("取消关联后应为 NULL, got %s", cleared.ProxyAssetID)
	}
}

// TestTerminateBooksLoss 是本文件的核心：终止与结转必须在**同一个事务**里。
//
// 终止而没结转，那笔钱就从账上消失了；结转而没终止，摊销会继续算下去，
// 同一笔钱被记两遍。
func TestTerminateBooksLoss(t *testing.T) {
	subs, account := newSubscriptionFixture(t)
	ctx := context.Background()

	created, err := subs.CreateBatch(ctx, intBatch(account.ID))
	if err != nil {
		t.Fatalf("登记批次失败: %v", err)
	}
	terminated, loss, err := subs.TerminateBatch(ctx, created.ID, day("2026-08-11"))
	if err != nil {
		t.Fatalf("终止失败: %v", err)
	}
	if !terminated.TerminatedOn.Equal(day("2026-08-11")) {
		t.Fatalf("终止日往返出错: %s", terminated.TerminatedOn)
	}

	// 损失金额必须与算术层给的一致——两处各算一遍迟早会分叉
	want, err := terminated.Term().UnamortizedMinor()
	if err != nil {
		t.Fatalf("UnamortizedMinor: %v", err)
	}
	if loss.LossMinor != want {
		t.Fatalf("结转损失 = %d, 算术层给的是 %d", loss.LossMinor, want)
	}
	if !loss.BookedOn.Equal(day("2026-08-11")) {
		t.Fatalf("结转日应为终止日, got %s", loss.BookedOn)
	}
	if loss.BatchID != created.ID || loss.ProxyAssetID != uuid.Nil {
		t.Fatalf("损失主体应恰好是那笔批次: %+v", loss)
	}

	// 已摊 + 损失 ≡ 份额（第二条恒等式，在真库读回的字段上再验一遍）
	var amortized int64
	for d := terminated.StartsOn; !d.After(terminated.ExpiresOn); d = d.AddDate(0, 0, 1) {
		amount, covered, err := terminated.Term().DailyMinor(d)
		if err != nil {
			t.Fatalf("DailyMinor: %v", err)
		}
		if covered {
			amortized += amount
		}
	}
	share, err := terminated.Term().ShareMinor()
	if err != nil {
		t.Fatalf("ShareMinor: %v", err)
	}
	if amortized+loss.LossMinor != share {
		t.Fatalf("已摊 %d + 损失 %d ≠ 份额 %d", amortized, loss.LossMinor, share)
	}
}

// TestRefundAfterTerminationRewritesLoss 钉住损失行的 **UPSERT** 路径。
//
// 唯一索引是**部分**索引（WHERE batch_id IS NOT NULL），ON CONFLICT 必须带
// 同样的谓词才推断得出来——写漏了会在这一步炸「no unique constraint matching」，
// 而那只有真库测得出来。
//
// 语义上：先退订、后到账是常态，退款让最终成本基础变小，那笔已经结转的
// 损失当然要跟着变小。不重算的话，损失科目里会永远留着一个比实际多的数。
func TestRefundAfterTerminationRewritesLoss(t *testing.T) {
	subs, account := newSubscriptionFixture(t)
	ctx := context.Background()

	created, err := subs.CreateBatch(ctx, intBatch(account.ID))
	if err != nil {
		t.Fatalf("登记批次失败: %v", err)
	}
	_, before, err := subs.TerminateBatch(ctx, created.ID, day("2026-08-11"))
	if err != nil {
		t.Fatalf("终止失败: %v", err)
	}

	if _, err := subs.SetBatchRefund(ctx, created.ID, 4_000_000, day("2026-08-20")); err != nil {
		t.Fatalf("记退款失败: %v", err)
	}

	rows, _, err := subs.ListBatches(ctx, finance.SubscriptionBatchQuery{Environment: intEnv})
	if err != nil {
		t.Fatalf("列批次失败: %v", err)
	}
	if len(rows) != 1 || rows[0].LossMinor == nil {
		t.Fatalf("应有一笔带损失的批次: %+v", rows)
	}
	// 退款 4_000_000 由 2 个账号分摊 ⇒ 本账号份额少 2_000_000，损失等量减少
	if got := *rows[0].LossMinor; got != before.LossMinor-2_000_000 {
		t.Fatalf("损失应随退款减少：%d → %d（want %d）",
			before.LossMinor, got, before.LossMinor-2_000_000)
	}
	// 仍然只有一行：损失是派生事实不是事件流水
	if rows[0].LossBookedOn.IsZero() {
		t.Fatal("结转日不该被重算抹掉")
	}
}

// TestRefundIsMonotonic：累计退款额只增不减（§3.5）。
//
// 调小它等于凭空多算一笔成本，而那笔钱已经按旧基础摊进过去的台账行里了
// ——那些行冻结，改不了。
func TestRefundIsMonotonic(t *testing.T) {
	subs, account := newSubscriptionFixture(t)
	ctx := context.Background()

	created, err := subs.CreateBatch(ctx, intBatch(account.ID))
	if err != nil {
		t.Fatalf("登记批次失败: %v", err)
	}
	if _, err := subs.SetBatchRefund(ctx, created.ID, 5_000_000, day("2026-08-10")); err != nil {
		t.Fatalf("首次退款失败: %v", err)
	}
	_, err = subs.SetBatchRefund(ctx, created.ID, 3_000_000, day("2026-08-12"))
	if !errors.Is(err, finance.ErrRefundNotDecreasing) {
		t.Fatalf("调小累计退款额必须被拒, got %v", err)
	}
}

// TestTerminateIsOnce：终止是一次性事件，改终止日等于让一个已经出现在
// 报表上的损失悄悄变个数。
func TestTerminateIsOnce(t *testing.T) {
	subs, account := newSubscriptionFixture(t)
	ctx := context.Background()

	created, err := subs.CreateBatch(ctx, intBatch(account.ID))
	if err != nil {
		t.Fatalf("登记批次失败: %v", err)
	}
	if _, _, err := subs.TerminateBatch(ctx, created.ID, day("2026-08-11")); err != nil {
		t.Fatalf("首次终止失败: %v", err)
	}
	_, _, err = subs.TerminateBatch(ctx, created.ID, day("2026-08-20"))
	if !errors.Is(err, finance.ErrAlreadyTerminated) {
		t.Fatalf("重复终止必须被拒, got %v", err)
	}
}

// TestListAmortizableBatchesFiltersByDay 钉住摊销取数的三条谓词
// （§3.5：starts_on ≤ day ≤ expires_on 且未 terminated），
// 并验证代理随批次一起取回。
func TestListAmortizableBatchesFiltersByDay(t *testing.T) {
	subs, account := newSubscriptionFixture(t)
	ctx := context.Background()

	proxy, err := subs.CreateProxy(ctx, intProxy())
	if err != nil {
		t.Fatalf("登记代理失败: %v", err)
	}
	covering := intBatch(account.ID)
	covering.ProxyAssetID = proxy.ID
	if _, err := subs.CreateBatch(ctx, covering); err != nil {
		t.Fatalf("登记批次失败: %v", err)
	}
	past := intBatch(account.ID)
	past.StartsOn, past.ExpiresOn = day("2026-07-01"), day("2026-07-31")
	if _, err := subs.CreateBatch(ctx, past); err != nil {
		t.Fatalf("登记过期批次失败: %v", err)
	}

	got, err := subs.ListAmortizableBatches(ctx, account.ID, day("2026-08-10"))
	if err != nil {
		t.Fatalf("取摊销批次失败: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("只有覆盖当日的那一笔该回来, got %d", len(got))
	}
	if got[0].Proxy == nil || got[0].Proxy.ID != proxy.ID {
		t.Fatal("代理应随批次一起取回")
	}
	if !got[0].Proxy.Mounted {
		t.Fatal("挂载状态往返出错")
	}

	// 终止之后，**当天起**不再回来（§3.5：terminated_on..expires_on 转损失）
	if _, _, err := subs.TerminateBatch(ctx, got[0].Batch.ID, day("2026-08-10")); err != nil {
		t.Fatalf("终止失败: %v", err)
	}
	after, err := subs.ListAmortizableBatches(ctx, account.ID, day("2026-08-10"))
	if err != nil {
		t.Fatalf("取摊销批次失败: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("终止当天起不该再摊, got %d", len(after))
	}
}

// TestWriteAmortizedRowCreatesRowWithoutRevenue 钉住本模块对 §5.1 的
// **唯一一处偏离**，以及它在库层的具体形状。
//
// 第一轮：只有成本 → 建行（WriteRow 在同样情形下会返回 ErrProfitOneSidedNoRow）。
// 第二轮：仍然只有成本 → 上一轮已读到的收入**不被抹掉**（COALESCE 方向）。
func TestWriteAmortizedRowCreatesRowWithoutRevenue(t *testing.T) {
	pool := testPool(t)
	registry := finance.NewStore(pool)
	account := mustCreate(t, registry, subscriptionIntegrationAccount())
	ledger := finance.NewProfitStore(pool, func() time.Time { return profitClock })
	ctx := context.Background()

	cost := int64(967_419)
	row := finance.ProfitRow{
		UpstreamAccountID: account.ID,
		BusinessDay:       profitToday(),
		BusinessDayTZ:     account.BusinessDayTZ,
		TokenID:           finance.AccountGrainTokenID("acct-sub"),
		AccountID:         "acct-sub",
		CostMinor:         &cost,
		Currency:          "USD",
		RatioSnapshot:     finance.AmortizationRatio,
		Source:            "finance-collect-test",
	}

	// 对照组：同样一行走 WriteRow 会被 §5.1 拦下（只有一侧、台账无此行）
	if _, err := ledger.WriteRow(ctx, row); !errors.Is(err, finance.ErrProfitOneSidedNoRow) {
		t.Fatalf("计量型那条路应「不建行」, got %v", err)
	}

	stored, err := ledger.WriteAmortizedRow(ctx, row)
	if err != nil {
		t.Fatalf("摊销入账失败: %v", err)
	}
	if stored.CostMinor == nil || *stored.CostMinor != cost {
		t.Fatalf("成本 = %v, want %d", stored.CostMinor, cost)
	}
	if stored.RevenueMinor != nil {
		t.Fatalf("收入未知时必须是 NULL, got %v", stored.RevenueMinor)
	}
	if got := stored.RatioSnapshot.String(); got != "1" {
		t.Fatalf("ratio_snapshot = %q, want 1", got)
	}

	// 收入到位后再写一轮
	revenue := int64(12_345_600)
	withRevenue := row
	withRevenue.RevenueMinor = &revenue
	if _, err := ledger.WriteAmortizedRow(ctx, withRevenue); err != nil {
		t.Fatalf("带收入入账失败: %v", err)
	}
	// 下一轮又读不到收入了：**不得把已经读到的那个数抹掉**
	again, err := ledger.WriteAmortizedRow(ctx, row)
	if err != nil {
		t.Fatalf("再次入账失败: %v", err)
	}
	if again.RevenueMinor == nil || *again.RevenueMinor != revenue {
		t.Fatalf("收入应保留今天早些时候读到的值, got %v", again.RevenueMinor)
	}
	if again.RevenueObservedAt != nil {
		t.Fatal("这一轮没有收入读数，观测时刻该保持上一轮的（本例上一轮也没给）")
	}
}

// TestWriteAmortizedRowRejectsNonUnitRatio：摊销行的 ratio_snapshot 恒为 1
// （§12 拍板）。这道收窄让计量型的读取失败没法从这条路绕过 §5.1。
func TestWriteAmortizedRowRejectsNonUnitRatio(t *testing.T) {
	pool := testPool(t)
	registry := finance.NewStore(pool)
	account := mustCreate(t, registry, integrationAccount())
	ledger := finance.NewProfitStore(pool, func() time.Time { return profitClock })

	row := ledgerRow(account, "tok-a")
	row.RevenueMinor = nil // 只有成本
	if _, err := ledger.WriteAmortizedRow(context.Background(), row); !errors.Is(err, finance.ErrInconsistent) {
		t.Fatalf("非 1 倍率的行不该走摊销入账, got %v", err)
	}
}

// TestListProxiesCarriesLoss：代理列表带出已结转的损失，且金额可为负（贷记）。
func TestListProxiesCarriesLoss(t *testing.T) {
	subs, _ := newSubscriptionFixture(t)
	ctx := context.Background()

	proxy, err := subs.CreateProxy(ctx, intProxy())
	if err != nil {
		t.Fatalf("登记代理失败: %v", err)
	}
	rows, _, err := subs.ListProxies(ctx, intEnv, 0)
	if err != nil {
		t.Fatalf("列代理失败: %v", err)
	}
	if len(rows) != 1 || rows[0].LossMinor != nil {
		t.Fatalf("未终止的代理不该有损失: %+v", rows)
	}

	if _, _, err := subs.TerminateProxy(ctx, proxy.ID, day("2026-08-15")); err != nil {
		t.Fatalf("终止代理失败: %v", err)
	}
	rows, _, err = subs.ListProxies(ctx, intEnv, 0)
	if err != nil {
		t.Fatalf("列代理失败: %v", err)
	}
	if len(rows) != 1 || rows[0].LossMinor == nil {
		t.Fatalf("终止后应带出损失: %+v", rows)
	}
	if *rows[0].LossMinor <= 0 {
		t.Fatalf("8-15 终止的月代理应有正的未摊销额, got %d", *rows[0].LossMinor)
	}
}

// TestUpdateProxyKeepsAmountsFrozen：可编辑的只有挂载状态与购买信息。
//
// 金额与期间没有 UPDATE 路径——改它们会让历史台账与代理对不上
// （台账冻结、代理变了），而两边各自看起来都正常。
func TestUpdateProxyKeepsAmountsFrozen(t *testing.T) {
	subs, _ := newSubscriptionFixture(t)
	ctx := context.Background()

	created, err := subs.CreateProxy(ctx, intProxy())
	if err != nil {
		t.Fatalf("登记代理失败: %v", err)
	}
	desired := created
	desired.Mounted = false
	desired.BuyPlatform = "example-idc"
	desired.PaidMinor = 99_000_000 // 这一改不该生效

	updated, err := subs.UpdateProxy(ctx, desired)
	if err != nil {
		t.Fatalf("改代理失败: %v", err)
	}
	if updated.Mounted {
		t.Fatal("挂载状态应可改")
	}
	if updated.BuyPlatform != "example-idc" {
		t.Fatalf("购买平台应可改, got %q", updated.BuyPlatform)
	}
	if updated.PaidMinor != created.PaidMinor {
		t.Fatalf("金额登记后冻结, got %d want %d", updated.PaidMinor, created.PaidMinor)
	}
}

// TestPlatformIDRoundTrips：登记簿的归属标注（XM-0037c 新增列）经库往返，
// 且能被清空成 NULL。
//
// 它是采集器 PlatformResolver 的取值处——存不住这一列，037d 的四桶归集
// 就永远只剩「未归属」那一桶。
func TestPlatformIDRoundTrips(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	created, err := store.CreateAccount(ctx, subscriptionIntegrationAccount())
	if err != nil {
		t.Fatalf("登记账号失败: %v", err)
	}
	if created.PlatformID != "solo-prod" {
		t.Fatalf("platform_id 往返出错: %q", created.PlatformID)
	}

	cleared := created
	cleared.PlatformID = ""
	updated, err := store.UpdateAccount(ctx, cleared)
	if err != nil {
		t.Fatalf("清空归属失败: %v", err)
	}
	if updated.PlatformID != "" {
		t.Fatalf("空串应落 NULL 并读回空串, got %q", updated.PlatformID)
	}
}
