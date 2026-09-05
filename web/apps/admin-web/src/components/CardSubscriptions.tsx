import { useQuery } from "@tanstack/react-query";
import { DataTableV2, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { Link } from "react-router";
import { listCards, type CardItem } from "../api/cards";
import { ApiStateView } from "./ApiStateView";
import { cycleLabel } from "./CardWorkbench";

const CARDS_QUERY = "cards";

/** 一条订阅 = 一张登记过订阅信息的卡。 */
interface SubscriptionRow {
  key: string;
  account: string;
  cardId: string;
  cardName: string;
  service: string;
  amount: string;
  cycle: string;
  nextOn: string;
  daysLeft: number | null;
}

/** 订阅：接下来要扣什么钱（XM-CARD9）。
 *
 *  对标 Infini 后台的「订阅」页签，但**数据来源根本不同**。
 *
 *  Infini 那一页是从交易记录**自动识别**的，他们自己在页面上写着「仅供参考，
 *  可能与实际订阅不一致」。我们不猜：这里列的全是运营在「用途登记」里
 *  填过的。代价是没登记的卡不出现，好处是出现的每一条都可信——而一个
 *  错了的续费提醒比没有提醒更糟，因为人会拿它做预算、会信它。
 *
 *  为什么不推断：试用转正、年付转月付、涨价、首月折扣、汇率波动，任何一样
 *  都会让推断悄悄错掉，而错在哪儿没有任何迹象。 */
export function CardSubscriptions() {
  const query = useQuery({
    queryKey: [CARDS_QUERY],
    queryFn: ({ signal }) => listCards({ signal }),
    staleTime: 30_000,
  });

  const rows = toSubscriptions(query.data?.cards ?? []);

  const columns: DataTableColumn<SubscriptionRow>[] = [
    {
      id: "service",
      header: "订阅服务",
      cell: (row) => row.service,
      value: (row) => row.service,
      primary: true,
    },
    {
      id: "amount",
      header: "金额",
      cell: (row) => <span className="tabular-nums">{row.amount || "—"}</span>,
      value: (row) => row.amount,
    },
    {
      id: "cycle",
      header: "扣款周期",
      cell: (row) => (row.cycle ? cycleLabel(row.cycle) : "—"),
      value: (row) => row.cycle,
    },
    {
      id: "next",
      header: "下次扣款日期",
      cell: (row) => (
        <span className="flex items-center gap-2">
          <span className="font-mono tabular-nums">{row.nextOn || "—"}</span>
          <DueBadge daysLeft={row.daysLeft} />
        </span>
      ),
      // 排序按日期文本即可（YYYY-MM-DD 字典序 = 时间序）；
      // 没登记日期的排最后，用一个不会出现的大值。
      value: (row) => row.nextOn || "9999-12-31",
    },
    {
      id: "card",
      header: "支付卡片",
      cell: (row) => (
        <Link
          className="underline"
          to={`/cards/${encodeURIComponent(row.account)}/${encodeURIComponent(row.cardId)}`}
        >
          {row.cardName}
        </Link>
      ),
      value: (row) => row.cardName,
    },
  ];

  return (
    // max-w 与卡片页右栏同一档：一张五列的表拉到满屏两端并不更好读，
    // 而两个页签宽度不一致会让人以为切错了页。
    <section className="flex min-w-0 max-w-5xl flex-col gap-3">
      <SubscriptionSummary rows={rows} />
      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
        <DataTableV2
          caption="已登记的订阅：服务、金额、扣款周期、下次扣款日期与支付卡片"
          searchable
          rows={rows}
          columns={columns}
          rowKey={(row) => row.key}
          emptyState={
            <div className="text-fg-muted flex max-w-xl flex-col gap-1 text-sm">
              <p>还没有登记任何订阅。</p>
              <p>
                到「卡片管理」选中一张卡，点右侧的「登记用途」，
                填上订阅服务、金额与下次扣款日期。
              </p>
              <p className="text-xs">
                这一页只列登记过的，不从流水里猜——所以列出来的每一条都是准的。
              </p>
            </div>
          }
        />
      </ApiStateView>
    </section>
  );
}

/** 把卡片投影翻成订阅行。**只取登记过服务名的**。
 *
 *  没登记的卡不是「没有订阅」，是「还没人登记」——两者在这一页上都该是
 *  「不出现」，因为这一页回答的是「我已知的订阅有哪些」。 */
function toSubscriptions(cards: CardItem[]): SubscriptionRow[] {
  const today = new Date();
  return cards
    .filter((c) => (c.service_name ?? "").trim() !== "")
    .map((c) => ({
      key: `${c.account}/${c.card_id}`,
      account: c.account,
      cardId: c.card_id,
      cardName: c.card_alias || c.mask || c.card_id,
      service: c.service_name ?? "",
      amount: c.subscription_amount ?? "",
      cycle: c.subscription_cycle ?? "",
      nextOn: c.next_renewal_on ?? "",
      daysLeft: daysUntil(c.next_renewal_on, today),
    }))
    .sort((a, b) => (a.nextOn || "9999-12-31").localeCompare(b.nextOn || "9999-12-31"));
}

/** 距离扣款还有几天。没登记日期返回 null。
 *
 *  按 UTC 日期算：登记的日期本来就是一个没有时区的「哪一天」，
 *  按本地时区算会让同一条订阅在两个人屏幕上差一天。 */
function daysUntil(date: string | undefined, today: Date): number | null {
  if (!date) return null;
  const target = Date.parse(`${date}T00:00:00Z`);
  if (Number.isNaN(target)) return null;
  const start = Date.UTC(today.getUTCFullYear(), today.getUTCMonth(), today.getUTCDate());
  return Math.round((target - start) / 86_400_000);
}

function DueBadge({ daysLeft }: { daysLeft: number | null }) {
  if (daysLeft === null) return null;
  if (daysLeft < 0) return <Badge tone="danger">已过期</Badge>;
  if (daysLeft === 0) return <Badge tone="danger">今天</Badge>;
  // 七天是一周：够时间去取消或换卡，也不至于把整页都染成警告色。
  if (daysLeft <= 7) return <Badge tone="warning">{daysLeft} 天后</Badge>;
  return null;
}

/** 汇总磁贴。
 *
 *  「每月订阅支出」只把**周期为每月**的加起来。把年付摊成月付看着更完整，
 *  但那是一次换算，而换算需要一个汇率之外的假设（一年几个月是确定的，
 *  但「年付要不要摊」是记账口径问题）。宁可少算也不要给一个说不清怎么来的数。
 *  年付的条目仍在表里，只是不进这个合计。 */
function SubscriptionSummary({ rows }: { rows: SubscriptionRow[] }) {
  const monthly = rows.filter((r) => r.cycle === "monthly");
  const total = monthly.reduce((sum, r) => sum + parseAmount(r.amount), 0);
  const withoutAmount = rows.filter((r) => r.amount.trim() === "").length;

  return (
    <div className="flex flex-wrap items-start gap-3">
      <div className="border-edge min-w-40 rounded-md border p-3">
        <p className="text-2xl font-semibold tabular-nums">{total.toFixed(2)}</p>
        <p className="text-fg-muted text-xs">每月订阅支出（仅按月扣款的）</p>
      </div>
      <div className="border-edge min-w-40 rounded-md border p-3">
        <p className="text-2xl font-semibold tabular-nums">{rows.length}</p>
        <p className="text-fg-muted text-xs">已登记订阅</p>
      </div>
      {withoutAmount > 0 ? (
        // 说清合计漏了几条，而不是给一个看起来完整的数——
        // 一个偏低的「每月支出」比没有这个数更容易误导预算。
        <p className="text-fg-muted self-center text-xs">
          其中 {withoutAmount} 条没登记金额，未计入合计。
        </p>
      ) : null}
    </div>
  );
}

/** 金额文本转数字，仅用于页面合计。
 *
 *  解析不了的当 0：一条登记成「约 20 刀」的金额不该让整个合计变成 NaN，
 *  而上面那句「N 条没登记金额」会把它算进去（空串与解析失败都不计入）。 */
function parseAmount(raw: string): number {
  const n = Number.parseFloat(raw.replace(/[^0-9.-]/g, ""));
  return Number.isFinite(n) ? n : 0;
}
