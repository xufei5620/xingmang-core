package finance_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
)

// 订阅型那一轮采集的行为（XM-0037c，设计稿 §3.5）。计量型那一轮在 collector_test.go。
//
// 三条与计量型不同的地方，每条各有一组用例：
//
//	成本不是读来的     没有「成本读失败」，只有「当天没有批次覆盖」（未知）
//	行是账号级的       token_id 用 account: 哨兵，ratio_snapshot 恒为 1
//	收入未知照样建行   §5.1 的唯一一处偏离，理由见 ProfitStore.WriteAmortizedRow

// subscriptionAccount 造一个订阅型登记簿条目。
//
// **没有 recharge_ratio**：订阅型渠道的成本走 §3.5 摊销，登记簿的库层 CHECK
// 与领域校验都拒绝给它配倍率（§2.0）。
// **没有 base_url**：订阅账号常常没有可读端点，那正是「只有成本没有收入」
// 这条路径的现实来源。
func subscriptionAccount(id uuid.UUID) finance.UpstreamAccount {
	return finance.UpstreamAccount{
		ID:            id,
		SystemType:    finance.SystemOfficial,
		AccessMethod:  finance.AccessSubscriptionAccount,
		CredentialRef: collectAccountRef,
		Currency:      "USD",
		BusinessDayTZ: finance.DefaultBusinessDayTZ,
		Status:        finance.StatusActive,
		Environment:   "production",
	}
}

// coveringBatch 是一笔覆盖 collectDay（2026-08-28）的月订阅：
// $29.99、8-01 开、8-31 到期、一个账号独享。
func coveringBatch(accountID uuid.UUID) finance.SubscriptionBatch {
	return finance.SubscriptionBatch{
		ID:                uuid.New(),
		UpstreamAccountID: accountID,
		PaidMinor:         29_990_000,
		Currency:          "USD",
		StartsOn:          day("2026-08-01"),
		ExpiresOn:         day("2026-08-31"),
		AccountCount:      1,
	}
}

func subscriptionFixture(accountID uuid.UUID) *collectFixture {
	f := newFixture()
	f.registry.subscriptionAccounts = []finance.UpstreamAccount{subscriptionAccount(accountID)}
	f.registry.mappings[accountID] = []finance.TokenMapping{
		{UpstreamAccountID: accountID, UpstreamTokenID: "seat-1", OwnAccountID: "acct-sub"},
	}
	return f
}

func amortizedRowKey(accountID uuid.UUID, owner string) string {
	return fmt.Sprintf("%s|%s|%s", accountID, collectDay, finance.AccountGrainTokenID(owner))
}

// TestAmortizeWritesAccountGrainRow 是订阅型的主路径：
// 当日摊销进 cost_minor、ratio_snapshot = 1、token_id 用账号级哨兵。
func TestAmortizeWritesAccountGrainRow(t *testing.T) {
	id := uuid.New()
	f := subscriptionFixture(id)
	batch := coveringBatch(id)
	f.subs.batches[id] = []finance.AmortizableBatch{{Batch: batch}}

	result, err := f.collector().CollectOnce(context.Background())
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if result.SubscriptionAccountsTotal != 1 || result.SubscriptionRowsWritten != 1 {
		t.Fatalf("计数不对: %+v", result)
	}

	row, ok := f.ledger.rows[amortizedRowKey(id, "acct-sub")]
	if !ok {
		t.Fatal("订阅账号应写出账号级摊销行")
	}
	// $29.99 摊 31 天，8-28 是第 28 天（非末日）：29990000/31 截断 = 967419
	if row.CostMinor == nil || *row.CostMinor != 967_419 {
		t.Fatalf("当日摊销 = %v, want 967419", row.CostMinor)
	}
	if got := row.RatioSnapshot.String(); got != "1" {
		t.Fatalf("ratio_snapshot = %q, want 1（§12 拍板：摊销值即成本，未经折算）", got)
	}
	if row.AccountID != "acct-sub" {
		t.Fatalf("account_id = %q", row.AccountID)
	}
	if row.CostObservedAt == nil {
		t.Fatal("摊销值也要带观测时刻——它是此刻现算的，留空会显示成「从未采到」")
	}
}

// TestAmortizeWritesRowWithoutRevenue 钉住本模块对 §5.1 的**唯一一处偏离**。
//
// 订阅账号没有 base_url ⇒ 没有收入通道（v1 的常态）。此时仍然建行：
// 成本已知、收入 NULL、毛利 NULL。压住不写的代价是平台自己承诺的支出
// 在收入通道接上之前完全不可见——那比「毛利未知」更不诚实。
func TestAmortizeWritesRowWithoutRevenue(t *testing.T) {
	id := uuid.New()
	f := subscriptionFixture(id)
	f.subs.batches[id] = []finance.AmortizableBatch{{Batch: coveringBatch(id)}}

	result, err := f.collector().CollectOnce(context.Background())
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if result.SubscriptionRowsCostOnly != 1 {
		t.Fatalf("只有成本的行必须单独计数（看板要说得出毛利为什么全是 NULL）: %+v", result)
	}

	row := f.ledger.rows[amortizedRowKey(id, "acct-sub")]
	if row.RevenueMinor != nil {
		t.Fatalf("没有收入通道时收入必须是 NULL（未知），got %v", row.RevenueMinor)
	}
	if row.ProfitMinor() != nil {
		t.Fatal("收入未知 ⇒ 毛利未知，不该是「等于负成本」")
	}
}

// TestAmortizeSkipsWhenNoCoveringBatch：当日没有批次覆盖 → 成本**未知**，
// 不是 0，一行都不写（§5.1 的同一条纪律）。
//
// 「还没登记批次」与「订阅真的到期了」在库里长得一样，平台分不出来。
// 写 0 会让这条渠道显示一个笃定的「零成本、毛利 = 收入」。
func TestAmortizeSkipsWhenNoCoveringBatch(t *testing.T) {
	id := uuid.New()
	f := subscriptionFixture(id)
	expired := coveringBatch(id)
	expired.StartsOn, expired.ExpiresOn = day("2026-07-01"), day("2026-07-31")
	f.subs.batches[id] = []finance.AmortizableBatch{{Batch: expired}}

	result, err := f.collector().CollectOnce(context.Background())
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if result.RowsSkippedNoBatch != 1 || result.SubscriptionRowsWritten != 0 {
		t.Fatalf("无覆盖批次应跳过并计数: %+v", result)
	}
	if len(f.ledger.rows) != 0 {
		t.Fatal("成本未知时一个字都不该写")
	}
	if !result.Partial {
		t.Fatal("跳过必须标记 partial —— 缺一块要说清楚")
	}
}

// TestAmortizeSkipsWhenOwnerUnresolved：摊销成本是账号级的一笔钱，
// 必须记在**某一个**自营账号头上。零个映射（还没配）与多个不同的自营账号
// （一笔订阅摊给几个自营账号的口径未定）都给不出那个头。
func TestAmortizeSkipsWhenOwnerUnresolved(t *testing.T) {
	for name, mappings := range map[string][]finance.TokenMapping{
		"没有映射": nil,
		"多个自营账号": {
			{UpstreamTokenID: "seat-1", OwnAccountID: "acct-a"},
			{UpstreamTokenID: "seat-2", OwnAccountID: "acct-b"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			id := uuid.New()
			f := subscriptionFixture(id)
			for i := range mappings {
				mappings[i].UpstreamAccountID = id
			}
			f.registry.mappings[id] = mappings
			f.subs.batches[id] = []finance.AmortizableBatch{{Batch: coveringBatch(id)}}

			result, err := f.collector().CollectOnce(context.Background())
			if err != nil {
				t.Fatalf("采集失败: %v", err)
			}
			if result.RowsSkippedNoOwner != 1 {
				t.Fatalf("归属不出唯一自营账号应跳过并计数: %+v", result)
			}
			if len(f.ledger.rows) != 0 {
				t.Fatal("归属不明时不该入账")
			}
		})
	}
}

// TestAmortizeIncludesProxyCost：当日成本 = 订阅摊销 + 代理摊销（§3.5）。
func TestAmortizeIncludesProxyCost(t *testing.T) {
	id := uuid.New()
	f := subscriptionFixture(id)
	proxy := mountedProxy() // $6.20 / 2 账号 / 31 天 = 100000 微单位/天
	batch := coveringBatch(id)
	batch.ProxyAssetID = proxy.ID
	f.subs.batches[id] = []finance.AmortizableBatch{{Batch: batch, Proxy: &proxy}}

	if _, err := f.collector().CollectOnce(context.Background()); err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	row := f.ledger.rows[amortizedRowKey(id, "acct-sub")]
	if row.CostMinor == nil || *row.CostMinor != 967_419+100_000 {
		t.Fatalf("当日成本 = %v, want %d（订阅 + 代理）", row.CostMinor, 967_419+100_000)
	}
}

// TestAmortizeUnmountedProxyCostsNothing 钉住 §10.3 的「未挂载 = 0」：
// 代理在期内但没挂上，成本明确是 0，不猜、也不因此让整行未知。
func TestAmortizeUnmountedProxyCostsNothing(t *testing.T) {
	id := uuid.New()
	f := subscriptionFixture(id)
	proxy := mountedProxy()
	proxy.Mounted = false
	batch := coveringBatch(id)
	batch.ProxyAssetID = proxy.ID
	f.subs.batches[id] = []finance.AmortizableBatch{{Batch: batch, Proxy: &proxy}}

	if _, err := f.collector().CollectOnce(context.Background()); err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	row := f.ledger.rows[amortizedRowKey(id, "acct-sub")]
	if row.CostMinor == nil || *row.CostMinor != 967_419 {
		t.Fatalf("未挂载的代理不该产生成本: %v", row.CostMinor)
	}
}

// TestAmortizeIsolatesAccountFailures：一个账号的批次配坏了（币种混杂）
// 不该让其余订阅账号今天也没有数——与计量型完全对称的失败隔离。
func TestAmortizeIsolatesAccountFailures(t *testing.T) {
	bad, good := uuid.New(), uuid.New()
	f := newFixture()
	f.registry.subscriptionAccounts = []finance.UpstreamAccount{
		subscriptionAccount(bad), subscriptionAccount(good),
	}
	f.registry.mappings[bad] = []finance.TokenMapping{
		{UpstreamAccountID: bad, UpstreamTokenID: "seat-1", OwnAccountID: "acct-bad"},
	}
	f.registry.mappings[good] = []finance.TokenMapping{
		{UpstreamAccountID: good, UpstreamTokenID: "seat-1", OwnAccountID: "acct-good"},
	}
	cny := coveringBatch(bad)
	cny.Currency = "CNY"
	f.subs.batches[bad] = []finance.AmortizableBatch{
		{Batch: coveringBatch(bad)}, {Batch: cny},
	}
	f.subs.batches[good] = []finance.AmortizableBatch{{Batch: coveringBatch(good)}}

	result, err := f.collector().CollectOnce(context.Background())
	if err != nil {
		t.Fatalf("单个账号失败不该让整轮失败: %v", err)
	}
	if result.AccountsFailed != 1 {
		t.Fatalf("币种混杂的账号应计为失败: %+v", result)
	}
	if result.SubscriptionRowsWritten != 1 {
		t.Fatalf("健康账号应照常入账: %+v", result)
	}
	if _, ok := f.ledger.rows[amortizedRowKey(good, "acct-good")]; !ok {
		t.Fatal("健康账号的行不见了")
	}
}

// TestCollectorRequiresSubscriptionRegistry：装配漏了订阅取数端时**整轮失败**，
// 而不是安静地跳过订阅型那一轮。
//
// 跳过的话，报表上那几条订阅渠道的成本会全部消失，而毛利恰好等于收入
// ——一个看起来完全正常的错数字（宪法 12 条）。
func TestCollectorRequiresSubscriptionRegistry(t *testing.T) {
	collector := finance.NewCollector(finance.CollectorOptions{
		Logger:      quietLogger(),
		Environment: "production",
		InstanceID:  "finance-collect-test",
		Registry:    &memRegistry{mappings: map[uuid.UUID][]finance.TokenMapping{}},
		Ledger:      newMemLedger(),
		NewClient:   finance.NewFakeMeteringClientFactory(func() time.Time { return collectNow }),
		Now:         func() time.Time { return collectNow },
	})
	if _, err := collector.CollectOnce(context.Background()); err == nil {
		t.Fatal("缺少订阅取数端必须整轮失败，不得静默跳过订阅型渠道")
	}
}

// TestFakeSubscriptionChainEndToEnd 是 brief 要求的 fake 演示：
// **登记几笔订阅批次（含代理、含续费重叠）→ 摊销 → 入账**，整条链路跑通。
//
// 它同时覆盖两个只有在多批次下才出现的口径：
//   - 续费重叠时，当日成本是**两笔批次之和**（§3.5：不覆盖历史）；
//   - 两笔批次引用同一份代理时，那份代理当日只算**一次**（它就一份）。
//
// 生产禁 fake 的闸沿用 037b（jobs.Config.validate 启动即拒），本用例只跑
// staging 语义的内存链路。
func TestFakeSubscriptionChainEndToEnd(t *testing.T) {
	id := uuid.New()
	f := subscriptionFixture(id)

	proxy := mountedProxy()
	// 旧批次：8-01..8-31（$29.99）；续费批次：8-20..9-19（$39.99），两者在 8-28 重叠
	old := coveringBatch(id)
	old.ProxyAssetID = proxy.ID
	renewal := finance.SubscriptionBatch{
		ID:                uuid.New(),
		UpstreamAccountID: id,
		PaidMinor:         39_990_000,
		Currency:          "USD",
		StartsOn:          day("2026-08-20"),
		ExpiresOn:         day("2026-09-19"),
		AccountCount:      1,
		ProxyAssetID:      proxy.ID,
	}
	f.subs.batches[id] = []finance.AmortizableBatch{
		{Batch: old, Proxy: &proxy},
		{Batch: renewal, Proxy: &proxy},
	}

	result, err := f.collector().CollectOnce(context.Background())
	if err != nil {
		t.Fatalf("fake 订阅链路应跑得通: %v", err)
	}
	if result.SubscriptionRowsWritten != 1 {
		t.Fatalf("同一账号当日只该有一行（账号级）: %+v", result)
	}

	row := f.ledger.rows[amortizedRowKey(id, "acct-sub")]
	// old: 29990000/31 截断 = 967419；renewal: 39990000/31 截断 = 1290000；代理 100000
	want := int64(967_419 + 1_290_000 + 100_000)
	if row.CostMinor == nil || *row.CostMinor != want {
		t.Fatalf("当日成本 = %v, want %d（两笔批次之和 + 一份代理，代理不得算两遍）",
			row.CostMinor, want)
	}
	if days := result.BusinessDays(); len(days) != 0 {
		// 摊销不产生上游读数，所以 BusinessDays（取自 Costs/Revenues）为空。
		// 钉住它是为了让「订阅型不进 metering 观测」这件事被写下来，
		// 而不是某天有人以为观测漏了。
		t.Fatalf("摊销不该产生上游读数, got %v", days)
	}
}
