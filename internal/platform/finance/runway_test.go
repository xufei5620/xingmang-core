package finance_test

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
)

// 可用天数与看板聚合的口径（XM-0037d，设计稿 §7 + UI 交接 §10.4）。
//
// §10.4 给了五条硬要求，每条在这里都有一个用例：余额与消耗单位一致、
// 必须显示观测时间、日均窗口明确、无消耗或数据过期不显示伪精确天数、
// 低于阈值进告警。
//
// 贯穿全篇的一条：**给不出天数时必须给得出原因**。一个没有解释的空位
// 会被读成 bug，然后有人就去把它「修」成 0 了（宪法 12 条）。

var runwayNow = time.Date(2026, 8, 28, 6, 0, 0, 0, time.UTC) // CST 14:00

func balanceAt(minor int64, currency string, age time.Duration) *finance.BalanceReading {
	return &finance.BalanceReading{
		ID:                1,
		UpstreamAccountID: uuid.New(),
		BalanceMinor:      minor,
		Currency:          currency,
		CapturedAt:        runwayNow.Add(-age),
		ObservedAt:        runwayNow.Add(-age),
		Source:            "finance-collect-test",
	}
}

func meteredRunway(in finance.RunwayInput) finance.Runway {
	in.AccessMethod = finance.AccessUpstreamKey
	in.Now = runwayNow
	if (in.Thresholds == finance.RunwayThresholds{}) {
		in.Thresholds = finance.DefaultRunwayThresholds()
	}
	if in.CostCurrency == "" {
		in.CostCurrency = "USD"
	}
	got, err := finance.ComputeRunway(in)
	if err != nil {
		panic(err)
	}
	return got
}

// TestRunwayDividesBalanceByDailyAverage 是 §10.4 的公式本身。
//
// $420 余额 ÷ 每天 $4（7 天共 $28）= 105 天。整数除法**向下**取整——
// 可用天数是一个要拿去做决策的保守估计，把 3.9 天说成 4 天正好是错的方向。
func TestRunwayDividesBalanceByDailyAverage(t *testing.T) {
	got := meteredRunway(finance.RunwayInput{
		Balance:      balanceAt(420_000_000, "USD", time.Minute),
		CostMinorSum: 28_000_000,
		CoveredDays:  7,
	})
	if !got.Known() {
		t.Fatalf("应算得出天数, reason=%s", got.Reason)
	}
	if *got.Days != 105 {
		t.Fatalf("可用天数 = %d, want 105", *got.Days)
	}
	if got.DailyAverageMinor == nil || *got.DailyAverageMinor != 4_000_000 {
		t.Fatalf("日均 = %v, want 4000000", got.DailyAverageMinor)
	}
	if got.Level != finance.RunwayHealthy {
		t.Fatalf("105 天应是 healthy, got %s", got.Level)
	}
	// §10.4：必须显示观测时间
	if got.BalanceObservedAt == nil {
		t.Fatal("余额观测时刻必须带出来（§10.4 硬要求）")
	}
	// 日均窗口必须明确
	if got.WindowDays != finance.RunwayWindowDays || got.CoveredDays != 7 {
		t.Fatalf("窗口 %d / 覆盖 %d 都要回报", got.WindowDays, got.CoveredDays)
	}
}

// TestRunwayRoundsDown 钉住向下取整。
//
// $10 ÷ 每天 $3 = 3.33 天 → 3 天。给 4 天会让人以为还能撑到后天。
func TestRunwayRoundsDown(t *testing.T) {
	got := meteredRunway(finance.RunwayInput{
		Balance:      balanceAt(10_000_000, "USD", time.Minute),
		CostMinorSum: 3_000_000,
		CoveredDays:  1,
	})
	if !got.Known() || *got.Days != 3 {
		t.Fatalf("可用天数 = %v, want 3（向下取整）", got.Days)
	}
}

// TestDailyAverageUsesCoveredDaysNotWindow 是本片最容易实现错的一处。
//
// 只采到 3 天数据时，除以 7 会把日均压低到实际的四成，可用天数因而虚高
// 一倍多——一个**偏乐观**的预警值，正好是最危险的方向。
func TestDailyAverageUsesCoveredDaysNotWindow(t *testing.T) {
	got := meteredRunway(finance.RunwayInput{
		Balance:      balanceAt(90_000_000, "USD", time.Minute),
		CostMinorSum: 9_000_000, // 3 天共 $9
		CoveredDays:  3,
	})
	if got.DailyAverageMinor == nil || *got.DailyAverageMinor != 3_000_000 {
		t.Fatalf("日均应为 9000000/3 = 3000000, got %v", got.DailyAverageMinor)
	}
	if *got.Days != 30 {
		t.Fatalf("可用天数 = %d, want 30（除以 7 会给出 70，虚高一倍多）", *got.Days)
	}
}

// TestRunwayLevels 钉住三档阈值与它们**反直觉的命名**（SoloAI 迁移 0177）。
//
// critical=5 < warning=10 < serious=20：天数越少越严重，
// 所以 critical 是最紧的一档，serious 反而最松。看着别扭，
// 但改名会让与 SoloAI 的对照失效。
func TestRunwayLevels(t *testing.T) {
	cases := map[int]finance.RunwayLevel{
		0: finance.RunwayCritical, 5: finance.RunwayCritical,
		6: finance.RunwayWarning, 10: finance.RunwayWarning,
		11: finance.RunwaySerious, 20: finance.RunwaySerious,
		21: finance.RunwayHealthy, 999: finance.RunwayHealthy,
	}
	for days, want := range cases {
		// 让余额恰好等于 days 天的量：日均固定 $1，余额 = days 美元
		got := meteredRunway(finance.RunwayInput{
			Balance:      balanceAt(int64(days)*1_000_000, "USD", time.Minute),
			CostMinorSum: 1_000_000,
			CoveredDays:  1,
		})
		if !got.Known() {
			t.Fatalf("%d 天应算得出, reason=%s", days, got.Reason)
		}
		if got.Level != want {
			t.Fatalf("%d 天的档位 = %s, want %s", days, got.Level, want)
		}
	}
}

// TestOverdrawnBalanceIsZeroDaysCritical：余额为负（已透支）落成 0 天。
//
// 负的可用天数没有意义，而 0 天已经是最高档的告警，表达力不缺。
func TestOverdrawnBalanceIsZeroDaysCritical(t *testing.T) {
	got := meteredRunway(finance.RunwayInput{
		Balance:      balanceAt(-5_000_000, "USD", time.Minute),
		CostMinorSum: 1_000_000,
		CoveredDays:  1,
	})
	if !got.Known() || *got.Days != 0 {
		t.Fatalf("透支应为 0 天, got %v", got.Days)
	}
	if got.Level != finance.RunwayCritical {
		t.Fatalf("透支必须是 critical, got %s", got.Level)
	}
}

// TestRunwayUnknownReasons 逐条钉住「给不出天数」的五种原因（§10.4）。
//
// 每种都必须给得出原因——一个没有解释的空位会被读成 bug。
func TestRunwayUnknownReasons(t *testing.T) {
	healthy := finance.RunwayInput{
		Balance:      balanceAt(420_000_000, "USD", time.Minute),
		CostMinorSum: 28_000_000,
		CoveredDays:  7,
	}

	t.Run("订阅型渠道没有余额这个概念", func(t *testing.T) {
		in := healthy
		in.AccessMethod = finance.AccessSubscriptionAccount
		in.Now = runwayNow
		in.Thresholds = finance.DefaultRunwayThresholds()
		in.CostCurrency = "USD"
		got, err := finance.ComputeRunway(in)
		if err != nil {
			t.Fatal(err)
		}
		if got.Known() || got.Reason != finance.RunwayReasonNotApplicable {
			t.Fatalf("订阅型应为 not_applicable, got days=%v reason=%s", got.Days, got.Reason)
		}
	})

	t.Run("从未读到余额", func(t *testing.T) {
		in := healthy
		in.Balance = nil
		got := meteredRunway(in)
		if got.Known() || got.Reason != finance.RunwayReasonNoBalance {
			t.Fatalf("无余额应为 no_balance, got %v/%s", got.Days, got.Reason)
		}
	})

	t.Run("余额观测已过期", func(t *testing.T) {
		in := healthy
		in.Balance = balanceAt(420_000_000, "USD", 2*time.Hour)
		got := meteredRunway(in)
		if got.Known() || got.Reason != finance.RunwayReasonBalanceStale {
			t.Fatalf("过期余额应为 balance_stale, got %v/%s", got.Days, got.Reason)
		}
	})

	t.Run("窗口内无消耗", func(t *testing.T) {
		in := healthy
		in.CostMinorSum, in.CoveredDays = 0, 0
		got := meteredRunway(in)
		if got.Known() || got.Reason != finance.RunwayReasonNoConsumption {
			t.Fatalf("无消耗应为 no_consumption, got %v/%s", got.Days, got.Reason)
		}
	})

	t.Run("币种不一致不换算", func(t *testing.T) {
		in := healthy
		in.CostCurrency = "CNY"
		got := meteredRunway(in)
		if got.Known() || got.Reason != finance.RunwayReasonCurrencyMismatch {
			t.Fatalf("币种不一致应为 currency_mismatch, got %v/%s", got.Days, got.Reason)
		}
	})
}

// TestBalanceStalenessUsesObservedAtNotCapturedAt 是余额这条链路最容易搞反的一处。
//
// 一个健康账号的余额可以一周不动：CapturedAt 很旧，ObservedAt 却是刚才。
// 按 CapturedAt 判过期会把**正常状态显示成故障**——把可用天数抹掉，
// 而那正是最该有数的时候。
func TestBalanceStalenessUsesObservedAtNotCapturedAt(t *testing.T) {
	stable := &finance.BalanceReading{
		ID: 1, UpstreamAccountID: uuid.New(),
		BalanceMinor: 420_000_000, Currency: "USD",
		CapturedAt: runwayNow.Add(-7 * 24 * time.Hour), // 一周前变成这个数
		ObservedAt: runwayNow.Add(-time.Minute),        // 一分钟前刚确认过
		Source:     "finance-collect-test",
	}
	got := meteredRunway(finance.RunwayInput{
		Balance: stable, CostMinorSum: 28_000_000, CoveredDays: 7,
	})
	if !got.Known() {
		t.Fatalf("余额稳定不是过期，应照常算出天数, reason=%s", got.Reason)
	}
	if age := stable.Age(runwayNow); age > time.Hour {
		t.Fatalf("Age 应按 ObservedAt 算, got %s", age)
	}
}

// TestRunwayThresholdsMustStrictlyIncrease：不递增的阈值会让某一档永远
// 匹配不上——一个静静失效的告警档。
func TestRunwayThresholdsMustStrictlyIncrease(t *testing.T) {
	for _, bad := range []finance.RunwayThresholds{
		{CriticalDays: 10, WarningDays: 5, SeriousDays: 20},
		{CriticalDays: 5, WarningDays: 5, SeriousDays: 20},
		{CriticalDays: 0, WarningDays: 10, SeriousDays: 20},
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("%+v 应被拒", bad)
		}
	}
	if err := finance.DefaultRunwayThresholds().Validate(); err != nil {
		t.Fatalf("默认阈值应合法: %v", err)
	}
}

func TestRunwayThresholdsClassifyUsesInclusiveBoundaries(t *testing.T) {
	thresholds := finance.DefaultRunwayThresholds()
	cases := []struct {
		days int
		want finance.RunwayLevel
	}{
		{days: 0, want: finance.RunwayCritical},
		{days: 5, want: finance.RunwayCritical},
		{days: 6, want: finance.RunwayWarning},
		{days: 10, want: finance.RunwayWarning},
		{days: 11, want: finance.RunwaySerious},
		{days: 20, want: finance.RunwaySerious},
		{days: 21, want: finance.RunwayHealthy},
	}
	for _, tc := range cases {
		got, err := thresholds.Classify(tc.days)
		if err != nil || got != tc.want {
			t.Fatalf("Classify(%d) = %q, %v; want %q", tc.days, got, err, tc.want)
		}
	}
}

func TestRunwayThresholdsClassifyRejectsInvalidConfiguration(t *testing.T) {
	if _, err := (finance.RunwayThresholds{CriticalDays: 10, WarningDays: 5, SeriousDays: 20}).Classify(1); err == nil {
		t.Fatal("invalid threshold order must return an error")
	}
}

// TestInvalidThresholdsFailClosed：阈值非法时返回错误，不把它解释成 critical。
func TestInvalidThresholdsFailClosed(t *testing.T) {
	_, err := finance.ComputeRunway(finance.RunwayInput{
		AccessMethod: finance.AccessUpstreamKey,
		Balance:      balanceAt(999_000_000, "USD", time.Minute),
		CostMinorSum: 1_000_000, CoveredDays: 1,
		CostCurrency: "USD", Now: runwayNow,
		Thresholds: finance.RunwayThresholds{}, // 未初始化
	})
	if err == nil {
		t.Fatal("阈值非法必须 fail closed")
	}
}

func TestComputeRunwayMarksEveryNonMeteredMethodNotApplicable(t *testing.T) {
	for _, method := range []finance.AccessMethod{finance.AccessOfficialAPI, finance.AccessSubscriptionAccount, finance.AccessMethod("future_non_metered")} {
		got, err := finance.ComputeRunway(finance.RunwayInput{
			AccessMethod: method,
			Balance:      balanceAt(999_000_000, "USD", time.Minute),
			CostMinorSum: 1_000_000, CoveredDays: 1, CostCurrency: "USD", Now: runwayNow,
			Thresholds: finance.DefaultRunwayThresholds(),
		})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", method, err)
		}
		if got.Known() || got.Reason != finance.RunwayReasonNotApplicable {
			t.Fatalf("%s: got %+v, want not_applicable without days", method, got)
		}
	}
}

// TestRunwayCoverageExcludesSubscriptions：订阅型不进覆盖率的分母。
//
// 把「没有这个概念」算成「没覆盖到」，会让覆盖率随订阅渠道数量下降，
// 而那与采集能力毫无关系——一个会自己变差的指标没人会信。
func TestRunwayCoverageExcludesSubscriptions(t *testing.T) {
	items := []finance.UpstreamSummary{
		{Account: finance.UpstreamAccount{AccessMethod: finance.AccessUpstreamKey}, Runway: finance.Runway{Days: intPtr(30), Level: finance.RunwayHealthy}},
		{Account: finance.UpstreamAccount{AccessMethod: finance.AccessUpstreamKey}, Runway: finance.Runway{Reason: finance.RunwayReasonNoBalance}},
		{Account: finance.UpstreamAccount{AccessMethod: finance.AccessSubscriptionAccount}, Runway: finance.Runway{Reason: finance.RunwayReasonNotApplicable}},
		{Account: finance.UpstreamAccount{AccessMethod: finance.AccessSubscriptionAccount}, Runway: finance.Runway{Reason: finance.RunwayReasonNotApplicable}},
	}
	got := finance.SummarizeRunwayCoverage(items)
	if got.Total != 2 {
		t.Fatalf("分母 = %d, want 2（两个订阅型不进分母）", got.Total)
	}
	if got.Known != 1 {
		t.Fatalf("已知 = %d, want 1", got.Known)
	}
	if got.Reasons[finance.RunwayReasonNoBalance] != 1 {
		t.Fatalf("原因分布要说得出「为什么是 1/2」: %+v", got.Reasons)
	}
	if got.Reasons[finance.RunwayReasonNotApplicable] != 0 {
		t.Fatalf("所有非计量型都不应进入覆盖率原因分布: %+v", got.Reasons)
	}
}

func TestRunwayCoverageExcludesNonMeteredEvenWhenReasonLooksUnknown(t *testing.T) {
	items := []finance.UpstreamSummary{
		{Account: finance.UpstreamAccount{AccessMethod: finance.AccessOfficialAPI}, Runway: finance.Runway{Reason: finance.RunwayReasonNoBalance}},
		{Account: finance.UpstreamAccount{AccessMethod: finance.AccessMethod("future_non_metered")}, Runway: finance.Runway{Reason: finance.RunwayReasonCurrencyMismatch}},
		{Account: finance.UpstreamAccount{AccessMethod: finance.AccessUpstreamKey}, Runway: finance.Runway{Reason: finance.RunwayReasonNoBalance}},
	}
	got := finance.SummarizeRunwayCoverage(items)
	if got.Total != 1 || got.Known != 0 || got.Reasons[finance.RunwayReasonNoBalance] != 1 {
		t.Fatalf("只有计量型应进入覆盖率: %+v", got)
	}
}

func intPtr(v int) *int { return &v }

// TestRunwayAlwaysYieldsDaysOrReason 是本文件的**总不变量**，
// 也是上层（HTTP 投影、看板）敢直接渲染 Runway 的全部依据：
//
//	要么给得出天数，要么给得出为什么给不出——不存在第三种。
//
// 穷举输入的各种组合来验它：接入方式 × 有无余额 × 余额新旧 × 有无消耗 × 币种。
// 少了这条，某天加一个新的提前返回分支忘了写 Reason，看板上就会多出一个
// 没有解释的空位，而没有任何测试会红。
func TestRunwayAlwaysYieldsDaysOrReason(t *testing.T) {
	methods := []finance.AccessMethod{
		finance.AccessUpstreamKey, finance.AccessOfficialAPI, finance.AccessSubscriptionAccount,
	}
	balances := []*finance.BalanceReading{
		nil,
		balanceAt(420_000_000, "USD", time.Minute),
		balanceAt(0, "USD", time.Minute),
		balanceAt(-1, "USD", time.Minute),
		balanceAt(420_000_000, "USD", 48*time.Hour),
		balanceAt(420_000_000, "CNY", time.Minute),
	}
	type consumption struct {
		sum      int64
		days     int
		currency string
	}
	consumptions := []consumption{
		{0, 0, ""}, {28_000_000, 7, "USD"}, {0, 3, "USD"}, {1, 1, "USD"}, {5_000_000, 2, "CNY"},
	}

	for _, method := range methods {
		for _, balance := range balances {
			for _, c := range consumptions {
				got, err := finance.ComputeRunway(finance.RunwayInput{
					AccessMethod: method, Balance: balance,
					CostMinorSum: c.sum, CoveredDays: c.days, CostCurrency: c.currency,
					Now: runwayNow, Thresholds: finance.DefaultRunwayThresholds(),
				})
				if err != nil {
					t.Fatalf("%s/%v/%+v: unexpected error: %v", method, balance, c, err)
				}
				switch {
				case got.Known() && got.Reason != "":
					t.Fatalf("%s/%v/%+v: 算出了天数就不该再带原因", method, balance, c)
				case !got.Known() && got.Reason == "":
					t.Fatalf("%s/%v/%+v: 给不出天数就必须给得出原因", method, balance, c)
				}
				if got.Known() && *got.Days < 0 {
					t.Fatalf("%s/%v/%+v: 天数不该为负, got %d", method, balance, c, *got.Days)
				}
				if got.Known() && got.Level == "" {
					t.Fatalf("%s/%v/%+v: 算出了天数就必须有档位——"+
						"没有颜色的数字看起来像没问题", method, balance, c)
				}
			}
		}
	}
}

// TestParseRunwayThresholds 钉住 bootstrap 生命周期命令使用的唯一 env 解析器
// （XM-0049）。运行中的 API/worker 不读取这些变量，而是从 DB snapshot provider
// 取值；本用例只验证一次性导入输入的默认、格式和派生规则。
func TestParseRunwayThresholds(t *testing.T) {
	t.Run("空串用默认档", func(t *testing.T) {
		got, err := finance.ParseRunwayThresholds("", "")
		if err != nil {
			t.Fatalf("空串应回落默认: %v", err)
		}
		if got != finance.DefaultRunwayThresholds() {
			t.Fatalf("got %+v, want %+v", got, finance.DefaultRunwayThresholds())
		}
	})

	t.Run("显式值生效", func(t *testing.T) {
		got, err := finance.ParseRunwayThresholds("14", "7")
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if got.WarningDays != 14 || got.CriticalDays != 7 {
			t.Fatalf("got %+v", got)
		}
		if err := got.Validate(); err != nil {
			t.Fatalf("解析结果必须自洽: %v", err)
		}
	})

	t.Run("warning 超过默认 serious 时 serious 自动让位", func(t *testing.T) {
		// serious 是最松的一档，作用只是给「还算充裕」一个颜色。
		// 不让位的话三档不递增 → levelFor 的兜底把**每一条**上游判成 critical，
		// 一次配置手滑变成满屏红。
		got, err := finance.ParseRunwayThresholds("30", "5")
		if err != nil {
			t.Fatalf("解析失败: %v", err)
		}
		if got.SeriousDays <= got.WarningDays {
			t.Fatalf("serious 应让位到 warning 之上, got %+v", got)
		}
		if err := got.Validate(); err != nil {
			t.Fatalf("让位后仍须严格递增: %v", err)
		}
	})

	t.Run("非法值报错而不是回落默认", func(t *testing.T) {
		// 静默回落会让一个把 WARN_DAYS 写成 "ten" 的部署以为自己调过了
		for _, tc := range [][2]string{
			{"ten", "5"}, {"10", "five"}, {"0", "5"}, {"10", "0"},
			{"-1", "5"}, {"10", "-1"},
		} {
			if _, err := finance.ParseRunwayThresholds(tc[0], tc[1]); err == nil {
				t.Fatalf("warn=%q crit=%q 应报错", tc[0], tc[1])
			}
		}
	})

	t.Run("critical 不小于 warning 时报错", func(t *testing.T) {
		// 天数越少越严重：critical 必须是更紧的那一档
		for _, tc := range [][2]string{{"5", "10"}, {"5", "5"}} {
			_, err := finance.ParseRunwayThresholds(tc[0], tc[1])
			if !errors.Is(err, finance.ErrInconsistent) {
				t.Fatalf("warn=%q crit=%q 应报 ErrInconsistent, got %v", tc[0], tc[1], err)
			}
		}
	})
}

func TestRunwayThresholdsValidateRejectsValuesThatDoNotFitDatabaseInt32(t *testing.T) {
	tooLarge := int(1<<31 - 1)
	if strconv.IntSize > 32 {
		tooLarge++
	}
	if _, err := finance.ParseRunwayThresholds(strconv.Itoa(tooLarge), "1"); err == nil {
		t.Fatalf("threshold %d should not be accepted when it cannot be persisted as int32", tooLarge)
	}
}
