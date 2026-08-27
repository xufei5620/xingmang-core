package money

import (
	"errors"
	"testing"
)

func TestParseRatioRoundTrip(t *testing.T) {
	for _, tt := range []struct {
		raw       string
		num       int64
		scale     int32
		canonical string
	}{
		{"1.5", 15, 1, "1.5"},
		{"1", 1, 0, "1"},
		{"2.00", 200, 2, "2.00"},
		{"0.85", 85, 2, "0.85"},
		{"0.000000001", 1, 9, "0.000000001"},
		{" 1.5 ", 15, 1, "1.5"},
		{"+1.5", 15, 1, "1.5"},
		{"-1.5", -15, 1, "-1.5"},
	} {
		r, err := ParseRatio(tt.raw)
		if err != nil {
			t.Fatalf("ParseRatio(%q) 报错: %v", tt.raw, err)
		}
		if r.Num() != tt.num || r.Scale() != tt.scale {
			t.Fatalf("ParseRatio(%q) = num %d scale %d, want %d / %d",
				tt.raw, r.Num(), r.Scale(), tt.num, tt.scale)
		}
		if got := r.String(); got != tt.canonical {
			t.Fatalf("ParseRatio(%q).String() = %q, want %q", tt.raw, got, tt.canonical)
		}
		// 往返稳定：存进 NUMERIC 再读回来不能变成另一个数
		again, err := ParseRatio(r.String())
		if err != nil || again != r {
			t.Fatalf("往返不稳定: %q → %v → %v (%v)", tt.raw, r, again, err)
		}
	}
}

func TestParseRatioRejects(t *testing.T) {
	for _, raw := range []string{
		"", "  ", "abc", "1.2.3", "1,5",
		"1.5e0",                // 倍率不收科学计数法
		"0.0000000001",         // 超出 maxRatioScale
		"99999999999999999999", // 超出 int64
	} {
		if _, err := ParseRatio(raw); err == nil {
			t.Fatalf("ParseRatio(%q) 应报错", raw)
		}
	}
}

// TestDivideMatchesDesignWorkedExample 复核设计稿 §2.4 给出的标准答案。
//
// actual_cost = "5.813729"、ratio = 1.5：
//
//	平台整数路 5813729×10/15 = 3875819.33… 半进 → 3875819 微单位 = $3.875819 → 分 $3.88
//	SoloAI 浮点路 float64(5.813729)/1.5 = 3.8758193… → 分 $3.88
//
// 这一条对上，整条影子对比（§9，分粒度 0 差异）才有立足点。
func TestDivideMatchesDesignWorkedExample(t *testing.T) {
	usage, err := ParseMinorUnits("5.813729", MicroScale)
	if err != nil {
		t.Fatalf("解析实扣失败: %v", err)
	}
	cost, err := Divide(usage, MustParseRatio("1.5"))
	if err != nil {
		t.Fatalf("折算失败: %v", err)
	}
	if cost != 3_875_819 {
		t.Fatalf("cost = %d 微单位, want 3875819", cost)
	}
	cents, err := Rescale(cost, MicroScale, 2)
	if err != nil {
		t.Fatalf("折到分失败: %v", err)
	}
	if cents != 388 {
		t.Fatalf("折到分 = %d, want 388（$3.88）", cents)
	}
}

func TestDivideHalfUp(t *testing.T) {
	for _, tt := range []struct {
		usage int64
		ratio string
		want  int64
	}{
		{1_000_000, "1", 1_000_000},
		{1_000_000, "2", 500_000},
		// 恰好落在半个最小单位上：半进（away from zero），不是 banker's rounding
		{1, "2", 1},
		{3, "2", 2},
		{-1, "2", -1},
		{-3, "2", -2},
		// 倍率小于 1 时成本高于实扣
		{1_000_000, "0.5", 2_000_000},
		{1_000_000, "0.85", 1_176_471}, // 1176470.588… → 半进 1176471
	} {
		got, err := Divide(tt.usage, MustParseRatio(tt.ratio))
		if err != nil {
			t.Fatalf("Divide(%d, %s) 报错: %v", tt.usage, tt.ratio, err)
		}
		if got != tt.want {
			t.Fatalf("Divide(%d, %s) = %d, want %d", tt.usage, tt.ratio, got, tt.want)
		}
	}
}

// TestDivideDoesNotOverflowOnRealisticAmounts 锁住「中间乘积必须走 big.Int」。
//
// 用 int64 直接算 usage×10^ratioScale 时，usage 超过约 9.2×10^9 微单位
// （约 $9200，一个**每天都会踩到**的量级）就会回绕成负数——而回绕不会报错，
// 只会让当天的成本变成一个荒谬的负数。
func TestDivideDoesNotOverflowOnRealisticAmounts(t *testing.T) {
	// $1,000,000 = 10^12 微单位，倍率 9 位小数（maxRatioScale）
	const million = int64(1_000_000_000_000)
	got, err := Divide(million, MustParseRatio("1.000000001"))
	if err != nil {
		t.Fatalf("百万美元级折算报错: %v", err)
	}
	if got <= 0 {
		t.Fatalf("折算结果 %d 非正——中间乘积回绕了", got)
	}
	// 1e12 / 1.000000001 ≈ 999999999000.000001 → 半进 999999999000
	if got != 999_999_999_000 {
		t.Fatalf("Divide = %d, want 999999999000", got)
	}
}

// TestDivideTreatsNonPositiveRatioAsOne 对齐 SoloAI relay_profit.go:97
// （设计稿 §3.1 逐字要求）。平台自己的登记簿在库层拒绝这种倍率，
// 本分支只保证算术层与标准答案一致。
func TestDivideTreatsNonPositiveRatioAsOne(t *testing.T) {
	for _, raw := range []string{"0", "0.00", "-1.5"} {
		got, err := Divide(5_813_729, MustParseRatio(raw))
		if err != nil {
			t.Fatalf("Divide(_, %s) 报错: %v", raw, err)
		}
		if got != 5_813_729 {
			t.Fatalf("Divide(_, %s) = %d, want 原值 5813729", raw, got)
		}
	}
}

// TestDivideByUnitsIsExactAtNewAPIQuota 锁住 §2.4 选 scale-6 的第一条理由：
// quota/500000 在微单位下恒等于 quota×2，**不产生任何舍入**。
func TestDivideByUnitsIsExactAtNewAPIQuota(t *testing.T) {
	const quotaPerUnit = int64(500_000)
	for _, quota := range []int64{0, 1, 7, 12345, 999_999_999} {
		got, err := DivideByUnits(quota, quotaPerUnit, MicroScale)
		if err != nil {
			t.Fatalf("DivideByUnits(%d) 报错: %v", quota, err)
		}
		if got != quota*2 {
			t.Fatalf("DivideByUnits(%d, 500000, 6) = %d, want %d（应精确无舍入）",
				quota, got, quota*2)
		}
	}
}

// TestDivideByUnitsSumThenDivideBeatsDivideThenSum 说明「先 SUM 再除」为什么
// 是纪律而不是口味：quota_per_unit 不整除 10^6 时，逐条折算各带半个微单位的
// 舍入，累加之后就会与一次折算差出可见的量。
func TestDivideByUnitsSumThenDivideBeatsDivideThenSum(t *testing.T) {
	// 300000 不整除 10^6：1 quota = 3.333… 微单位
	const quotaPerUnit = int64(300_000)
	quotas := []int64{1, 1, 1, 1, 1, 1}

	var sumThenDivide, divideThenSum int64
	var total int64
	for _, q := range quotas {
		total += q
		one, err := DivideByUnits(q, quotaPerUnit, MicroScale)
		if err != nil {
			t.Fatalf("逐条折算报错: %v", err)
		}
		divideThenSum += one
	}
	sumThenDivide, err := DivideByUnits(total, quotaPerUnit, MicroScale)
	if err != nil {
		t.Fatalf("合计折算报错: %v", err)
	}
	// 6 quota / 300000 = 20 微单位（精确）；逐条则是 6×round(3.333)=6×3=18
	if sumThenDivide != 20 {
		t.Fatalf("先 SUM 再除 = %d, want 20", sumThenDivide)
	}
	if divideThenSum == sumThenDivide {
		t.Fatalf("本用例要证明两条路不同，实测都等于 %d——判据失效了", sumThenDivide)
	}
}

func TestDivideByUnitsRejectsNonPositiveUnit(t *testing.T) {
	for _, unit := range []int64{0, -1, -500_000} {
		if _, err := DivideByUnits(100, unit, MicroScale); !errors.Is(err, ErrFormat) {
			t.Fatalf("DivideByUnits(_, %d, _) 必须报错", unit)
		}
	}
}

// TestReciprocalStringIsDisplayOnly 校验「充值成本率」的展示投影（§3.4）。
func TestReciprocalStringIsDisplayOnly(t *testing.T) {
	for _, tt := range []struct {
		ratio string
		scale int
		want  string
	}{
		{"1.5", 6, "0.666667"}, // 1/1.5 = 0.6666… 半进
		{"2", 6, "0.500000"},
		{"1", 6, "1.000000"},
		{"0.85", 6, "1.176471"}, // 1/0.85 = 1.17647058… 半进
		{"1.5", 2, "0.67"},
	} {
		got, err := ReciprocalString(MustParseRatio(tt.ratio), tt.scale)
		if err != nil {
			t.Fatalf("ReciprocalString(%s, %d) 报错: %v", tt.ratio, tt.scale, err)
		}
		if got != tt.want {
			t.Fatalf("ReciprocalString(%s, %d) = %q, want %q", tt.ratio, tt.scale, got, tt.want)
		}
	}
	// 非正倍率没有可展示的倒数——不返回 0，也不返回空串装作正常
	if _, err := ReciprocalString(MustParseRatio("0"), 6); !errors.Is(err, ErrFormat) {
		t.Fatalf("零倍率的倒数必须报错")
	}
}

// TestReciprocalIsNotUsedForCost 是一条**口径**断言（§3.4）：
// 先把充值成本率舍入成有限小数再乘，与直接整数除法在分粒度上就能差出来。
// 有它在，任何把 ReciprocalString 拿去算成本的改动都会当场失败。
func TestReciprocalIsNotUsedForCost(t *testing.T) {
	ratio := MustParseRatio("0.85")
	usage, err := ParseMinorUnits("5.813729", MicroScale)
	if err != nil {
		t.Fatalf("解析实扣失败: %v", err)
	}
	direct, err := Divide(usage, ratio)
	if err != nil {
		t.Fatalf("直接折算失败: %v", err)
	}

	// 「先取倒数再乘」这条错误路径：把 1/0.85 舍入到 6 位小数（1.176471）
	// 再乘回去，尾数已经变了。
	rateText, err := ReciprocalString(ratio, MicroScale)
	if err != nil {
		t.Fatalf("倒数投影失败: %v", err)
	}
	rate, err := ParseRatio(rateText)
	if err != nil {
		t.Fatalf("倒数解析失败: %v", err)
	}
	// usage × rate，标度相加后折回微单位
	viaRate, err := Rescale(usage*rate.Num(), MicroScale+int(rate.Scale()), MicroScale)
	if err != nil {
		t.Fatalf("乘法路径失败: %v", err)
	}
	if viaRate == direct {
		t.Fatalf("两条路在本用例上恰好相等（%d）——判据失效，需换一组倍率/金额", direct)
	}
	t.Logf("整数除法 %d 微单位 vs 先取倒数再乘 %d 微单位：差 %d",
		direct, viaRate, viaRate-direct)
}

// TestUnsetRatioIsDistinctFromZeroRatio 锁住「未配置」与「配置成 0」的区分。
//
// 没有这个区分时，ParseRatio("0") 与从未赋值会给出同一个结构体，于是
// 「订阅型渠道本就不该有倍率」（§2.0 的正常状态）与「有人把倍率填成 0」
// （一个会被静默当成 1 的错误配置）在校验里长得一模一样，只有一条能报出来。
func TestUnsetRatioIsDistinctFromZeroRatio(t *testing.T) {
	var unset Ratio
	if !unset.IsZero() {
		t.Fatal("零值 Ratio 必须报告为未配置")
	}
	if unset.String() != "" {
		t.Fatalf("未配置的倍率应渲染成空串，got %q", unset.String())
	}

	zero := MustParseRatio("0")
	if zero.IsZero() {
		t.Fatal("ParseRatio(\"0\") 是**配置成 0**，不是未配置")
	}
	if zero.IsPositive() {
		t.Fatal("0 倍率不是正数")
	}
	if zero.String() != "0" {
		t.Fatalf("配置成 0 的倍率应渲染成 \"0\"，got %q", zero.String())
	}
	if unset == zero {
		t.Fatal("未配置与配置成 0 必须是两个不同的值")
	}
}
