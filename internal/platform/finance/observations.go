package finance

// 采集结果 → 运营指标（XM-0037b，设计稿 §8.2、规格 §9.1、宪法 12 条）。
//
// 成本与收入两条指标由 connectors/metering 定义并生成（那是**取数**的观测）；
// 本文件只多一条 finance.profit.daily——它观测的是**入账**这一步：
// 这一轮把多少行写进了台账、跳过了多少、为什么跳过。
// 两者缺一不可：上游读到了数不等于它进了台账（§5.1 的三条纪律本来就会
// 让一部分读数不入账），而看板问的是「毛利现在是多少、什么时候的数」。

import (
	"time"

	"github.com/xufei5620/xingmang-platform/connectors/metering"
	"github.com/xufei5620/xingmang-platform/internal/platform/ops"
)

// MetricProfitDaily 是当日毛利入账的指标键（设计稿 §8.4）。
//
// ⚠️ 与 connectors/metering 的两条同族但**不同层**：那两条是「上游说了什么」，
// 这条是「台账记下了什么」。它们的值在三条纪律生效时**本就该不同**
// （读到了但不入账正是纪律的效果），合并成一条会让那个差异永远看不见。
//
// ⚠️ 绝不能与 connectors/sub2api 的 `sub2api.cost.daily` 混用——那条是自营
// 面板口径（admin trend[].cost），与本核算成本完全是两个数（§3.1 的显著标注）。
//
// ⚠️ 这个常量在 internal/platform/ops 的白名单里有一份**字面量副本**
// （ops 不能反向 import 本包，会成环）。改这里必须同步改那边，
// ops_test 的 TestRegisteredMetricsMatchConnectorContracts 会当场拦住遗漏。
const MetricProfitDaily = "finance.profit.daily"

// ProfitStalenessThresholdSeconds 是本指标的新鲜度阈值。
//
// 1800 秒，与 connectors/metering 取齐：采集按 §12 拍板默认 5 分钟一轮，
// 半小时没有新数据说明入账链路有问题，而不是「今天没有毛利」。
const ProfitStalenessThresholdSeconds int32 = 1800

// ToObservations 把一轮采集的结果转成运营指标（规格 §9.1）。
//
// 三条：metering 的成本与收入（取数侧，由契约定义），加上本包的入账侧。
// 复用 metering.ToObservations 而不是自己再拼一遍成本 / 收入的 value：
// 指标键、来源、新鲜度阈值只该有一个来源，否则采集侧改了字段名，
// 入账侧还在按老名字写，两条曲线会在同一张图上各说各话。
func (r CollectResult) ToObservations(
	now time.Time, instanceID, environment string,
) []ops.Observation {
	out := metering.ToObservations(now, instanceID, environment, r.Costs, r.Revenues)
	return append(out, r.ProfitObservation(now, instanceID, environment))
}

// ProfitObservation 把入账结果转成一条观测。
//
// 观测时刻取本轮读数里**最旧**的那个（metering 的 aggregateSnapshot 同款纪律）：
// 聚合指标的新鲜度由最不新鲜的成员决定。取最新会让一个刚刷新的令牌替十个
// 陈旧的令牌背书，看板显示「数据新鲜」，而那一格里九成的数字已经过期了。
func (r CollectResult) ProfitObservation(
	now time.Time, instanceID, environment string,
) ops.Observation {
	value := map[string]any{
		"scale":          metering.UsageScale,
		"business_days":  r.BusinessDays(),
		"accounts_total": r.AccountsTotal,
		// 每一类「没写成」单独一个数：上游今天没数（跳过）、纪律生效（不建行）、
		// 真写不进去（故障）是三件事，合成一个 failed 之后，看板上的红点
		// 就再也说不清该不该有人起来处理。
		"accounts_failed":            r.AccountsFailed,
		"rows_written":               r.RowsWritten,
		"rows_skipped_nothing_known": r.RowsSkippedNothingKnown,
		"rows_skipped_one_sided":     r.RowsSkippedOneSided,
		"rows_failed":                r.RowsFailed,
		// 账号级聚合行（§12.2）：这几把令牌从此没有独立的下钻行。
		// 数字忽然变大意味着有人给某个自营账号加挂了第二把 key——
		// 一次值得知道的拓扑变化，不该静默发生。
		"rows_aggregated": r.RowsAggregated,
		// 订阅型（XM-0037c，§3.5）。与计量型分开呈现：两者的成本来路完全不同
		// （一个是读来的、一个是算出来的），合成一个数之后，
		// 「上游读不到」与「批次没登记」在看板上会长得一模一样。
		"subscription_accounts_total": r.SubscriptionAccountsTotal,
		"subscription_rows_written":   r.SubscriptionRowsWritten,
		// 只有成本、收入未知的行数。不是失败——订阅渠道的收入通道 v1 常常
		// 还没接——但必须可见：那些行的毛利全是 NULL，看板得说得出为什么。
		"subscription_rows_cost_only": r.SubscriptionRowsCostOnly,
		"rows_skipped_no_batch":       r.RowsSkippedNoBatch,
		"rows_skipped_no_owner":       r.RowsSkippedNoOwner,
		// 合计只覆盖两侧都已知的那些行，所以行数必须一起给——
		// 否则「十行里有一行算得出毛利」与「十行都算得出」呈现完全相同。
		"rows_with_profit": r.RowsWithProfit,
	}
	if r.MixedCurrency {
		// 合计不给（会是错数），但**说清为什么不给**：留一个空位比留一个错
		// 数字好，留一个没有解释的空位则会让人以为是 bug（宪法 12 条）。
		value["total_omitted_reason"] = "mixed_currency"
	} else {
		value["revenue_minor_units"] = r.RevenueMinorSum
		value["cost_minor_units"] = r.CostMinorSum
		if profit := r.ProfitMinorSum(); profit != nil {
			value["profit_minor_units"] = *profit
		}
		value["currency"] = r.Currency
	}

	observation := ops.Observation{
		MetricKey:   MetricProfitDaily,
		Source:      instanceID,
		Environment: environment,
		// SyncedAt 是最近一次入账**尝试**的时刻，无论成败——这是「任务还活着」
		// 的唯一证据，和 observed_at 各管各的。
		SyncedAt:                  now.UTC(),
		Status:                    ops.SyncOK,
		IsPartial:                 r.Partial,
		StalenessThresholdSeconds: ProfitStalenessThresholdSeconds,
		Value:                     value,
	}
	if observedAt := r.oldestObservedAt(); observedAt != nil {
		observation.ObservedAt = observedAt
		observation.LastSuccess = observedAt
		observation.Watermark = observedAt.UTC().Format(time.RFC3339)
	}
	// 零值 ObservedAt 保持为空而不是用 now 冒充：一轮什么都没采到的采集与
	// 一轮刚采到的采集必须分得开（规格 §9.1）。
	return observation
}

// oldestObservedAt 取本轮全部读数里最旧的观测时刻。
func (r CollectResult) oldestObservedAt() *time.Time {
	var oldest time.Time
	consider := func(t time.Time) {
		if t.IsZero() {
			return
		}
		if oldest.IsZero() || t.Before(oldest) {
			oldest = t
		}
	}
	for _, c := range r.Costs {
		consider(c.ObservedAt)
	}
	for _, v := range r.Revenues {
		consider(v.ObservedAt)
	}
	if oldest.IsZero() {
		return nil
	}
	utc := oldest.UTC()
	return &utc
}
