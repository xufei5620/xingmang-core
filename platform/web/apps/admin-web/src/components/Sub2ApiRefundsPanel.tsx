import {
  DataTableV2,
  FreshnessBadge,
  FreshnessNote,
  PageState,
  PeriodControls,
  formatUtcTimestamp,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { useSearchParams } from "react-router";

import { describePaymentStatus, REFUND_LIFECYCLE_STATUSES, type PlatformOrderItem } from "../api/finance";
import { toIntegerValue } from "../lib/money";
import { periodRangeFor, type FinancePeriodMode } from "../lib/financeOverview";
import { parseBusinessDay, parseGranularity } from "../lib/period";
import { ApiStateView } from "./ApiStateView";
import { amountBodyText, detailColumn } from "./platformOrdersColumns";
import { businessTodayDateOnly, useSub2ApiOrdersQuery } from "./Sub2ApiOrdersPanel";

const COLUMN_ORDER: DataTableColumn<PlatformOrderItem> = {
  id: "order",
  header: "订单",
  headerTitle: "上游没有独立的退款流水号，这里定位到的是发生退款的原始订单本身",
  primary: true,
  value: (o) => o.order_id,
  cell: (o) => (
    <>
      <span className="font-mono">{o.order_id}</span>
      <p className="font-mono text-xs text-fg-muted">{o.upstream_order_ref || "—"}</p>
    </>
  ),
};

const COLUMN_USER: DataTableColumn<PlatformOrderItem> = {
  id: "user",
  header: "用户",
  value: (o) => o.user_ref,
  cell: (o) => <span className="font-mono text-xs">{o.user_ref || "—"}</span>,
};

const COLUMN_ORIGINAL_AMOUNT: DataTableColumn<PlatformOrderItem> = {
  id: "original",
  header: "原金额",
  headerTitle: "订单面值（amount），不是实付金额（pay_amount）",
  numeric: true,
  value: (o) => toIntegerValue(o.amount.minor_units),
  cell: (o) => <span>{amountBodyText(o.amount)}</span>,
};

const COLUMN_REFUND_AMOUNT: DataTableColumn<PlatformOrderItem> = {
  id: "refund",
  header: "退款金额",
  headerTitle: "上游 refund_amount 字段——部分退款时小于原金额，与原金额相等的才是全额退款",
  numeric: true,
  value: (o) => toIntegerValue(o.refund_amount.minor_units),
  cell: (o) => {
    const missing = o.refund_amount.minor_units === null;
    return <span className={missing ? "text-fg-muted" : "font-medium"}>{amountBodyText(o.refund_amount)}</span>;
  },
};

const COLUMN_METHOD: DataTableColumn<PlatformOrderItem> = {
  id: "method",
  header: "支付方式",
  value: (o) => o.method,
  cell: (o) => o.method || "—",
};

const COLUMN_STATUS: DataTableColumn<PlatformOrderItem> = {
  id: "status",
  header: "状态",
  value: (o) => describePaymentStatus(o.status).label,
  cell: (o) => {
    const shown = describePaymentStatus(o.status);
    return <Badge tone={shown.tone} title={o.status}>{shown.label}</Badge>;
  },
};

const COLUMN_CREATED: DataTableColumn<PlatformOrderItem> = {
  id: "created",
  header: "创建时间",
  headerTitle: "订单创建时刻——上游没有单独的「退款申请时间」字段",
  value: (o) => o.created_at,
  cell: (o) => <span className="text-xs tabular-nums">{formatUtcTimestamp(o.created_at)}</span>,
};

/** 「退款与冲正」页签（原型 `V["s2/finance"]` sub=`refunds`）。
 *
 *  没有独立的退款查询端点——payments.read.v1 只有 ListOrders，这一页
 *  从同一批订单里挑出落在退款生命周期状态的那些（REFUND_REQUESTED…
 *  REFUND_FAILED，与 DailyPaymentSummary 的 refunded 桶同一份判据），
 *  与"充值订单"页签共用同一个 useInfiniteQuery（见该 Hook 的注释）。
 *
 *  只有 Sub2API 有这一页——NewAPI 没有退款概念，不应该被路由到这里
 *  （由 PlatformFinancePanel 的 sub2apiFinanceSubTab 保证）。 */
export function Sub2ApiRefundsPanel() {
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
  const allItems = pages.flatMap((p) => p.items);
  const refundItems = allItems.filter((o) => REFUND_LIFECYCLE_STATUSES.has(o.status.toUpperCase().trim()));
  const methodOptions = [...new Set(refundItems.map((o) => o.method).filter(Boolean))].sort();

  return (
    <div className="flex flex-col gap-4">
      <p className="text-xs text-fg-muted">
        从充值订单台账里挑出处于退款生命周期的记录；退款是写操作，本页只读展示，这一格上线之后也不会有「直接退款」的按钮（退款写路径尚未设计）。
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
                来源 {lastPage.data_source || "—"} · 窗口 {lastPage.from} 至 {lastPage.to} · 已加载 {allItems.length}{" "}
                笔订单，其中 {refundItems.length} 笔处于退款生命周期
              </span>
              <FreshnessBadge freshness={lastPage.freshness} />
            </div>
            <FreshnessNote freshness={lastPage.freshness} />
            <DataTableV2
              caption="Sub2API 退款与冲正：原金额、退款金额与状态"
              columns={[
                COLUMN_ORDER,
                COLUMN_USER,
                COLUMN_ORIGINAL_AMOUNT,
                COLUMN_REFUND_AMOUNT,
                COLUMN_METHOD,
                COLUMN_STATUS,
                COLUMN_CREATED,
                detailColumn("sub2api", "refunds"),
              ]}
              rows={refundItems}
              rowKey={(o) => o.order_id}
              searchable
              filters={[{ columnId: "method", label: "支付方式", options: methodOptions }]}
              emptyState={
                <PageState
                  kind="empty"
                  title="这个窗口没有退款记录"
                  description="已读取 payments.read.v1 的充值订单台账，当前统计区间内没有处于退款生命周期的订单；这不等于退款金额为 0。"
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
