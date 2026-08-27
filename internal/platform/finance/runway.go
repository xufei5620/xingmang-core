package finance

// 可用天数（runway）的计算（XM-0037d，设计稿 §7 + UI 交接 §10.4）。
//
//	预计可用天数 = 上游余额 ÷ 该上游全部映射渠道的统一口径近 7 日日均消耗
//
// §10.4 给了五条硬要求，本文件逐条落实：
//
//	余额和消耗单位一致        → 币种不一致返回 currency_mismatch，绝不换算
//	必须显示观测时间          → Runway 带出 BalanceObservedAt
//	日均窗口明确              → WindowDays 与 CoveredDays 一起回报
//	无消耗或数据过期不显示伪精确天数 → Days 为 nil + 一个说得清的 Reason
//	低于阈值时进入告警         → Level 三档（阈值可传入）
//
// **这一整条链路只做预警，不参与毛利**（§7 末段）。它算错了不会让报表错，
// 但它编一个数出来会让人不去充值——所以宁可给「未知」也不给伪精确。

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RunwayWindowDays 是日均消耗的统计窗口：近 **7 个完整业务日**（§10.4 的「近 7 日」）。
//
// **不含今天**。今天还在累积——凌晨算出来的日均会只有实际的零头，
// 于是可用天数每天早上暴涨、随时间往下滑。一个每天规律性说谎的预警值
// 比没有预警更糟，因为人会开始按那条曲线的形状去解释它。
const RunwayWindowDays = 7

// DefaultBalanceStaleAfter 是余额读数被判为过期的时长。
//
// 30 分钟，与 metering 的新鲜度阈值取齐：采集默认 5 分钟一轮，
// 半小时没读到说明连着六轮没成功，那不是「余额没变」而是「我们看不见它了」。
//
// ⚠️ 它衡量的是**最近一次确认**（BalanceReading.ObservedAt），不是余额本身
// 多久没动过。余额稳定是常态，不是故障——那正是游程编码的两个时刻要分开的理由。
const DefaultBalanceStaleAfter = 30 * time.Minute

// RunwayThresholds 是三档预警阈值（§12 拍板，对齐 SoloAI 迁移 0177）。
//
// ⚠️ **名字的严重程度与数值方向相反**，这是 SoloAI 的既有命名，照搬过来是为了
// 对得上标准答案：`critical=5 < warning=10 < serious=20`——天数越少越严重，
// 所以 critical 是最紧的那一档，serious 反而是最松的。看着别扭，但改名会让
// 与 SoloAI 的对照失效。设计稿 §7 原文标了「严格递增」，指的正是数值。
type RunwayThresholds struct {
	CriticalDays int
	WarningDays  int
	SeriousDays  int
}

// DefaultRunwayThresholds 是 §12 拍板的默认档位。
//
// ⚠️ §12 说它「可在设置调」，而平台目前没有设置面。所以它是常量 + 由 Query
// 端点**原样回报**：前端不硬编码这三个数，改的时候只改一处。
// 真正的可配置化（设置表或环境变量）记在 PR 的 follow_ups 里。
func DefaultRunwayThresholds() RunwayThresholds {
	return RunwayThresholds{CriticalDays: 5, WarningDays: 10, SeriousDays: 20}
}

// ParseRunwayThresholds 从两个字符串解析告警阈值（XM-0049）。
//
// **这是全平台唯一的一份解析。** 阈值有两个消费者，跑在**两个进程**里：
// platform-api 的 `/finance/upstreams/summary` 要把它回报给前端，
// platform-worker 的告警规则要拿它判档。两处各写一遍解析，
// 「看板说还有 11 天」与「告警说已经低于阈值」就会同时出现在一个人面前——
// 那时没人知道该信哪个。所以两个 cmd 都调这个函数。
//
// ⚠️ 两个进程仍然必须被配成**同一组环境变量**：函数保证解析一致，
// 保证不了部署一致。这一条记在 deploy/compose/.env.example 里。
//
// 空串回落到默认值（10 / 5）；非法值**报错**而不是回落——
// 一个把 `XM_FINANCE_RUNWAY_WARN_DAYS=ten` 写错的部署，
// 静默用回默认值会让人以为自己调过了。
//
// serious 档不可配（本片只开放 warning / critical 两个环境变量），
// 但它必须始终**严格大于 warning**，否则 Validate 会判定三档不递增、
// levelFor 退回 critical，于是所有渠道都变红。所以它取
// `max(默认 20, warning + 1)` —— 自动让位而不是报错：serious 是最松的一档，
// 它的作用只是给「还算充裕」一个颜色，让位不损失任何告警能力。
func ParseRunwayThresholds(warnDays, critDays string) (RunwayThresholds, error) {
	defaults := DefaultRunwayThresholds()

	warning, err := parsePositiveDays(warnDays, defaults.WarningDays, "warning")
	if err != nil {
		return RunwayThresholds{}, err
	}
	critical, err := parsePositiveDays(critDays, defaults.CriticalDays, "critical")
	if err != nil {
		return RunwayThresholds{}, err
	}
	if critical >= warning {
		return RunwayThresholds{}, fmt.Errorf(
			"critical 档 %d 必须严格小于 warning 档 %d（天数越少越严重）: %w",
			critical, warning, ErrInconsistent)
	}

	serious := defaults.SeriousDays
	if serious <= warning {
		serious = warning + 1
	}
	out := RunwayThresholds{
		CriticalDays: critical, WarningDays: warning, SeriousDays: serious,
	}
	if err := out.Validate(); err != nil {
		return RunwayThresholds{}, err
	}
	return out, nil
}

// parsePositiveDays 解析一个「天数」环境变量；空串用默认值。
func parsePositiveDays(raw string, fallback int, name string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s 档 %q 必须是整数: %w", name, raw, ErrInvalidFormat)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s 档 %d 必须为正: %w", name, value, ErrInvalidFormat)
	}
	return value, nil
}

// Validate 校验三档严格递增。
//
// 不递增的阈值不会报错，只会让某一档永远匹配不上——一个静静失效的告警档。
func (t RunwayThresholds) Validate() error {
	if t.CriticalDays <= 0 {
		return fmt.Errorf("critical 档 %d 必须为正: %w", t.CriticalDays, ErrInvalidFormat)
	}
	if !(t.CriticalDays < t.WarningDays && t.WarningDays < t.SeriousDays) {
		return fmt.Errorf("三档必须严格递增（critical %d < warning %d < serious %d）: %w",
			t.CriticalDays, t.WarningDays, t.SeriousDays, ErrInconsistent)
	}
	return nil
}

// RunwayLevel 是可用天数的预警档。
type RunwayLevel string

const (
	// RunwayCritical 是最紧的一档（天数 ≤ CriticalDays）。见上文对命名的说明。
	RunwayCritical RunwayLevel = "critical"
	// RunwayWarning 居中。
	RunwayWarning RunwayLevel = "warning"
	// RunwaySerious 是最松的一档——SoloAI 的命名，不是笔误。
	RunwaySerious RunwayLevel = "serious"
	// RunwayHealthy 表示高于全部三档。
	RunwayHealthy RunwayLevel = "healthy"
)

// RunwayUnknownReason 说明可用天数为什么给不出来。
//
// 有 Reason 而不是只给一个 nil，是这条能力最要紧的设计：§10.4 要求
// 「不显示伪精确天数」，但一个没有解释的空位会被读成 bug，
// 然后有人就去把它「修」成 0 了（宪法 12 条）。
type RunwayUnknownReason string

const (
	// RunwayReasonNotApplicable：订阅型渠道**没有余额这个概念**（§7 末段）。
	// 它的成本是固定摊销，可用天数对它无意义——这不是缺数据。
	RunwayReasonNotApplicable RunwayUnknownReason = "not_applicable"
	// RunwayReasonNoBalance：从未读到过余额。
	//
	// ⚠️ **当前的常态**：两个真实驱动的余额读取都还没接通（§7 的覆盖率边界，
	// 见 connectors/metering 里两个 UpstreamBalance 的注释）。
	RunwayReasonNoBalance RunwayUnknownReason = "no_balance"
	// RunwayReasonBalanceStale：最近一次确认余额已超过阈值。
	RunwayReasonBalanceStale RunwayUnknownReason = "balance_stale"
	// RunwayReasonNoConsumption：窗口内没有已知消耗（§10.4 的「无消耗」）。
	//
	// 除以 0 会得到无穷大——一个「永远用不完」的余额是最糟的那种伪精确。
	RunwayReasonNoConsumption RunwayUnknownReason = "no_consumption"
	// RunwayReasonCurrencyMismatch：余额与消耗币种不一致（§10.4 第一条硬要求）。
	//
	// **不换算**：这里没有汇率，编一个出来算出的天数是纯粹的错数字。
	RunwayReasonCurrencyMismatch RunwayUnknownReason = "currency_mismatch"
)

// RunwayInput 是算一次可用天数需要的全部输入。
//
// 全部显式传入而不是让它自己去查：这样它是一个**纯函数**，
// 运营拿着这几个数就能自己验一遍（同摊销计算器的理由）。
type RunwayInput struct {
	// AccessMethod 决定这条渠道有没有「余额」这个概念（§2.0/§7）。
	AccessMethod AccessMethod

	// Balance 是最新一条余额读数；nil = 从未读到。
	Balance *BalanceReading

	// CostMinorSum 是窗口内**已知**成本之和；CoveredDays 是其中有已知成本的天数。
	//
	// 日均取 `CostMinorSum / CoveredDays` 而不是 `/ WindowDays`：
	// 只采到 3 天数据时除以 7 会把日均压低到实际的四成，
	// 于是可用天数虚高一倍多——一个**偏乐观**的预警值，正好是最危险的方向。
	// 覆盖天数一并回报，让「这个数基于几天」看得见。
	CostMinorSum int64
	CoveredDays  int
	CostCurrency string

	Now        time.Time
	Thresholds RunwayThresholds
	// StaleAfter 为 0 时用 DefaultBalanceStaleAfter。
	StaleAfter time.Duration
}

// Runway 是一次可用天数计算的完整结果。
type Runway struct {
	// Days 为 nil 表示给不出来，此时 Reason 说明为什么（§10.4）。
	Days  *int
	Level RunwayLevel
	// Reason 只在 Days == nil 时有值。
	Reason RunwayUnknownReason

	// WindowDays / CoveredDays 让日均可解释：一个基于 1 天的日均
	// 与一个基于 7 天的日均，可信度差得远（宪法 12 条）。
	WindowDays  int
	CoveredDays int

	// DailyAverageMinor 是算出来的日均消耗；给不出天数时可能仍有值
	// （例如余额过期但消耗算得出来）。
	DailyAverageMinor *int64
	// BalanceMinor / Currency / BalanceObservedAt 是分子那一侧的证据。
	// §10.4 硬性要求「必须显示观测时间」，所以它跟着结果一起走。
	BalanceMinor      *int64
	Currency          string
	BalanceObservedAt *time.Time
}

// Known 报告这次算出了可用天数。
func (r Runway) Known() bool { return r.Days != nil }

// ComputeRunway 算一次可用天数（§10.4 的公式 + 五条硬要求）。
//
// 判定顺序是刻意的：**先答「这个问题成不成立」，再答「数据够不够」**。
// 订阅型渠道排在最前，因为对它来说可用天数不是「缺数据」而是「没有这个概念」
// ——把它和「还没采到余额」混成一个 reason，运营会一直等一个永远不会来的数。
func ComputeRunway(in RunwayInput) Runway {
	out := Runway{
		WindowDays:  RunwayWindowDays,
		CoveredDays: in.CoveredDays,
	}
	if in.Balance != nil {
		balance := in.Balance.BalanceMinor
		observed := in.Balance.ObservedAt
		out.BalanceMinor = &balance
		out.Currency = in.Balance.Currency
		out.BalanceObservedAt = &observed
	}
	// 日均先算出来：即便天数给不出，它本身也是有用的信息（成本卡要显示它）。
	if in.CoveredDays > 0 {
		daily := in.CostMinorSum / int64(in.CoveredDays)
		out.DailyAverageMinor = &daily
	}

	staleAfter := in.StaleAfter
	if staleAfter <= 0 {
		staleAfter = DefaultBalanceStaleAfter
	}

	switch {
	case in.AccessMethod == AccessSubscriptionAccount:
		// 订阅制上游无边际成本（固定月费），可用天数对其无意义（§7 边界）。
		out.Reason = RunwayReasonNotApplicable
		return out
	case in.Balance == nil:
		out.Reason = RunwayReasonNoBalance
		return out
	case in.Balance.Age(in.Now) > staleAfter:
		// 「数据过期不显示伪精确天数」（§10.4）。余额可能已经被充值了，
		// 也可能已经见底了——两者算出来的天数天差地别。
		out.Reason = RunwayReasonBalanceStale
		return out
	case out.DailyAverageMinor == nil || *out.DailyAverageMinor <= 0:
		// 「无消耗不显示伪精确天数」（§10.4）。除以 0 是无穷大——
		// 一个「永远用不完」的余额是最糟的那种伪精确。
		out.Reason = RunwayReasonNoConsumption
		return out
	case in.Balance.Currency != in.CostCurrency:
		// §10.4 第一条硬要求。这里**不换算**：没有汇率，
		// 编一个出来算出的天数是纯粹的错数字。
		out.Reason = RunwayReasonCurrencyMismatch
		return out
	}

	days := 0
	if in.Balance.BalanceMinor > 0 {
		// 整数除法（向零截断），**不向上取整**：可用天数是一个要拿去做决策的
		// 保守估计，把 3.9 天说成 4 天正好是错的方向。
		days = int(in.Balance.BalanceMinor / *out.DailyAverageMinor)
	}
	// 余额 ≤ 0（已透支）落成 0 天而不是负数：负的可用天数没有意义，
	// 而 0 天已经是最高档的告警，表达力不缺。
	out.Days = &days
	out.Level = in.Thresholds.levelFor(days)
	return out
}

// levelFor 把天数映射成预警档。
//
// 阈值非法（未初始化 / 不递增）时一律归 critical：一个算得出天数却
// 给不出档位的结果，会在看板上变成一个没有颜色的数字，
// 而「没有颜色」看起来就像「没问题」。宁可误报也不漏报。
func (t RunwayThresholds) levelFor(days int) RunwayLevel {
	if err := t.Validate(); err != nil {
		return RunwayCritical
	}
	switch {
	case days <= t.CriticalDays:
		return RunwayCritical
	case days <= t.WarningDays:
		return RunwayWarning
	case days <= t.SeriousDays:
		return RunwaySerious
	default:
		return RunwayHealthy
	}
}
