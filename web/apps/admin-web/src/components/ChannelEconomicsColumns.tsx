import type { DataTableColumn } from "@xingmang/ui-admin";
import type { ChannelSummary, Money } from "../api/finance";
import {
  ECONOMICS_PENDING_NOTE,
  formatGrossMargin,
  isNegativeMargin,
} from "../lib/channelEconomics";
import { formatScaledMinorUnits } from "../lib/money";

/** 渠道表右侧的经营三列：供给成本 / 毛利 / 毛利率（交接文档 §9.5）。
 *
 *  两个平台的渠道表共用同一份列定义：它们的**指标**形状不同（Sub2API 是余额+
 *  令牌，NewAPI 是启停+错误率+延迟），但经营口径是同一个——供给成本、我方计费
 *  收入、毛利、毛利率的定义不该因为看的是哪个平台而不同。
 *
 *  `summaries` 为空 = 这一行接不上汇总（原因见 ECONOMICS_PENDING_NOTE),
 *  三列一起显示「未接入」。刻意不显示 0：一个 ¥0.00 的供给成本会被读成
 *  「这条渠道这期没花钱」，而事实是我们还没能把它和上游账号对上。 */
export function channelEconomicsColumns<T>(
  channelIdOf: (row: T) => string,
  summaries: Map<string, ChannelSummary>,
): DataTableColumn<T>[] {
  const lookup = (row: T) => summaries.get(channelIdOf(row));
  // 没有数据源时**不给 value**：给了就意味着这一列可排序,
  // 而按一列全是「未接入」的东西排序只会让人以为排序坏了
  const sortable = summaries.size > 0;

  return [
    {
      id: "supplyCost",
      header: "供给成本",
      numeric: true,
      ...(sortable ? { value: (row: T) => moneyValue(lookup(row)?.supplyCost) } : {}),
      cell: (row) => <MoneyCell money={lookup(row)?.supplyCost ?? null} />,
      headerTitle: "上游/订阅/代理折算到这条渠道的现金成本",
    },
    {
      id: "grossProfit",
      header: "毛利",
      numeric: true,
      ...(sortable ? { value: (row: T) => moneyValue(lookup(row)?.grossProfit) } : {}),
      cell: (row) => <MoneyCell money={lookup(row)?.grossProfit ?? null} />,
      headerTitle: "我方计费收入 − 供给成本",
    },
    {
      id: "grossMargin",
      header: "毛利率",
      numeric: true,
      ...(sortable
        ? { value: (row: T) => marginSortValue(lookup(row)?.grossMargin ?? null) }
        : {}),
      cell: (row) => <MarginCell summary={lookup(row)} />,
      headerTitle: "由后端按业务日窗口算出；没有收入时不给百分比",
    },
  ];
}

/** 排序值取最小单位整数，不取格式化后的文本。
 *
 *  用 bigint 而不是 Number：成本线是 scale-6 微单位，一笔上万元的成本
 *  就已经是十位数，几笔加起来很快越过 2^53。 */
function moneyValue(money: Money | null | undefined): bigint | null {
  if (!money || !/^-?\d+$/.test(money.amountMinor)) return null;
  return BigInt(money.amountMinor);
}

/** 毛利率的排序值。定点字符串右移 6 位取整——**只用于排序**，不用于显示。 */
function marginSortValue(raw: string | null): bigint | null {
  if (raw === null) return null;
  const text = raw.trim();
  if (!text) return null;
  const negative = text.startsWith("-");
  const body = negative ? text.slice(1) : text;
  const [intPart = "", fracPart = ""] = body.split(".");
  if (!/^\d*$/.test(intPart) || !/^\d*$/.test(fracPart) || intPart + fracPart === "") return null;
  const digits = intPart + fracPart.slice(0, 6).padEnd(6, "0");
  return BigInt(negative ? `-${digits}` : digits);
}

function MoneyCell({ money }: { money: Money | null }) {
  // null 是**「给不出」不是 0**（覆盖不全或币种混杂，XM-0037d 的纪律）
  if (!money) return <PendingCell />;
  return <span>{formatScaledMinorUnits(money.amountMinor, money.currency, money.scale)}</span>;
}

function MarginCell({ summary }: { summary: ChannelSummary | undefined }) {
  if (!summary) return <PendingCell />;
  const shown = formatGrossMargin(summary.grossMargin);
  if (shown === null) {
    return (
      <span className="text-fg-muted" title="没有我方计费收入，毛利率的分母是零">
        —
      </span>
    );
  }
  // 亏损标红：一条负毛利的渠道和一条 0.3% 的渠道在一列数字里长得太像
  return (
    <span className={isNegativeMargin(summary.grossMargin) ? "text-danger" : undefined}>
      {shown}
    </span>
  );
}

function PendingCell() {
  return (
    <span className="text-fg-muted" title={ECONOMICS_PENDING_NOTE}>
      未接入
    </span>
  );
}
