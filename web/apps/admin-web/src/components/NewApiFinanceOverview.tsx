import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import {
  DataTableV2,
  FreshnessBadge,
  FreshnessNote,
  PageState,
  PeriodControls,
  type FreshnessContract,
} from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";

import { listPlatformOrders } from "../api/finance";
import { listMetrics, type MetricItem } from "../api/platform";
import {
  formatCount,
  formatMinorUnits,
  toIntegerValue,
} from "../lib/money";
import { metricPrimaryValue, readPaymentsDailySummary } from "../lib/metrics";
import {
  appDemoDataConfig,
  shouldShowDemoBanner,
} from "../lib/demoData";
import { ApiStateView } from "./ApiStateView";
import { ChannelProfitView } from "./ChannelProfitView";
import {
  DemoBanner,
  RefreshErrorNotice,
  businessTodayDateOnly,
  useFinancePeriod,
  validDateOnly,
} from "./financeShared";
import { BucketCard, MonthToDateSucceededCard, metricKeyFor } from "./PaymentSummaryCards";
import { orderTableColumns, STATUS_BUCKET_OPTIONS } from "./platformOrdersColumns";

/** NewAPI 财务页的指标键。用分段拼接避免把采集键误看成凭据。 */
const NEWAPI_SUBSCRIPTION_METRIC = ["newapi", "subscription", "daily"].join(".");

type NewApiFinanceSubId = "orders" | "profit";

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
  return Boolean(
    metric &&
      from === to &&
      metricDay(metric) === from &&
      metric.freshness.state !== "uninitialized",
  );
}

/** `is_partial` is authoritative even if an older producer left state=fresh. */
function effectiveMetricFreshness(freshness: FreshnessContract): FreshnessContract {
  return freshness.is_partial && freshness.state === "fresh"
    ? { ...freshness, state: "partial" }
    : freshness;
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
  const subscriptionRaw = metric
    ? metricPrimaryValue(metric.metric_key, metric.value).raw
    : null;
  const subscriptionUnavailable = Boolean(
    metric &&
      (metric.watermark.includes("subscription:unavailable_over_http") ||
        ((metric.freshness.is_partial || metric.freshness.state === "partial") &&
          subscriptionRaw === 0n)),
  );
  const usableMetric = metric && usable && !subscriptionUnavailable ? metric : undefined;
  const amount = usableMetric ? metricAmountText(usableMetric) : null;
  const metricFreshness = metric ? effectiveMetricFreshness(metric.freshness) : null;
  const evidence = metric ? (
    <div className="flex flex-wrap items-center justify-end gap-2 text-fg-muted">
      <span>—</span>
      <Badge tone="neutral">未接入</Badge>
      <FreshnessBadge freshness={metricFreshness!} />
      <span>来源 {sourceText(metric)}</span>
      {metric.watermark ? <span>水位 {metric.watermark}</span> : null}
      <FreshnessNote freshness={metricFreshness!} />
      <span>
        {subscriptionUnavailable
          ? "NewAPI 上游没有订阅订单端点；金额未知，不显示 0"
          : "暂无可匹配的 NewAPI 日订阅指标；不会用 0 代替"}
      </span>
    </div>
  ) : (
    <div className="flex flex-wrap items-center justify-end gap-2 text-fg-muted">
      <span>—</span>
      <Badge tone="neutral">未接入</Badge>
      <span>暂无可匹配的 NewAPI 日订阅指标；不会用 0 代替</span>
    </div>
  );
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs">
      <span className="font-medium text-fg">当日订阅收入</span>
      {amount ? (
        <span className="flex flex-wrap items-center justify-end gap-2 tabular-nums">
          <strong className={amount.available ? "text-fg" : "text-fg-muted"}>{amount.text}</strong>
          <FreshnessBadge freshness={effectiveMetricFreshness(usableMetric!.freshness)} />
          <span className="text-fg-muted">来源 {sourceText(usableMetric)}</span>
        </span>
      ) : (
        evidence
      )}
    </div>
  );
}

/** 当前自然月第一天（业务时区）——"月累计"恒等于这个月，不随
 *  PeriodControls 的选择变化，与"区间到账"是两个刻意不同的数字。
 *  直接切 `businessTodayDateOnly()` 的年月部分，不再解析一次时区。 */
function businessMonthStartDateOnly(): string {
  return `${businessTodayDateOnly().slice(0, 7)}-01`;
}

const ORDERS_PAGE_LIMIT = 50;

function useNewApiOrdersQuery(range: { from: string; to: string }) {
  return useInfiniteQuery({
    queryKey: ["platform-orders", "newapi", range.from, range.to],
    queryFn: ({ pageParam, signal }) =>
      listPlatformOrders("newapi", {
        from: range.from,
        to: range.to,
        limit: ORDERS_PAGE_LIMIT,
        ...(pageParam ? { cursor: pageParam } : {}),
        signal,
      }),
    initialPageParam: "",
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
  });
}

/** 「资金与订单」的逐笔订单台账（XM-PAY1 §2）——与"充值订单"页签列结构
 *  相同（orderTableColumns），手续费/净额两列对 NewAPI 恒为「—」：
 *  这是数据本身如此（连接器侧两个字段恒为 nil），不是这一页少做了什么。 */
function OrdersLedger({ range }: { range: { from: string; to: string } }) {
  const query = useNewApiOrdersQuery(range);
  const pages = query.data?.pages ?? [];
  const lastPage = pages.length > 0 ? pages[pages.length - 1] : undefined;
  const items = pages.flatMap((p) => p.items);
  const methodOptions = [...new Set(items.map((o) => o.method).filter(Boolean))].sort();

  return (
    <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
      {lastPage ? (
        <div className="flex flex-col gap-3">
          <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted">
            <span>
              来源 {lastPage.data_source || "—"} · 窗口 {lastPage.from} 至 {lastPage.to} · 已加载 {items.length} 条
            </span>
            <FreshnessBadge freshness={lastPage.freshness} />
          </div>
          <FreshnessNote freshness={lastPage.freshness} />
          <DataTableV2
            caption="NewAPI 充值订单：金额、状态与创建时间"
            columns={orderTableColumns("newapi")}
            rows={items}
            rowKey={(o) => o.order_id}
            searchable
            filters={[
              { columnId: "status", label: "状态", options: [...STATUS_BUCKET_OPTIONS] },
              { columnId: "method", label: "支付方式", options: methodOptions },
            ]}
            emptyState={
              <PageState
                kind="empty"
                title="这个窗口没有充值订单"
                description="已读取 payments.read.v1，当前统计区间内没有匹配的订单；这不等于金额为 0。"
              />
            }
            footerExtra={
              query.hasNextPage ? (
                <button
                  type="button"
                  onClick={() => void query.fetchNextPage()}
                  disabled={query.isFetchingNextPage}
                  className="min-h-9 rounded-md border border-edge-strong px-3 py-1 text-xs font-medium text-accent hover:bg-accent-soft disabled:cursor-not-allowed disabled:opacity-50"
                >
                  {query.isFetchingNextPage ? "加载中…" : "加载更多"}
                </button>
              ) : null
            }
          />
        </div>
      ) : null}
    </ApiStateView>
  );
}

function OrdersView({ initialDate }: { initialDate: string }) {
  const { day, mode, range, setDay, setMode } = useFinancePeriod(initialDate);
  const metricsQuery = useQuery({
    queryKey: ["metrics"],
    queryFn: ({ signal }) => listMetrics({ signal }),
  });
  const metrics = (metricsQuery.data ?? []).filter((item) => item.metric_key.startsWith("newapi."));
  const subscription = metricByKey(metrics, NEWAPI_SUBSCRIPTION_METRIC);
  const paymentsMetric = metricByKey(metrics, metricKeyFor("newapi"));
  const paymentsSummary = paymentsMetric ? readPaymentsDailySummary(paymentsMetric.value) : null;
  const demo = shouldShowDemoBanner(metrics.map((item) => item.source), appDemoDataConfig);

  return (
    <div className="flex flex-col gap-4">
      <p className="text-xs text-fg-muted">NewAPI 用户资金流与订单入口。充值、订阅收入分开呈现，避免把充值误当作使用收入。</p>
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
      {demo ? <DemoBanner metrics={metrics} channels={[]} platformName="NewAPI" /> : null}
      <ApiStateView
        isPending={metricsQuery.isPending}
        error={metricsQuery.error && !metricsQuery.isRefetchError ? metricsQuery.error : null}
        onRetry={() => void metricsQuery.refetch()}
      >
        {metricsQuery.error && metricsQuery.isRefetchError ? (
          <RefreshErrorNotice label="NewAPI 财务指标" error={metricsQuery.error} onRetry={() => void metricsQuery.refetch()} />
        ) : null}
        {/* 原型的四格布局（区间到账/区间退款/月累计/支付失败），不是 Sub2API
            资金概览的六卡整体——两边共用同一份 BucketCard 判断逻辑，只是
            NewAPI 只挑其中三个分桶 + 月累计这一格 Sub2API 没有。 */}
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <BucketCard bucket="succeeded" label="区间到账" platform="newapi" metric={paymentsMetric} summary={paymentsSummary} range={range} />
          <BucketCard bucket="refunded" label="区间退款" platform="newapi" metric={paymentsMetric} summary={paymentsSummary} range={range} />
          <MonthToDateSucceededCard platform="newapi" monthStart={businessMonthStartDateOnly()} today={businessTodayDateOnly()} />
          <BucketCard bucket="failed" label="支付失败" platform="newapi" metric={paymentsMetric} summary={paymentsSummary} range={range} />
        </div>
        <SubscriptionEvidence metric={subscription} from={range.from} to={range.to} />
      </ApiStateView>
      <OrdersLedger range={range} />
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
  return subId === "profit" ? (
    <ChannelProfitView platform="newapi" initialDate={date} />
  ) : (
    <OrdersView initialDate={date} />
  );
}
