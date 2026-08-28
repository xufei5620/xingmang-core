import { useQuery } from "@tanstack/react-query";
import { FreshnessBadge, FreshnessNote, MetricCard, StatTile } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { Fragment, useMemo, useState } from "react";
import { listChannelSummaries } from "../api/finance";
import { formatScaledMinorUnits } from "../lib/money";
import { aggregateChannelMoney, aggregateFailureText, aggregateFreshness, periodRangeFor, type ChannelMoneyAggregate, type FinancePeriodMode } from "../lib/financeOverview";
import { ApiStateView } from "./ApiStateView";
import { PeriodRangeControl } from "./PeriodRangeControl";

const PAYMENT_CONNECTOR_NOTE = "支付 Connector（M3）未接入";
const PAYMENT_CARD_LABELS = ["区间成功到账", "区间待处理", "区间失败", "退款与冲正", "支付手续费", "净现金流入"];

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

function PaymentUnavailableCard({ label }: { label: string }) {
  return <StatTile label={label} value="—" unavailable status={<Badge tone="neutral">未接入</Badge>} note={`${PAYMENT_CONNECTOR_NOTE}；接入逐笔支付事件后提供此区间汇总。`} />;
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
    <PeriodRangeControl date={date} mode={mode} range={range} onDateChange={(value) => setDate(value || fallbackDate)} onModeChange={setMode} />
    <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
      <div className="flex flex-col gap-4">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
          {PAYMENT_CARD_LABELS.map((label) => <PaymentUnavailableCard key={label} label={label} />)}
          <SourcedMetric label="使用收入" aggregate={revenue} />
          <SourcedMetric label="渠道毛利" aggregate={profit} />
        </div>
        <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
          <section className="rounded-lg border border-edge bg-surface p-4"><h2 className="mb-3 text-base font-semibold text-fg">资金对账</h2><dl className="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)] gap-x-4 gap-y-3 text-sm">{["订单成功金额", "支付机构净结算", "退款与冲正", "净现金流入", "对账差异"].map((label) => <Fragment key={label}><dt className="text-fg-muted">{label}</dt><dd><UnavailableAmount /></dd></Fragment>)}</dl></section>
          <section className="rounded-lg border border-edge bg-surface p-4"><h2 className="mb-3 text-base font-semibold text-fg">经营利润桥</h2><dl className="grid grid-cols-[minmax(0,1fr)_minmax(0,1fr)] gap-x-4 gap-y-3 text-sm"><dt className="text-fg-muted">使用收入</dt><dd><EvidenceAmount aggregate={revenue} /></dd><dt className="text-fg-muted">上游现金成本</dt><dd><EvidenceAmount aggregate={cost} /></dd><dt className="text-fg-muted">渠道毛利</dt><dd><EvidenceAmount aggregate={profit} /></dd><dt className="text-fg-muted">支付手续费</dt><dd><UnavailableAmount /></dd><dt className="text-fg-muted">贡献利润</dt><dd><UnavailableAmount note="支付费用与可归属基础设施/代理成本未接入" /></dd></dl></section>
        </div>
        <section className="overflow-hidden rounded-lg border border-edge bg-surface"><div className="border-b border-edge px-4 py-3"><h2 className="text-base font-semibold text-fg">最近事件</h2><p className="text-xs text-fg-muted">支付事件端点尚未接入；金额与笔数不会用样例值替代。</p></div><div className="overflow-x-auto"><table className="w-full min-w-150 text-left text-sm" aria-label="最近事件"><caption className="sr-only">最近事件；支付 Connector（M3）未接入，因此金额、笔数与状态均不可用。</caption><thead className="bg-surface-muted text-xs text-fg-muted"><tr>{["最近事件", "金额", "笔数", "状态", "去向"].map((label) => <th key={label} className="px-3 py-2 font-medium">{label}</th>)}</tr></thead><tbody>{[["成功充值", "充值订单"], ["待处理", "充值订单"], ["失败", "充值订单"], ["退款", "退款与冲正"]].map(([event, destination]) => <tr key={event} className="border-t border-edge"><td className="px-3 py-2 text-fg">{event}</td><td className="px-3 py-2 text-fg-muted">—</td><td className="px-3 py-2 text-fg-muted">—</td><td className="px-3 py-2"><Badge tone="neutral">未接入</Badge></td><td className="px-3 py-2 text-fg-muted">{destination}</td></tr>)}</tbody></table></div></section>
      </div>
    </ApiStateView>
  </div>;
}
