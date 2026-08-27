package money_test

import (
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// 本文件钉住摊销算术的**两条恒等式**与**一条边界**。它们是订阅成本账目自洽的
// 全部地基（设计稿 §12.1），逐条写清各自防的是什么：
//
//	Σ 恒等式      N 期日额之和 ≡ 总额       —— 不成立就是「账对不上且不报错」
//	前缀和一致    InstallmentsThrough ≡ 逐期相加 —— 不一致会让退款重算与损失结转打架
//	末期非负      total ≥ 0 时每期都 ≥ 0     —— 半进会让末期变负，那是一天负成本

// TestInstallmentsSumToTotal 穷举一批 (总额, 期数) 验 Σ 恒等式。
//
// 取值刻意包含三类：除得尽的、除不尽的、以及**总额远小于期数**的
// （5 微单位摊 7 天）——最后一类正是半进方案会摊出负数的那一类，
// 也是 fake 演示与小额附加费真会踩到的量级。
func TestInstallmentsSumToTotal(t *testing.T) {
	totals := []int64{0, 1, 2, 5, 7, 99, 100, 999_999, 1_000_000, 29_990_000, 123_456_789}
	counts := []int{1, 2, 3, 7, 28, 30, 31, 365}

	for _, total := range totals {
		for _, n := range counts {
			var sum int64
			for i := 1; i <= n; i++ {
				amount, err := money.Installment(total, n, i)
				if err != nil {
					t.Fatalf("Installment(%d, %d, %d): %v", total, n, i, err)
				}
				sum += amount
			}
			if sum != total {
				t.Fatalf("Σ Installment(%d, %d) = %d，必须精确等于总额（§12.1 账目自洽）",
					total, n, sum)
			}
		}
	}
}

// TestInstallmentsNeverNegativeForPositiveTotal 钉住末期非负。
//
// 这条就是**不用半进摊天轴**的理由：total=5、n=7 时半进得 1，前 6 天共 6 > 5，
// 末日 = −1——报表上是「最后一天成本为负、毛利凭空变大」。截断的商恒 ≤ 真值，
// 末期因而恒 ≥ 其余各期 ≥ 0。
func TestInstallmentsNeverNegativeForPositiveTotal(t *testing.T) {
	for _, total := range []int64{1, 2, 5, 6, 29, 30} {
		for n := 1; n <= 40; n++ {
			for i := 1; i <= n; i++ {
				amount, err := money.Installment(total, n, i)
				if err != nil {
					t.Fatalf("Installment(%d, %d, %d): %v", total, n, i, err)
				}
				if amount < 0 {
					t.Fatalf("Installment(%d, %d, %d) = %d，正总额不该摊出负日额",
						total, n, i, amount)
				}
			}
		}
	}
}

// TestInstallmentsHandleNegativeTotals：负总额同样满足 Σ 恒等式。
//
// 负总额不是垃圾输入：终止之后又收到大额退款时，剩余天数要摊一笔**贷记**
// （见 finance.AmortizationTerm.refundSplit）。把它钳成 0 会让那笔钱消失。
func TestInstallmentsHandleNegativeTotals(t *testing.T) {
	for _, total := range []int64{-1, -5, -7, -1_000_000, -123_457} {
		for _, n := range []int{1, 3, 7, 30} {
			var sum int64
			for i := 1; i <= n; i++ {
				amount, err := money.Installment(total, n, i)
				if err != nil {
					t.Fatalf("Installment(%d, %d, %d): %v", total, n, i, err)
				}
				if amount > 0 {
					t.Fatalf("Installment(%d, %d, %d) = %d，负总额不该摊出正日额",
						total, n, i, amount)
				}
				sum += amount
			}
			if sum != total {
				t.Fatalf("Σ Installment(%d, %d) = %d，必须精确等于总额", total, n, sum)
			}
		}
	}
}

// TestInstallmentsThroughMatchesRunningSum：前缀和的闭式与逐期相加必须一致。
//
// 两者分头被用在不同地方——日额走 Installment，退款重算与损失结转走
// InstallmentsThrough。它们一旦不一致，「已摊 + 未摊 = 份额」就会在某个
// 角落里悄悄不成立，而两边各自看起来都对。
func TestInstallmentsThroughMatchesRunningSum(t *testing.T) {
	for _, total := range []int64{0, 1, 7, 100, 29_990_000, -5, -123_457} {
		for _, n := range []int{1, 2, 7, 31, 365} {
			var running int64
			for k := 0; k <= n; k++ {
				if k > 0 {
					amount, err := money.Installment(total, n, k)
					if err != nil {
						t.Fatalf("Installment: %v", err)
					}
					running += amount
				}
				got, err := money.InstallmentsThrough(total, n, k)
				if err != nil {
					t.Fatalf("InstallmentsThrough(%d, %d, %d): %v", total, n, k, err)
				}
				if got != running {
					t.Fatalf("InstallmentsThrough(%d, %d, %d) = %d，逐期相加 = %d",
						total, n, k, got, running)
				}
			}
		}
	}
}

// TestSplitRoundsHalfUp 钉住账号轴用的是 §2.4 的半进，而不是截断。
//
// 半进在这一轴是对的：一笔批次只落到一个账号头上，没有需要配平的 Σ，
// 于是要的就是「最接近真值的那个整数」。截断会让每个账号都系统性地少算。
func TestSplitRoundsHalfUp(t *testing.T) {
	cases := []struct {
		total  int64
		shares int
		want   int64
	}{
		{100, 4, 25},               // 除得尽
		{10, 4, 3},                 // 2.5 → 半进到 3（截断会给 2）
		{9, 4, 2},                  // 2.25 → 2
		{11, 4, 3},                 // 2.75 → 3
		{-10, 4, -3},               // 半进 away from zero
		{29_990_000, 3, 9_996_667}, // 9996666.67 → 9996667
		{0, 7, 0},
	}
	for _, c := range cases {
		got, err := money.Split(c.total, c.shares)
		if err != nil {
			t.Fatalf("Split(%d, %d): %v", c.total, c.shares, err)
		}
		if got != c.want {
			t.Fatalf("Split(%d, %d) = %d, want %d", c.total, c.shares, got, c.want)
		}
	}
}

// TestAmortizeRejectsNonPositiveDivisors：份数 / 期数为 0 一律报错。
//
// 兜底成 1 的话，一份「分摊账号数填成 0」的代理会让每个账号都摊满额，
// 成本翻 N 倍且完全看不出来（同 CurrencyScale 不猜未知币种的理由）。
func TestAmortizeRejectsNonPositiveDivisors(t *testing.T) {
	if _, err := money.Split(100, 0); err == nil {
		t.Fatal("份数 0 必须报错，不得兜底成 1")
	}
	if _, err := money.Split(100, -3); err == nil {
		t.Fatal("负份数必须报错")
	}
	if _, err := money.Installment(100, 0, 1); err == nil {
		t.Fatal("期数 0 必须报错")
	}
	if _, err := money.Installment(100, 5, 0); err == nil {
		t.Fatal("期次 0 越界，必须报错（期次从 1 起）")
	}
	if _, err := money.Installment(100, 5, 6); err == nil {
		t.Fatal("期次超出期数，必须报错")
	}
	if _, err := money.InstallmentsThrough(100, 5, 6); err == nil {
		t.Fatal("前缀超出期数，必须报错")
	}
}

// TestInstallmentLastDayAbsorbsRemainder 用一个能手算的例子说明「末日吸收」
// 到底长什么样，免得上面的性质测试全绿却没人知道结果是什么形状。
//
// $29.99 的月订阅（29_990_000 微单位）摊 31 天：
// 前 30 天各 967_419 微单位（29990000/31 = 967419.35… 截断），
// 末日 = 29990000 − 30×967419 = 967_430。差 11 微单位 = $0.000011，
// 在「分」粒度上看不见，而 31 天之和精确等于 $29.99。
func TestInstallmentLastDayAbsorbsRemainder(t *testing.T) {
	const total, days = int64(29_990_000), 31

	first, err := money.Installment(total, days, 1)
	if err != nil {
		t.Fatalf("Installment: %v", err)
	}
	if first != 967_419 {
		t.Fatalf("前 30 天日额 = %d, want 967419", first)
	}
	last, err := money.Installment(total, days, days)
	if err != nil {
		t.Fatalf("Installment: %v", err)
	}
	if last != 967_430 {
		t.Fatalf("末日 = %d, want 967430（吸收 11 微单位余数）", last)
	}
	if last < first {
		t.Fatal("末日吸收余数，不该小于其余各期")
	}
}
