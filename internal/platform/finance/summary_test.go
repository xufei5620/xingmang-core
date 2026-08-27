package finance

import (
	"testing"
	"time"
)

// 看板聚合的「金额什么时候给得出」纪律（XM-0037d，设计稿 §8.5 + 宪法 12 条）。
//
// 本文件在包内（而不是 finance_test）是因为 ProfitWindow 的金额字段刻意不导出：
// 调用方只能经 RevenueMinor() / CostMinor() 拿它们，而那两个方法**会先判覆盖率**。
// 导出字段等于给出一条绕过覆盖率判定的路，然后总有一处会走上去。

func window(rowCount, revenueKnown, costKnown, revenueSum, costSum int64, mixed bool) ProfitWindow {
	day := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	return newProfitWindow(day, day, rowCount, revenueKnown, costKnown,
		revenueSum, costSum, 0, "USD", mixed)
}

// TestPartialCoverageYieldsNoAmount 是本文件的核心：**覆盖不全就给不出金额**。
//
// 少一行收入的和与完整的和长得一模一样，而它偏低——一个偏低的收入配上
// 完整的成本，毛利就凭空缩水了，而报表上没有任何东西提示这件事。
// 与 PlatformBucket.ProfitMinorSum 在覆盖行数不足时返回 nil 是同一条纪律。
func TestPartialCoverageYieldsNoAmount(t *testing.T) {
	// 5 行里只有 4 行有收入
	w := window(5, 4, 5, 40_000_000, 20_000_000, false)
	if w.RevenueMinor() != nil {
		t.Fatalf("收入覆盖不全时必须给不出, got %v", *w.RevenueMinor())
	}
	if w.CostMinor() == nil {
		t.Fatal("成本覆盖满，应给得出")
	}
	if w.GrossProfitMinor() != nil {
		t.Fatal("任一侧给不出，毛利就给不出——不是「等于另一侧」")
	}
	if w.GrossMargin() != "" {
		t.Fatalf("毛利给不出时毛利率也给不出, got %q", w.GrossMargin())
	}
}

// TestFullCoverageYieldsAmounts：两侧都覆盖满且币种单一时才给得出。
func TestFullCoverageYieldsAmounts(t *testing.T) {
	w := window(3, 3, 3, 40_000_000, 12_500_000, false)
	revenue, cost, profit := w.RevenueMinor(), w.CostMinor(), w.GrossProfitMinor()
	if revenue == nil || *revenue != 40_000_000 {
		t.Fatalf("收入 = %v", revenue)
	}
	if cost == nil || *cost != 12_500_000 {
		t.Fatalf("成本 = %v", cost)
	}
	if profit == nil || *profit != 27_500_000 {
		t.Fatalf("毛利 = %v, want 27500000", profit)
	}
	// 27500000 / 40000000 = 0.6875
	if got := w.GrossMargin(); got != "0.687500" {
		t.Fatalf("毛利率 = %q, want 0.687500", got)
	}
}

// TestMixedCurrencyYieldsNothing：不同币种的最小单位不能相加。
//
// 币种混杂时连币种字段都清空——一个标着 USD 的混合合计比没有合计更危险。
func TestMixedCurrencyYieldsNothing(t *testing.T) {
	w := window(3, 3, 3, 40_000_000, 12_500_000, true)
	if w.RevenueMinor() != nil || w.CostMinor() != nil || w.GrossProfitMinor() != nil {
		t.Fatal("币种混杂时三个金额都必须给不出")
	}
	if w.Currency != "" {
		t.Fatalf("币种混杂时不该留一个币种字段, got %q", w.Currency)
	}
}

// TestEmptyWindowYieldsNothing：一行都没有的窗口不是「0 收入 0 成本」。
//
// 「今天还没入账」与「今天收入是 0」是两件事：前者是未知，后者是已知的零。
// 写 0 会让一条刚登记的渠道显示成「毛利为 0」（§5.1 的同一条纪律）。
func TestEmptyWindowYieldsNothing(t *testing.T) {
	w := window(0, 0, 0, 0, 0, false)
	if w.RevenueMinor() != nil || w.CostMinor() != nil {
		t.Fatal("空窗口给不出金额——那是未知，不是 0")
	}
	if w.GrossMargin() != "" {
		t.Fatal("空窗口给不出毛利率")
	}
}

// TestZeroRevenueYieldsNoMargin 钉住 §3.3 的「收入 ≤ 0 时毛利率为 nil」
// （对齐 SoloAI relay_profit.go:331）。
//
// 没有收入的渠道谈不上毛利率。给 0 会让「今天没有流量」看起来像
// 「毛利率是零」——两件完全不同的事，而后者会让人去查渠道定价。
func TestZeroRevenueYieldsNoMargin(t *testing.T) {
	w := window(2, 2, 2, 0, 3_000_000, false)
	if w.GrossProfitMinor() == nil || *w.GrossProfitMinor() != -3_000_000 {
		t.Fatalf("毛利仍算得出（是负的）, got %v", w.GrossProfitMinor())
	}
	if got := w.GrossMargin(); got != "" {
		t.Fatalf("收入为 0 时毛利率必须给不出, got %q", got)
	}
}

// TestNegativeMarginIsRendered：亏损渠道的毛利率是负数，照样要给出来。
//
// 把它藏起来（或钳到 0）会让一条真在亏钱的渠道看起来只是「没赚」。
func TestNegativeMarginIsRendered(t *testing.T) {
	w := window(1, 1, 1, 10_000_000, 15_000_000, false)
	// (10000000 − 15000000) / 10000000 = −0.5
	if got := w.GrossMargin(); got != "-0.500000" {
		t.Fatalf("毛利率 = %q, want -0.500000", got)
	}
}
