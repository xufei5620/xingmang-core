package finance_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
)

// 余额历史与看板供数跑在真库上（XM-0037d）。
//
// 三样只有真库测得出来：
//   - **游程编码**：值变了插新行、值没变只推 observed_at（§7 的「仅变化时落一条」）；
//   - `observed_at >= captured_at` 的库层 CHECK；
//   - 窗口聚合的 SQL 本身（覆盖行数、account_grain_rows 的 LIKE、
//     可用天数分母那条「不含今天」的区间）。

var summaryClock = time.Date(2026, 8, 28, 6, 0, 0, 0, time.UTC) // CST 14:00

func summaryToday() time.Time {
	return finance.BusinessDayAt(summaryClock, finance.DefaultBusinessDayLocation())
}

// summaryFixture 持有本用例的 pool。
//
// ⚠️ **pool 必须从这里传下去，不能再调一次 testPool**：那个助手每次调用都会
// TRUNCATE 整个 finance schema，第二次调用会把刚建好的账号一起清掉，
// 然后外键炸在一个看起来毫不相干的地方。
type summaryFixture struct {
	store    *finance.SummaryStore
	registry *finance.Store
	account  finance.UpstreamAccount
	pool     *pgxpool.Pool
}

func newSummaryFixture(t *testing.T) summaryFixture {
	t.Helper()
	pool := testPool(t)
	registry := finance.NewStore(pool)
	account := mustCreate(t, registry, integrationAccount())
	return summaryFixture{
		store:    finance.NewSummaryStore(pool, func() time.Time { return summaryClock }),
		registry: registry,
		account:  account,
		pool:     pool,
	}
}

// insertHistoricalProfitRow 直接写一行历史台账。
//
// **刻意绕开 ProfitStore**：它按 §5.3 拒绝写非今天的业务日（「今日可覆盖、
// 过去冻结」），而可用天数的分母恰恰要的是过去 7 个完整业务日。
// 用例要造的是「历史已经在那儿了」这个前提，不是去测写入路径——
// 那条纪律自己的用例在 profit_store_integration_test.go。
func (f summaryFixture) insertHistoricalProfitRow(
	t *testing.T, day time.Time, tokenID, ownAccountID string, revenue, cost *int64,
) {
	t.Helper()
	_, err := f.pool.Exec(context.Background(), `
		INSERT INTO finance.profit_daily (
			upstream_account_id, business_day, business_day_tz, token_id, account_id,
			revenue_minor, cost_minor, currency, ratio_snapshot, source, updated_at
		) VALUES ($1, $2, '+08:00', $3, $4, $5, $6, 'USD', 1.5, 'itest', now())`,
		f.account.ID, day, tokenID, ownAccountID, revenue, cost)
	if err != nil {
		t.Fatalf("写历史台账行失败: %v", err)
	}
}

// TestBalanceIsRunLengthEncoded 是余额这条链路的核心（§7）。
//
// 值没变时**不新增行**，只把游程的右端点推到此刻。一个一周没动过的余额
// 否则会变成 2000 行一模一样的记录；而只有 captured_at 的话，
// 那个健康账号会被判成「观测已过期」——把正常状态显示成故障。
func TestBalanceIsRunLengthEncoded(t *testing.T) {
	f := newSummaryFixture(t)
	ctx := context.Background()

	first, err := f.store.RecordBalance(ctx, finance.BalanceReading{
		UpstreamAccountID: f.account.ID, BalanceMinor: 420_000_000, Currency: "USD",
		ObservedAt: summaryClock.Add(-2 * time.Hour), Source: "itest",
	})
	if err != nil {
		t.Fatalf("首条余额: %v", err)
	}
	if !first.CapturedAt.Equal(first.ObservedAt) {
		t.Fatal("新开的游程两端应重合")
	}

	// 同一个值再来一轮：只推 observed_at，不新增行
	same, err := f.store.RecordBalance(ctx, finance.BalanceReading{
		UpstreamAccountID: f.account.ID, BalanceMinor: 420_000_000, Currency: "USD",
		ObservedAt: summaryClock, Source: "itest",
	})
	if !errors.Is(err, finance.ErrBalanceUnchanged) {
		t.Fatalf("值没变应报 ErrBalanceUnchanged, got %v", err)
	}
	if same.ID != first.ID {
		t.Fatalf("不该新增行：id %d → %d", first.ID, same.ID)
	}
	if !same.ObservedAt.Equal(summaryClock) {
		t.Fatalf("observed_at 应被推到此刻, got %s", same.ObservedAt)
	}
	if !same.CapturedAt.Equal(first.CapturedAt) {
		t.Fatal("captured_at 不该动——它记的是「什么时候变成这个数的」")
	}

	// 值变了：开新游程
	changed, err := f.store.RecordBalance(ctx, finance.BalanceReading{
		UpstreamAccountID: f.account.ID, BalanceMinor: 410_000_000, Currency: "USD",
		ObservedAt: summaryClock.Add(time.Hour), Source: "itest",
	})
	if err != nil {
		t.Fatalf("值变了应正常写入: %v", err)
	}
	if changed.ID == first.ID {
		t.Fatal("值变了必须新开一行")
	}

	latest, err := f.store.LatestBalance(ctx, f.account.ID)
	if err != nil {
		t.Fatalf("取最新余额: %v", err)
	}
	if latest.BalanceMinor != 410_000_000 {
		t.Fatalf("最新余额 = %d", latest.BalanceMinor)
	}
}

// TestBalanceCurrencyChangeOpensNewRun：币种变了也算「变了」。
//
// 一个从 USD 改成 CNY 的账号，金额数字可能恰好没变，但那绝不是
// 「余额没变化」——它是一次口径变更，必须落成新的一行。
func TestBalanceCurrencyChangeOpensNewRun(t *testing.T) {
	f := newSummaryFixture(t)
	ctx := context.Background()

	first, err := f.store.RecordBalance(ctx, finance.BalanceReading{
		UpstreamAccountID: f.account.ID, BalanceMinor: 100_000_000, Currency: "USD",
		ObservedAt: summaryClock.Add(-time.Hour), Source: "itest",
	})
	if err != nil {
		t.Fatalf("首条余额: %v", err)
	}
	second, err := f.store.RecordBalance(ctx, finance.BalanceReading{
		UpstreamAccountID: f.account.ID, BalanceMinor: 100_000_000, Currency: "CNY",
		ObservedAt: summaryClock, Source: "itest",
	})
	if err != nil {
		t.Fatalf("币种变了应正常写入: %v", err)
	}
	if second.ID == first.ID {
		t.Fatal("币种变了必须新开一行——数字一样不代表余额没变")
	}
}

// TestBalanceObservedAtOnlyMovesForward：慢半拍的一轮不该把观测时刻往回拨。
func TestBalanceObservedAtOnlyMovesForward(t *testing.T) {
	f := newSummaryFixture(t)
	ctx := context.Background()

	if _, err := f.store.RecordBalance(ctx, finance.BalanceReading{
		UpstreamAccountID: f.account.ID, BalanceMinor: 1_000_000, Currency: "USD",
		ObservedAt: summaryClock, Source: "itest",
	}); err != nil {
		t.Fatalf("首条余额: %v", err)
	}
	stale, err := f.store.RecordBalance(ctx, finance.BalanceReading{
		UpstreamAccountID: f.account.ID, BalanceMinor: 1_000_000, Currency: "USD",
		ObservedAt: summaryClock.Add(-time.Hour), Source: "itest",
	})
	if !errors.Is(err, finance.ErrBalanceUnchanged) {
		t.Fatalf("值没变仍应报 ErrBalanceUnchanged, got %v", err)
	}
	if stale.ObservedAt.Before(summaryClock) {
		t.Fatalf("观测时刻被往回拨了: %s", stale.ObservedAt)
	}
}

// TestSummaryAggregatesWindowAndCoverage 验窗口聚合的 SQL 本身。
//
// 覆盖行数与金额一起给：只给和不给覆盖行数的话，「三行里只有两行有成本」
// 与「三行都有成本」会给出同一种呈现（宪法 12 条）。
func TestSummaryAggregatesWindowAndCoverage(t *testing.T) {
	f := newSummaryFixture(t)
	ctx := context.Background()
	today := summaryToday()

	rev, cost := int64(12_000_000), int64(4_000_000)
	f.insertHistoricalProfitRow(t, today, "tok-a", "acct-a", &rev, &cost)
	// 账号级聚合行（XM-0037c 的哨兵）——SQL 里那条 LIKE 'account:%' 要数到它
	f.insertHistoricalProfitRow(t, today,
		finance.AccountGrainTokenID("acct-b"), "acct-b", &rev, &cost)
	// 只有成本、没有收入的一行：覆盖率因此不满
	f.insertHistoricalProfitRow(t, today, "tok-c", "acct-c", nil, &cost)

	items, err := f.store.ChannelSummaries(ctx, finance.SummaryQuery{
		Environment: intEnv, From: today, To: today, Thresholds: finance.DefaultRunwayThresholds(),
	})
	if err != nil {
		t.Fatalf("渠道摘要: %v", err)
	}
	var got finance.ChannelSummary
	for _, item := range items {
		if item.Account.ID == f.account.ID {
			got = item
		}
	}
	if got.Window.RowCount != 3 {
		t.Fatalf("行数 = %d, want 3", got.Window.RowCount)
	}
	if got.Window.CostKnownRows != 3 || got.Window.RevenueKnownRows != 2 {
		t.Fatalf("覆盖行数不对: %+v", got.Window)
	}
	if got.Window.AccountGrainRows != 1 {
		t.Fatalf("账号级聚合行数 = %d, want 1", got.Window.AccountGrainRows)
	}
	// 成本覆盖满 → 给得出；收入缺一行 → 给不出；毛利因而也给不出
	if c := got.Window.CostMinor(); c == nil || *c != 12_000_000 {
		t.Fatalf("成本 = %v, want 12000000", c)
	}
	if got.Window.RevenueMinor() != nil {
		t.Fatal("收入覆盖不全时必须给不出——偏低的和长得和完整的一模一样")
	}
	if got.Window.GrossProfitMinor() != nil {
		t.Fatal("任一侧给不出，毛利就给不出")
	}
	if got.Window.OldestCostObservedAt != nil {
		t.Fatal("本用例没写观测时刻，应保持为空而不是零时间")
	}
}

// TestSummaryListsAccountsWithoutLedgerRows：登记簿里有、台账里没有的账号
// 要出现在列表里，显示成「今天没有数据」。
//
// 从列表里消失会让人以为它被删了（宪法 12 条）。
func TestSummaryListsAccountsWithoutLedgerRows(t *testing.T) {
	f := newSummaryFixture(t)
	today := summaryToday()

	items, err := f.store.ChannelSummaries(context.Background(), finance.SummaryQuery{
		Environment: intEnv, From: today, To: today, Thresholds: finance.DefaultRunwayThresholds(),
	})
	if err != nil {
		t.Fatalf("渠道摘要: %v", err)
	}
	if len(items) != 1 || items[0].Account.ID != f.account.ID {
		t.Fatalf("没有台账行的账号也该出现: %+v", items)
	}
	w := items[0].Window
	if w.RowCount != 0 || w.RevenueMinor() != nil || w.CostMinor() != nil {
		t.Fatal("空窗口给不出金额——那是未知，不是 0")
	}
	if !w.From.Equal(today) || !w.To.Equal(today) {
		t.Fatalf("空窗口也要带上区间（否则前端会渲染 0001-01-01）: %s..%s", w.From, w.To)
	}
}

// TestRunwayUsesCompleteDaysExcludingToday 钉住可用天数分母的窗口。
//
// 今天还在累积，算进去会让日均偏低、可用天数虚高，而且那个虚高幅度
// 每天早上最大、随时间缩小——一个每天规律性说谎的预警值。
func TestRunwayUsesCompleteDaysExcludingToday(t *testing.T) {
	f := newSummaryFixture(t)
	ctx := context.Background()
	today := summaryToday()

	// 过去 7 个完整业务日各 $4 成本
	cost := int64(4_000_000)
	for i := 1; i <= finance.RunwayWindowDays; i++ {
		f.insertHistoricalProfitRow(t, today.AddDate(0, 0, -i),
			"tok-a", "acct-a", nil, &cost)
	}
	// 今天再来一大笔——**不该进分母**
	huge := int64(400_000_000)
	f.insertHistoricalProfitRow(t, today, "tok-a", "acct-a", nil, &huge)

	if _, err := f.store.RecordBalance(ctx, finance.BalanceReading{
		UpstreamAccountID: f.account.ID, BalanceMinor: 420_000_000, Currency: "USD",
		ObservedAt: summaryClock.Add(-time.Minute), Source: "itest",
	}); err != nil {
		t.Fatalf("记余额: %v", err)
	}

	items, err := f.store.UpstreamSummaries(ctx, finance.SummaryQuery{
		Environment: intEnv, From: today, To: today, Thresholds: finance.DefaultRunwayThresholds(),
	})
	if err != nil {
		t.Fatalf("上游摘要: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("应有一条, got %d", len(items))
	}
	got := items[0].Runway
	if !got.Known() {
		t.Fatalf("应算得出天数, reason=%s", got.Reason)
	}
	if got.CoveredDays != finance.RunwayWindowDays {
		t.Fatalf("覆盖天数 = %d, want %d", got.CoveredDays, finance.RunwayWindowDays)
	}
	if got.DailyAverageMinor == nil || *got.DailyAverageMinor != 4_000_000 {
		t.Fatalf("日均 = %v, want 4000000（今天那笔不该进分母）", got.DailyAverageMinor)
	}
	if *got.Days != 105 {
		t.Fatalf("可用天数 = %d, want 105", *got.Days)
	}
}

// TestRunwayUnknownWithoutBalance：没读到余额时给不出天数，但**给得出原因**。
//
// 这是当前的常态（§7 覆盖率边界：两个真实驱动的余额读取都还没接通），
// 所以它必须是一个说得清的状态，而不是一个空位。
func TestRunwayUnknownWithoutBalance(t *testing.T) {
	f := newSummaryFixture(t)
	today := summaryToday()

	items, err := f.store.UpstreamSummaries(context.Background(), finance.SummaryQuery{
		Environment: intEnv, From: today, To: today, Thresholds: finance.DefaultRunwayThresholds(),
	})
	if err != nil {
		t.Fatalf("上游摘要: %v", err)
	}
	if items[0].Runway.Known() {
		t.Fatal("没有余额时不该算出天数")
	}
	if items[0].Runway.Reason != finance.RunwayReasonNoBalance {
		t.Fatalf("原因 = %s, want no_balance", items[0].Runway.Reason)
	}
	coverage := finance.SummarizeRunwayCoverage(items)
	if coverage.Total != 1 || coverage.Known != 0 {
		t.Fatalf("覆盖率应是 0/1: %+v", coverage)
	}
}

// TestBalanceRejectsObservedBeforeCaptured 钉住库层 CHECK。
//
// 游程的两端写反了会让「这个值持续了多久」变成负数，
// 而那个负数会一路传到可用天数的新鲜度判定里。
func TestBalanceRejectsObservedBeforeCaptured(t *testing.T) {
	f := newSummaryFixture(t)
	_, err := f.pool.Exec(context.Background(), `
		INSERT INTO finance.balance_history (
			upstream_account_id, balance_minor, currency, captured_at, observed_at, source
		) VALUES ($1, 1, 'USD', now(), now() - interval '1 hour', 'itest')`, f.account.ID)
	if err == nil {
		t.Fatal("observed_at 早于 captured_at 必须被库层拒绝")
	}
}
