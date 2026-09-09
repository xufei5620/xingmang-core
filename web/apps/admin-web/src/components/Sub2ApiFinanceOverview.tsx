import { useQuery } from "@tanstack/react-query";
import { FreshnessBadge, FreshnessNote, MetricCard, PeriodControls } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { Fragment, useMemo, useState } from "react";
import { listChannelSummaries, type PlatformOrdersPage } from "../api/finance";
import { formatCount, formatMinorUnits, formatScaledMinorUnits } from "../lib/money";
import { aggregateChannelMoney, aggregateFailureText, aggregateFreshness, periodRangeFor, type ChannelMoneyAggregate, type DateOnlyRange, type FinancePeriodMode } from "../lib/financeOverview";
import { presentPaymentBucket, rollupPaymentStatuses, type PaymentBucketCell } from "../lib/paymentStatusRollup";
import { ApiStateView } from "./ApiStateView";
import { PaymentSummaryCards } from "./PaymentSummaryCards";
import { useSub2ApiOrdersQuery } from "./Sub2ApiOrdersPanel";

const PAYMENT_CONNECTOR_NOTE = "支付 Connector（M3）未接入";

function todayDateOnly(): string {
  const now = new Date();
  return `${now.getFullYear()}-${(now.getMonth() + 1).toString().padStart(2, "0")}-${now.getDate().toString().padStart(2, "0")}`;
}

function aggregateText(aggregate: ChannelMoneyAggregate): string {
  return aggregate.money ? formatScaledMinorUnits(aggregate.money.amountMinor, aggregate.money.currency, aggregate.money.scale) : "—";
}

function coverageText(aggregate: ChannelMoneyAggregate): string {
  const { completeRows, totalRows } = aggregate.coverage;
  return totalRows > 0 && completeRows === totalRows ? `覆盖完整 ${completeRows}/${totalRows} 条渠道` : `覆盖不全 ${completeRows}/${totalRows} 条渠道`;
}

function SourcedMetric({ label, aggregate }: { label: string; aggregate: ChannelMoneyAggregate }) {
  const unavailable = aggregate.money === null;
  const reason = aggregateFailureText(aggregate);
  return <MetricCard label={label} value={aggregateText(aggregate)} unavailable={unavailable} secondary={reason ? `${coverageText(aggregate)} · ${reason}` : coverageText(aggregate)} freshness={aggregateFreshness(aggregate, Date.now())} source={aggregate.source || "渠道汇总来源未声明"} />;
}

function EvidenceAmount({ aggregate }: { aggregate: ChannelMoneyAggregate }) {
  const freshness = aggregateFreshness(aggregate, Date.now());
  const reason = aggregateFailureText(aggregate);
  return <span className="flex flex-wrap items-center justify-end gap-1 text-right tabular-nums"><strong className={aggregate.money ? undefined : "text-fg-muted"}>{aggregateText(aggregate)}</strong><FreshnessBadge freshness={freshness} /><span className="w-full text-xs text-fg-muted">{coverageText(aggregate)} · 来源 {aggregate.source || "渠道汇总来源未声明"}</span>{reason ? <span className="w-full text-xs text-fg-muted">{reason}</span> : null}<span className="w-full text-xs text-fg-muted"><FreshnessNote freshness={freshness} /></span></span>;
}

function UnavailableAmount({ note = PAYMENT_CONNECTOR_NOTE }: { note?: string }) {
  return <span className="flex flex-col items-end gap-1 text-right"><span className="text-fg-muted">—</span><span className="text-xs text-fg-muted">{note}</span></span>;
}

// ---------------------------------------------------------------------------
// 两组「四个桶」的口径标注（XM-SUB2API-RECENT-EVENTS，产品负责人裁定「两组都留」）
//
// 这一页有两组算的是同一批分桶、但口径不同的数字：
//   · 上面六张卡  ← `sub2api.payments.daily` 指标，**单个业务日**，不随区间聚合
//   · 下面最近事件 ← 逐笔订单端点 stats_by_status，**跟随所选统计区间**
// 选周/月时上面全是「—」、下面有数，构造上完全正常。但两者今天在界面上都
// 只是「一组卡片」，读者的第一反应是「有一个坏了」——所以各自的口径必须写在
// 界面上，而不是留在这段注释里。
// ---------------------------------------------------------------------------

/** 六张日快照卡外面的口径说明。
 *
 *  **只包一层说明，不改卡片本身**：卡片在 `PaymentSummaryCards`，那是 NewAPI
 *  「资金与订单」共用的组件，不在本片许可范围内。因此「非单日区间的『—』是
 *  口径不覆盖、不是数据缺失」这句话放在组别这一层讲一次，而不是逐卡去改
 *  徽章文案（后者需要改 `PaymentSummaryCards`，已单独报备）。 */
function PaymentDaySnapshot({ range }: { range: DateOnlyRange }) {
  const singleDay = range.from === range.to;
  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-col gap-1">
        <h2 className="text-base font-semibold text-fg">支付日快照</h2>
        <p className="text-xs text-fg-muted">
          来自 <code className="font-mono">sub2api.payments.daily</code> 指标，口径是<strong>单个业务日</strong>，不随所选区间聚合。它与下方「最近事件」的<strong>区间合计</strong>是两条独立通道、两种口径，<strong>不是同一个数的两个答案</strong>；两者对不上不代表有一个错了。
        </p>
        {singleDay ? null : (
          // 缺席型断言在测试里守着这一支：单日区间**不能**出现这段话，否则
          // 它就成了永远显示的噪声，读者会连真正该看的那次也一并忽略。
          <p role="status" className="rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted">
            所选区间是 {range.from} 至 {range.to}，<strong>超出这个口径能回答的范围</strong>，所以下面六张卡显示「—」。这是<strong>口径不覆盖，不是数据缺失</strong>，链路没有故障，不需要排查；要看这个区间的合计，见下方「最近事件」。
          </p>
        )}
      </div>
      <PaymentSummaryCards platform="sub2api" range={range} />
    </section>
  );
}

// ---------------------------------------------------------------------------
// 「最近事件」四行（XM-SUB2API-RECENT-EVENTS）
// ---------------------------------------------------------------------------

/** 行名是原型措辞，桶名是 `bucketLabel` 的归一化取值——两者不同名是刻意的：
 *  行名说的是**业务事件**（用户充了一笔钱），桶名说的是**订单状态归类**，
 *  而「去向」指向能看到这一行明细的地方。三列合起来，一个人才能从这张表
 *  走到「充值订单」页签按同一个桶筛出那几十笔具体订单。 */
const RECENT_EVENT_ROWS: { label: string; bucket: string; destination: string }[] = [
  { label: "成功充值", bucket: "成功到账", destination: "充值订单" },
  { label: "待处理", bucket: "待处理", destination: "充值订单" },
  { label: "失败", bucket: "失败", destination: "充值订单" },
  { label: "退款", bucket: "退款与冲正", destination: "退款与冲正" },
];

function eventAmountText(cell: PaymentBucketCell): string {
  if (cell.minorUnits === null) return "—";
  // 确认过的零（区间里一笔都没有）时币种无从谈起：零在任何币种下都是零，
  // 直接写 0，而不是让 formatMinorUnits 附一句「金额单位未知」——那句话
  // 对一个已经确定的零只会凭空制造疑问。
  if (cell.minorUnits === 0n && !cell.currency) return "0";
  return formatMinorUnits(cell.minorUnits, cell.currency);
}

/** 「状态」列说的是**这一行数据的可信程度**，不是订单状态——订单状态就是
 *  行名本身，在同一行里重复一遍只是噪声。原型在这一列放的也是可用性
 *  （「未接入」），这里沿用同一种语义，只是现在它有四挡而不是一挡。 */
const COVERAGE_BADGE: Record<PaymentBucketCell["coverage"], { label: string; tone: "neutral" | "warning"; hint: string }> = {
  known: { label: "完整", tone: "neutral", hint: "本区间的金额与笔数都来自上游汇总，且上游未标记不完整" },
  "confirmed-zero": { label: "完整", tone: "neutral", hint: "本区间内确认没有落进这个分桶的订单——是 0，不是「没接上」" },
  partial: { label: "覆盖不全", tone: "warning", hint: "笔数完整，金额不完整；具体原因见左侧金额下方的说明" },
  unknown: { label: "说不清", tone: "warning", hint: "上游把本次汇总标记为不完整，且这个分桶没有出现，无法断言它是零" },
};

function RecentEventRow({ label, bucket, destination, cell }: { label: string; bucket: string; destination: string; cell: PaymentBucketCell }) {
  const badge = COVERAGE_BADGE[cell.coverage];
  return (
    <tr className="border-t border-edge">
      <td className="px-3 py-2 text-fg" title={`归一化分桶「${bucket}」；在「充值订单」页签按同一个桶筛选，就是这一行的逐笔明细`}>{label}</td>
      <td className="px-3 py-2">
        <span className={cell.minorUnits === null ? "text-fg-muted" : "tabular-nums text-fg"}>{eventAmountText(cell)}</span>
        {cell.amountNote ? <span className="mt-0.5 block text-xs text-fg-muted">{cell.amountNote}</span> : null}
      </td>
      <td className="px-3 py-2">
        <span className={cell.count === null ? "text-fg-muted" : "tabular-nums text-fg"}>{cell.count === null ? "—" : `${formatCount(cell.count)} 笔`}</span>
      </td>
      <td className="px-3 py-2"><Badge tone={badge.tone} title={badge.hint}>{badge.label}</Badge></td>
      <td className="px-3 py-2 text-fg-muted">{destination}</td>
    </tr>
  );
}

/** 一页订单响应 → 四行。分桶、求和与「给不出」的判据全部来自
 *  `lib/paymentStatusRollup`，与「充值订单」页签的「本区间汇总」是同一份
 *  实现——同一个口径两份实现迟早分叉。 */
function RecentPaymentEventsTable({ page }: { page: PlatformOrdersPage }) {
  const rollup = rollupPaymentStatuses(page.stats_by_status);
  return (
    <div className="flex flex-col gap-2 px-4 py-3">
      <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-fg-muted">
        <span>来源 {page.data_source || "—"} · 窗口 {page.from} 至 {page.to} · 本区间共 {formatCount(rollup.totalCount)} 笔</span>
        <FreshnessBadge freshness={page.freshness} />
      </div>
      <FreshnessNote freshness={page.freshness} />
      {rollup.unknownStatuses.length > 0 ? (
        <p role="status" className="rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg">
          上游出现了尚未归类的状态：<span className="mx-1 font-mono">{rollup.unknownStatuses.join("、")}</span>
          。它们不属于下面四行中的任何一行，因此四行之和小于「本区间共 {formatCount(rollup.totalCount)} 笔」；逐笔明细在「充值订单」页签的「未知」行。
        </p>
      ) : null}
      <div className="overflow-x-auto">
        <table className="w-full min-w-150 text-left text-sm" aria-label="最近事件">
          <caption className="sr-only">最近事件：本统计区间内用户充值与支付事件按归一化分桶的金额与笔数；这是充值口径，不是收入。</caption>
          <thead className="bg-surface-muted text-xs text-fg-muted"><tr>{["最近事件", "金额", "笔数", "状态", "去向"].map((label) => <th key={label} className="px-3 py-2 font-medium">{label}</th>)}</tr></thead>
          <tbody>
            {RECENT_EVENT_ROWS.map((row) => (
              <RecentEventRow key={row.label} label={row.label} bucket={row.bucket} destination={row.destination} cell={presentPaymentBucket(rollup, row.bucket, page.freshness.is_partial)} />
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

/** 「最近事件」区块。
 *
 *  自己包一层 `ApiStateView`（而不是靠外层那一层）：订单端点在
 *  `XM_PLATFORM_PAYMENTS_MODE=off` 的部署上根本不挂载，那时这一块该显示
 *  「未接入」，而整页的渠道汇总、使用收入、经营利润桥都还是好的——一条
 *  查询的缺席不该把另一条查询的结论一起抹掉。
 *
 *  查询直接复用「充值订单」页签导出的 `useSub2ApiOrdersQuery`：**同一个
 *  queryKey**，两个页签命中同一份缓存，因此两处显示的数字在构造上就不可能
 *  对不上（不是「我们记得要让它们一致」，是它们本来就是同一个响应）。 */
function RecentPaymentEvents({ range }: { range: DateOnlyRange }) {
  const query = useSub2ApiOrdersQuery(range);
  const pages = query.data?.pages ?? [];
  const lastPage = pages.length > 0 ? pages[pages.length - 1] : undefined;

  return (
    <section className="overflow-hidden rounded-lg border border-edge bg-surface">
      <div className="border-b border-edge px-4 py-3">
        <h2 className="text-base font-semibold text-fg">最近事件</h2>
        {/* 这四个数最容易被读成收入，所以「不是收入」写在标题正下方，而不是
            只靠页顶那条横幅——横幅在这里已经滚出屏幕了。 */}
        <p className="text-xs text-fg-muted">
          本统计区间内的<strong>用户充值与支付事件</strong>分桶，<strong>不是当期收入</strong>：收入是上方「经营利润桥」的「使用收入」一行，两个数永远分开列，不相加。逐笔明细在「充值订单」页签，那里的「本区间汇总」与这四行同一份口径、同一个查询。
        </p>
        <p className="mt-1 text-xs text-fg-muted">
          口径是<strong>区间合计</strong>，跟随上方所选的统计区间（来自逐笔订单端点）；上方「支付日快照」是<strong>单个业务日</strong>的指标值。<strong>两组算的是同一批分桶，但覆盖的时间范围不同</strong>，数字对不上是口径差异，不是其中一个坏了。
        </p>
      </div>
      <ApiStateView compact isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
        {lastPage ? <RecentPaymentEventsTable page={lastPage} /> : null}
      </ApiStateView>
    </section>
  );
}

export function Sub2ApiFinanceOverview({ initialDate }: { initialDate?: string }) {
  const fallbackDate = initialDate ?? todayDateOnly();
  const [date, setDate] = useState(fallbackDate);
  const [mode, setMode] = useState<FinancePeriodMode>("day");
  const range = useMemo(() => periodRangeFor(date, mode), [date, mode]);
  const query = useQuery({
    queryKey: ["finance", "channels", "summary", range.from, range.to],
    queryFn: ({ signal }) => listChannelSummaries({ ...range, signal }),
  });
  const channels = query.data?.items ?? [];
  const revenue = aggregateChannelMoney(channels, "usageRevenue");
  const cost = aggregateChannelMoney(channels, "supplyCost");
  const profit = aggregateChannelMoney(channels, "grossProfit");

  return <div className="flex flex-col gap-4">
    <p role="status" className="rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg">「用户充值」不是当期收入：用户发生<strong>使用消费</strong>时才确认使用收入。这两个数在这一页上永远分开列，不相加。</p>
    <PeriodControls
      day={date}
      granularity={mode}
      period={{ day: date, granularity: mode, ...range }}
      dateLabel="统计日期"
      dateAriaLabel="统计日期"
      granularityAriaLabel="统计模式"
      onDayChange={(value) => setDate(value || fallbackDate)}
      onGranularityChange={setMode}
    />
    <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
      <div className="flex flex-col gap-4">
        <PaymentDaySnapshot range={range} />
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <SourcedMetric label="使用收入" aggregate={revenue} />
          <SourcedMetric label="渠道毛利" aggregate={profit} />
        </div>
        <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
          <section className="rounded-lg border border-edge bg-surface p-4"><h2 className="mb-3 text-base font-semibold text-fg">资金对账</h2><dl className="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)] gap-x-4 gap-y-3 text-sm">{["订单成功金额", "支付机构净结算", "退款与冲正", "净现金流入", "对账差异"].map((label) => <Fragment key={label}><dt className="text-fg-muted">{label}</dt><dd><UnavailableAmount /></dd></Fragment>)}</dl></section>
          <section className="rounded-lg border border-edge bg-surface p-4"><h2 className="mb-3 text-base font-semibold text-fg">经营利润桥</h2><dl className="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)] gap-x-4 gap-y-3 text-sm"><dt className="text-fg-muted">使用收入</dt><dd><EvidenceAmount aggregate={revenue} /></dd><dt className="text-fg-muted">上游现金成本</dt><dd><EvidenceAmount aggregate={cost} /></dd><dt className="text-fg-muted">渠道毛利</dt><dd><EvidenceAmount aggregate={profit} /></dd><dt className="text-fg-muted">支付手续费</dt><dd><UnavailableAmount /></dd><dt className="text-fg-muted">贡献利润</dt><dd><UnavailableAmount note="支付费用与可归属基础设施/代理成本未接入" /></dd></dl></section>
        </div>
        <RecentPaymentEvents range={range} />
      </div>
    </ApiStateView>
  </div>;
}
