import { useInfiniteQuery } from "@tanstack/react-query";
import { DataTableV2, FreshnessBadge, FreshnessNote, PageState, PeriodControls } from "@xingmang/ui-admin";
import { useSearchParams } from "react-router";

import { listPlatformOrders } from "../api/finance";
import { periodRangeFor, type FinancePeriodMode } from "../lib/financeOverview";
import { parseBusinessDay, parseGranularity } from "../lib/period";
import { ApiStateView } from "./ApiStateView";
import { PaymentStatusRollupTable } from "./PaymentStatusRollupTable";
import { orderTableColumns, STATUS_BUCKET_OPTIONS } from "./platformOrdersColumns";

const PAGE_LIMIT = 50;

export function businessTodayDateOnly(): string {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: "Asia/Shanghai",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(new Date());
  const values = Object.fromEntries(parts.map((part) => [part.type, part.value]));
  return `${values.year ?? "1970"}-${values.month ?? "01"}-${values.day ?? "01"}`;
}

/** Sub2API 逐笔订单的翻页查询，"充值订单"与"退款与冲正"两个页签共用——
 *  两页看的是同一批底层订单（后者只是按归一化分桶客户端筛出一个子集），
 *  同一个 queryKey 让切换页签时不必重新请求同一天的数据。 */
export function useSub2ApiOrdersQuery(range: { from: string; to: string }) {
  return useInfiniteQuery({
    queryKey: ["platform-orders", "sub2api", range.from, range.to],
    queryFn: ({ pageParam, signal }) =>
      listPlatformOrders("sub2api", {
        from: range.from,
        to: range.to,
        limit: PAGE_LIMIT,
        ...(pageParam ? { cursor: pageParam } : {}),
        signal,
      }),
    initialPageParam: "",
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
  });
}

/** 「充值订单」页签（原型 `V["s2/finance"]` sub=`orders`）。
 *
 *  资金概览卡不在这一页——那是"overview"子页签自己的东西（XM-PAY1 §1）；
 *  这一页只有筛选 + 逐笔台账，与原型逐字对齐。 */
export function Sub2ApiOrdersPanel() {
  const [searchParams, setSearchParams] = useSearchParams();
  const fallback = businessTodayDateOnly();
  const day = parseBusinessDay(searchParams.get("day")) || fallback;
  const mode = parseGranularity(searchParams.get("granularity")) as FinancePeriodMode;
  const range = periodRangeFor(day, mode);

  const setParam = (key: string, value: string) => {
    const next = new URLSearchParams(searchParams);
    if (value.trim() === "") next.delete(key);
    else next.set(key, value);
    setSearchParams(next, { replace: true });
  };

  const query = useSub2ApiOrdersQuery(range);

  const pages = query.data?.pages ?? [];
  const lastPage = pages.length > 0 ? pages[pages.length - 1] : undefined;
  const items = pages.flatMap((p) => p.items);
  const methodOptions = [...new Set(items.map((o) => o.method).filter(Boolean))].sort();

  return (
    <div className="flex flex-col gap-4">
      <p className="text-xs text-fg-muted">
        逐笔充值订单台账，来自 payments.read.v1 的 <code>ListOrders</code>；金额只展示已接入的字段，未知、缺失或未接入不会被折算成 0。
      </p>
      <PeriodControls
        day={day}
        granularity={mode}
        period={{ day, granularity: mode, ...range }}
        dateLabel="统计日期"
        dateAriaLabel="统计日期"
        granularityAriaLabel="统计模式"
        onDayChange={(value) => setParam("day", value)}
        onGranularityChange={(value) => setParam("granularity", value)}
      />
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
            {/* 区间汇总放在逐笔表**之前**：先看整体，再决定要不要翻明细。
                它来自 stats_by_status，覆盖整个查询区间，不随翻页变化。 */}
            <PaymentStatusRollupTable
              stats={lastPage.stats_by_status}
              from={lastPage.from}
              to={lastPage.to}
            />
            <DataTableV2
              caption="Sub2API 充值订单：金额、手续费、状态与创建时间"
              columns={orderTableColumns("sub2api")}
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
    </div>
  );
}
