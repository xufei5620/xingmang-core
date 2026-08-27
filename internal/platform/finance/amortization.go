package finance

// 订阅 / 代理成本的摊销计算器（XM-0037c，设计稿 §3.5 + §12.1 + §12 拍板）。
//
// 计量型渠道的成本是**读**出来的（上游实扣 ÷ 倍率）；订阅型渠道的成本是
// **算**出来的——一笔我们自己付出去的钱，按天、按账号摊开。这个区别贯穿本片：
// 读会失败（§5.1 的 NULL），算不会。
//
// 本文件只有算术与日期，不碰库、不碰 HTTP。一个批次与一份代理的摊销规则
// 逐条相同（只是列名不同），所以它们共用同一个 AmortizationTerm——
// 两份实现迟早在退款或末日那一格上分叉，而那种分叉不会报错。

import (
	"fmt"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// dayDuration 是一个日历日。业务日都归一到 UTC 零点（见 ProfitRow.BusinessDay），
// 所以两个业务日之差恒为 24h 的整数倍，用它做整除是精确的。
const dayDuration = 24 * time.Hour

// AmortizationRatio 是订阅型台账行的 ratio_snapshot：恒为 1。
//
// §12 拍板：「摊销行 ratio_snapshot = 1，语义『摊销值即成本，未经折算』」。
// 台账的 ratio_snapshot 列 NOT NULL 且 CHECK > 0（000009），它冻结的是
// 「本行 cost 是用哪个倍率折出来的」。订阅型压根没有折算这一步，1 是唯一
// 诚实的填法：写 0 会被 CHECK 拒（且语义是「除以 0」），留空写不进去，
// 编一个别的数会让人以为这笔成本被打过折。
//
// 反过来读也成立：ratio_snapshot = 1 且 token_id 是账号级哨兵的行，
// 就是摊销行——037e 的影子对比据此把订阅型排除在外（§9 只比计量型）。
var AmortizationRatio = money.MustParseRatio("1")

// AmortizationTerm 是一段可摊销的付款：一笔订阅批次，或一份代理资产。
//
// 它是**纯值**：给定同样的字段与同一个业务日，永远给出同一个金额。
// 这条性质是台账可复核的前提——运营拿着票据和这几个数就能自己算一遍。
type AmortizationTerm struct {
	// PaidMinor / SurchargeMinor 是实付与附加费用（scale-6 微单位，§2.4）。
	PaidMinor      int64
	SurchargeMinor int64

	// RefundedMinor 是**累计**退款额；RefundedOn 是它生效的业务日。
	//
	// 两者必须同时有或同时无。退款从 RefundedOn 起冲减成本基础，
	// **只重算剩余未摊天**，不追溯改已摊日额（§3.5/§12.1）。
	RefundedMinor int64
	RefundedOn    time.Time

	// StartsOn / ExpiresOn 是有效期，**含两端**（§12 拍板）：
	// 有效天数 = ExpiresOn − StartsOn + 1。
	StartsOn  time.Time
	ExpiresOn time.Time

	// TerminatedOn 是提前失效日；零值 = 未终止。
	//
	// **当天起不再摊销**：已摊区间是 [StartsOn, TerminatedOn−1]，
	// 剩余转损失（§3.5 逐字：「terminated_on..expires_on 的剩余未摊销成本转为损失」）。
	TerminatedOn time.Time

	// ShareCount 是分摊份数（批次的 account_count / 代理的 shared_account_count）。
	ShareCount int
}

// TotalDays 返回有效天数（含两端，§12 拍板）。
//
// ⚠️ 设计稿 §3.5 的公式字面写的是 `expires_on − starts_on`，比这里少一天。
// 以 §12 的拍板记录为准（更晚、且经产品负责人确认「起止含两端」）：
// 一个 8-01 开、8-31 到期的月订阅是 31 天，按 30 天摊会让 8-31 那天凭空免费，
// 而末日吸收会把那一天的钱塞进 8-30——报表上看是「最后第二天贵了一倍」。
func (t AmortizationTerm) TotalDays() int {
	return daysBetween(t.StartsOn, t.ExpiresOn) + 1
}

// GrossBasisMinor 是未扣退款的成本基础：实付 + 附加。
func (t AmortizationTerm) GrossBasisMinor() (int64, error) {
	if t.SurchargeMinor > 0 && t.PaidMinor > maxInt64Amount-t.SurchargeMinor {
		return 0, fmt.Errorf("实付 %d + 附加 %d 超出 int64: %w",
			t.PaidMinor, t.SurchargeMinor, ErrInvalidFormat)
	}
	return t.PaidMinor + t.SurchargeMinor, nil
}

// NetBasisMinor 是扣掉累计退款后的成本基础（§3.5：部分退款冲减成本基础）。
func (t AmortizationTerm) NetBasisMinor() (int64, error) {
	gross, err := t.GrossBasisMinor()
	if err != nil {
		return 0, err
	}
	return gross - t.RefundedMinor, nil
}

// ShareMinor 是**本账号**最终要承担的那一份（净基础 ÷ 份数，半进）。
//
// 全期日额之和恒等于它（末日吸收，§12.1）；终止时未摊出去的部分等于
// 它减去已摊之和——两条恒等式由 amortization_test.go 的穷举用例钉住。
func (t AmortizationTerm) ShareMinor() (int64, error) {
	net, err := t.NetBasisMinor()
	if err != nil {
		return 0, err
	}
	return money.Split(net, t.ShareCount)
}

// grossShareMinor 是退款生效前那段用的份额（未扣退款）。
func (t AmortizationTerm) grossShareMinor() (int64, error) {
	gross, err := t.GrossBasisMinor()
	if err != nil {
		return 0, err
	}
	return money.Split(gross, t.ShareCount)
}

// Covers 报告某业务日是否落在本段的摊销区间内。
//
// 三条同时成立：不早于开始日、不晚于到期日、且早于终止日（终止当天不摊）。
func (t AmortizationTerm) Covers(day time.Time) bool {
	if day.Before(t.StartsOn) || day.After(t.ExpiresOn) {
		return false
	}
	if !t.TerminatedOn.IsZero() && !day.Before(t.TerminatedOn) {
		return false
	}
	return true
}

// DailyMinor 返回某业务日应摊的金额，以及这一天是否被本段覆盖。
//
// 不覆盖时返回 (0, false)：调用方必须用第二个返回值区分「这一天摊 0」
// （合法：一份未挂载的代理）与「这一天根本不在期内」——把后者当 0 加进去
// 不会出错，但会让「今天有几笔批次在摊」这个计数说谎。
func (t AmortizationTerm) DailyMinor(day time.Time) (int64, bool, error) {
	if err := t.Validate(); err != nil {
		return 0, false, err
	}
	if !t.Covers(day) {
		return 0, false, nil
	}
	amount, err := t.scheduledDay(daysBetween(t.StartsOn, day) + 1)
	if err != nil {
		return 0, false, err
	}
	return amount, true, nil
}

// UnamortizedMinor 返回提前失效时剩余未摊销的金额（§12 拍板的**损失科目**）。
//
// 未终止时为 0——不是「还没摊完的那部分」：一个还在正常摊销的批次没有损失，
// 它只是还没摊完。
//
// **可以为负**，且那不是 bug：终止之后又收到一大笔退款时，最终成本基础会低于
// 已经摊出去的金额，差额是一笔**贷记**。把它钳到 0 等于让那笔钱从账上消失
// （宪法 12 条）。amortization_loss.loss_minor 因此没有非负 CHECK。
func (t AmortizationTerm) UnamortizedMinor() (int64, error) {
	if err := t.Validate(); err != nil {
		return 0, err
	}
	if t.TerminatedOn.IsZero() {
		return 0, nil
	}
	// 终止当天不摊 ⇒ 已摊天数 = TerminatedOn − StartsOn。
	// TerminatedOn 落在 [StartsOn, ExpiresOn]（Validate 保证），故 k ∈ [0, N−1]。
	amortized, err := t.amortizedThrough(daysBetween(t.StartsOn, t.TerminatedOn))
	if err != nil {
		return 0, err
	}
	share, err := t.ShareMinor()
	if err != nil {
		return 0, err
	}
	return share - amortized, nil
}

// scheduledDay 给出第 d 天（1 起）的日额，不判覆盖。
//
// 无退款时就是一条末日吸收的日程。有退款时分成两段（§12.1）：
//
//	[1, r−1]  按**未扣退款**的份额摊（已摊，不追溯）
//	[r, N]    把「最终份额 − 已摊之和」在剩下 N−r+1 天里重摊一遍
//
// 两段之和恒等于最终份额：第二段的总额就是按这条差额定义的。
// r 是退款生效日的天序号，由 Validate 保证落在 [1, N]。
func (t AmortizationTerm) scheduledDay(d int) (int64, error) {
	days := t.TotalDays()
	gross, err := t.grossShareMinor()
	if err != nil {
		return 0, err
	}
	if t.RefundedMinor == 0 {
		return money.Installment(gross, days, d)
	}

	refundIndex := daysBetween(t.StartsOn, t.RefundedOn) + 1
	if d < refundIndex {
		return money.Installment(gross, days, d)
	}
	_, remaining, err := t.refundSplit(gross, days, refundIndex)
	if err != nil {
		return 0, err
	}
	return money.Installment(remaining, days-refundIndex+1, d-refundIndex+1)
}

// amortizedThrough 返回前 k 天（k ∈ [0, N]）已摊之和。
func (t AmortizationTerm) amortizedThrough(k int) (int64, error) {
	days := t.TotalDays()
	if k <= 0 {
		return 0, nil
	}
	if k > days {
		k = days
	}
	gross, err := t.grossShareMinor()
	if err != nil {
		return 0, err
	}
	if t.RefundedMinor == 0 {
		return money.InstallmentsThrough(gross, days, k)
	}

	refundIndex := daysBetween(t.StartsOn, t.RefundedOn) + 1
	if k < refundIndex {
		return money.InstallmentsThrough(gross, days, k)
	}
	before, remaining, err := t.refundSplit(gross, days, refundIndex)
	if err != nil {
		return 0, err
	}
	tail, err := money.InstallmentsThrough(remaining, days-refundIndex+1, k-refundIndex+1)
	if err != nil {
		return 0, err
	}
	return before + tail, nil
}

// refundSplit 把退款生效日切成两段，返回（生效前已摊之和，生效后还要摊的总额）。
//
// 第二个值 = 最终份额 − 第一个值，这是「不追溯改已摊日额」（§12.1）的**定义**：
// 已经摊出去的那部分照原样留着，剩下的钱重新分配到剩余天数上。两段相加恒等于
// 最终份额，因而全期日额之和仍然精确等于份额。
//
// 它**可以为负**：大额退款晚到时，已摊之和已经超过最终份额，剩余天数上的日额
// 就是负的——一笔逐日退回的贷记。那是账目正确的表达，不是要被钳掉的异常。
func (t AmortizationTerm) refundSplit(
	grossShare int64, days, refundIndex int,
) (before, remaining int64, err error) {
	before, err = money.InstallmentsThrough(grossShare, days, refundIndex-1)
	if err != nil {
		return 0, 0, err
	}
	share, err := t.ShareMinor()
	if err != nil {
		return 0, 0, err
	}
	return before, share - before, nil
}

// Validate 校验一段付款的领域不变量。
//
// 与 000010 迁移里的 CHECK 是同一套规则的两处实现（同 UpstreamAccount.Validate）：
// 领域层说人话，库层保证任何写入路径都绕不过去。多出来的一条是日期必须是
// **归一到 UTC 零点的日历日**——库里的 date 列天然如此，而 Go 侧一个带时分秒的
// "日期" 会让 daysBetween 少算一天，且只在跨零点前后才表现出来。
func (t AmortizationTerm) Validate() error {
	if t.StartsOn.IsZero() || t.ExpiresOn.IsZero() {
		return fmt.Errorf("有效期起止: %w", ErrMissingField)
	}
	for name, day := range map[string]time.Time{
		"starts_on": t.StartsOn, "expires_on": t.ExpiresOn,
		"refunded_on": t.RefundedOn, "terminated_on": t.TerminatedOn,
	} {
		if !day.IsZero() && !isCalendarDay(day) {
			return fmt.Errorf("%s %s 必须是归一到 UTC 零点的日历日: %w",
				name, day.Format(time.RFC3339), ErrInvalidFormat)
		}
	}
	if t.ExpiresOn.Before(t.StartsOn) {
		return fmt.Errorf("有效期 %s..%s 起止颠倒: %w",
			t.StartsOn.Format(ProfitBusinessDayLayout),
			t.ExpiresOn.Format(ProfitBusinessDayLayout), ErrInvalidFormat)
	}
	if t.ShareCount <= 0 {
		return fmt.Errorf("分摊份数 %d 必须为正: %w", t.ShareCount, ErrInvalidFormat)
	}
	if t.PaidMinor < 0 || t.SurchargeMinor < 0 || t.RefundedMinor < 0 {
		// 负的「实付」不是折扣，是有人把符号写反了
		return fmt.Errorf("实付 / 附加 / 退款金额不得为负: %w", ErrInvalidFormat)
	}
	gross, err := t.GrossBasisMinor()
	if err != nil {
		return err
	}
	if t.RefundedMinor > gross {
		return fmt.Errorf("退款 %d 超过实付+附加 %d：成本基础不得为负: %w",
			t.RefundedMinor, gross, ErrInconsistent)
	}
	if err := t.validateRefundDate(); err != nil {
		return err
	}
	if !t.TerminatedOn.IsZero() &&
		(t.TerminatedOn.Before(t.StartsOn) || t.TerminatedOn.After(t.ExpiresOn)) {
		// 到期之后的「提前失效」什么也没有提前，结转出来必然是 0，
		// 只会在损失科目里留一条看不懂的记录
		return fmt.Errorf("终止日 %s 必须落在有效期 %s..%s 内: %w",
			t.TerminatedOn.Format(ProfitBusinessDayLayout),
			t.StartsOn.Format(ProfitBusinessDayLayout),
			t.ExpiresOn.Format(ProfitBusinessDayLayout), ErrInvalidFormat)
	}
	return nil
}

// validateRefundDate 落实「退款额与生效日同时有或同时无，且生效日在期内」。
//
// 期外的退款没有「剩余未摊天」可以吸收那笔冲减（§12.1），而平台 v1 没有
// 「过期后信用」这个科目。与其静默把那笔钱丢掉，不如在登记这一刻拒绝。
func (t AmortizationTerm) validateRefundDate() error {
	switch {
	case t.RefundedMinor == 0 && !t.RefundedOn.IsZero():
		return fmt.Errorf("退款额为 0 却带着生效日: %w", ErrInconsistent)
	case t.RefundedMinor > 0 && t.RefundedOn.IsZero():
		return fmt.Errorf("有退款额就必须给生效日（决定从哪天起冲减剩余未摊天，§12.1）: %w",
			ErrMissingField)
	case t.RefundedMinor > 0 &&
		(t.RefundedOn.Before(t.StartsOn) || t.RefundedOn.After(t.ExpiresOn)):
		return fmt.Errorf(
			"退款生效日 %s 必须落在有效期 %s..%s 内：期外的退款没有剩余未摊天可冲减: %w",
			t.RefundedOn.Format(ProfitBusinessDayLayout),
			t.StartsOn.Format(ProfitBusinessDayLayout),
			t.ExpiresOn.Format(ProfitBusinessDayLayout), ErrInvalidFormat)
	}
	return nil
}

// daysBetween 返回两个日历日相差几天（to − from）。
func daysBetween(from, to time.Time) int {
	return int(to.Sub(from) / dayDuration)
}

// maxInt64Amount 让金额加法的溢出判断有个名字。
const maxInt64Amount = int64(^uint64(0) >> 1)
