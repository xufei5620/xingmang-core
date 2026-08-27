package money

// 摊销算术（XM-0037c，设计稿 §3.5 + §12.1）。
//
// 订阅成本要被两次除法拆开：先按账号数分，再按有效天数分（§12.1 固定的顺序）。
// 整数最小单位下除不尽必留余数，而余数往哪去决定了一件很具体的事——
// **N 天的日额之和还等不等于那笔付款**。各天各自四舍五入的话不等，
// 差额随天数增长，账目从此对不上且没有任何报错。
//
// 本文件只做整数运算，任何路径上都不出现 float（宪法 13 条）。
// 两个轴用两套规则，理由见各自的函数注释：
//
//	账号轴 Split        半进（§2.4 的舍入框架）——一笔批次只落到一个账号头上，
//	                    没有需要配平的 Σ
//	天轴   Installment  截断 + 末日吸收——Σ 必须精确等于份额，一微单位都不许漂

import "fmt"

// Split 把一笔金额按份数平分，**半进**（away from zero）。
//
// 用于 §3.5 的第一次除法「/ account_count」「/ shared_account_count」。
// 这一轴用半进而不是末位吸收，是因为它**没有需要配平的和**：一笔批次挂在
// 一个上游账号下，平台只为那一个账号算一行成本，其余份额由它们各自的批次登记
// （见 docs/modules/finance/README.md 的「两个轴为什么用两套规则」）。
// 于是这里要的只是「最接近真值的那个整数」，那正是 §2.4 的舍入规则。
//
// shares 必须为正：份数为 0 时「每份多少」没有答案，而一个被兜底成 1 的 0
// 会让成本翻 N 倍且完全看不出来（同 CurrencyScale 不猜未知币种的理由）。
func Split(totalMinorUnits int64, shares int) (int64, error) {
	if shares <= 0 {
		return 0, fmt.Errorf("份数 %d 必须为正: %w", shares, ErrFormat)
	}
	return DivideByUnits(totalMinorUnits, int64(shares), 0)
}

// Installment 给出「把 total 摊到 n 期」的第 index 期金额（index 从 1 起）。
//
// **末日吸收**（设计稿 §12.1 的第三种方案，§12 拍板后由本片选定）：
//
//	第 1..n−1 期 = total / n      （整数除法，向零截断）
//	第 n 期      = total − (n−1) × 上面那个商
//
// 关键性质：`Σ_{i=1..n} Installment(total, n, i) ≡ total`,**精确**。
// 这条恒等式与商怎么取无关——前 n−1 期取什么值，末期都是「总额减掉前面之和」。
// 所以「累计误差归零」不是靠舍入方式凑出来的，是构造出来的。
//
// 为什么前 n−1 期用截断而不是半进：半进时 (n−1) × 商可能**超过** total，
// 于是末期变成负数——一个负的成本会让那天的毛利凭空变大。举个真会踩到的例子：
// total=5 微单位、n=7 天，半进得 1，前 6 天共 6 > 5，末日 = −1。
// 截断的商恒 ≤ total/n，末期因而恒 ≥ 商 ≥ 0（total ≥ 0 时），且末期与其余各期
// 相差不到 n 个微单位——报表上解释得清。
//
// total 为负同样自洽（末期最负），那是「终止后又收到大额退款」这个角落里
// 会出现的贷记，见 finance.AmortizationTerm.UnamortizedMinor。
func Installment(totalMinorUnits int64, n, index int) (int64, error) {
	if n <= 0 {
		return 0, fmt.Errorf("期数 %d 必须为正: %w", n, ErrFormat)
	}
	if index < 1 || index > n {
		return 0, fmt.Errorf("期次 %d 超出 1~%d: %w", index, n, ErrFormat)
	}
	base := totalMinorUnits / int64(n)
	if index < n {
		return base, nil
	}
	// 末期吸收全部余数。(n−1)×base 不会溢出：|base| ≤ |total|/n，
	// 所以 |(n−1)×base| ≤ |total|，而 total 本来就在 int64 内。
	return totalMinorUnits - int64(n-1)*base, nil
}

// InstallmentsThrough 给出前 k 期之和（k ∈ [0, n]）。
//
// 存在的理由是**退款要只重算剩余未摊天**（§12.1）：算「剩余多少」必须先算
// 「已经摊了多少」，而那是一个前缀和。直接循环调 Installment 也对，但那让
// 一个 365 天的批次每天多跑几百次除法，且掩盖了 k=n 时结果必然等于 total
// 这条不变量——写成闭式，那条不变量就摆在眼前。
func InstallmentsThrough(totalMinorUnits int64, n, k int) (int64, error) {
	if n <= 0 {
		return 0, fmt.Errorf("期数 %d 必须为正: %w", n, ErrFormat)
	}
	if k < 0 || k > n {
		return 0, fmt.Errorf("期数前缀 %d 超出 0~%d: %w", k, n, ErrFormat)
	}
	if k == n {
		// 末期吸收余数 ⇒ 全部期次之和精确等于总额。这一支不是优化，
		// 它就是那条恒等式本身。
		return totalMinorUnits, nil
	}
	return int64(k) * (totalMinorUnits / int64(n)), nil
}
