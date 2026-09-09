import { useQuery } from "@tanstack/react-query";
import {
  FreshnessBadge,
  FreshnessNote,
  MetricCard,
  StatTile,
  type FreshnessContract,
} from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";

import { listMetricHistory, listMetrics, type MetricItem } from "../api/platform";
import { formatCount, formatMinorUnits } from "../lib/money";
import {
  monthToDateSucceeded,
  readPaymentsDailySummary,
  type PaymentBucketAmount,
  type PaymentsDailySummary,
} from "../lib/metrics";
import { ApiStateView } from "./ApiStateView";

/** 资金概览的六张卡（XM-PAY1），Sub2API「资金概览」与 NewAPI「资金与订单」
 *  共用同一个组件：两边消费的都是 XM-PAY0 写入的 `sub2api.payments.daily`/
 *  `newapi.payments.daily` 指标，形状完全一致，差异只在「退款与冲正」——
 *  NewAPI 这张卡固定显示「不适用」，见 bucketState 的判断。
 *
 *  指标是**单日**粒度：区间收窄到单日且与指标自己的业务日一致时才展示真实
 *  值，否则给出「周/月需要按天聚合」的诚实说明——与 NewApiFinanceOverview.tsx
 *  既有的 NEWAPI_RECHARGE_METRIC/periodMatchesMetric 同一条纪律，这里复用
 *  同款判断而不是另起一套。 */

export type PaymentsPlatform = "sub2api" | "newapi";

export interface PaymentSummaryCardsRange {
  from: string;
  to: string;
}

export function metricKeyFor(platform: PaymentsPlatform): string {
  return platform === "sub2api" ? "sub2api.payments.daily" : "newapi.payments.daily";
}

export function metricByKey(items: readonly MetricItem[], key: string): MetricItem | undefined {
  return items.find((item) => item.metric_key === key);
}

/** 区间是否收窄到单日、且与指标自己的业务日一致——指标是单日粒度，
 *  周/月区间没有对应的单日聚合可用，不能拿单日值冒充周/月合计。 */
function periodMatchesMetric(
  metric: MetricItem | undefined,
  summary: PaymentsDailySummary | null,
  range: PaymentSummaryCardsRange,
): boolean {
  return Boolean(
    metric &&
      summary &&
      range.from === range.to &&
      summary.day === range.from &&
      metric.freshness.state !== "uninitialized",
  );
}

function amountText(amountMinor: bigint | null, currency: string): string {
  return amountMinor === null ? "—" : formatMinorUnits(amountMinor, currency);
}

function countNote(bucket: PaymentBucketAmount | null): string {
  if (bucket === null) return "0 笔";
  return bucket.count === null ? "笔数未知" : `${formatCount(bucket.count)} 笔`;
}

/** `is_partial` 优先于陈旧生产者留下的 state=fresh（与既有
 *  effectiveMetricFreshness 同一条纪律，见 NewApiFinanceOverview.tsx）。 */
function effectiveFreshness(freshness: FreshnessContract): FreshnessContract {
  return freshness.is_partial && freshness.state === "fresh"
    ? { ...freshness, state: "partial" }
    : freshness;
}

/** 值给不出时的卡片。
 *
 *  `badge` 缺省是「未接入」——**不传的调用点行为与加这个参数之前逐字相同**，
 *  有用例钉着。只有确实不是「未接入」的那一种给不出才传，见
 *  `coverageBadgeFor`。 */
function UnavailableCard({ label, note, badge = "未接入" }: { label: string; note: string; badge?: string }) {
  return (
    <StatTile
      label={label}
      value="—"
      unavailable
      note={note}
      status={<Badge tone="neutral">{badge}</Badge>}
    />
  );
}

/** 非单日区间时这几张卡显示「—」，原因是**这个口径只覆盖单日**，不是数据缺失。
 *
 *  这两件事此前在屏幕上长得一模一样（都是「未接入」徽章 + 「—」），而**下一步
 *  完全相反**：口径不覆盖不用管，数据缺失要查。徽章因此必须分开——只把区别写在
 *  下面那行小字里不够，徽章才是扫一眼就会读到的东西。
 *
 *  返回 `undefined` 让 `UnavailableCard` 用回默认的「未接入」：单日区间下取不到
 *  指标，那**就是**真的没接上/没数据，措辞不该改。 */
function coverageBadgeFor(range: PaymentSummaryCardsRange): string | undefined {
  return range.from === range.to ? undefined : "仅支持单日";
}

function NotApplicableCard({ label, note }: { label: string; note: string }) {
  return (
    <StatTile
      label={label}
      value="—"
      unavailable
      note={note}
      status={<Badge tone="neutral">不适用</Badge>}
    />
  );
}

/** 四个归一化分桶卡（成功到账/待处理/失败/退款与冲正）。
 *
 *  `by_status` 里键不出现，在 `is_partial=false` 时**就是确认过的零**
 *  （契约原话："上游这一天没有落进某个桶的订单，那个桶的键就不出现"，
 *  见 payments.read.v1.md），不是「未知」——这里据此展示真实的 0，
 *  而不是显示一张「未接入」的卡去掩盖一个已经查明的事实。只有
 *  `is_partial=true` 时，键缺席才说明不清「真的是零」还是「翻页/币种缺口
 *  漏掉了」，这时才退回「覆盖不全」而不是断言 0。
 *
 *  导出供 NewApiFinanceOverview 复用：NewAPI「资金与订单」按团队负责人
 *  裁定改回原型的四格布局（区间到账/区间退款/月累计/支付失败），不再用
 *  下面的 `PaymentSummaryCards` 六卡整体——但"区间到账"就是
 *  `bucket="succeeded"`、"区间退款"就是 `bucket="refunded"`（对 NewAPI 会
 *  自动落到"不适用"分支）、"支付失败"就是 `bucket="failed"`，与 Sub2API
 *  六卡里的同名卡片必须是同一份逻辑，不能另起一套判断。 */
export function BucketCard({
  bucket,
  label,
  platform,
  metric,
  summary,
  range,
}: {
  bucket: "succeeded" | "pending" | "failed" | "refunded";
  label: string;
  platform: PaymentsPlatform;
  metric: MetricItem | undefined;
  summary: PaymentsDailySummary | null;
  range: PaymentSummaryCardsRange;
}) {
  if (platform === "newapi" && bucket === "refunded") {
    return (
      <NotApplicableCard
        label={label}
        note="NewAPI 没有退款概念（上游没有 refund 字段/状态/函数），不是「今天没有退款」"
      />
    );
  }

  if (!metric || !summary || !periodMatchesMetric(metric, summary, range)) {
    return (
      <UnavailableCard
        label={label}
        badge={coverageBadgeFor(range)}
        note={
          range.from === range.to
            ? "暂无匹配业务日的支付日汇总指标"
            : "周/月需要按天聚合；目前只提供单日汇总，不能用单日值冒充区间合计"
        }
      />
    );
  }

  const freshness = effectiveFreshness(metric.freshness);
  const bucketAmount = summary.byStatus[bucket] ?? null;
  const isPartial = metric.freshness.is_partial;

  if (bucketAmount === null && isPartial) {
    return (
      <UnavailableCard
        label={label}
        note="覆盖不全：本次汇总翻页到顶或遇到非合约状态，无法确认这个分桶是否真的是零"
      />
    );
  }

  const amount = bucketAmount?.amountMinor ?? 0n;
  // 覆盖不全：命中非合约币种的订单笔数仍计入 count，但金额被排除在 amount 之外
  // （契约"跨币种处理"一节），合计因此可能偏低——必须在这张卡自己的副行说清楚，
  // 不能只靠 FreshnessBadge 的"数据不完整"一个笼统状态词代替具体原因。
  const secondary = isPartial
    ? `${countNote(bucketAmount)} · 业务日 ${summary.day ?? "—"} · 覆盖不全：可能有非合约币种订单未计入金额（笔数仍计入）`
    : `${countNote(bucketAmount)} · 业务日 ${summary.day ?? "—"}`;
  return (
    <MetricCard
      label={label}
      metricKey={metric.metric_key}
      value={amountText(amount, summary.currency)}
      secondary={secondary}
      freshness={freshness}
      source={metric.source || "指标来源未声明"}
      link={<FreshnessNote freshness={metric.freshness} />}
    />
  );
}

/** 支付手续费卡：Sub2API 在有 succeeded/refunded 订单的日子才有值，
 *  NewAPI 恒为 nil（上游没有第二个金额字段可供相减，见契约"手续费与净
 *  现金流"一节）。 */
function FeeCard({
  metric,
  summary,
  range,
}: {
  metric: MetricItem | undefined;
  summary: PaymentsDailySummary | null;
  range: PaymentSummaryCardsRange;
}) {
  if (!metric || !summary || !periodMatchesMetric(metric, summary, range)) {
    return (
      <UnavailableCard
        label="支付手续费"
        badge={coverageBadgeFor(range)}
        note={
          range.from === range.to
            ? "暂无匹配业务日的支付日汇总指标"
            : "周/月需要按天聚合；目前只提供单日汇总"
        }
      />
    );
  }
  if (summary.feeMinorUnits === null) {
    return (
      <UnavailableCard
        label="支付手续费"
        note="这个上游当天没有可推出手续费的订单，或该连接器不提供第二个金额字段（NewAPI 恒为未知）"
      />
    );
  }
  const freshness = effectiveFreshness(metric.freshness);
  const secondary = metric.freshness.is_partial
    ? `业务日 ${summary.day ?? "—"} · 仅对成功到账与退款订单求和 · 覆盖不全：可能有非合约币种订单未计入`
    : `业务日 ${summary.day ?? "—"} · 仅对成功到账与退款订单求和`;
  return (
    <MetricCard
      label="支付手续费"
      metricKey={metric.metric_key}
      value={amountText(summary.feeMinorUnits, summary.currency)}
      secondary={secondary}
      freshness={freshness}
      source={metric.source || "指标来源未声明"}
      link={<FreshnessNote freshness={metric.freshness} />}
    />
  );
}

/** 净现金流入卡：两平台恒为未接入——净现金流公式尚未确定（需要业务侧先
 *  确认手续费是否已经从 amount 里扣除），前端不现算 amount-fee 去冒充一个
 *  后端明确拒绝下结论的数字。 */
function NetCashFlowCard() {
  return (
    <UnavailableCard
      label="净现金流入"
      note="净现金流公式尚未确定（需先确认手续费承担方与是否有未建模成本），本片刻意留白，不猜"
    />
  );
}

/** 「月累计」（NewAPI「资金与订单」四格之一，Sub2API 六卡没有这一格）：
 *  自然日历月至今的 succeeded 桶累计，与"区间到账"是两个刻意不同的数字——
 *  前者恒等于当前自然月，不随 PeriodControls 的选择变化；后者跟着所选区间走。
 *
 *  自己发起 `/api/v1/metrics/history` 查询（与 `PaymentSummaryCards`/
 *  `BucketCard` 共用的 `/api/v1/metrics` 是两条独立的 Query），服务端硬顶
 *  7 天（见 `monthToDateSucceeded` 的注释），月初超过 7 天前的部分天然覆盖
 *  不到，`覆盖不全` 必须在这张卡自己说清楚，不能显示一个看起来完整的月合计。 */
export function MonthToDateSucceededCard({
  platform,
  monthStart,
  today,
}: {
  platform: PaymentsPlatform;
  /** 自然月第一天，YYYY-MM-DD（业务时区，调用方按 Asia/Shanghai 算好再传）。 */
  monthStart: string;
  /** 目标日（通常是"今天"），YYYY-MM-DD。 */
  today: string;
}) {
  const query = useQuery({
    queryKey: ["metrics-history", metricKeyFor(platform), 168],
    queryFn: ({ signal }) => listMetricHistory(metricKeyFor(platform), { hours: 168, signal }),
  });

  // 全程只返回 <StatTile>（不经由 UnavailableCard 这层包装组件）：这张卡
  // 自己管理异步状态，加载中→已加载会在同一个位置切换 JSX——如果两支分别
  // 委托给不同的包装组件（UnavailableCard vs 这里直接调 StatTile），
  // React 按元素类型做协调，会把整个子树卸载重挂，而不是原地更新同一个
  // <article> DOM 节点。多数卡片没有这个问题是因为它们的加载态由外层
  // ApiStateView 统一兜底、组件本身只在数据就绪后才渲染一次；这张卡是
  // 唯一一个自己发起独立 Query 的卡片，必须自己保证类型稳定。
  let value = "—";
  let note: string;
  let unavailable = true;
  let status: ReactNode = <Badge tone="neutral">未接入</Badge>;

  if (query.isPending) {
    note = "加载中…";
  } else if (query.error) {
    const message = query.error instanceof Error ? query.error.message : "读取历史观测失败";
    note = `月累计读取失败：${message}`;
  } else {
    const summary = monthToDateSucceeded(query.data ?? [], monthStart, today);
    if (summary.amountMinor === null) {
      note = "历史观测里没有落在本月的可用样本；月度订单聚合需要按天累加，暂无法确认";
    } else {
      value = formatMinorUnits(summary.amountMinor, summary.currency);
      unavailable = false;
      status = summary.complete ? undefined : <Badge tone="warning">覆盖不全</Badge>;
      note = summary.complete
        ? `覆盖 ${summary.coveredDays}/${summary.totalDays} 天 · 本月至今`
        : `覆盖不全：${summary.coveredDays}/${summary.totalDays} 天（历史查询最多回看 7 天，或个别日观测缺失/不完整）`;
    }
  }

  return <StatTile label="月累计" value={value} unavailable={unavailable} note={note} status={status} />;
}

export function PaymentSummaryCards({
  platform,
  range,
}: {
  platform: PaymentsPlatform;
  range: PaymentSummaryCardsRange;
}) {
  const query = useQuery({
    queryKey: ["metrics"],
    queryFn: ({ signal }) => listMetrics({ signal }),
  });

  return (
    <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
      {(() => {
        const items = query.data ?? [];
        const metric = metricByKey(items, metricKeyFor(platform));
        const summary = metric ? readPaymentsDailySummary(metric.value) : null;
        return (
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
            <BucketCard bucket="succeeded" label="区间成功到账" platform={platform} metric={metric} summary={summary} range={range} />
            <BucketCard bucket="pending" label="区间待处理" platform={platform} metric={metric} summary={summary} range={range} />
            <BucketCard bucket="failed" label="区间失败" platform={platform} metric={metric} summary={summary} range={range} />
            <BucketCard bucket="refunded" label="退款与冲正" platform={platform} metric={metric} summary={summary} range={range} />
            <FeeCard metric={metric} summary={summary} range={range} />
            <NetCashFlowCard />
          </div>
        );
      })()}
    </ApiStateView>
  );
}
