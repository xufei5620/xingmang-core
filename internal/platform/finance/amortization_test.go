package finance_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
)

// 本文件在没有库的情况下跑完摊销的全部口径（设计稿 §3.5 + §12.1 + §12 拍板）。
//
// 三条恒等式贯穿始终，每条对应一种「不成立就悄悄错账」的形态：
//
//	Σ 全期日额 ≡ 份额              摊销总量对得上那笔付款
//	已摊 + 损失 ≡ 份额             提前失效时钱不会凭空多出或消失
//	退款前的日额不因退款而改变      §12.1 的「不追溯改已摊日额」
//
// 库层那一半（CHECK、外键、损失行的唯一性）在 subscription_store_integration_test.go。

// monthlyTerm 是贯穿本文件的样例：$29.99 的月订阅，8-01 开、8-31 到期、
// 一个账号独享。含两端 ⇒ 31 天（§12 拍板）。
func monthlyTerm() finance.AmortizationTerm {
	return finance.AmortizationTerm{
		PaidMinor:  29_990_000,
		StartsOn:   day("2026-08-01"),
		ExpiresOn:  day("2026-08-31"),
		ShareCount: 1,
	}
}

// sumOverPeriod 把一段付款在 [StartsOn, ExpiresOn] 上逐日累加，
// 并顺带断言「覆盖」这件事与日期区间一致。
func sumOverPeriod(t *testing.T, term finance.AmortizationTerm) int64 {
	t.Helper()
	var sum int64
	for d := term.StartsOn; !d.After(term.ExpiresOn); d = d.AddDate(0, 0, 1) {
		amount, covered, err := term.DailyMinor(d)
		if err != nil {
			t.Fatalf("DailyMinor(%s): %v", d.Format("2006-01-02"), err)
		}
		if !covered {
			continue
		}
		sum += amount
	}
	return sum
}

// TestTotalDaysIncludesBothEnds 钉住 §12 拍板的「起止含两端」。
//
// ⚠️ 设计稿 §3.5 的公式字面是 `expires_on − starts_on`，比这里少一天。
// 少那一天的后果不是「最后一天免费」——末日吸收会把它的钱塞进倒数第二天，
// 报表上表现为「8-30 的成本忽然翻倍」。
func TestTotalDaysIncludesBothEnds(t *testing.T) {
	if got := monthlyTerm().TotalDays(); got != 31 {
		t.Fatalf("8-01..8-31 有效天数 = %d, want 31（§12 拍板：含两端）", got)
	}
	single := finance.AmortizationTerm{
		PaidMinor: 1_000_000, StartsOn: day("2026-08-05"), ExpiresOn: day("2026-08-05"),
		ShareCount: 1,
	}
	if got := single.TotalDays(); got != 1 {
		t.Fatalf("同一天开同一天到期 = %d 天, want 1", got)
	}
}

// TestDailySumEqualsShare 是第一条恒等式：全期日额之和精确等于份额。
//
// 顺带覆盖两次除法都除不尽的情况（3 个账号分 $29.99 摊 31 天）。
func TestDailySumEqualsShare(t *testing.T) {
	for _, shareCount := range []int{1, 2, 3, 7} {
		term := monthlyTerm()
		term.ShareCount = shareCount
		term.SurchargeMinor = 1_230_000
		term.ExpiresOn = day("2026-08-31")

		share, err := term.ShareMinor()
		if err != nil {
			t.Fatalf("ShareMinor: %v", err)
		}
		if got := sumOverPeriod(t, term); got != share {
			t.Fatalf("账号数 %d：Σ 日额 = %d，份额 = %d，必须精确相等（§12.1）",
				shareCount, got, share)
		}
	}
}

// TestCoverageBoundaries 钉住三条边界：起止含两端、终止当天不摊。
//
// 「终止当天不摊」是 §3.5 的逐字要求（terminated_on..expires_on 整段算作
// 剩余未摊销额）。差一天的后果是那一天的钱既进了成本又进了损失。
func TestCoverageBoundaries(t *testing.T) {
	term := monthlyTerm()

	if _, covered, _ := term.DailyMinor(day("2026-07-31")); covered {
		t.Fatal("开始日前一天不该被覆盖")
	}
	if _, covered, _ := term.DailyMinor(day("2026-08-01")); !covered {
		t.Fatal("开始日必须被覆盖（含两端）")
	}
	if _, covered, _ := term.DailyMinor(day("2026-08-31")); !covered {
		t.Fatal("到期日必须被覆盖（含两端）")
	}
	if _, covered, _ := term.DailyMinor(day("2026-09-01")); covered {
		t.Fatal("到期日次日不该被覆盖")
	}

	term.TerminatedOn = day("2026-08-10")
	if _, covered, _ := term.DailyMinor(day("2026-08-09")); !covered {
		t.Fatal("终止日前一天仍在摊")
	}
	if _, covered, _ := term.DailyMinor(day("2026-08-10")); covered {
		t.Fatal("终止**当天**起不再摊（§3.5）")
	}
}

// TestTerminationSplitsShareIntoAmortizedAndLoss 是第二条恒等式：
// 已摊之和 + 损失 ≡ 份额。钱既不会凭空多出，也不会消失（§6.4）。
func TestTerminationSplitsShareIntoAmortizedAndLoss(t *testing.T) {
	for _, terminateDay := range []int{1, 2, 10, 30, 31} {
		term := monthlyTerm()
		term.TerminatedOn = day(fmt.Sprintf("2026-08-%02d", terminateDay))

		amortized := sumOverPeriod(t, term)
		loss, err := term.UnamortizedMinor()
		if err != nil {
			t.Fatalf("UnamortizedMinor: %v", err)
		}
		share, err := term.ShareMinor()
		if err != nil {
			t.Fatalf("ShareMinor: %v", err)
		}
		if amortized+loss != share {
			t.Fatalf("8-%02d 终止：已摊 %d + 损失 %d ≠ 份额 %d",
				terminateDay, amortized, loss, share)
		}
		if loss < 0 {
			t.Fatalf("8-%02d 终止：无退款时损失不该为负，got %d", terminateDay, loss)
		}
	}
}

// TestTerminationOnStartDayLosesEverything：开通当天就退订，整份都是损失。
//
// 这是「提前失效」的极端形态，也是登记打错后最可能出现的形态——
// 所以它必须给出一个显眼的大数字，而不是悄悄摊 0 天然后什么也不记。
func TestTerminationOnStartDayLosesEverything(t *testing.T) {
	term := monthlyTerm()
	term.TerminatedOn = term.StartsOn

	if got := sumOverPeriod(t, term); got != 0 {
		t.Fatalf("开始当天终止不该摊出任何成本, got %d", got)
	}
	loss, err := term.UnamortizedMinor()
	if err != nil {
		t.Fatalf("UnamortizedMinor: %v", err)
	}
	if loss != 29_990_000 {
		t.Fatalf("损失 = %d, want 29990000（整份未摊）", loss)
	}
}

// TestNotTerminatedHasNoLoss：还在正常摊销的批次**没有损失**。
//
// 「还没摊完的那部分」不是损失，它只是还没摊完。把它算成损失会让
// 损失科目在每个月初都凭空鼓起来一大块。
func TestNotTerminatedHasNoLoss(t *testing.T) {
	loss, err := monthlyTerm().UnamortizedMinor()
	if err != nil {
		t.Fatalf("UnamortizedMinor: %v", err)
	}
	if loss != 0 {
		t.Fatalf("未终止时损失必须为 0, got %d", loss)
	}
}

// TestRefundDoesNotRewriteAlreadyAmortizedDays 是第三条恒等式，也是 §12.1
// 最容易被实现错的一条：**退款只重算剩余未摊天**。
//
// 断言分三层：
//  1. 退款生效日**之前**的日额与没有退款时逐位相同（不追溯）；
//  2. 生效日**之后**的日额确实变小（冲减成本基础）；
//  3. 全期之和仍然精确等于**扣完退款**的份额（账目自洽）。
func TestRefundDoesNotRewriteAlreadyAmortizedDays(t *testing.T) {
	base := monthlyTerm()
	refunded := base
	refunded.RefundedMinor = 10_000_000
	refunded.RefundedOn = day("2026-08-15")

	for d := day("2026-08-01"); d.Before(day("2026-08-15")); d = d.AddDate(0, 0, 1) {
		before, _, err := base.DailyMinor(d)
		if err != nil {
			t.Fatalf("DailyMinor: %v", err)
		}
		after, _, err := refunded.DailyMinor(d)
		if err != nil {
			t.Fatalf("DailyMinor: %v", err)
		}
		if before != after {
			t.Fatalf("%s 的日额被退款追溯改写了：%d → %d（§12.1 禁止）",
				d.Format("2006-01-02"), before, after)
		}
	}

	beforeRefund, _, _ := refunded.DailyMinor(day("2026-08-14"))
	afterRefund, _, _ := refunded.DailyMinor(day("2026-08-15"))
	if afterRefund >= beforeRefund {
		t.Fatalf("退款生效后日额应下降：%d → %d", beforeRefund, afterRefund)
	}

	share, err := refunded.ShareMinor()
	if err != nil {
		t.Fatalf("ShareMinor: %v", err)
	}
	if got := sumOverPeriod(t, refunded); got != share {
		t.Fatalf("退款后 Σ 日额 = %d，净份额 = %d，必须精确相等", got, share)
	}
}

// TestRefundOnFirstDayBehavesLikeSmallerBatch：退款生效日 = 开始日时，
// 整批就按净额摊——没有「已摊」那一段要保留。
func TestRefundOnFirstDayBehavesLikeSmallerBatch(t *testing.T) {
	term := monthlyTerm()
	term.RefundedMinor = 9_990_000
	term.RefundedOn = term.StartsOn

	plain := monthlyTerm()
	plain.PaidMinor = 20_000_000

	for d := term.StartsOn; !d.After(term.ExpiresOn); d = d.AddDate(0, 0, 1) {
		got, _, err := term.DailyMinor(d)
		if err != nil {
			t.Fatalf("DailyMinor: %v", err)
		}
		want, _, err := plain.DailyMinor(d)
		if err != nil {
			t.Fatalf("DailyMinor: %v", err)
		}
		if got != want {
			t.Fatalf("%s: 首日退款后应与直接登记净额的批次逐日相同（%d vs %d）",
				d.Format("2006-01-02"), got, want)
		}
	}
}

// TestRefundAfterTerminationReducesLoss 覆盖「先退订、后到账」这条真实路径。
//
// 退款让最终成本基础变小，那笔已经结转的损失当然也要跟着变小——
// 不重算的话，损失科目里会永远留着一个比实际多的数（宪法 12 条）。
func TestRefundAfterTerminationReducesLoss(t *testing.T) {
	terminated := monthlyTerm()
	terminated.TerminatedOn = day("2026-08-11")

	lossBefore, err := terminated.UnamortizedMinor()
	if err != nil {
		t.Fatalf("UnamortizedMinor: %v", err)
	}

	// 退款在终止之后才到账，生效日仍须落在有效期内（期外无剩余未摊天可冲减）
	refunded := terminated
	refunded.RefundedMinor = 5_000_000
	refunded.RefundedOn = day("2026-08-20")

	lossAfter, err := refunded.UnamortizedMinor()
	if err != nil {
		t.Fatalf("UnamortizedMinor: %v", err)
	}
	if lossAfter != lossBefore-5_000_000 {
		t.Fatalf("损失应按退款额等量减少：%d → %d（差 %d，want 5000000）",
			lossBefore, lossAfter, lossBefore-lossAfter)
	}

	// 已摊那 10 天完全不受影响
	if got := sumOverPeriod(t, refunded); got != sumOverPeriod(t, terminated) {
		t.Fatal("终止后到账的退款不该改写已摊区间")
	}
}

// TestLateLargeRefundProducesCredit：终止后收到的退款大于未摊部分时，
// 损失变成**负数**——一笔贷记，不是 bug。
//
// 钳成 0 会让那笔钱从账上消失，所以 amortization_loss.loss_minor 没有非负 CHECK。
func TestLateLargeRefundProducesCredit(t *testing.T) {
	term := monthlyTerm()
	term.TerminatedOn = day("2026-08-29") // 已摊 28 天，未摊很少
	term.RefundedMinor = 29_990_000       // 全额退款
	term.RefundedOn = day("2026-08-30")

	loss, err := term.UnamortizedMinor()
	if err != nil {
		t.Fatalf("UnamortizedMinor: %v", err)
	}
	if loss >= 0 {
		t.Fatalf("全额退款且已摊 28 天时，损失应为负（贷记），got %d", loss)
	}
	amortized := sumOverPeriod(t, term)
	share, err := term.ShareMinor()
	if err != nil {
		t.Fatalf("ShareMinor: %v", err)
	}
	if amortized+loss != share {
		t.Fatalf("贷记情形下恒等式仍须成立：已摊 %d + 损失 %d ≠ 份额 %d",
			amortized, loss, share)
	}
}

// TestValidateRejectsOutOfPeriodRefundDate：期外的退款生效日一律拒绝。
//
// 期外没有「剩余未摊天」可以吸收那笔冲减（§12.1），而平台 v1 没有
// 「过期后信用」这个科目。静默接受等于把那笔钱丢掉。
func TestValidateRejectsOutOfPeriodRefundDate(t *testing.T) {
	term := monthlyTerm()
	term.RefundedMinor = 1_000_000
	term.RefundedOn = day("2026-09-05")
	if err := term.Validate(); !errors.Is(err, finance.ErrInvalidFormat) {
		t.Fatalf("到期后的退款生效日必须被拒, got %v", err)
	}

	term.RefundedOn = time.Time{}
	if err := term.Validate(); !errors.Is(err, finance.ErrMissingField) {
		t.Fatalf("有退款额却没有生效日必须被拒, got %v", err)
	}

	noRefund := monthlyTerm()
	noRefund.RefundedOn = day("2026-08-03")
	if err := noRefund.Validate(); !errors.Is(err, finance.ErrInconsistent) {
		t.Fatalf("没有退款额却带着生效日必须被拒, got %v", err)
	}
}

// TestValidateRejectsRefundBeyondBasis：退款不得超过实付 + 附加。
func TestValidateRejectsRefundBeyondBasis(t *testing.T) {
	term := monthlyTerm()
	term.RefundedMinor = term.PaidMinor + 1
	term.RefundedOn = term.StartsOn
	if err := term.Validate(); !errors.Is(err, finance.ErrInconsistent) {
		t.Fatalf("成本基础不得为负, got %v", err)
	}
}

// TestValidateRejectsTerminationOutsidePeriod：到期之后的「提前失效」
// 什么也没有提前，结转出来必然是 0，只会留一条看不懂的记录。
func TestValidateRejectsTerminationOutsidePeriod(t *testing.T) {
	term := monthlyTerm()
	term.TerminatedOn = day("2026-09-01")
	if err := term.Validate(); !errors.Is(err, finance.ErrInvalidFormat) {
		t.Fatalf("到期后的终止日必须被拒, got %v", err)
	}
}

// TestValidateRejectsNonCalendarDay：带时分秒的「日期」会让 daysBetween
// 少算一天，而且只在跨零点前后才表现出来——最难查的一类。
func TestValidateRejectsNonCalendarDay(t *testing.T) {
	term := monthlyTerm()
	term.StartsOn = time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC)
	if err := term.Validate(); !errors.Is(err, finance.ErrInvalidFormat) {
		t.Fatalf("非日历日必须被拒, got %v", err)
	}
}

// --- 代理资产 ---

func mountedProxy() finance.ProxyAsset {
	return finance.ProxyAsset{
		ID:                 uuid.New(),
		PaidMinor:          6_200_000,
		Currency:           "USD",
		OpenedOn:           day("2026-08-01"),
		ExpiresOn:          day("2026-08-31"),
		SharedAccountCount: 2,
		Mounted:            true,
		Environment:        "production",
	}
}

// TestUnmountedProxyCostsKnownZero 钉住 §10.3 的「代理未挂载 → 每日成本 = 0」。
//
// 关键在第二个返回值：那是**已知的 0**（在期内、但没挂上），不是「不在期内」。
// 两者混成一个的话，「这份代理今天没在服务」与「这份代理今天不该被算进来」
// 在计数上就分不开了。
func TestUnmountedProxyCostsKnownZero(t *testing.T) {
	proxy := mountedProxy()
	proxy.Mounted = false

	amount, covered, err := proxy.DailyMinor(day("2026-08-10"))
	if err != nil {
		t.Fatalf("DailyMinor: %v", err)
	}
	if !covered {
		t.Fatal("未挂载的代理在期内仍应报告「被覆盖」——那是已知的 0，不是不在期内")
	}
	if amount != 0 {
		t.Fatalf("未挂载的代理每日成本必须为 0, got %d", amount)
	}

	if _, covered, _ := proxy.DailyMinor(day("2026-09-05")); covered {
		t.Fatal("期外仍不该被覆盖")
	}
}

// TestMountedProxySplitsBySharedAccounts：挂载的代理按分摊账号数摊。
func TestMountedProxySplitsBySharedAccounts(t *testing.T) {
	proxy := mountedProxy() // $6.20 / 2 账号 / 31 天
	amount, covered, err := proxy.DailyMinor(day("2026-08-10"))
	if err != nil {
		t.Fatalf("DailyMinor: %v", err)
	}
	if !covered {
		t.Fatal("期内必须被覆盖")
	}
	// 6200000/2 = 3100000（半进，除得尽）；3100000/31 = 100000（除得尽）
	if amount != 100_000 {
		t.Fatalf("每日代理成本 = %d, want 100000", amount)
	}
}

// --- 当日成本聚合 ---

func batchFor(accountID uuid.UUID, paid int64, start, expire time.Time) finance.SubscriptionBatch {
	return finance.SubscriptionBatch{
		ID:                uuid.New(),
		UpstreamAccountID: accountID,
		PaidMinor:         paid,
		Currency:          "USD",
		StartsOn:          start,
		ExpiresOn:         expire,
		AccountCount:      1,
	}
}

// TestAmortizeDaySumsBatchesAndProxy：当日成本 = 覆盖当日的全部批次之和
// + 各自的代理分摊（§3.5）。
func TestAmortizeDaySumsBatchesAndProxy(t *testing.T) {
	account := uuid.New()
	proxy := mountedProxy()
	batch := batchFor(account, 29_990_000, day("2026-08-01"), day("2026-08-31"))
	batch.ProxyAssetID = proxy.ID

	cost, err := finance.AmortizeDay(
		[]finance.AmortizableBatch{{Batch: batch, Proxy: &proxy}}, day("2026-08-10"))
	if err != nil {
		t.Fatalf("AmortizeDay: %v", err)
	}
	if cost.CostMinor != 967_419+100_000 {
		t.Fatalf("当日成本 = %d, want %d（订阅 967419 + 代理 100000）",
			cost.CostMinor, 967_419+100_000)
	}
	if cost.BatchCount != 1 || cost.ProxyCount != 1 {
		t.Fatalf("计数不对: %+v", cost)
	}
	if cost.Currency != "USD" {
		t.Fatalf("币种 = %q", cost.Currency)
	}
}

// TestAmortizeDayDeduplicatesSharedProxy 钉住续费期重叠时的去重。
//
// 两条批次引用同一份代理是常态（旧批次还没到期、新批次已经开始）。
// 不去重的话那份代理当天被算两遍——它就一份，不会因为被两条批次指着就贵一倍。
func TestAmortizeDayDeduplicatesSharedProxy(t *testing.T) {
	account := uuid.New()
	proxy := mountedProxy()
	first := batchFor(account, 29_990_000, day("2026-08-01"), day("2026-08-31"))
	first.ProxyAssetID = proxy.ID
	second := batchFor(account, 29_990_000, day("2026-08-10"), day("2026-09-09"))
	second.ProxyAssetID = proxy.ID

	cost, err := finance.AmortizeDay([]finance.AmortizableBatch{
		{Batch: first, Proxy: &proxy},
		{Batch: second, Proxy: &proxy},
	}, day("2026-08-10"))
	if err != nil {
		t.Fatalf("AmortizeDay: %v", err)
	}
	if cost.BatchCount != 2 {
		t.Fatalf("两条批次都该计入: %+v", cost)
	}
	if cost.ProxyCount != 1 || cost.DedupedProxies != 1 {
		t.Fatalf("同一份代理当日只该算一次: %+v", cost)
	}
	// 8-10 落在两条批次里：第一条 967419，第二条（31 天，8-10 起）967419，代理 100000
	if cost.CostMinor != 967_419+967_419+100_000 {
		t.Fatalf("当日成本 = %d，代理疑似被算了两遍", cost.CostMinor)
	}
}

// TestAmortizeDayWithoutCoveringBatchIsUnknown：没有批次覆盖当日 →
// **成本未知**，不是 0（§5.1 的同一条纪律）。
//
// 「还没登记批次」与「订阅真的到期了」在库里长得一模一样，平台分不出来。
// 写 0 会让这条渠道显示一个笃定的「零成本、毛利 = 收入」。
func TestAmortizeDayWithoutCoveringBatchIsUnknown(t *testing.T) {
	account := uuid.New()
	expired := batchFor(account, 29_990_000, day("2026-07-01"), day("2026-07-31"))

	_, err := finance.AmortizeDay(
		[]finance.AmortizableBatch{{Batch: expired}}, day("2026-08-10"))
	if !errors.Is(err, finance.ErrNoAmortizableBatch) {
		t.Fatalf("无覆盖批次应为 ErrNoAmortizableBatch, got %v", err)
	}

	if _, err := finance.AmortizeDay(nil, day("2026-08-10")); !errors.Is(err, finance.ErrNoAmortizableBatch) {
		t.Fatalf("空清单同样是「未知」, got %v", err)
	}
}

// TestAmortizeDayRefusesMixedCurrency：不同币种的最小单位不能相加。
func TestAmortizeDayRefusesMixedCurrency(t *testing.T) {
	account := uuid.New()
	usd := batchFor(account, 29_990_000, day("2026-08-01"), day("2026-08-31"))
	cny := batchFor(account, 99_000_000, day("2026-08-01"), day("2026-08-31"))
	cny.Currency = "CNY"

	_, err := finance.AmortizeDay([]finance.AmortizableBatch{
		{Batch: usd}, {Batch: cny},
	}, day("2026-08-10"))
	if !errors.Is(err, finance.ErrAmortizationMixedCurrency) {
		t.Fatalf("币种混杂必须报错而不是相加, got %v", err)
	}
}

// TestAmortizeDayIgnoresProxyOutsideItsOwnPeriod：代理先于订阅到期是常见形态，
// 那几天的代理成本确实是 0，但**不计入 ProxyCount**——
// 「有一份代理但今天不在期内」与「今天摊了一份代理」是两回事。
func TestAmortizeDayIgnoresProxyOutsideItsOwnPeriod(t *testing.T) {
	account := uuid.New()
	proxy := mountedProxy()
	proxy.ExpiresOn = day("2026-08-05")
	batch := batchFor(account, 29_990_000, day("2026-08-01"), day("2026-08-31"))
	batch.ProxyAssetID = proxy.ID

	cost, err := finance.AmortizeDay(
		[]finance.AmortizableBatch{{Batch: batch, Proxy: &proxy}}, day("2026-08-10"))
	if err != nil {
		t.Fatalf("AmortizeDay: %v", err)
	}
	if cost.ProxyCount != 0 {
		t.Fatalf("代理已过期，不该计入: %+v", cost)
	}
	if cost.CostMinor != 967_419 {
		t.Fatalf("当日成本 = %d, want 967419（只有订阅）", cost.CostMinor)
	}
}

// TestAccountGrainTokenIDRoundTrips 钉住账号级聚合行的哨兵形态。
//
// 前缀是台账主键的一部分，改它等于改主键——所以构造与识别只有一处实现。
func TestAccountGrainTokenIDRoundTrips(t *testing.T) {
	token := finance.AccountGrainTokenID("acct-258")
	if token != "account:acct-258" {
		t.Fatalf("哨兵 = %q, want account:acct-258", token)
	}
	if !finance.IsAccountGrain(token) {
		t.Fatal("哨兵必须被识别为账号级")
	}
	owner, ok := finance.AccountGrainOwner(token)
	if !ok || owner != "acct-258" {
		t.Fatalf("取回自营账号 = %q/%v", owner, ok)
	}
	if finance.IsAccountGrain("tok-a") {
		t.Fatal("真令牌不该被识别为账号级")
	}
	if _, ok := finance.AccountGrainOwner("tok-a"); ok {
		t.Fatal("真令牌里取不出自营账号")
	}
}
