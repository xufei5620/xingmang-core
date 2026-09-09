import type { Money } from "../api/finance";
import { formatGrossMargin, isNegativeMargin } from "../lib/channelEconomics";
import { formatScaledMinorUnits } from "../lib/money";

/** 渠道表里跟钱有关的几个格子。
 *
 *  从 XM-0048 的 `ChannelEconomicsColumns` 搬过来，但**不再是三列**：
 *  那三列是按 id 把汇总 join 到平台渠道行上的产物，而 XM-0052 把行粒度
 *  改成了上游账号本身（行就是汇总），join 这一层没有了。
 *  留下来的是它真正值钱的部分——「给不出」与「0」在屏幕上必须长得不一样。
 *
 *  原型把毛利率放在毛利下面当副行（不是独立一列），所以这里只导出格子，
 *  由 `ChannelTableColumns` 去组装。 */

/** 金额排序值取最小单位整数，不取格式化后的文本。
 *
 *  用 bigint 而不是 Number：成本线是 scale-6 微单位，一笔上万元的成本
 *  就已经是十位数，几笔加起来很快越过 2^53。 */
export function moneyValue(money: Money | null | undefined): bigint | null {
  if (!money || !/^-?\d+$/.test(money.amountMinor)) return null;
  return BigInt(money.amountMinor);
}

/** 毛利率的排序值。定点字符串右移 6 位取整——**只用于排序**，不用于显示。 */
export function marginSortValue(raw: string | null): bigint | null {
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

/** 金额格。`null` 是**「给不出」不是 0**（覆盖不全或币种混杂，XM-0037d 的纪律）。
 *
 *  刻意不显示 ¥0.00：一个 0 的供给成本会被读成「这条渠道这期没花钱」，
 *  而事实是我们还没读到它的数。 */
export function MoneyCell({ money, note }: { money: Money | null; note?: string }) {
  if (!money) return <PendingCell reason="这一行在这个业务日窗口里没有可用的汇总数" />;
  return (
    <>
      <span>{formatScaledMinorUnits(money.amountMinor, money.currency, money.scale)}</span>
      {note ? <span className="block text-xs font-normal text-fg-muted">{note}</span> : null}
    </>
  );
}

/** 毛利率格（原型里它是毛利的副行）。
 *
 *  只格式化后端的 `grossMargin`，前端不自己用毛利除以收入再算一遍——
 *  两处算法一旦在舍入或分母口径上漂开，页面上的百分比与台账里的就对不上，
 *  而两边看起来都完全正常（`channelEconomics.ts` 的原话）。 */
export function MarginText({ margin }: { margin: string | null }) {
  const shown = formatGrossMargin(margin);
  if (shown === null) {
    return (
      <span
        className="block text-xs font-normal text-fg-muted"
        title="没有我方计费收入，毛利率的分母是零"
      >
        —
      </span>
    );
  }
  // 亏损标红：一条负毛利的渠道和一条 0.3% 的渠道在一列数字里长得太像
  return (
    <span
      className={`block text-xs font-normal ${
        isNegativeMargin(margin) ? "text-danger" : "text-fg-muted"
      }`}
    >
      {shown}
    </span>
  );
}

/** 没有数据源 / 取不到数时的格子。**说清是哪一种**由调用方给 reason。 */
export function PendingCell({ reason, label = "未接入" }: { reason: string; label?: string }) {
  return (
    <span className="text-fg-muted" title={reason}>
      {label}
    </span>
  );
}
