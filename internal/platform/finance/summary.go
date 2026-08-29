package finance

// 看板供数的领域类型（XM-0037d，设计稿 §8.5 + UI 交接 §13）。
//
// 台账（profit_daily）的粒度是 (上游账号, 业务日, 令牌)，比看板要的细。
// 本文件是它的**向上聚合**，不是缺口（§12.2）——两个形状对应 §13 的两个接口：
//
//	ChannelSummary   逐渠道的**钱**：收入 / 成本 / 毛利 / 毛利率
//	UpstreamSummary  逐上游的**供给**：余额 / 可用天数 / 充值成本率 / 令牌数
//
// **两者当前是同一个粒度（一个 upstream_account 一行），差别在投影。**
// §12.2 裁定「渠道 = upstream_account，令牌为其下钻」，而登记簿的唯一索引
// `(environment, system_type, base_url)` 又让「一个上游供应商挂多个账号」
// 在有 base_url 的账号上不可能发生。所以今天两者一一对应。
// 分成两个形状仍然值得：§13 的两个接口有各自的字段集与各自的页面，
// 而把余额 / 可用天数塞进渠道摘要会让「渠道」这个词同时指两件事。
// 详见 docs/modules/finance/README.md 的「两个摘要为什么同粒度」。

import (
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/money"
)

// MarginScale 是毛利率的小数位数。
//
// 6 位与金额同标度，不是为了显示——展示层只会用到 2~4 位——而是为了让
// 「毛利率」与「毛利 ÷ 收入」在任何比对里都对得上。截到 4 位再去反推毛利，
// 会与台账差出一个尾数，然后有人会花半天去查那个差额。
const MarginScale = 6

// ProfitWindow 是某个上游账号在一段业务日区间里的台账聚合。
//
// 金额与**覆盖行数**一起给，这是本类型存在的主要理由：SUM 会跳过 NULL，
// 只给和不给覆盖行数的话，「五行里只有一行有成本」与「五行都有成本」
// 会给出同一种呈现（宪法 12 条，同 PlatformBucket）。
type ProfitWindow struct {
	From time.Time
	To   time.Time

	RowCount         int64
	RevenueKnownRows int64
	CostKnownRows    int64

	revenueMinorSum int64
	costMinorSum    int64

	// AccountGrainRows 是其中的**账号级聚合行**数（XM-0037c 的 `account:` 哨兵）。
	//
	// 单独回报是因为那些行没有独立的令牌下钻——前端要能说出
	// 「这个渠道的 3 行里有 1 行是账号级聚合」，而不是让人点开一个空表。
	AccountGrainRows int64

	Currency string
	// MixedCurrency 为真时金额合计**给不出**：不同币种的最小单位不能相加。
	MixedCurrency bool

	// 两侧各一个观测时刻，取本窗口内**最旧**的那个。
	//
	// 聚合值的新鲜度由最不新鲜的成员决定——取最新会让一行刚刷新的记录
	// 替其余几十行陈旧的读数背书（同 metering.aggregateSnapshot）。
	OldestCostObservedAt    *time.Time
	OldestRevenueObservedAt *time.Time
	// LatestUpdatedAt 是本窗口内最后一次写入的时刻（「任务还活着」的证据）。
	LatestUpdatedAt *time.Time
	Source          string
}

// FullyCovered 报告某一侧的已知行数是否等于总行数。
func (w ProfitWindow) FullyCovered(known int64) bool {
	return w.RowCount > 0 && known == w.RowCount && !w.MixedCurrency
}

// RevenueMinor 返回使用计费收入合计；**覆盖不全或币种混杂时返回 nil**。
//
// 少一行收入的和与完整的和长得一模一样，而它偏低——一个偏低的收入配上
// 完整的成本，毛利就凭空缩水了。给不出时返回 nil，由调用方显示「部分数据」。
func (w ProfitWindow) RevenueMinor() *int64 {
	if !w.FullyCovered(w.RevenueKnownRows) {
		return nil
	}
	v := w.revenueMinorSum
	return &v
}

// CostMinor 返回供给成本合计；覆盖不全或币种混杂时返回 nil（理由同上）。
func (w ProfitWindow) CostMinor() *int64 {
	if !w.FullyCovered(w.CostKnownRows) {
		return nil
	}
	v := w.costMinorSum
	return &v
}

// GrossProfitMinor 返回毛利 = 收入 − 成本；**任一侧给不出则为 nil**。
//
// 不是「等于另一侧」——这与 profit_daily.profit_minor 生成列的 NULL 传播
// 是同一条纪律，只是搬到了聚合层。
func (w ProfitWindow) GrossProfitMinor() *int64 {
	revenue, cost := w.RevenueMinor(), w.CostMinor()
	if revenue == nil || cost == nil {
		return nil
	}
	profit := *revenue - *cost
	return &profit
}

// GrossMargin 返回毛利率的定点十进制字符串；给不出时返回空串。
//
// **收入 ≤ 0 时给不出**（对齐 SoloAI relay_profit.go:331 的 nil，§3.3 逐字）：
// 没有收入的渠道谈不上毛利率，返回 0 会让「今天没有流量」看起来像
// 「毛利率是零」——两件完全不同的事。
func (w ProfitWindow) GrossMargin() string {
	revenue, profit := w.RevenueMinor(), w.GrossProfitMinor()
	if revenue == nil || profit == nil || *revenue <= 0 {
		return ""
	}
	margin, err := money.RatioString(*profit, *revenue, MarginScale)
	if err != nil {
		return ""
	}
	return margin
}

// newProfitWindow 由仓储层构造（金额字段不导出，避免调用方绕过覆盖率判定）。
func newProfitWindow(
	from, to time.Time, rowCount, revenueKnown, costKnown, revenueSum, costSum,
	accountGrainRows int64, currency string, mixed bool,
) ProfitWindow {
	if mixed {
		// 币种混杂时连币种都不给：一个标着 USD 的混合合计比没有合计更危险。
		currency = ""
	}
	return ProfitWindow{
		From: from, To: to,
		RowCount:         rowCount,
		RevenueKnownRows: revenueKnown,
		CostKnownRows:    costKnown,
		revenueMinorSum:  revenueSum,
		costMinorSum:     costSum,
		AccountGrainRows: accountGrainRows,
		Currency:         currency,
		MixedCurrency:    mixed,
	}
}

// ChannelSummary 是一条渠道（= 一个上游账号，§12.2）的看板供数。
type ChannelSummary struct {
	// Account 是登记簿那一行：接入方式、倍率、归属平台、凭据引用都在里面。
	Account UpstreamAccount
	// TokenCount 是该账号名下的令牌映射数（§13 的 keyCount）。
	TokenCount int
	// Window 是所选业务日区间的收入 / 成本 / 毛利。
	Window ProfitWindow
}

// UpstreamSummary 是一个上游账号的供给侧供数（§13 UpstreamSummary + §10.4）。
type UpstreamSummary struct {
	Account    UpstreamAccount
	TokenCount int
	Window     ProfitWindow

	// Balance 是最新一条余额读数；nil = 从未读到（当前的常态，见 §7 覆盖率边界）。
	Balance *BalanceReading
	// Runway 恒有值——**给不出天数时它带着 Reason**，而不是一个空结构体。
	Runway Runway
}

// RechargeCostRate 是 §13 的 `rechargeCostRate`（充值成本率）= 1 / 充值倍率。
//
// 展示投影，不是存储量（§3.4）：每次按当前倍率现算，两个值不可能漂移。
// 未配倍率（订阅型渠道）时返回空串——不编一个 "1.000000" 冒充「没打折」。
func (u UpstreamSummary) RechargeCostRate() string {
	return u.Account.RechargeCostRate()
}

// RunwayCoverage 统计一批上游摘要里有多少条真的算出了可用天数。
//
// 覆盖率必须和可用天数一起呈现（§12 拍板：「runway 随 037d 做并**标注覆盖率
// 边界**」）：两个真实驱动的余额读取都还没接通，所以今天这个比值多半是 0/N。
// 不把它显式说出来，看板上就只是一排「—」，看起来像坏了。
type RunwayCoverage struct {
	// Total 是参与统计的上游数（不含订阅型——它们本就没有余额这个概念）。
	Total int
	// Known 是算出了天数的条数。
	Known int
	// Reasons 是给不出天数的原因分布，让「为什么是 0/N」一眼可答。
	Reasons map[RunwayUnknownReason]int
}

// SummarizeRunwayCoverage 汇总一批上游摘要的可用天数覆盖率。
func SummarizeRunwayCoverage(items []UpstreamSummary) RunwayCoverage {
	out := RunwayCoverage{Reasons: map[RunwayUnknownReason]int{}}
	for _, item := range items {
		// 只有计量型（目前为 upstream_key）才有「余额 ÷ 日均消耗」
		// 的可用天数口径。按 access method 过滤，而不是只看
		// RunwayReasonNotApplicable：历史行或未来非计量枚举可能带着
		// no_balance/currency_mismatch 等原因，仍不能进入分母或原因分布。
		if !item.Account.AccessMethod.IsMetered() {
			continue
		}
		out.Total++
		if item.Runway.Known() {
			out.Known++
			continue
		}
		out.Reasons[item.Runway.Reason]++
	}
	return out
}
