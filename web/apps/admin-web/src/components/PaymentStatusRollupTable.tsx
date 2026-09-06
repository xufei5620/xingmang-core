import { DataTableV2, PageState, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";

import type { PlatformOrderStat } from "../api/finance";
import { formatMinorUnits } from "../lib/money";
import { rollupPaymentStatuses, type PaymentBucketRollup } from "../lib/paymentStatusRollup";

/** 「本区间汇总」——按归一化分桶把 `stats_by_status` 摊开（XM-PAY-STATUS-ROLLUP）。
 *
 *  它回答的是订单表回答不了的问题：**整个查询区间**里，成功了多少笔、多少钱。
 *  下面那张表只有已翻到的几页，翻到一半自己加出来的数字是错的，而且看不出错。
 *
 *  原型把这一块画成「最近事件表」（成功充值 / 待处理 / 失败 / 退款 四行）。这里
 *  用同一组归一化分桶，并把落进每个桶的上游原始状态一并列出——归一化是为了好读，
 *  不是为了掩盖上游实际说了什么。 */
export function PaymentStatusRollupTable({
  stats,
  from,
  to,
}: {
  stats: Readonly<Record<string, PlatformOrderStat>>;
  from: string;
  to: string;
}) {
  const rollup = rollupPaymentStatuses(stats);

  return (
    <section className="flex flex-col gap-2">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="text-sm font-semibold text-fg">本区间汇总</h3>
        <p className="text-xs text-fg-muted">
          覆盖 {from} 至 {to} 的**全部** {rollup.totalCount} 笔订单，不受下方表格翻到第几页影响。
        </p>
      </div>
      {rollup.unknownStatuses.length > 0 ? (
        <p
          role="status"
          className="rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg"
        >
          上游出现了尚未归类的状态：
          <span className="mx-1 font-mono">{rollup.unknownStatuses.join("、")}</span>
          。它们计入「未知」行，不会被悄悄丢掉；归类规则要更新时改
          <code className="mx-1 font-mono">bucketLabel</code>。
        </p>
      ) : null}
      <DataTableV2
        caption={`充值订单按状态分桶的笔数与金额，覆盖 ${from} 至 ${to} 的全部订单`}
        columns={ROLLUP_COLUMNS}
        rows={rollup.buckets}
        rowKey={(row) => row.bucket}
        emptyState={
          <PageState
            kind="empty"
            title="这个区间没有订单"
            description="所选统计区间内上游没有返回任何订单；换一个日期或粒度再看。"
            compact
          />
        }
      />
    </section>
  );
}

const ROLLUP_COLUMNS: DataTableColumn<PaymentBucketRollup>[] = [
  {
    id: "bucket",
    header: "状态",
    primary: true,
    value: (row) => row.bucket,
    cell: (row) => (
      <Badge tone={row.bucket === "未知" ? "warning" : "neutral"}>{row.bucket}</Badge>
    ),
  },
  {
    id: "count",
    header: "笔数",
    numeric: true,
    value: (row) => row.count,
    cell: (row) => <span className="tabular-nums">{row.count}</span>,
  },
  {
    id: "amount",
    header: "金额",
    numeric: true,
    // 金额缺席时显示「—」而不是 0：一个桶里没有一条给出金额，与这个桶合计
    // 为零，是两件完全不同的事（宪法 12 条）。
    headerTitle: "该桶内所有订单的金额之和；桶内没有任何一条给出金额时显示「—」，不显示 0",
    value: (row) => row.minorUnits,
    cell: (row) => (
      <span className={row.minorUnits === null ? "text-fg-muted" : "tabular-nums"}>
        {row.minorUnits === null ? "—" : formatMinorUnits(row.minorUnits, row.currency || "CNY")}
      </span>
    ),
  },
  {
    id: "raw",
    header: "上游原始状态",
    headerTitle: "归一化前上游实际返回的状态字面量；归类只为好读，不掩盖上游说了什么",
    value: (row) => row.rawStatuses.join(" "),
    cell: (row) => (
      <span className="font-mono text-xs break-all text-fg-muted">
        {row.rawStatuses.join("、") || "—"}
      </span>
    ),
  },
];
