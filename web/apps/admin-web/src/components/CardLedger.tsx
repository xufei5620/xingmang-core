import { useQuery } from "@tanstack/react-query";
import { DataTableV2, formatUtcTimestamp, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { listAllCardTransactions, type CardTransactionItem } from "../api/cards";
import { transactionStatusLabel, transactionTypeLabel } from "../lib/cardStatus";
import { formatMinorUnits } from "../lib/money";
import { ApiStateView } from "./ApiStateView";

const LEDGER_QUERY = "card-ledger";

/** 交易记录：跨全部卡的流水（XM-CARD9）。
 *
 *  对标 Infini 后台的「交易记录」页签，**列名逐字照抄**：时间 / 商户 /
 *  交易类型 / 结算金额 / 交易金额 / 卡片名称 / 手续费 / 状态。产品负责人
 *  每天在两个界面之间切换，同一个东西两个名字是纯粹的认知税。
 *
 *  此前平台只有「按卡查流水」，没有跨卡视图——而「这批卡刚刚发生了什么」
 *  才是每天真正会问的问题。 */
export function CardLedger() {
  const query = useQuery({
    queryKey: [LEDGER_QUERY],
    queryFn: ({ signal }) => listAllCardTransactions({ signal }),
    // 流水由同步作业与回调推进，页面自己不必轮询太勤。
    staleTime: 30_000,
  });

  const rows = query.data ?? [];

  const columns: DataTableColumn<CardTransactionItem>[] = [
    {
      id: "occurred",
      header: "时间",
      cell: (row) => (row.occurred_at ? formatUtcTimestamp(row.occurred_at) : "—"),
      value: (row) => row.occurred_at ?? "",
      primary: true,
    },
    { id: "merchant", header: "商户", cell: (row) => row.merchant || "—" },
    {
      id: "type",
      header: "交易类型",
      cell: (row) => transactionTypeLabel(row.type),
      value: (row) => transactionTypeLabel(row.type),
    },
    {
      id: "settled",
      header: "结算金额",
      // 结算金额是**从卡里扣掉的那个数**（我们的 amount_minor），
      // 与下面「交易金额」（商户侧原始币种）不是一回事。Infini 把两列并排
      // 显示，因为跨境消费时它们不同，而差额就是汇率加价。
      cell: (row) => (
        <span className={`tabular-nums ${row.amount_minor < 0 ? "text-danger" : ""}`}>
          {formatMinorUnits(row.amount_minor, row.currency)}
        </span>
      ),
      value: (row) => row.amount_minor,
    },
    {
      id: "tx_amount",
      header: "交易金额",
      cell: (row) =>
        row.transaction_amount
          ? `${row.transaction_amount} ${row.transaction_currency ?? ""}`.trim()
          : "—",
      value: (row) => row.transaction_amount ?? "",
    },
    {
      id: "card",
      header: "卡片名称",
      // 这张表的定位列。卡已关停、投影行没了时名称为空——**流水本身必须还在**
      // （钱确实花了），退回卡号后四位而不是显示一个裸的内部 id。
      cell: (row) => (
        <span className="truncate">
          {row.card_alias || (row.card_id ? `••${row.card_id.slice(-4)}` : "—")}
        </span>
      ),
      value: (row) => row.card_alias || row.card_id || "",
    },
    {
      id: "fee",
      header: "手续费",
      cell: (row) => (
        <span className="tabular-nums">{formatMinorUnits(row.fee_minor, row.currency)}</span>
      ),
      value: (row) => row.fee_minor,
    },
    {
      id: "status",
      header: "状态",
      cell: (row) => <Badge tone={statusTone(row.status)}>{transactionStatusLabel(row.status)}</Badge>,
      value: (row) => transactionStatusLabel(row.status),
    },
  ];

  return (
    <section className="flex min-w-0 flex-col gap-3">
      <LedgerSummary rows={rows} />
      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
        {/* 八列表：窄屏上让**表自己**横滚，页面本身不横滚——
            页面横滚会把左侧导航也带跑。 */}
        <div className="min-w-0 overflow-x-auto">
        <DataTableV2
          caption="全部卡片的交易流水：时间、商户、类型、金额、卡片与状态"
          searchable
          filters={[
            {
              columnId: "card",
              label: "卡片",
              options: Array.from(new Set(rows.map((r) => r.card_alias || r.card_id || ""))).filter(Boolean),
            },
            {
              columnId: "type",
              label: "类型",
              // 取值来自实际数据而不是写死的枚举：上游加新类型时不该被漏掉。
              options: Array.from(new Set(rows.map((r) => transactionTypeLabel(r.type)))).filter(Boolean),
            },
            {
              columnId: "status",
              label: "状态",
              options: Array.from(new Set(rows.map((r) => transactionStatusLabel(r.status)))).filter(Boolean),
            },
          ]}
          rows={rows}
          columns={columns}
          // 上游不给交易 id，所以键由「时间 + 金额 + 商户 + 卡」拼出来——
          // 与后端派生去重键同一条思路。
          rowKey={(row) =>
            `${row.account}/${row.card_id}/${row.occurred_at ?? ""}/${row.amount_minor}/${row.merchant}`
          }
          emptyState={
            <p className="text-fg-muted text-sm">
              还没有流水。同步作业每 5 分钟拉一轮，回调到达时也会即时推进对应的卡。
            </p>
          }
        />
        </div>
      </ApiStateView>
    </section>
  );
}

/** 汇总磁贴。
 *
 *  只做 Infini 那四个里**我们算得出来的两个**：本月消费与手续费合计。
 *  「本月预估返现」「累计返现」不做——上游没有返现的读法，编一个数出来
 *  会被当成钱。 */
function LedgerSummary({ rows }: { rows: CardTransactionItem[] }) {
  const now = new Date();
  const prefix = `${now.getUTCFullYear()}-${String(now.getUTCMonth() + 1).padStart(2, "0")}`;
  const thisMonth = rows.filter((r) => (r.occurred_at ?? "").startsWith(prefix));

  // 消费是负数，取绝对值显示成「花了多少」。只统计**已完成**的：
  // 授权中的金额还会变，把它算进「本月消费」会让这个数随后自己缩水。
  const spentMinor = thisMonth
    .filter((r) => r.amount_minor < 0 && r.status.toLowerCase() === "completed")
    .reduce((sum, r) => sum + Math.abs(r.amount_minor), 0);
  const feeMinor = thisMonth.reduce((sum, r) => sum + r.fee_minor, 0);
  const currency = rows[0]?.currency ?? "USD";

  return (
    <div className="flex flex-wrap gap-3">
      <Tile label="本月消费" value={formatMinorUnits(spentMinor, currency)} />
      <Tile label="本月手续费" value={formatMinorUnits(feeMinor, currency)} />
      <Tile label="本月笔数" value={String(thisMonth.length)} />
    </div>
  );
}

function Tile({ label, value }: { label: string; value: string }) {
  return (
    <div className="border-edge min-w-40 rounded-md border p-3">
      <p className="text-2xl font-semibold tabular-nums">{value}</p>
      <p className="text-fg-muted text-xs">{label}</p>
    </div>
  );
}

function statusTone(status: string): "success" | "warning" | "danger" | "neutral" {
  switch (status.toLowerCase()) {
    case "completed":
      return "success";
    case "failed":
    case "reversed":
      return "danger";
    case "authorized":
    case "pending":
      return "warning";
    default:
      return "neutral";
  }
}
