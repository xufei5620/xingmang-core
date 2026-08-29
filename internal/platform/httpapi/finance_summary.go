package httpapi

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xufei5620/xingmang-platform/internal/platform/action"
	"github.com/xufei5620/xingmang-platform/internal/platform/finance"
	"github.com/xufei5620/xingmang-platform/internal/platform/principal"
)

// 看板供数的两个只读端点（XM-0037d，设计稿 §8.5 + UI 交接 §13）。
//
//	GET /api/v1/finance/channels/summary    逐渠道的**钱**（§13 ChannelSummary）
//	GET /api/v1/finance/upstreams/summary   逐上游的**供给**（§13 UpstreamSummary）
//
// 两者当前是同一个粒度（一个 upstream_account 一行），差别在投影——
// 理由见 finance/summary.go 顶部与 docs/modules/finance/README.md。
//
// **都没有写路径**：登记走 Action，台账只由采集任务写（宪法 2 条）。

// FinanceSummaryLister 是看板供数的只读查询能力（*finance.SummaryStore 满足）。
type FinanceSummaryLister interface {
	UpstreamSummaries(ctx context.Context, q finance.SummaryQuery) ([]finance.UpstreamSummary, error)
}

// runwayThresholdsOrDefault 补齐未注入的阈值。
//
// ⚠️ 阈值**必须由装配层注入**（cmd/platform-api 从环境变量解析），
// 不能在这里就地取默认：告警那一侧（platform-worker）读的是同一组环境变量，
// 两处一个用配置一个用默认，「看板说还有 11 天」与「告警说已经低于阈值」
// 就会同时出现在一个人面前。回落只是为了让没配的部署也起得来。
func runwayThresholdsOrDefault(t finance.RunwayThresholds) finance.RunwayThresholds {
	if err := t.Validate(); err != nil {
		return finance.DefaultRunwayThresholds()
	}
	return t
}

const (
	// maxSummaryWindowDays 是业务日窗口上限（92 天 ≈ 一个季度，同 profit-daily）。
	//
	// 上限的意义不是省 CPU，而是不让这个端点被当成数据导出口。
	maxSummaryWindowDays = 92
)

// coverageItem 是一段窗口的覆盖率（宪法 12 条：金额必须可解释）。
//
// 金额与覆盖行数一起给：SUM 会跳过 NULL，只给和不给覆盖行数的话，
// 「五行里只有一行有成本」与「五行都有成本」会给出同一种呈现。
type coverageItem struct {
	RowCount         int64 `json:"row_count"`
	RevenueKnownRows int64 `json:"revenue_known_rows"`
	CostKnownRows    int64 `json:"cost_known_rows"`
	// AccountGrainRows 是账号级聚合行数（XM-0037c 的 `account:` 哨兵）。
	//
	// 这些行**没有独立的令牌下钻**——前端要能说出「3 行里有 1 行是账号级
	// 聚合」，而不是让人点开一个空表。判据用 finance.IsAccountGrain，
	// 不让前端去 string-match 那个前缀。
	AccountGrainRows int64 `json:"account_grain_rows"`
	// MixedCurrency 为真时三个金额一律 null：不同币种的最小单位不能相加。
	MixedCurrency bool `json:"mixed_currency"`
	// Complete 表示两侧都覆盖满且币种单一——只有这时金额才是可断言的。
	Complete bool `json:"complete"`
}

// observedItem 是 §13 的 `Observed` 语义在本端点的落点。
//
// 两侧**各一个观测时刻**，取窗口内最旧的那个：聚合值的新鲜度由最不新鲜的
// 成员决定，取最新会让一行刚刷新的记录替其余几十行陈旧的读数背书。
type observedItem struct {
	CostObservedAt    *string `json:"cost_observed_at"`
	RevenueObservedAt *string `json:"revenue_observed_at"`
	UpdatedAt         *string `json:"updated_at"`
	Source            string  `json:"source"`
}

// runwayItem 是可用天数（§10.4）。
type runwayItem struct {
	// Days 为 null 表示给不出来，此时 Reason 说明为什么——
	// §10.4 要求「无消耗或数据过期不显示伪精确天数」，而一个没有解释的
	// 空位会被读成 bug，然后有人就去把它「修」成 0 了。
	Days  *int   `json:"days"`
	Level string `json:"level"`
	// Reason 只在 Days 为 null 时非空：
	// not_applicable（订阅型渠道没有余额这个概念）/ no_balance / balance_stale
	// / no_consumption / currency_mismatch。
	Reason string `json:"reason"`

	// WindowDays / CoveredDays 让日均可解释：一个基于 1 天的日均与一个
	// 基于 7 天的日均，可信度差得远。
	WindowDays  int `json:"window_days"`
	CoveredDays int `json:"covered_days"`

	DailyAverage *moneyItem `json:"daily_average"`
	Balance      *moneyItem `json:"balance"`
	// BalanceObservedAt 是 §10.4 的硬要求「必须显示观测时间」。
	//
	// 它是「最近一次**确认**余额还是这个值」的时刻，不是「余额什么时候变的」
	// ——余额稳定是常态不是故障（见 000011 迁移 observed_at 那一列）。
	BalanceObservedAt *string `json:"balance_observed_at"`
}

// runwayThresholdsItem 原样回报本次判档用的三个阈值。
//
// 回报而不是让前端硬编码：§12 说它「可在设置调」，而平台目前没有设置面，
// 所以它今天是后端常量——改的时候只该改一处。
//
// ⚠️ 名字的严重程度与数值方向相反（SoloAI 的既有命名，见
// finance.RunwayThresholds 的注释）：天数越少越严重，
// 所以 critical 是最紧的一档，serious 反而最松。
type runwayThresholdsItem struct {
	CriticalDays int `json:"critical_days"`
	WarningDays  int `json:"warning_days"`
	SeriousDays  int `json:"serious_days"`
	// Revision/source/updated_at make the exact classifier snapshot auditable.
	// They are optional for the legacy env-backed handler during cutover.
	Revision  int64  `json:"revision,omitempty"`
	Source    string `json:"source,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// channelSummaryItem 是 §13 ChannelSummary 的对外形状。
type channelSummaryItem struct {
	ID string `json:"id"`
	// Name 是给人看的渠道名：`system_type · base_url`，没有 base_url
	// （订阅型）时退化成 `system_type · <接入方式>`。
	//
	// 后端拼而不是前端拼：这个名字会出现在告警、审计与看板三处，
	// 三处各拼一遍迟早会有一处不一样，然后没人能确定说的是不是同一条渠道。
	Name         string `json:"name"`
	SystemType   string `json:"system_type"`
	AccessMethod string `json:"access_method"`
	// Metered 报告这条渠道走不走「实扣 ÷ 倍率」的计量口径（§2.0）。
	// 让后端算好而不是让前端按 access_method 再判一次——那个判断散到几个
	// 页面之后迟早会有一个漏掉新枚举值。
	Metered bool   `json:"metered"`
	BaseURL string `json:"base_url"`
	// PlatformID 是 §13 的 managedPlatform；空串 = 未归属（§5.2 的第三桶）。
	PlatformID string `json:"platform_id"`
	// CredentialRef 是**引用**不是凭据（ADR-014、UI 交接 §14.2）。
	CredentialRef string `json:"credential_ref"`
	// RechargeRatio 是规范存储量（除数）；RechargeCostRate = 1/它，是展示投影
	// （§3.4）。两个都是定点十进制**字符串**：JSON 数字一路解成 double，
	// 1.15 到了页面上就变成 1.1499999999999999（宪法 13 条）。
	//
	RechargeRatio    string `json:"recharge_ratio"`
	RechargeCostRate string `json:"recharge_cost_rate"`
	// GroupRate 是 §13 的 `groupRate`（分组倍率，XM-0049 补上存储）。
	//
	// ⚠️ 它与上面两个是**完全不同的量**：recharge_ratio 是成本折算的除数，
	// group_rate 只是定价分组的展示标注——§10.2 明确要求「分组倍率独立，
	// 前端不重复乘算」，后端也一次都不会乘它。
	//
	// **omitempty：没配就不出这个字段**，而不是给一个 ""。与 recharge_ratio
	// 恒出（计量型必须有它，空串本身就是「这条渠道没有倍率」的信息）不同，
	// 分组倍率对绝大多数渠道本就不存在——出一个空字段只会让前端多写一次
	// 「这个空串是什么意思」的判断。
	GroupRate     string `json:"group_rate,omitempty"`
	BusinessDayTZ string `json:"business_day_tz"`
	Status        string `json:"status"`
	// TokenCount 是该账号名下的令牌映射数（§13 的 keyCount）。
	TokenCount int `json:"token_count"`

	// 三个金额都**可空**（null = 给不出，不是 0）：覆盖不全或币种混杂时
	// 那个和是偏低的，而偏低的和长得和完整的一模一样。
	UsageRevenue *moneyItem `json:"usage_revenue"`
	SupplyCost   *moneyItem `json:"supply_cost"`
	GrossProfit  *moneyItem `json:"gross_profit"`
	// GrossMargin 是定点十进制字符串；**收入 ≤ 0 时为 null**
	// （对齐 SoloAI relay_profit.go:331，§3.3）：没有收入的渠道谈不上毛利率，
	// 给 0 会让「今天没有流量」看起来像「毛利率是零」。
	GrossMargin *string `json:"gross_margin"`

	Coverage coverageItem `json:"coverage"`
	Observed observedItem `json:"observed"`
	Runway   runwayItem   `json:"runway"`
}

// upstreamSummaryItem 是 §13 UpstreamSummary 的对外形状（供给侧投影）。
type upstreamSummaryItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// SupplierKey 是「哪些账号背后是同一个上游供应商」的归并键
	// （§7 的去重口径：按 system_type + base_url）。
	//
	// ⚠️ **今天它与账号一一对应**：登记簿的唯一索引
	// `(environment, system_type, base_url)` 让「一个供应商挂多个账号」
	// 在有 base_url 的账号上不可能发生，而没有 base_url 的账号各自成一个
	// 供应商（没有可共享的端点去认它）。所以本端点一行一个账号——
	// 那**就是**按供应商聚合的结果，不是没做聚合。
	//
	// 键回报出来，是为了让这条不变量可被检查（httpapi 侧有断言钉住），
	// 也让「哪天唯一索引放宽了」变成前端的一次 groupBy 而不是后端重构。
	SupplierKey string `json:"supplier_key"`

	SystemType   string `json:"system_type"`
	AccessMethod string `json:"access_method"`
	BaseURL      string `json:"base_url"`
	// RechargeCostRate 是 §13 的 rechargeCostRate = 1 / 充值倍率（展示投影）。
	// 未配倍率（订阅型）时为空串——不编一个 "1.000000" 冒充「没打折」。
	RechargeCostRate string `json:"recharge_cost_rate"`
	// GroupRate 同渠道项：没配就不出这个字段，且它不参与任何计算。
	GroupRate     string `json:"group_rate,omitempty"`
	CredentialRef string `json:"credential_ref"`
	Status        string `json:"status"`
	// TokenCount 是 §13 的 keyCount。
	TokenCount int `json:"token_count"`

	UsageRevenue *moneyItem `json:"usage_revenue"`
	SupplyCost   *moneyItem `json:"supply_cost"`
	GrossProfit  *moneyItem `json:"gross_profit"`

	Coverage coverageItem `json:"coverage"`
	Observed observedItem `json:"observed"`
	Runway   runwayItem   `json:"runway"`
}

// runwayCoverageItem 是可用天数的覆盖率（§12 拍板要求「标注覆盖率边界」）。
//
// 它必须和可用天数一起呈现：两个真实驱动的余额读取都还没接通（§7），
// 所以今天这个比值多半是 0/N。不显式说出来，看板上就只是一排「—」，
// 看起来像坏了。
type runwayCoverageItem struct {
	Total int `json:"total"`
	Known int `json:"known"`
	// Reasons 是给不出天数的原因分布，让「为什么是 0/N」一眼可答。
	Reasons map[string]int `json:"reasons"`
}

type channelSummaryPage struct {
	Items []channelSummaryItem `json:"items"`
	From  string               `json:"from"`
	To    string               `json:"to"`
}

type upstreamSummaryPage struct {
	Items      []upstreamSummaryItem `json:"items"`
	From       string                `json:"from"`
	To         string                `json:"to"`
	Runway     runwayCoverageItem    `json:"runway_coverage"`
	Thresholds runwayThresholdsItem  `json:"runway_thresholds"`
}

func moneyPtr(minor *int64, currency string) *moneyItem {
	if minor == nil || currency == "" {
		// 币种为空时也给 null：一个没有币种的金额是不可解释的
		// （§10.1：每个金额必带 Currency）。
		return nil
	}
	item := moneyValue(*minor, currency)
	return &item
}

func stringPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func coverageOf(w finance.ProfitWindow) coverageItem {
	return coverageItem{
		RowCount:         w.RowCount,
		RevenueKnownRows: w.RevenueKnownRows,
		CostKnownRows:    w.CostKnownRows,
		AccountGrainRows: w.AccountGrainRows,
		MixedCurrency:    w.MixedCurrency,
		Complete:         w.FullyCovered(w.RevenueKnownRows) && w.FullyCovered(w.CostKnownRows),
	}
}

func observedOf(w finance.ProfitWindow) observedItem {
	return observedItem{
		CostObservedAt:    rfc3339Ptr(w.OldestCostObservedAt),
		RevenueObservedAt: rfc3339Ptr(w.OldestRevenueObservedAt),
		UpdatedAt:         rfc3339Ptr(w.LatestUpdatedAt),
		Source:            w.Source,
	}
}

func runwayOf(r finance.Runway) runwayItem {
	out := runwayItem{
		Days:        r.Days,
		Level:       string(r.Level),
		Reason:      string(r.Reason),
		WindowDays:  r.WindowDays,
		CoveredDays: r.CoveredDays,
	}
	out.DailyAverage = moneyPtr(r.DailyAverageMinor, r.Currency)
	out.Balance = moneyPtr(r.BalanceMinor, r.Currency)
	out.BalanceObservedAt = rfc3339Ptr(r.BalanceObservedAt)
	return out
}

// SupplierKeyOf 给出「哪些账号背后是同一个上游供应商」的归并键（§7）。
//
// 有 base_url 的按 `system_type|base_url` 归并（§7 的 newapi 去重口径）；
// 没有 base_url 的账号各自成一个供应商——没有可共享的端点去认它，
// 硬把它们归成一堆会让几个互不相干的订阅账号共享一个余额。
func SupplierKeyOf(a finance.UpstreamAccount) string {
	if a.BaseURL != "" {
		return string(a.SystemType) + "|" + a.BaseURL
	}
	return "account|" + a.ID.String()
}

func channelToItem(s finance.UpstreamSummary) channelSummaryItem {
	return channelSummaryItem{
		ID:               s.Account.ID.String(),
		Name:             finance.AccountDisplayName(s.Account),
		SystemType:       string(s.Account.SystemType),
		AccessMethod:     string(s.Account.AccessMethod),
		Metered:          s.Account.AccessMethod.IsMetered(),
		BaseURL:          s.Account.BaseURL,
		PlatformID:       s.Account.PlatformID,
		CredentialRef:    s.Account.CredentialRef,
		RechargeRatio:    s.Account.RechargeRatio.String(),
		RechargeCostRate: s.Account.RechargeCostRate(),
		GroupRate:        s.Account.GroupRate.String(),
		BusinessDayTZ:    s.Account.BusinessDayTZ,
		Status:           string(s.Account.Status),
		TokenCount:       s.TokenCount,
		UsageRevenue:     moneyPtr(s.Window.RevenueMinor(), s.Window.Currency),
		SupplyCost:       moneyPtr(s.Window.CostMinor(), s.Window.Currency),
		GrossProfit:      moneyPtr(s.Window.GrossProfitMinor(), s.Window.Currency),
		GrossMargin:      stringPtr(s.Window.GrossMargin()),
		Coverage:         coverageOf(s.Window),
		Observed:         observedOf(s.Window),
		Runway:           runwayOf(s.Runway),
	}
}

func upstreamToItem(s finance.UpstreamSummary) upstreamSummaryItem {
	return upstreamSummaryItem{
		ID:               s.Account.ID.String(),
		Name:             finance.AccountDisplayName(s.Account),
		SupplierKey:      SupplierKeyOf(s.Account),
		SystemType:       string(s.Account.SystemType),
		AccessMethod:     string(s.Account.AccessMethod),
		BaseURL:          s.Account.BaseURL,
		RechargeCostRate: s.RechargeCostRate(),
		GroupRate:        s.Account.GroupRate.String(),
		CredentialRef:    s.Account.CredentialRef,
		Status:           string(s.Account.Status),
		TokenCount:       s.TokenCount,
		UsageRevenue:     moneyPtr(s.Window.RevenueMinor(), s.Window.Currency),
		SupplyCost:       moneyPtr(s.Window.CostMinor(), s.Window.Currency),
		GrossProfit:      moneyPtr(s.Window.GrossProfitMinor(), s.Window.Currency),
		Coverage:         coverageOf(s.Window),
		Observed:         observedOf(s.Window),
		Runway:           runwayOf(s.Runway),
	}
}

// ListChannelSummaryHandler 返回逐渠道的收入 / 成本 / 毛利（§13 ChannelSummary）。
//
// 复用 finance.ScopeRead，不另立 scope：这里的每一个数都是台账的向上聚合，
// 能看台账的人已经能自己加出来，泄漏面完全相同。
func ListChannelSummaryHandler(
	store FinanceSummaryLister, thresholds finance.RunwayThresholds,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, from, to, err := loadSummaries(r, store, thresholds)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]channelSummaryItem, 0, len(items))
		for _, item := range items {
			out = append(out, channelToItem(item))
		}
		// 渠道表按稳定顺序（接入方式 → 系统 → 地址）而不是按紧急程度：
		// 它是一张人反复扫的对照表，行序每次刷新都变的话，
		// 「这条渠道刚才是不是在上面」就没法回答。
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].AccessMethod != out[j].AccessMethod {
				return out[i].AccessMethod < out[j].AccessMethod
			}
			if out[i].SystemType != out[j].SystemType {
				return out[i].SystemType < out[j].SystemType
			}
			if out[i].BaseURL != out[j].BaseURL {
				return out[i].BaseURL < out[j].BaseURL
			}
			return out[i].ID < out[j].ID
		})
		WriteJSON(w, http.StatusOK, channelSummaryPage{
			Items: out,
			From:  from.Format(finance.ProfitBusinessDayLayout),
			To:    to.Format(finance.ProfitBusinessDayLayout),
		})
	}
}

// ListChannelSummaryHandlerWithProvider is the DB-snapshot cutover variant.
// It reads exactly one threshold snapshot per request and reuses that same
// value for the store query and response envelope.
func ListChannelSummaryHandlerWithProvider(
	store FinanceSummaryLister, provider RunwayThresholdProvider,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, from, to, snapshot, err := loadSummariesWithProvider(r, store, provider)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]channelSummaryItem, 0, len(items))
		for _, item := range items {
			out = append(out, channelToItem(item))
		}
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].AccessMethod != out[j].AccessMethod {
				return out[i].AccessMethod < out[j].AccessMethod
			}
			if out[i].SystemType != out[j].SystemType {
				return out[i].SystemType < out[j].SystemType
			}
			if out[i].BaseURL != out[j].BaseURL {
				return out[i].BaseURL < out[j].BaseURL
			}
			return out[i].ID < out[j].ID
		})
		WriteJSON(w, http.StatusOK, channelSummaryPageWithThresholds{
			Items: out, From: from.Format(finance.ProfitBusinessDayLayout), To: to.Format(finance.ProfitBusinessDayLayout),
			Thresholds: thresholdItemFromSnapshot(snapshot),
		})
	}
}

// ListUpstreamSummaryHandler 返回逐上游的供给侧供数（§13 UpstreamSummary + §10.4）。
func ListUpstreamSummaryHandler(
	store FinanceSummaryLister, thresholds finance.RunwayThresholds,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, from, to, err := loadSummaries(r, store, thresholds)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		// 上游表按「最该被看见的排前面」：可用天数最少的在最上面。
		// 与渠道表刻意不同——这张表是预警用的。
		finance.SortUpstreamSummaries(items)
		out := make([]upstreamSummaryItem, 0, len(items))
		for _, item := range items {
			out = append(out, upstreamToItem(item))
		}
		coverage := finance.SummarizeRunwayCoverage(items)
		reasons := make(map[string]int, len(coverage.Reasons))
		for reason, count := range coverage.Reasons {
			reasons[string(reason)] = count
		}
		effective := runwayThresholdsOrDefault(thresholds)
		WriteJSON(w, http.StatusOK, upstreamSummaryPage{
			Items: out,
			From:  from.Format(finance.ProfitBusinessDayLayout),
			To:    to.Format(finance.ProfitBusinessDayLayout),
			Runway: runwayCoverageItem{
				Total: coverage.Total, Known: coverage.Known, Reasons: reasons,
			},
			Thresholds: runwayThresholdsItem{
				CriticalDays: effective.CriticalDays,
				WarningDays:  effective.WarningDays,
				SeriousDays:  effective.SeriousDays,
			},
		})
	}
}

// ListUpstreamSummaryHandlerWithProvider is the DB-snapshot cutover variant;
// see ListChannelSummaryHandlerWithProvider for the one-read invariant.
func ListUpstreamSummaryHandlerWithProvider(
	store FinanceSummaryLister, provider RunwayThresholdProvider,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items, from, to, snapshot, err := loadSummariesWithProvider(r, store, provider)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		finance.SortUpstreamSummaries(items)
		out := make([]upstreamSummaryItem, 0, len(items))
		for _, item := range items {
			out = append(out, upstreamToItem(item))
		}
		coverage := finance.SummarizeRunwayCoverage(items)
		reasons := make(map[string]int, len(coverage.Reasons))
		for reason, count := range coverage.Reasons {
			reasons[string(reason)] = count
		}
		WriteJSON(w, http.StatusOK, upstreamSummaryPage{
			Items: out, From: from.Format(finance.ProfitBusinessDayLayout), To: to.Format(finance.ProfitBusinessDayLayout),
			Runway:     runwayCoverageItem{Total: coverage.Total, Known: coverage.Known, Reasons: reasons},
			Thresholds: thresholdItemFromSnapshot(snapshot),
		})
	}
}

type channelSummaryPageWithThresholds struct {
	Items      []channelSummaryItem `json:"items"`
	From       string               `json:"from"`
	To         string               `json:"to"`
	Thresholds runwayThresholdsItem `json:"runway_thresholds"`
}

func thresholdItemFromSnapshot(snapshot finance.RunwayThresholdSnapshot) runwayThresholdsItem {
	return runwayThresholdsItem{
		CriticalDays: snapshot.Thresholds.CriticalDays,
		WarningDays:  snapshot.Thresholds.WarningDays,
		SeriousDays:  snapshot.Thresholds.SeriousDays,
		Revision:     snapshot.Revision,
		Source:       snapshot.Source,
		UpdatedAt:    snapshot.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func loadSummariesWithProvider(
	r *http.Request, store FinanceSummaryLister, provider RunwayThresholdProvider,
) ([]finance.UpstreamSummary, time.Time, time.Time, finance.RunwayThresholdSnapshot, error) {
	p, ok := principal.FromContext(r.Context())
	if !ok {
		return nil, time.Time{}, time.Time{}, finance.RunwayThresholdSnapshot{}, action.NewError(action.CodePermissionDenied, "缺少身份", nil)
	}
	env, err := resolveEnvironment(r, p)
	if err != nil {
		return nil, time.Time{}, time.Time{}, finance.RunwayThresholdSnapshot{}, err
	}
	snapshot, err := provider.Current(r.Context(), string(env))
	if err != nil {
		return nil, time.Time{}, time.Time{}, finance.RunwayThresholdSnapshot{}, runwayConfigUnavailable(err)
	}
	from, to, err := parseSummaryWindow(r.URL.Query().Get("from"), r.URL.Query().Get("to"), time.Now().UTC())
	if err != nil {
		return nil, time.Time{}, time.Time{}, finance.RunwayThresholdSnapshot{}, err
	}
	items, err := store.UpstreamSummaries(r.Context(), finance.SummaryQuery{Environment: string(env), From: from, To: to, Thresholds: snapshot.Thresholds})
	if err != nil {
		return nil, time.Time{}, time.Time{}, finance.RunwayThresholdSnapshot{}, err
	}
	return items, from, to, snapshot, nil
}

// loadSummaries 是两个端点共用的取数：身份 → 环境 → 窗口 → 仓储。
func loadSummaries(
	r *http.Request, store FinanceSummaryLister, thresholds finance.RunwayThresholds,
) ([]finance.UpstreamSummary, time.Time, time.Time, error) {
	p, ok := principal.FromContext(r.Context())
	if !ok {
		return nil, time.Time{}, time.Time{},
			action.NewError(action.CodePermissionDenied, "缺少身份", nil)
	}
	env, err := resolveEnvironment(r, p)
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}
	from, to, err := parseSummaryWindow(
		r.URL.Query().Get("from"), r.URL.Query().Get("to"), time.Now().UTC())
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}
	items, err := store.UpstreamSummaries(r.Context(), finance.SummaryQuery{
		Environment: string(env),
		From:        from,
		To:          to,
		Thresholds:  runwayThresholdsOrDefault(thresholds),
	})
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}
	return items, from, to, nil
}

// parseSummaryWindow 解析 from / to 业务日参数。
//
// **两个都不传 → 只取今天**，而不是像 profit-daily 那样取最近 7 天：
// 这两个端点喂的是「今日毛利 / 今日供给成本」那几张卡，默认给 7 天合计
// 会让卡上的数字是卡片标题的七倍——一个不报错的错数字。
// 要看趋势的调用方显式给区间。
//
// 只传一个 → 报错而不是替调用方补另一个：「from=2026-08-01」到底是想要一天
// 还是想要从那天到今天，猜错了就是一张跨度完全不同的图（同 parseProfitWindow）。
//
// 「今天」按 CST 固定 +08:00 算（★口径常量 §4），不按服务器本地时区。
func parseSummaryWindow(rawFrom, rawTo string, now time.Time) (from, to time.Time, err error) {
	rawFrom, rawTo = strings.TrimSpace(rawFrom), strings.TrimSpace(rawTo)
	switch {
	case rawFrom == "" && rawTo == "":
		today := finance.BusinessDayAt(now, finance.DefaultBusinessDayLocation())
		return today, today, nil
	case rawFrom == "" || rawTo == "":
		return time.Time{}, time.Time{}, action.NewError(action.CodeInvalidParams,
			"from 与 to 必须同时给出（都不给则只取今天）", nil)
	}

	from, err = finance.ParseBusinessDay(rawFrom)
	if err != nil {
		return time.Time{}, time.Time{}, action.NewError(action.CodeInvalidParams,
			"from 必须形如 YYYY-MM-DD", err)
	}
	to, err = finance.ParseBusinessDay(rawTo)
	if err != nil {
		return time.Time{}, time.Time{}, action.NewError(action.CodeInvalidParams,
			"to 必须形如 YYYY-MM-DD", err)
	}
	if to.Before(from) {
		return time.Time{}, time.Time{}, action.NewError(action.CodeInvalidParams,
			"业务日区间起止颠倒", nil)
	}
	// +1 因为区间含两端
	if days := int(to.Sub(from).Hours()/24) + 1; days > maxSummaryWindowDays {
		return time.Time{}, time.Time{}, action.NewError(action.CodeInvalidParams,
			"业务日跨度 "+strconv.Itoa(days)+" 天超出上限 "+
				strconv.Itoa(maxSummaryWindowDays)+" 天", nil)
	}
	return from, to, nil
}
