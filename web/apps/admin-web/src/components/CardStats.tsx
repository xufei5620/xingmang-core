import { useQuery } from "@tanstack/react-query";
import { DataTableV2, type DataTableColumn } from "@xingmang/ui-admin";
import { Button } from "@xingmang/ui-primitives";
import { useState } from "react";
import {
  getCardStats,
  type CardStatsBucket,
  type CardStatsCard,
  type CardStatsIssueFee,
  type CardStatsMerchant,
} from "../api/cards";
import { formatMinorUnits } from "../lib/money";
import { ApiStateView } from "./ApiStateView";

const STATS_QUERY = "card-stats";

/** 期间。按 UTC 算，与全仓一致。 */
const RANGES = [
  { id: "month", label: "本月" },
  { id: "d30", label: "近 30 天" },
  { id: "all", label: "全部" },
] as const;

type RangeID = (typeof RANGES)[number]["id"];

function rangeSince(range: RangeID): string | undefined {
  const now = new Date();
  if (range === "all") return undefined;
  if (range === "month") {
    return new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1)).toISOString();
  }
  return new Date(now.getTime() - 30 * 24 * 3600 * 1000).toISOString();
}

/** 统计（XM-CARD10）。
 *
 *  **聚合在服务端做**：流水端点有 limit，前端对已加载的行求和不会报错，
 *  只会给出一个偏小的数——而「这个月花了多少」看起来完全正常，没人会去
 *  怀疑一个像模像样的数字。
 *
 *  **按币种分开，不跨币种相加**：把 USD 和 PHP 加到一起得出的数没有任何
 *  意义，却会被当成钱。
 *
 *  这一页只回答「发生了什么」。「成本多少、赚了多少」还差两样东西：成本的
 *  口径（充进去多少算成本，还是花掉多少算成本）与收入的来源（卡给谁用、
 *  那笔收入记在哪个系统），两者都要产品负责人定，定之前不猜。 */
export function CardStats({ account }: { account: string }) {
  const [range, setRange] = useState<RangeID>("month");
  const since = rangeSince(range);

  const query = useQuery({
    queryKey: [STATS_QUERY, account, range],
    queryFn: ({ signal }) =>
      getCardStats({ ...(account ? { account } : {}), ...(since ? { since } : {}) }, { signal }),
    staleTime: 30_000,
  });

  const stats = query.data;

  return (
    <section className="flex min-w-0 flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-sm font-semibold">统计</h2>
          <p className="text-fg-muted text-xs">
            {account ? `账号 ${account}` : "全部账号"}
            {" · "}按整表聚合（不是这一页加载的那几行），按币种分开不相加。
          </p>
        </div>
        <div className="flex flex-wrap gap-2" role="group" aria-label="统计期间">
          {RANGES.map((r) => (
            <Button
              key={r.id}
              size="sm"
              variant={range === r.id ? "primary" : "secondary"}
              onClick={() => setRange(r.id)}
              aria-pressed={range === r.id}
            >
              {r.label}
            </Button>
          ))}
        </div>
      </div>

      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        {!stats || stats.buckets.length === 0 ? (
          <p className="text-fg-muted text-sm">这个期间还没有流水。</p>
        ) : (
          <div className="flex min-w-0 flex-col gap-4">
            <CurrencySummaries buckets={stats.buckets} />
            {/* 没有发生时间的那几笔要说出来。悄悄丢掉的话，按月统计里
                它们会凭空消失，而消失的钱是查不出来的。 */}
            {stats.undated_count > 0 ? (
              <p className="text-fg-muted text-xs">
                另有 {stats.undated_count} 笔流水上游没有给发生时间，未计入期间统计；
                切到「全部」可以看到它们。
              </p>
            ) : null}
            <IssueFees rows={stats.issue_fees} />
            <div className="grid min-w-0 gap-4 xl:grid-cols-2">
              <MerchantTable rows={stats.merchants} />
              <CardTable rows={stats.cards} />
            </div>
            <p className="text-fg-muted text-xs">
              成本口径是「实际消费 + 手续费 + 开卡费」（产品负责人 2026-09-06 定）：
              充值只是资金搬家，不计成本，剩在卡里的算在途资金。
              收入不在这套数据里（卡分自用、给客户用、代客充值三种，收入记在别处），
              所以这一页只出成本，利润的合并等跨平台财务。
            </p>
          </div>
        )}
      </ApiStateView>
    </section>
  );
}

/** 开卡费。**来自卡片表而不是流水表**，所以单独一块。
 *
 *  按计价代币分组：一列 USDT 的开卡费直接加到一列 USD 的消费上，得出的数
 *  看起来完全正常，却是把两种资产当成了同一种。
 *
 *  金额是**文本原样显示**，不做 Number() 再格式化——相加已经在服务端的
 *  numeric 里精确做完了，前端再过一遍浮点正是误差进来的地方。 */
function IssueFees({ rows }: { rows: CardStatsIssueFee[] }) {
  if (rows.length === 0) return null;
  return (
    <div className="border-edge flex flex-col gap-2 rounded-md border p-3">
      <h3 className="text-sm font-semibold">开卡费</h3>
      <div className="flex flex-wrap gap-3">
        {rows.map((r) => (
          <Tile
            key={r.token || "unknown"}
            label={
              r.token
                ? `${r.token} · ${r.count} 张`
                : `单位未记录 · ${r.count} 张`
            }
            value={r.token ? `${r.amount_text} ${r.token}` : r.amount_text}
          />
        ))}
      </div>
      {rows.some((r) => !r.token) ? (
        <p className="text-fg-muted text-xs">
          「单位未记录」是 2026-09-06 之前开的卡：当时只存了金额没存代币。
          不按 USDT 计入——那是一个汇率假设，不是记录。新开的卡会带上代币。
        </p>
      ) : null}
    </div>
  );
}

/** 按币种一组磁贴。**不跨币种相加。** */
function CurrencySummaries({ buckets }: { buckets: CardStatsBucket[] }) {
  const currencies = [...new Set(buckets.map((b) => b.currency))].sort();

  return (
    <div className="flex flex-col gap-3">
      {currencies.map((currency) => {
        const mine = buckets.filter((b) => b.currency === currency);
        const sum = (pick: (b: CardStatsBucket) => boolean) =>
          mine.filter(pick).reduce((acc, b) => acc + b.amount_minor, 0);

        // 消费只算**已完成**的：授权中的金额还会变，把它算进「花了多少」
        // 会让这个数随后自己缩水，而缩水的时刻没有任何提示。所以它单独一格。
        const spent = -sum((b) => b.type === "consume" && b.status === "completed");
        const pending = -sum((b) => b.type === "consume" && b.status === "authorized");
        const topup = sum((b) => b.type === "topup" || b.type === "top_up");
        const back = sum((b) => b.type === "reversal" || b.type === "refund");
        const fee = mine.reduce((acc, b) => acc + b.fee_minor, 0);
        const count = mine.reduce((acc, b) => acc + b.count, 0);

        return (
          <div key={currency} className="border-edge flex flex-col gap-2 rounded-md border p-3">
            <h3 className="text-sm font-semibold">{currency}</h3>
            <div className="flex flex-wrap gap-3">
              <Tile label="已完成消费" value={formatMinorUnits(spent, currency)} />
              <Tile label="授权中（金额还会变）" value={formatMinorUnits(pending, currency)} />
              <Tile label="手续费" value={formatMinorUnits(fee, currency)} />
              <Tile label="充值" value={formatMinorUnits(topup, currency)} />
              <Tile label="冲正 / 退款" value={formatMinorUnits(back, currency)} />
              <Tile label="笔数" value={String(count)} />
            </div>
          </div>
        );
      })}
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

function MerchantTable({ rows }: { rows: CardStatsMerchant[] }) {
  const columns: DataTableColumn<CardStatsMerchant>[] = [
    { id: "merchant", header: "商户", primary: true, cell: (r) => r.merchant, value: (r) => r.merchant },
    {
      id: "amount",
      header: "花费",
      numeric: true,
      cell: (r) => formatMinorUnits(-r.amount_minor, r.currency),
      // 排序用最小单位整数，不用格式化后的字符串：
      // 按 "$1,000.00" 的字面排序会把 $9 排在 $1000 后面。
      sortAs: (r) => -r.amount_minor,
    },
    { id: "count", header: "笔数", numeric: true, cell: (r) => String(r.count), value: (r) => r.count },
  ];
  return (
    <div className="border-edge flex min-w-0 flex-col gap-2 rounded-md border p-3">
      <h3 className="text-sm font-semibold">钱花在谁那儿（前 10）</h3>
      <div className="min-w-0 overflow-x-auto">
        <DataTableV2
          caption="按商户汇总的卡片花费"
          columns={columns}
          rows={rows}
          rowKey={(r) => `${r.merchant}/${r.currency}`}
          emptyState={<p className="text-fg-muted text-sm">这个期间没有消费。</p>}
        />
      </div>
    </div>
  );
}

function CardTable({ rows }: { rows: CardStatsCard[] }) {
  const columns: DataTableColumn<CardStatsCard>[] = [
    // 卡关停后投影行会消失但流水还在（钱确实花了），那时没有卡名，退回卡号。
    {
      id: "card",
      header: "卡片",
      primary: true,
      cell: (r) => r.card_alias || r.card_id,
      value: (r) => r.card_alias || r.card_id,
    },
    { id: "account", header: "账号", cell: (r) => r.account, value: (r) => r.account },
    {
      id: "amount",
      header: "花费",
      numeric: true,
      cell: (r) => formatMinorUnits(-r.amount_minor, r.currency),
      sortAs: (r) => -r.amount_minor,
    },
    { id: "count", header: "笔数", numeric: true, cell: (r) => String(r.count), value: (r) => r.count },
  ];
  return (
    <div className="border-edge flex min-w-0 flex-col gap-2 rounded-md border p-3">
      <h3 className="text-sm font-semibold">哪张卡花得多（前 10）</h3>
      <div className="min-w-0 overflow-x-auto">
        <DataTableV2
          caption="按卡片汇总的花费"
          columns={columns}
          rows={rows}
          rowKey={(r) => `${r.account}/${r.card_id}/${r.currency}`}
          emptyState={<p className="text-fg-muted text-sm">这个期间没有消费。</p>}
        />
      </div>
    </div>
  );
}
