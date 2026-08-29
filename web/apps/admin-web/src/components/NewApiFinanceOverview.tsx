import { useQuery } from "@tanstack/react-query";
import {
  FreshnessBadge,
  FreshnessNote,
  MetricCard,
  PageState,
  PeriodControls,
  StatTile,
} from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { useMemo, useState, type ReactNode } from "react";
import { useSearchParams } from "react-router";

import {
  listChannelSummaries,
  type ChannelSummary,
} from "../api/finance";
import { listMetrics, type MetricItem } from "../api/platform";
import {
  formatCount,
  formatMinorUnits,
  formatScaledMinorUnits,
  toIntegerValue,
} from "../lib/money";
import { metricPrimaryValue } from "../lib/metrics";
import {
  appDemoDataConfig,
  shouldShowDemoBanner,
} from "../lib/demoData";
import {
  periodRangeFor,
  type FinancePeriodMode,
} from "../lib/financeOverview";
import { parseBusinessDay, parseGranularity } from "../lib/period";
import { ApiStateView } from "./ApiStateView";

/** NewAPI 财务页的指标键。用分段拼接避免把采集键误看成凭据。 */
const NEWAPI_RECHARGE_METRIC = ["newapi", "recharge", "daily"].join(".");
const NEWAPI_SUBSCRIPTION_METRIC = ["newapi", "subscription", "daily"].join(".");

type NewApiFinanceSubId = "orders" | "profit";

function validDateOnly(value: string): boolean {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return false;
  const date = new Date(`${value}T00:00:00Z`);
  return !Number.isNaN(date.getTime()) && date.toISOString().slice(0, 10) === value;
}

function metricByKey(items: readonly MetricItem[], key: string): MetricItem | undefined {
  return items.find((item) => item.metric_key === key);
}

function metricDay(metric: MetricItem | undefined): string | null {
  const day = metric?.value?.day;
  return typeof day === "string" && /^\d{4}-\d{2}-\d{2}$/.test(day) ? day : null;
}

function metricCurrency(metric: MetricItem): string {
  const currency = metric.value?.currency;
  return typeof currency === "string" ? currency : "";
}

function metricOrderCount(metric: MetricItem): string | null {
  const value = toIntegerValue(metric.value?.order_count);
  return value === null ? null : formatCount(value);
}

function metricAmountText(metric: MetricItem): { text: string; available: boolean } {
  const primary = metricPrimaryValue(metric.metric_key, metric.value);
  const currency = metricCurrency(metric);
  if (primary.raw === null || currency === "") {
    return { text: primary.raw === null ? "—" : formatMinorUnits(primary.raw, currency), available: false };
  }
  return { text: formatMinorUnits(primary.raw, currency), available: true };
}

function sourceText(metric: MetricItem | undefined): string {
  return metric?.source?.trim() || "指标来源未声明";
}

function periodMatchesMetric(metric: MetricItem | undefined, from: string, to: string): boolean {
  // NewAPI 目前只提供单日订单摘要。把单日值直接标成周/月合计会放大金额，
  // 因此只有选中的日期与指标业务日完全一致时才允许显示。
  return Boolean(metric && from === to && metricDay(metric) === from);
}

function MissingTile({ label, note }: { label: string; note: string }) {
  return (
    <StatTile
      label={label}
      value="—"
      unavailable
      note={note}
      status={<Badge tone="neutral">未接入</Badge>}
    />
  );
}

function MetricAmountTile({
  label,
  metric,
  from,
  to,
  missingNote,
  detail,
}: {
  label: string;
  metric: MetricItem | undefined;
  from: string;
  to: string;
  missingNote: string;
  detail: string;
}) {
  if (!metric || !periodMatchesMetric(metric, from, to)) {
    return <MissingTile label={label} note={missingNote} />;
  }

  const amount = metricAmountText(metric);
  const orderCount = metricOrderCount(metric);
  const day = metricDay(metric);
  const secondary = [
    day ? `业务日 ${day}` : null,
    orderCount ? `订单 ${orderCount} 笔` : "订单数未提供",
    detail,
  ]
    .filter(Boolean)
    .join(" · ");

  return (
    <MetricCard
      label={label}
      metricKey={metric.metric_key}
      value={amount.text}
      unavailable={!amount.available}
      secondary={secondary}
      freshness={metric.freshness}
      source={sourceText(metric)}
      watermark={metric.watermark}
      link={<FreshnessNote freshness={metric.freshness} />}
    />
  );
}

function DemoBanner({ metrics, channels }: { metrics: readonly MetricItem[]; channels: readonly ChannelSummary[] }) {
  const sources = [
    ...metrics.map((item) => item.source),
    ...channels.map((item) => item.observed.source),
  ].filter(Boolean);
  if (!shouldShowDemoBanner(sources, appDemoDataConfig)) return null;
  return (
    <p
      role="status"
      className="rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg"
    >
      当前展示的是演示数据（Fake 连接器），非真实运营数据；请在「连接与凭据」配置真实 NewAPI 实例。
    </p>
  );
}

function SubscriptionEvidence({
  metric,
  from,
  to,
}: {
  metric: MetricItem | undefined;
  from: string;
  to: string;
}) {
  const usable = periodMatchesMetric(metric, from, to);
  // NewAPI v1 cannot read subscription orders. The connector deliberately emits
  // zero with `is_partial=true` as a missing-value sentinel; never render that
  // zero as a real amount in the finance UI.
  const subscriptionUnavailable = Boolean(
    metric &&
      (metric.freshness.is_partial ||
        metric.freshness.state === "partial" ||
        metric.watermark.includes("subscription:unavailable_over_http")),
  );
  const usableMetric = metric && usable && !subscriptionUnavailable ? metric : undefined;
  const amount = usableMetric ? metricAmountText(usableMetric) : null;
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs">
      <span className="font-medium text-fg">当日订阅收入</span>
      {amount ? (
        <span className="flex flex-wrap items-center justify-end gap-2 tabular-nums">
          <strong className={amount.available ? "text-fg" : "text-fg-muted"}>{amount.text}</strong>
          <FreshnessBadge freshness={usableMetric!.freshness} />
          <span className="text-fg-muted">来源 {sourceText(usableMetric)}</span>
        </span>
      ) : (
        <span className="flex flex-wrap items-center justify-end gap-2 text-fg-muted">
          <span>—</span>
          <Badge tone="neutral">未接入</Badge>
          <span>
            {subscriptionUnavailable
              ? "NewAPI 上游没有订阅订单端点；金额未知，不显示 0"
              : "暂无可匹配的 NewAPI 日订阅指标；不会用 0 代替"}
          </span>
        </span>
      )}
    </div>
  );
}

interface LedgerFilter {
  id: string;
  label: string;
  options: readonly string[];
}

interface LedgerColumn<T> {
  id: string;
  label: string;
  numeric?: boolean;
  value: (row: T) => string;
  cell: (row: T) => ReactNode;
}

/**
 * 财务表的轻量交互壳。
 *
 * 筛选刻意先渲染，搜索放在最后；这是原型约定，也让窄屏下的操作顺序稳定。
 * 它不制造分页数据，订单端点尚未接入时仍保留完整表头与空态。
 */
function InteractiveLedgerTable<T>({
  title,
  tableLabel,
  columns,
  rows,
  filters,
  emptyTitle,
  emptyDescription,
}: {
  title: string;
  tableLabel: string;
  columns: readonly LedgerColumn<T>[];
  rows: readonly T[];
  filters: readonly LedgerFilter[];
  emptyTitle: string;
  emptyDescription: string;
}) {
  const [query, setQuery] = useState("");
  const [filterValues, setFilterValues] = useState<Record<string, string>>({});
  const normalizedQuery = query.trim().toLocaleLowerCase("zh-CN");
  const visibleRows = useMemo(
    () =>
      rows.filter((row) => {
        const matchesSearch =
          normalizedQuery === "" ||
          columns.some((column) => column.value(row).toLocaleLowerCase("zh-CN").includes(normalizedQuery));
        const matchesFilters = filters.every((filter) => {
          const selected = filterValues[filter.id] ?? "";
          return selected === "" || filterValuesForRow(filter.id, row, columns, selected);
        });
        return matchesSearch && matchesFilters;
      }),
    [columns, filterValues, filters, normalizedQuery, rows],
  );
  const hasCriteria = normalizedQuery !== "" || Object.values(filterValues).some(Boolean);

  return (
    <section className="overflow-hidden rounded-lg border border-edge bg-surface shadow-sm" aria-label={title}>
      <header className="border-b border-edge px-4 py-3">
        <h2 className="text-base font-semibold text-fg">{title}</h2>
        <p className="text-xs text-fg-muted">金额只展示已接入的来源；未知、缺失或未接入不会被折算成 0。</p>
      </header>
      <div
        role="toolbar"
        aria-label={`${title}筛选与搜索`}
        className="flex flex-wrap items-end gap-2 border-b border-edge bg-surface-muted px-3 py-2"
      >
        {filters.map((filter) => (
          <label key={filter.id} className="flex min-w-28 flex-col gap-1 text-xs text-fg-muted">
            <span>{filter.label}</span>
            <select
              aria-label={`${filter.label}筛选`}
              value={filterValues[filter.id] ?? ""}
              onChange={(event) =>
                setFilterValues((previous) => ({ ...previous, [filter.id]: event.target.value }))
              }
              className="min-h-9 rounded-md border border-edge-strong bg-surface px-2 py-1 text-xs text-fg outline-none focus-visible:outline-2 focus-visible:outline-accent"
            >
              <option value="">全部</option>
              {filter.options.map((option) => (
                <option key={option} value={option}>
                  {option}
                </option>
              ))}
            </select>
          </label>
        ))}
        <label className="ml-auto flex min-w-44 flex-col gap-1 text-xs text-fg-muted">
          <span>搜索</span>
          <input
            type="search"
            aria-label={`搜索${title}`}
            placeholder="搜索订单号 / 用户 / 来源"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            className="h-9 w-52 rounded-md border border-edge-strong bg-surface px-2 text-xs text-fg outline-none placeholder:text-fg-muted focus-visible:outline-2 focus-visible:outline-accent"
          />
        </label>
        {hasCriteria ? (
          <button
            type="button"
            onClick={() => {
              setQuery("");
              setFilterValues({});
            }}
            className="min-h-9 rounded-md border border-edge-strong px-2 py-1 text-xs text-fg-muted hover:bg-surface focus-visible:outline-2 focus-visible:outline-accent"
          >
            清除条件
          </button>
        ) : null}
      </div>
      <div className="overflow-x-auto">
        <table className="w-full min-w-[760px] text-left text-sm" aria-label={tableLabel}>
          <caption className="sr-only">{tableLabel}</caption>
          <thead className="bg-surface-muted text-xs text-fg-muted">
            <tr>
              {columns.map((column) => (
                <th key={column.id} scope="col" className={`whitespace-nowrap px-3 py-2 font-medium ${column.numeric ? "text-right" : ""}`}>
                  {column.label}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {visibleRows.length > 0 ? (
              visibleRows.map((row, index) => (
                <tr key={rowKeyFor(row, index)} className="border-t border-edge">
                  {columns.map((column) => (
                    <td key={column.id} className={`px-3 py-2 ${column.numeric ? "text-right tabular-nums" : ""}`}>
                      {column.cell(row)}
                    </td>
                  ))}
                </tr>
              ))
            ) : (
              <tr>
                <td colSpan={columns.length} className="p-3">
                  <PageState
                    kind={rows.length === 0 ? "unavailable" : "empty"}
                    compact
                    title={rows.length === 0 ? emptyTitle : "没有匹配的记录"}
                    description={rows.length === 0 ? emptyDescription : "请调整筛选或搜索条件后再试。"}
                  />
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      <p className="border-t border-edge px-3 py-2 text-xs text-fg-muted" aria-live="polite">
        {rows.length === 0 ? "暂无可展示记录；空态不代表金额为 0。" : `显示 ${visibleRows.length} / ${rows.length} 条`}
      </p>
    </section>
  );
}

function rowKeyFor<T>(row: T, index: number): string {
  if (typeof row === "object" && row !== null) {
    const candidate = (row as { id?: unknown }).id;
    if (typeof candidate === "string" && candidate) return candidate;
  }
  return String(index);
}

/** Filters are intentionally represented by visible column values. */
function filterValuesForRow<T>(
  id: string,
  row: T,
  columns: readonly LedgerColumn<T>[],
  selected: string,
): boolean {
  const column = columns.find((candidate) => candidate.id === id);
  return column ? column.value(row).toLocaleLowerCase("zh-CN").includes(selected.toLocaleLowerCase("zh-CN")) : true;
}

function hostOf(baseUrl: string): string {
  if (!baseUrl) return "来源未声明";
  try {
    return new URL(baseUrl).host || baseUrl;
  } catch {
    return baseUrl;
  }
}

function accessMethodText(method: string): string {
  switch (method) {
    case "upstream_key":
      return "上游 Key";
    case "subscription_account":
      return "订阅账号";
    case "official_api":
      return "官方 API";
    default:
      return method || "接入方式未知";
  }
}

function statusText(status: string): string {
  switch (status) {
    case "active":
      return "正常";
    case "disabled":
    case "retired":
      return "已停用";
    default:
      return status || "状态未知";
  }
}

function amountCell(value: ChannelSummary["usageRevenue"] | ChannelSummary["supplyCost"] | ChannelSummary["grossProfit"]) {
  if (!value) return <span className="text-fg-muted">—</span>;
  return <span>{formatScaled(value)}</span>;
}

function formatScaled(value: NonNullable<ChannelSummary["usageRevenue"]>): string {
  // 这里统一走 scale-aware formatter；财务台账金额是 scale-6 微单位，不能按
  // 普通货币最小单位直接除 100。
  return formatScaledMinorUnits(value.amountMinor, value.currency, value.scale);
}

const PROFIT_COLUMNS: readonly LedgerColumn<ChannelSummary>[] = [
  {
    id: "channel",
    label: "渠道",
    value: (row) => `${row.name} ${row.id}`,
    cell: (row) => (
      <span>
        <strong className="font-medium">{row.name || "未命名渠道"}</strong>
        <span className="block font-mono text-xs text-fg-muted">{row.id}</span>
      </span>
    ),
  },
  {
    id: "upstream",
    label: "上游",
    value: (row) => `${hostOf(row.baseUrl)} ${row.observed.source}`,
    cell: (row) => (
      <span>
        <span>{hostOf(row.baseUrl)}</span>
        <span className="block text-xs text-fg-muted">来源 {row.observed.source || "未声明"}</span>
      </span>
    ),
  },
  {
    id: "group",
    label: "分组 / 倍率",
    value: (row) => `${row.groupRate ?? ""} ${accessMethodText(row.accessMethod)}`,
    cell: (row) => (
      <span>
        <span className="font-medium">{row.groupRate ? `${row.groupRate}×` : "未配置倍率"}</span>
        <span className="block text-xs text-fg-muted">{accessMethodText(row.accessMethod)}</span>
      </span>
    ),
  },
  {
    id: "revenue",
    label: "我方计费",
    numeric: true,
    value: (row) => row.usageRevenue?.amountMinor ?? "",
    cell: (row) => amountCell(row.usageRevenue),
  },
  {
    id: "cost",
    label: "上游成本",
    numeric: true,
    value: (row) => row.supplyCost?.amountMinor ?? "",
    cell: (row) => amountCell(row.supplyCost),
  },
  {
    id: "profit",
    label: "毛利",
    numeric: true,
    value: (row) => row.grossProfit?.amountMinor ?? "",
    cell: (row) => (
      <span>
        {amountCell(row.grossProfit)}
        <span className="block text-xs text-fg-muted">{row.grossMargin ? formatMargin(row.grossMargin) : "毛利率未知"}</span>
      </span>
    ),
  },
  {
    id: "margin",
    label: "毛利率",
    numeric: true,
    value: (row) => row.grossMargin ?? "",
    cell: (row) => (row.grossMargin ? formatMargin(row.grossMargin) : <span className="text-fg-muted">—</span>),
  },
  {
    id: "status",
    label: "状态",
    value: (row) => statusText(row.status),
    cell: (row) => <Badge tone={row.status === "active" ? "success" : row.status === "retired" ? "neutral" : "warning"}>{statusText(row.status)}</Badge>,
  },
];

const ORDER_COLUMNS: readonly LedgerColumn<FinanceOrderRow>[] = [
  { id: "order", label: "订单号", value: (row) => row.order, cell: (row) => <span className="font-mono">{row.order}</span> },
  { id: "user", label: "用户", value: (row) => row.user, cell: (row) => row.user },
  { id: "amount", label: "金额", numeric: true, value: (row) => row.amount, cell: (row) => row.amount },
  { id: "method", label: "支付方式", value: (row) => row.method, cell: (row) => row.method },
  { id: "status", label: "状态", value: (row) => row.status, cell: (row) => row.status },
  { id: "time", label: "时间", value: (row) => row.time, cell: (row) => <span className="tabular-nums">{row.time}</span> },
];

interface FinanceOrderRow {
  order: string;
  user: string;
  amount: string;
  method: string;
  status: string;
  time: string;
}

function formatMargin(value: string): string {
  const match = /^(-?)(\d+)(?:\.(\d+))?$/.exec(value.trim());
  if (!match) return "毛利率未知";
  const [, sign, whole = "0", fraction = ""] = match;
  const digits = `${whole}${fraction}`;
  const point = whole.length + 2;
  const padded = digits.padEnd(Math.max(point + 2, digits.length), "0");
  const integer = padded.slice(0, point).replace(/^0+(?=\d)/, "");
  const decimal = padded.slice(point, point + 2).padEnd(2, "0");
  return `毛利率 ${sign}${integer}.${decimal}%`;
}

function RefreshErrorNotice({ label, error, onRetry }: { label: string; error: unknown; onRetry: () => void }) {
  const message = error instanceof Error ? error.message : "暂时无法读取最新数据";
  return (
    <div role="alert" className="flex flex-wrap items-center gap-2 rounded-md border border-danger bg-danger/10 px-3 py-2 text-xs text-danger">
      <span>{label}刷新失败：{message}；页面保留上一次成功数据。</span>
      <button type="button" onClick={onRetry} className="font-medium underline underline-offset-2 focus-visible:outline-2 focus-visible:outline-accent">
        重试
      </button>
    </div>
  );
}

interface FinancePeriodState {
  day: string;
  mode: FinancePeriodMode;
  range: { from: string; to: string };
  setDay: (value: string) => void;
  setMode: (value: FinancePeriodMode) => void;
}

/**
 * Keep the period shareable in the URL, matching the user-management pages.
 * An omitted day is interpreted as the current China business day for the
 * display/query boundary; it is not derived from the browser's local zone.
 */
function useFinancePeriod(initialDate: string): FinancePeriodState {
  const [searchParams, setSearchParams] = useSearchParams();
  const fallback = validDateOnly(initialDate) ? initialDate : businessTodayDateOnly();
  const parsed = parseBusinessDay(searchParams.get("day"));
  const day = parsed || fallback;
  const mode = parseGranularity(searchParams.get("granularity"));
  const range = useMemo(() => periodRangeFor(day, mode), [day, mode]);

  const setParam = (key: string, value: string) => {
    const next = new URLSearchParams(searchParams);
    if (value.trim() === "") next.delete(key);
    else next.set(key, value);
    setSearchParams(next, { replace: true });
  };

  return {
    day,
    mode,
    range,
    setDay: (value) => setParam("day", validDateOnly(value) ? value : ""),
    setMode: (value) => setParam("granularity", value),
  };
}

function businessTodayDateOnly(): string {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: "Asia/Shanghai",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(new Date());
  const values = Object.fromEntries(parts.map((part) => [part.type, part.value]));
  return `${values.year ?? "1970"}-${values.month ?? "01"}-${values.day ?? "01"}`;
}

function OrdersView({ initialDate }: { initialDate: string }) {
  const { day, mode, range, setDay, setMode } = useFinancePeriod(initialDate);
  const metricsQuery = useQuery({
    queryKey: ["metrics"],
    queryFn: ({ signal }) => listMetrics({ signal }),
  });
  const metrics = (metricsQuery.data ?? []).filter((item) => item.metric_key.startsWith("newapi."));
  const recharge = metricByKey(metrics, NEWAPI_RECHARGE_METRIC);
  const subscription = metricByKey(metrics, NEWAPI_SUBSCRIPTION_METRIC);
  const demo = shouldShowDemoBanner(metrics.map((item) => item.source), appDemoDataConfig);

  return (
    <div className="flex flex-col gap-4">
      <p className="text-xs text-fg-muted">NewAPI 用户资金流与订单入口。充值、订阅收入、退款和支付失败分开呈现，避免把充值误当作使用收入。</p>
      <PeriodControls
        day={day}
        granularity={mode}
        period={{ day, granularity: mode, ...range }}
        dateLabel="统计日期"
        dateAriaLabel="统计日期"
        granularityAriaLabel="统计模式"
        onDayChange={setDay}
        onGranularityChange={setMode}
      />
      {demo ? <DemoBanner metrics={metrics} channels={[]} /> : null}
      {!recharge && !subscription ? (
        <p role="status" className="rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted">
          <span>暂无本平台的资金类指标</span>
          <span>；NewAPI 日充值 / 日订阅指标采集后才会在对应日期显示。</span>
        </p>
      ) : null}
      <ApiStateView
        isPending={metricsQuery.isPending}
        error={metricsQuery.error && !metricsQuery.isRefetchError ? metricsQuery.error : null}
        onRetry={() => void metricsQuery.refetch()}
      >
        {metricsQuery.error && metricsQuery.isRefetchError ? (
          <RefreshErrorNotice label="NewAPI 财务指标" error={metricsQuery.error} onRetry={() => void metricsQuery.refetch()} />
        ) : null}
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <MetricAmountTile
            label="区间到账"
            metric={recharge}
            from={range.from}
            to={range.to}
            missingNote="暂无可匹配的 newapi.recharge.daily；周/月需要订单聚合端点，不能用单日值代替。"
            detail="充值是资金流入，不等同于当期使用收入"
          />
          <MissingTile label="区间退款" note="退款逐笔端点尚未接入；不会用 0 代替未知的退款金额。" />
          <MissingTile label="月累计" note="月度订单聚合尚未接入；请选择单日并等待 NewAPI 日充值指标。" />
          <MissingTile label="支付失败" note="支付 Connector 尚未接入失败订单计数；不会以空列表推断为 0。" />
        </div>
        <SubscriptionEvidence metric={subscription} from={range.from} to={range.to} />
        <InteractiveLedgerTable<FinanceOrderRow>
          title="区间订单"
          tableLabel="NewAPI 支付订单"
          columns={ORDER_COLUMNS}
          rows={[]}
          filters={[
            { id: "status", label: "状态", options: ["已到账", "待处理", "失败", "已退款"] },
            { id: "method", label: "支付方式", options: ["Stripe", "支付宝", "微信支付"] },
            { id: "time", label: "时间", options: ["今天", "本周", "本月"] },
          ]}
          emptyTitle="充值订单尚未接入"
          emptyDescription="支付 Connector（M3）尚未提供 NewAPI 逐笔订单；表头和筛选位置已保留，接入后才会出现真实订单。"
        />
      </ApiStateView>
    </div>
  );
}

function ProfitView({ initialDate }: { initialDate: string }) {
  const { day, mode, range, setDay, setMode } = useFinancePeriod(initialDate);
  const summaryQuery = useQuery({
    queryKey: ["finance", "channels", "summary", range.from, range.to],
    queryFn: ({ signal }) => listChannelSummaries({ ...range, signal }),
  });
  const rows = (summaryQuery.data?.items ?? []).filter((item) => item.systemType === "newapi");
  const upstreamOptions = [...new Set(rows.map((row) => hostOf(row.baseUrl)).filter(Boolean))].sort();
  const accessOptions = [...new Set(rows.map((row) => accessMethodText(row.accessMethod)).filter(Boolean))].sort();

  return (
    <div className="flex flex-col gap-4">
      <p className="text-xs text-fg-muted">按 NewAPI 渠道逐行核算我方计费、上游成本、毛利与毛利率；同一上游下的多个渠道不会在这里合并。</p>
      <PeriodControls
        day={day}
        granularity={mode}
        period={{ day, granularity: mode, ...range }}
        dateLabel="统计日期"
        dateAriaLabel="统计日期"
        granularityAriaLabel="统计模式"
        onDayChange={setDay}
        onGranularityChange={setMode}
      />
      <ApiStateView
        isPending={summaryQuery.isPending}
        error={summaryQuery.error && !summaryQuery.isRefetchError ? summaryQuery.error : null}
        onRetry={() => void summaryQuery.refetch()}
      >
        {summaryQuery.error && summaryQuery.isRefetchError ? (
          <RefreshErrorNotice label="NewAPI 渠道利润" error={summaryQuery.error} onRetry={() => void summaryQuery.refetch()} />
        ) : null}
        <DemoBanner metrics={[]} channels={rows} />
        <InteractiveLedgerTable<ChannelSummary>
          title="利润核算明细"
          tableLabel="NewAPI 渠道利润核算"
          columns={PROFIT_COLUMNS}
          rows={rows}
          filters={[
            { id: "upstream", label: "上游", options: upstreamOptions },
            { id: "group", label: "接入方式", options: accessOptions },
            { id: "status", label: "状态", options: ["正常", "需关注", "已停用"] },
          ]}
          emptyTitle="暂无 NewAPI 利润明细"
          emptyDescription="finance/channels/summary 已读取，但当前统计区间没有可归属的 NewAPI 渠道；这不等于利润为 0。"
        />
      </ApiStateView>
    </div>
  );
}

/** NewAPI「支付与财务」的两个原型子页签。 */
export function NewApiFinanceOverview({
  subId = "orders",
  initialDate,
}: {
  subId?: NewApiFinanceSubId;
  initialDate?: string;
}) {
  const requested = initialDate ?? businessTodayDateOnly();
  const date = validDateOnly(requested) ? requested : businessTodayDateOnly();
  return subId === "profit" ? <ProfitView initialDate={date} /> : <OrdersView initialDate={date} />;
}
