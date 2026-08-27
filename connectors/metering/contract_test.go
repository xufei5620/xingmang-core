package metering_test

import (
	"testing"
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/metering"
	"github.com/xufei5620/xingmang-platform/connectors/metering/contracttest"
	"github.com/xufei5620/xingmang-platform/connectors/sub2api"
	"github.com/xufei5620/xingmang-platform/internal/platform/money"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// TestFakeSatisfiesContract 让 Fake 成为套件的第一个被测实现——
// 套件本身要先被验证有效。
func TestFakeSatisfiesContract(t *testing.T) {
	contracttest.RunSuite(t, func(opts metering.FakeOptions) metering.ReadClient {
		return metering.NewFake(opts)
	})
}

// TestFakePropagatesPartial 单独跑那条不在共享套件里的断言（理由见
// contracttest.AssertPartialPropagates）。
func TestFakePropagatesPartial(t *testing.T) {
	contracttest.AssertPartialPropagates(t, func(opts metering.FakeOptions) metering.ReadClient {
		return metering.NewFake(opts)
	})
}

// TestMetricKeysDoNotCollideWithPanelCost 是设计稿 §3.1 那条显著警告的
// 可执行形态。
//
// `sub2api.cost.daily` 读的是 admin 面板 `trend[].cost`（自营实例口径，
// 刻意避开 actual_cost）；本包的 `finance.cost.daily` 是核算成本口径
// （每令牌 /v1/usage actual_cost ÷ recharge_ratio）。两个数同名不同义，
// 差着一个倍率和一整套语义。任何让它们相等或互相复用的改动，
// 都会让毛利在看板上静静地错着——这条断言让那种改动当场失败。
func TestMetricKeysDoNotCollideWithPanelCost(t *testing.T) {
	if metering.MetricCostDaily == sub2api.MetricCostDaily {
		t.Fatalf("核算成本指标不得与自营面板成本指标同名（都是 %q）："+
			"前者是 actual_cost ÷ 倍率，后者是面板 trend[].cost（§3.1）",
			metering.MetricCostDaily)
	}
	if metering.MetricRevenueDaily == sub2api.MetricRevenueDaily {
		t.Fatalf("核算收入指标不得与自营面板收入指标同名（都是 %q）",
			metering.MetricRevenueDaily)
	}
	// 前缀也要分开：`finance.` 与 `sub2api.` 分属两个口径域，
	// 混在一个前缀下迟早有人按前缀批量查询然后把两套数加起来
	for _, containsPanelPrefix := range []string{
		metering.MetricCostDaily, metering.MetricRevenueDaily,
	} {
		if len(containsPanelPrefix) < 8 || containsPanelPrefix[:8] != "finance." {
			t.Fatalf("核算口径指标 %q 应在 finance. 前缀下（设计稿 §8.2）", containsPanelPrefix)
		}
	}
}

// TestMetricKeysAreRegistered 与 ops 白名单对齐。
//
// 白名单落后于契约时，新指标会被 /metrics/history 判成「未注册」直接 400，
// 而 /metrics 照样返回它——同一个指标在两个端点上是两种事实。
func TestMetricKeysAreRegistered(t *testing.T) {
	for _, k := range []string{metering.MetricCostDaily, metering.MetricRevenueDaily} {
		if !ops.KnownMetricKey(k) {
			t.Fatalf("契约指标 %q 不在 ops 白名单里", k)
		}
	}
}

// TestCostOfMatchesDesignWorkedExample 复核 §2.4 的标准答案走完整条链路。
func TestCostOfMatchesDesignWorkedExample(t *testing.T) {
	usage := metering.TokenUsage{
		UpstreamTokenID: "tok-1",
		Day:             "2026-08-28",
		UsageMinorUnits: 5_813_729, // actual_cost = "5.813729"
		Currency:        "USD",
	}
	cost, err := metering.CostOf(usage, money.MustParseRatio("1.5"))
	if err != nil {
		t.Fatalf("折算失败: %v", err)
	}
	if cost.CostMinorUnits != 3_875_819 {
		t.Fatalf("成本 = %d 微单位, want 3875819（§2.4 worked example）", cost.CostMinorUnits)
	}
	// 倍率必须被逐条冻结（§6.3）：037b 要拿它写 profit_daily.ratio_snapshot
	if cost.RatioSnapshot.String() != "1.5" {
		t.Fatalf("ratio_snapshot = %q, want \"1.5\"", cost.RatioSnapshot)
	}
	// 原始实扣照样带出去：出问题时要能分清「上游扣多了」与「倍率改了」
	if cost.UsageMinorUnits != usage.UsageMinorUnits {
		t.Fatalf("折算不该改动原始实扣")
	}
	cents, err := money.Rescale(cost.CostMinorUnits, metering.UsageScale, 2)
	if err != nil {
		t.Fatalf("折到分失败: %v", err)
	}
	if cents != 388 {
		t.Fatalf("折到分 = %d, want 388（$3.88，与 SoloAI 一致）", cents)
	}
}

// TestCostOfRefusesUnconfiguredRatio：「没配倍率」是配置缺失，
// 悄悄按 1 算出来的成本会是一个看起来完全正常的错数字（宪法 12 条）。
func TestCostOfRefusesUnconfiguredRatio(t *testing.T) {
	usage := metering.TokenUsage{UpstreamTokenID: "tok-1", UsageMinorUnits: 5_813_729}
	if _, err := metering.CostOf(usage, money.Ratio{}); err == nil {
		t.Fatal("未配置倍率时必须报错，不能按 1 折算")
	}
}

// TestSumRawUnitsIsSumThenDivide 锁住 §3.1 的「先 SUM 再除」。
func TestSumRawUnitsIsSumThenDivide(t *testing.T) {
	// 300000 不整除 10^6：逐条折算各带舍入，合计后再折才精确
	const perUnit = int64(300_000)
	usages := make([]metering.TokenUsage, 0, 6)
	var divideThenSum int64
	for i := 0; i < 6; i++ {
		quota := int64(1)
		unit := perUnit
		one, err := money.DivideByUnits(quota, unit, metering.UsageScale)
		if err != nil {
			t.Fatalf("逐条折算失败: %v", err)
		}
		divideThenSum += one
		usages = append(usages, metering.TokenUsage{
			UsageMinorUnits: one, RawUnits: &quota, UnitsPerWhole: &unit,
		})
	}

	total, ok, err := metering.SumRawUnits(usages, metering.UsageScale)
	if err != nil {
		t.Fatalf("合计失败: %v", err)
	}
	if !ok {
		t.Fatal("有 RawUnits 的读数应报告可合计")
	}
	if total != 20 {
		t.Fatalf("先 SUM 再除 = %d 微单位, want 20", total)
	}
	if total == divideThenSum {
		t.Fatalf("本用例要证明两条路不同，实测都等于 %d——判据失效了", total)
	}
}

// TestSumRawUnitsRejectsMixedScale：上游在一轮采集中途改了 quota_per_unit
// （它是运行期可变的），两种刻度的计数不可相加。
func TestSumRawUnitsRejectsMixedScale(t *testing.T) {
	q1, u1 := int64(10), int64(500_000)
	q2, u2 := int64(10), int64(1_000_000)
	_, _, err := metering.SumRawUnits([]metering.TokenUsage{
		{RawUnits: &q1, UnitsPerWhole: &u1},
		{RawUnits: &q2, UnitsPerWhole: &u2},
	}, metering.UsageScale)
	if err == nil {
		t.Fatal("两种 quota_per_unit 混在一起必须报错")
	}
}

// TestSumRawUnitsReportsNothingToSum：全是 sub2api 形状（上游直接给金额）时，
// 返回 false 让调用方知道该退回逐条相加，而不是拿到一个理直气壮的 0。
func TestSumRawUnitsReportsNothingToSum(t *testing.T) {
	total, ok, err := metering.SumRawUnits([]metering.TokenUsage{
		{UsageMinorUnits: 5_813_729},
	}, metering.UsageScale)
	if err != nil {
		t.Fatalf("不该报错: %v", err)
	}
	if ok {
		t.Fatal("没有 RawUnits 时应报告不可合计")
	}
	if total != 0 {
		t.Fatalf("不可合计时不该给出数值，got %d", total)
	}
}

func TestValidateBusinessDay(t *testing.T) {
	for _, day := range []string{"2026-08-28", "2026-01-01", "2026-12-31"} {
		contracttest.AssertNoDayDrift(t, day, false)
	}
	for _, day := range []string{"", "2026-8-1", "2026/08/28", "28-08-2026", "2026-13-01"} {
		contracttest.AssertNoDayDrift(t, day, true)
	}
}

// TestToObservationsCarriesFreshnessAndRatio 验证接进看板的那一层。
func TestToObservationsCarriesFreshnessAndRatio(t *testing.T) {
	observedAt := time.Date(2026, 8, 28, 4, 0, 0, 0, time.UTC)
	older := observedAt.Add(-30 * time.Minute)

	costs := []metering.TokenCost{
		{
			TokenUsage: metering.TokenUsage{
				Snapshot:        metering.Snapshot{ObservedAt: observedAt, Watermark: "day:2026-08-28@1"},
				UpstreamTokenID: "tok-1", Day: "2026-08-28",
				UsageMinorUnits: 5_813_729, Currency: "USD",
			},
			CostMinorUnits: 3_875_819,
			RatioSnapshot:  money.MustParseRatio("1.5"),
		},
		{
			TokenUsage: metering.TokenUsage{
				// 更旧的一条：聚合指标的新鲜度必须由它决定
				Snapshot:        metering.Snapshot{ObservedAt: older, Watermark: "day:2026-08-28@0", IsPartial: true},
				UpstreamTokenID: "tok-2", Day: "2026-08-28",
				UsageMinorUnits: 1_000_000, Currency: "USD",
			},
			CostMinorUnits: 666_667,
			RatioSnapshot:  money.MustParseRatio("1.5"),
		},
	}
	revenues := []metering.AccountRevenue{{
		Snapshot:     metering.Snapshot{ObservedAt: observedAt, Watermark: "day:2026-08-28@1"},
		OwnAccountID: "258", Day: "2026-08-28",
		RevenueMinorUnits: 12_345_600, Currency: "USD",
	}}

	out := metering.ToObservations(time.Now().UTC(), "sub2api-prod", "production", costs, revenues)
	if len(out) != 2 {
		t.Fatalf("应产出 2 条观测，got %d", len(out))
	}

	byKey := map[string]struct {
		observedAt *time.Time
		isPartial  bool
		value      map[string]any
	}{}
	for _, o := range out {
		if err := o.Validate(); err != nil {
			t.Fatalf("观测 %s 不合法: %v", o.MetricKey, err)
		}
		byKey[o.MetricKey] = struct {
			observedAt *time.Time
			isPartial  bool
			value      map[string]any
		}{o.ObservedAt, o.IsPartial, o.Value}
	}

	cost := byKey[metering.MetricCostDaily]
	if cost.observedAt == nil || !cost.observedAt.Equal(older) {
		// 取最新会让一个刚更新的令牌替陈旧的令牌背书
		t.Fatalf("聚合观测时刻应取最旧的 %v, got %v", older, cost.observedAt)
	}
	if !cost.isPartial {
		t.Fatal("任一成员是部分数据，聚合就必须是部分数据")
	}
	if got := cost.value["total_cost_minor_units"]; got != int64(4_542_486) {
		t.Fatalf("成本合计 = %v, want 4542486", got)
	}
	tokens, _ := cost.value["tokens"].([]any)
	if len(tokens) != 2 {
		t.Fatalf("逐令牌明细应有 2 条, got %d", len(tokens))
	}
	first, _ := tokens[0].(map[string]any)
	if first["ratio_snapshot"] != "1.5" {
		// 「这条渠道成本怎么涨了」的第一个追问就是「倍率是不是被改了」
		t.Fatalf("逐令牌明细必须带 ratio_snapshot，got %v", first["ratio_snapshot"])
	}

	revenue := byKey[metering.MetricRevenueDaily]
	if got := revenue.value["total_revenue_minor_units"]; got != int64(12_345_600) {
		t.Fatalf("收入合计 = %v, want 12345600", got)
	}
}

// TestToObservationsOmitsMixedCurrencyTotal：把不同币种的最小单位加在一起
// 是纯粹的错数；不给合计，但要说清为什么不给（宪法 12 条）。
func TestToObservationsOmitsMixedCurrencyTotal(t *testing.T) {
	observedAt := time.Date(2026, 8, 28, 4, 0, 0, 0, time.UTC)
	mk := func(currency string) metering.TokenCost {
		return metering.TokenCost{
			TokenUsage: metering.TokenUsage{
				Snapshot:        metering.Snapshot{ObservedAt: observedAt, Watermark: "day:2026-08-28@1"},
				UpstreamTokenID: "tok-" + currency, Day: "2026-08-28",
				UsageMinorUnits: 1_000_000, Currency: currency,
			},
			CostMinorUnits: 1_000_000,
			RatioSnapshot:  money.MustParseRatio("1"),
		}
	}
	out := metering.ToObservations(time.Now().UTC(), "src", "production",
		[]metering.TokenCost{mk("USD"), mk("CNY")}, nil)

	for _, o := range out {
		if o.MetricKey != metering.MetricCostDaily {
			continue
		}
		if _, ok := o.Value["total_cost_minor_units"]; ok {
			t.Fatal("跨币种时不该给出合计——那是纯粹的错数")
		}
		if o.Value["total_omitted_reason"] != "mixed_currency" {
			t.Fatalf("不给合计必须说清原因，got %v", o.Value["total_omitted_reason"])
		}
	}
}
