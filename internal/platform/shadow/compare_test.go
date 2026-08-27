package shadow

import (
	"os"
	"strings"
	"testing"

	"github.com/xufei5620/xingmang-platform/internal/platform/pgreadonly"
)

// 对比器是纯函数，所以整套行为都能用手造数据钉死——不需要任何一个真库。
// 这不是省事：影子对比要判的恰恰是「两个真库对不对得上」，
// 用真库测它等于拿被测对象当判据。

// row 是构造 Row 的简写。
func row(account, day string, revenue, cost Amount) Row {
	return Row{
		AccountID: account, Day: day,
		Revenue: revenue, Cost: cost,
		Currency: DefaultCurrency, BusinessDayTZ: DefaultBusinessDayTZ,
		RowCount: 1,
	}
}

// TestCompareIdentical：两侧逐位相同 → clean。
//
// 这是 14 天验收每天要看到的那个结果，也是唯一一个允许 exit 0 的结果。
func TestCompareIdentical(t *testing.T) {
	platform := []Row{
		row("acc-1", "2026-08-27", KnownCents(12345), KnownCents(6789)),
		row("acc-2", "2026-08-27", KnownCents(0), KnownCents(0)),
	}
	soloai := []Row{
		row("acc-2", "2026-08-27", KnownCents(0), KnownCents(0)),
		row("acc-1", "2026-08-27", KnownCents(12345), KnownCents(6789)),
	}

	got, err := Compare(platform, soloai, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Clean() {
		t.Fatalf("逐位相同应判 clean: %+v", got.Summary)
	}
	if got.Summary.Equal != 2 || got.Summary.Pairs != 2 {
		t.Fatalf("统计不对: %+v", got.Summary)
	}
	// 输入顺序不同、输出顺序必须一致：14 天的报告要能逐日 diff，
	// 顺序不稳的话两份内容相同的报告也会 diff 出一堆噪音。
	if got.Pairs[0].Key.AccountID != "acc-1" || got.Pairs[1].Key.AccountID != "acc-2" {
		t.Fatalf("结果未按 (业务日, 账号) 稳定排序: %+v", got.Pairs)
	}
	// 已知的 0 参与比较并算「对上」——它与「未知」是两回事。
	if !got.Pairs[1].OK() {
		t.Fatal("两侧都是已知的 0，应判相等")
	}
}

// TestCompareHalfCentBoundary 是**容差旋钮存在的唯一理由**（设计稿 §9）。
//
// SoloAI 侧的成本是 float64 除法算出来的，平台侧是整数定点除法，两者在
// 「真值恰在半分边界」上有可能各进各的，差出 1 分。默认严格 0 时这必须报差异；
// 把旋钮开到 1 分才放行。
//
// 注意断言的是**两种行为都对**：默认严格（不许悄悄容忍），开了旋钮才放行
// （旋钮得真的有用）。只测一边的话，另一边坏掉不会被发现。
func TestCompareHalfCentBoundary(t *testing.T) {
	// 12.345 美元：平台的整数定点半进得 1235 分，SoloAI 的 float64 可能落 1234。
	platform := []Row{row("acc-1", "2026-08-27", KnownCents(1235), KnownCents(0))}
	soloai := []Row{row("acc-1", "2026-08-27", KnownCents(1234), KnownCents(0))}

	strict, err := Compare(platform, soloai, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strict.Clean() {
		t.Fatal("默认严格 0：差 1 分必须报出来，不能悄悄容忍（§12 拍板）")
	}
	if strict.Summary.Differs != 1 {
		t.Fatalf("应有 1 格差异: %+v", strict.Summary)
	}
	if got := strict.Pairs[0].Revenue.DiffCents; got != 1 {
		t.Fatalf("差额 = %d 分, want 1", got)
	}
	// 毛利是派生量：收入差 1 分、成本相同，毛利必然也差 1 分。
	if got := strict.Pairs[0].Profit.DiffCents; got != 1 {
		t.Fatalf("毛利差额 = %d 分, want 1（毛利 = 收入 − 成本，两侧都用折分后的值）", got)
	}

	loose, err := Compare(platform, soloai, Options{ToleranceCents: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !loose.Clean() {
		t.Fatalf("容差 1 分时应放行: %+v", loose.Summary)
	}
	// 但容差是**绝对值**，不是「随便多少都行」：差 2 分照样红。
	platform[0].Revenue = KnownCents(1236)
	over, err := Compare(platform, soloai, Options{ToleranceCents: 1})
	if err != nil {
		t.Fatal(err)
	}
	if over.Clean() {
		t.Fatal("差 2 分超出 1 分容差，必须报出来")
	}
}

// TestCompareToleranceIsSymmetric：容差对正负差额一视同仁。
func TestCompareToleranceIsSymmetric(t *testing.T) {
	for _, delta := range []int64{-1, 1} {
		platform := []Row{row("acc-1", "2026-08-27", KnownCents(1000+delta), KnownCents(0))}
		soloai := []Row{row("acc-1", "2026-08-27", KnownCents(1000), KnownCents(0))}
		got, err := Compare(platform, soloai, Options{ToleranceCents: 1})
		if err != nil {
			t.Fatal(err)
		}
		if !got.Clean() {
			t.Fatalf("差 %d 分应在 1 分容差内", delta)
		}
	}
}

// TestCompareMissingRowsAreNotZero 是本工具最要紧的一条纪律。
//
// 一侧有、另一侧没有 → 报「缺行」，**绝不当成那边是 0 去算差额**。
// 当成 0 的话，一个采集没跑的账号会显示成「差了 123.45 美元」，
// 于是所有人去查金额，而真正的问题是那天根本没采。
func TestCompareMissingRowsAreNotZero(t *testing.T) {
	platform := []Row{row("only-platform", "2026-08-27", KnownCents(10000), KnownCents(2000))}
	soloai := []Row{row("only-soloai", "2026-08-27", KnownCents(30000), KnownCents(4000))}

	got, err := Compare(platform, soloai, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Clean() {
		t.Fatal("单侧缺行不算对上")
	}
	if got.Summary.MissingOnPlatform != 1 || got.Summary.MissingOnSoloAI != 1 {
		t.Fatalf("两侧各缺一格: %+v", got.Summary)
	}
	// 差额不得被算出来——两侧不是都已知，diff 没有意义。
	for _, pair := range got.Pairs {
		for _, m := range pair.Measures() {
			if m.DiffCents != 0 {
				t.Fatalf("缺行的格不该算出差额 %d（%s/%s %s）",
					m.DiffCents, pair.Key.Day, pair.Key.AccountID, m.Measure)
			}
			if m.Verdict != VerdictMissingOnPlatform && m.Verdict != VerdictMissingOnSoloAI {
				t.Fatalf("判定 = %q, want missing_*", m.Verdict)
			}
		}
	}
}

// TestCompareUnknownIsNotMissingAndNotZero：未知与缺行、与已知 0 都不同。
//
// 三者的排查方向完全不同：
//
//	缺行   → 那天的采集有没有跑？
//	未知   → 跑了，但那一次上游读取为什么失败？
//	已知 0 → 跑了也读到了，那天就是没有流量。
//
// 把它们合成一类，等于把三个不同的问题合成一句「对不上」。
func TestCompareUnknownIsNotMissingAndNotZero(t *testing.T) {
	platform := []Row{row("acc-1", "2026-08-27", Unknown(), KnownCents(2000))}
	soloai := []Row{row("acc-1", "2026-08-27", KnownCents(10000), KnownCents(2000))}

	got, err := Compare(platform, soloai, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Clean() {
		t.Fatal("一侧未知不算对上")
	}
	if got.Summary.Unknown != 1 {
		t.Fatalf("应有 1 格未知: %+v", got.Summary)
	}
	pair := got.Pairs[0]
	if pair.Revenue.Verdict != VerdictUnknownOnPlatform {
		t.Fatalf("收入判定 = %q, want unknown_on_platform", pair.Revenue.Verdict)
	}
	if pair.Revenue.DiffCents != 0 {
		t.Fatalf("未知的项不该算出差额: %d", pair.Revenue.DiffCents)
	}
	// 成本两侧都已知且相同 → 那一项照样算对上：一项未知不该污染另一项。
	if !pair.Cost.OK() {
		t.Fatalf("成本两侧相同，应判相等: %+v", pair.Cost)
	}
	// 毛利跟着未知传播（与库里那个 GENERATED 列同一条语义）。
	if pair.Profit.Verdict != VerdictUnknownOnPlatform {
		t.Fatalf("收入未知时毛利也应未知, got %q", pair.Profit.Verdict)
	}
}

// TestCompareCurrencyMismatchIsProblemNotDiff：口径不一致是**错误**不是差异。
//
// 币种不同意味着两边根本没在量同一个东西，此时那个差额是个没有意义的数字。
// 混进差异统计的话，一个币种配错的账号会显示成一笔巨额差异，
// 然后所有人去查上游——而要修的是登记簿里的币种。
func TestCompareCurrencyMismatchIsProblemNotDiff(t *testing.T) {
	bad := row("acc-1", "2026-08-27", KnownCents(10000), KnownCents(2000))
	bad.Currency = "CNY"
	soloai := []Row{row("acc-1", "2026-08-27", KnownCents(10000), KnownCents(2000))}

	got, err := Compare([]Row{bad}, soloai, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Clean() {
		t.Fatal("口径错误必须让这次对比不 clean")
	}
	if got.Summary.Problems != 1 {
		t.Fatalf("应有 1 条口径错误: %+v", got.Summary)
	}
	if got.Summary.Differs != 0 {
		t.Fatalf("币种不一致不该被算成金额差异: %+v", got.Summary)
	}
	p := got.Problems[0]
	if p.Side != SidePlatform || !strings.Contains(p.Reason, "CNY") {
		t.Fatalf("问题描述应指出是哪一侧、什么币种: %+v", p)
	}
}

// TestCompareBusinessDayTZMismatchIsProblem：切日偏移不同同样是口径错误。
//
// 切日不同会让同一笔请求落进不同的天——收入记 D、成本记 D+1，
// 利润凭空多一天又少一天，而且不报错（§4 ★口径常量）。
func TestCompareBusinessDayTZMismatchIsProblem(t *testing.T) {
	bad := row("acc-1", "2026-08-27", KnownCents(10000), KnownCents(2000))
	bad.BusinessDayTZ = "+00:00"
	got, err := Compare([]Row{bad},
		[]Row{row("acc-1", "2026-08-27", KnownCents(10000), KnownCents(2000))}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.Problems != 1 {
		t.Fatalf("切日偏移不一致应报口径错误: %+v", got.Summary)
	}
	if !strings.Contains(got.Problems[0].Reason, "+08:00") {
		t.Fatalf("应说出约定值: %s", got.Problems[0].Reason)
	}
}

// TestCompareRejectsDuplicateKeys：同一侧重复键必须报错。
//
// 静默取一条或相加，都会让「差异」变成「拿一部分数据对全部数据」——
// 一个悄悄少算的报告比一个报错的报告危险得多。
func TestCompareRejectsDuplicateKeys(t *testing.T) {
	dup := []Row{
		row("acc-1", "2026-08-27", KnownCents(1), KnownCents(0)),
		row("acc-1", "2026-08-27", KnownCents(2), KnownCents(0)),
	}
	if _, err := Compare(dup, nil, Options{}); err == nil {
		t.Fatal("平台侧重复键必须报错")
	} else if !strings.Contains(err.Error(), "GROUP BY") {
		t.Fatalf("错误应指向读取器的分组问题: %v", err)
	}
	if _, err := Compare(nil, dup, Options{}); err == nil {
		t.Fatal("SoloAI 侧重复键同样必须报错")
	}
}

// TestCompareRejectsEmptyKeys：缺 account_id 或 day 的行无法配对。
func TestCompareRejectsEmptyKeys(t *testing.T) {
	for _, bad := range []Row{
		row("", "2026-08-27", KnownCents(1), KnownCents(0)),
		row("acc-1", "", KnownCents(1), KnownCents(0)),
	} {
		if _, err := Compare([]Row{bad}, nil, Options{}); err == nil {
			t.Fatalf("缺键的行必须报错: %+v", bad)
		}
	}
}

// TestCompareSeverityPicksWorstPerPair：一格按最严重的那一项归类。
//
// 三项各归各的会让一格被数三次，而报告头上的「今天有几格不对」必须是**格数**。
func TestCompareSeverityPicksWorstPerPair(t *testing.T) {
	// 收入未知（较严重）+ 成本有差异（较轻）→ 这一格算「未知」，不算「差异」。
	platform := []Row{row("acc-1", "2026-08-27", Unknown(), KnownCents(2001))}
	soloai := []Row{row("acc-1", "2026-08-27", KnownCents(10000), KnownCents(2000))}

	got, err := Compare(platform, soloai, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.Pairs != 1 {
		t.Fatalf("格数 = %d, want 1", got.Summary.Pairs)
	}
	if got.Summary.Unknown != 1 || got.Summary.Differs != 0 {
		t.Fatalf("应按最严重的一项归类成「未知」: %+v", got.Summary)
	}
}

// TestCompareEmptyInputsAreClean：两侧都没有数据 = 没有差异。
//
// 这是刻意的：窗口里没有计量型渠道时不该报错。
// 「没配 SoloAI DSN」那种情况在入口就退出了（ErrSoloAINotConfigured），
// 不会走到这里假装成一份 clean 报告。
func TestCompareEmptyInputsAreClean(t *testing.T) {
	got, err := Compare(nil, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Clean() || got.Summary.Pairs != 0 {
		t.Fatalf("空输入应是 clean 且 0 格: %+v", got.Summary)
	}
}

// TestSoloAIQueriesAreSelectOnly：本包发给 SoloAI 库的每条 SQL 都必须只读。
func TestSoloAIQueriesAreSelectOnly(t *testing.T) {
	if len(soloaiQueries) == 0 {
		t.Fatal("语句清单为空——这条测试就没有意义了")
	}
	for _, q := range soloaiQueries {
		if err := pgreadonly.AssertSelectOnly(q); err != nil {
			t.Fatalf("语句未通过只读判据:\n%s\n%v", q, err)
		}
	}
}

// TestSoloAIReaderHasNoWritePath 是闸 4 的源码级证明。
//
// 上一条证明「登记了的语句是只读的」，证明不了「没有别的路径绕过登记」。
// 这条直接扫源码：pgx 上任何能写的入口一个都不许出现。
func TestSoloAIReaderHasNoWritePath(t *testing.T) {
	src, err := os.ReadFile("soloai.go")
	if err != nil {
		t.Fatalf("读源码失败: %v", err)
	}
	text := string(src)
	for _, forbidden := range []string{".Exec(", ".CopyFrom(", ".SendBatch(", ".Begin("} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("soloai.go 出现了可写入口 %q——只读通道上不该有它（ADR-018 闸 4）", forbidden)
		}
	}
	// 唯一允许碰池的地方，且它前面必须先跑只读断言。
	if got := strings.Count(text, "r.pool.Query("); got != 1 {
		t.Fatalf("r.pool.Query( 出现 %d 次, want 1", got)
	}
	if !strings.Contains(text, "pgreadonly.AssertSelectOnly(soloaiProfitQuery)") {
		t.Fatal("取数前必须过 AssertSelectOnly")
	}
}
