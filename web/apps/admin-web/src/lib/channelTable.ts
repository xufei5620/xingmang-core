import type { ChannelSummary, Money } from "../api/finance";
import { sumMoney, type MoneyTotal } from "./upstreamTotals";

/** 渠道管理页（原型 `V["s2/upstream"]` / `V["newapi/upstream"]`）的纯逻辑。
 *
 *  与组件分开，是因为顶部四格里有三格是**判断**而不是取值：
 *  哪些行算「需补充」、哪些行根本没资格参与计数、合计能不能给毛利率。
 *  这些判断在 jsdom 里也能测，但混在 JSX 里就只能靠渲染快照去猜。
 *
 *  ⚠️ **本页一行 = 一个上游账号**（`finance.upstream_account`），
 *  不是被管平台自己的一条渠道。原型的行粒度是后者，但两边的 id 互不认识
 *  （XM-0048 试过按 id join，结果是一条都对不上、经营列全空）,
 *  所以这一片按「渠道 = 上游账号」落地：这样表上的钱是真的，
 *  代价是同一上游下的多条平台渠道**在这里合成一行**。 */

/** 本页只看归属本平台的账号，外加未配对的那些：**未配对的照样显示**。
 *  藏起来的话，一个忘了配 platform_id 的账号会同时从两个平台的页面上消失,
 *  而它的钱一直在花。 */
export function summariesForPlatform(
  items: readonly ChannelSummary[],
  platform: string,
): ChannelSummary[] {
  return items.filter((s) => s.platformId === platform || s.platformId === "");
}

/** 按接入方式三分的计数（原型顶部第一格「Key 账号 / 订阅账号」）。
 *
 *  三分而不是两分：原型只画了 Key 与订阅两类，但登记簿里还有 official_api
 *  （官方直连，成本口径待定）。把它并进任何一边都是错的——并进 Key 会让人
 *  以为它有余额和倍率，并进订阅会让人以为它在摊销。所以单独一格。 */
export interface AccessMethodCounts {
  metered: number;
  subscription: number;
  official: number;
  other: number;
}

export function countByAccessMethod(items: readonly ChannelSummary[]): AccessMethodCounts {
  const counts: AccessMethodCounts = { metered: 0, subscription: 0, official: 0, other: 0 };
  for (const s of items) {
    switch (s.accessMethod) {
      case "upstream_key":
        counts.metered += 1;
        break;
      case "subscription_account":
        counts.subscription += 1;
        break;
      case "official_api":
        counts.official += 1;
        break;
      default:
        counts.other += 1;
    }
  }
  return counts;
}

/** 顶部第二格「N 天内需补充」。
 *
 *  三个数一起返回，因为**只给 count 是会骗人的**：
 *  今天绝大多数上游的余额还读不到（§7 的覆盖率边界，两个真实驱动都没接通),
 *  它们的 `runway.days` 是 null。只显示「0 个需补充」会被读成「余额都很充裕」,
 *  而事实是「我们不知道」。所以调用方必须同时把 `unknown` 说出来。
 *
 *  这与 XM-0049 的告警规则 R5 是**同一条判据**：算不出天数的不算作告警,
 *  也不算作「需补充」。两处如果分叉，看板说 3 个要补而告警只响 1 条，
 *  没人知道该信哪个。 */
export interface TopupCount {
  /** 天数已知且 ≤ 阈值。 */
  count: number;
  /** 计量型但天数算不出来——这些既不算需补充，也不能当作健康。 */
  unknown: number;
  /** 参与判断的行数（计量型）。订阅型不参与：它没有余额这个概念。 */
  applicable: number;
}

export const TOPUP_WINDOW_DAYS = 7;

export function countNeedTopup(
  items: readonly ChannelSummary[],
  withinDays: number = TOPUP_WINDOW_DAYS,
): TopupCount {
  let count = 0;
  let unknown = 0;
  let applicable = 0;

  for (const s of items) {
    // 判据是后端算好的 metered，不是前端按 access_method 再判一次
    // （登记簿注释里的原话：那个判断散到几个页面就会漂）
    if (!s.metered) continue;
    applicable += 1;
    if (s.runway.days === null) {
      unknown += 1;
      continue;
    }
    if (s.runway.days <= withinDays) count += 1;
  }

  return { count, unknown, applicable };
}

/** 顶部第三、四格的合计。 */
export interface ChannelTotals {
  revenue: MoneyTotal;
  profit: MoneyTotal;
  /** 有收入数的行数 / 总行数——覆盖不全时合计只是下界。 */
  revenueCovered: number;
  profitCovered: number;
  rowCount: number;
}

export function channelTotals(items: readonly ChannelSummary[]): ChannelTotals {
  const revenues = items.map((s) => s.usageRevenue);
  const profits = items.map((s) => s.grossProfit);
  return {
    revenue: sumMoney(revenues),
    profit: sumMoney(profits),
    revenueCovered: countKnown(revenues),
    profitCovered: countKnown(profits),
    rowCount: items.length,
  };
}

function countKnown(amounts: readonly (Money | null)[]): number {
  return amounts.filter((m) => Boolean(m)).length;
}

/** 顶部第四格「今日毛利」的角标毛利率。
 *
 *  **这是本文件里唯一一处前端自己算的比率，所以门槛定得比别处高。**
 *
 *  逐行的毛利率一律用后端的 `grossMargin`，前端只格式化——理由见
 *  `channelEconomics.ts`：两处算法在舍入或分母口径上漂开之后，
 *  页面上的百分比与台账里的对不上，而两边看起来都正常。
 *  合计这一格没有后端值可用（汇总端点只逐行给），所以只能在这里算，
 *  于是必须把「什么时候不算」写死：
 *
 *  - 两个合计里任何一个不是 ok（币种混杂 / 标度不一致 / 没有数）→ 不给；
 *  - **覆盖不全 → 不给**。一个只含 3 行里 1 行的毛利除以只含 1 行的收入，
 *    得到的百分比既不是那一行的也不是整页的，纯属编造；
 *  - 收入 ≤ 0 → 不给（分母是零，与后端 `grossMargin` 为 null 同一条规矩）。
 *
 *  算法本身全程 BigInt，一次浮点都不经过（宪法 13 条）：
 *  毛利 × 10^6 ÷ 收入，向零截断，产出与后端同形状的定点小数字符串,
 *  再交给**同一个** `formatGrossMargin` 去变成百分号——
 *  共用格式化函数，至少保证「显示」这一半不会有两套规则。 */
export const AGGREGATE_MARGIN_SCALE = 6;

export function aggregateMargin(totals: ChannelTotals): string | null {
  const { revenue, profit, revenueCovered, profitCovered, rowCount } = totals;
  if (revenue.kind !== "ok" || profit.kind !== "ok") return null;
  if (revenue.currency !== profit.currency || revenue.scale !== profit.scale) return null;
  // 覆盖不全的合计不配有比率
  if (revenueCovered !== rowCount || profitCovered !== rowCount || rowCount === 0) return null;
  if (revenue.total <= 0n) return null;

  const unit = 10n ** BigInt(AGGREGATE_MARGIN_SCALE);
  const scaled = (profit.total * unit) / revenue.total; // 向零截断
  const negative = scaled < 0n;
  const digits = (negative ? -scaled : scaled).toString().padStart(AGGREGATE_MARGIN_SCALE + 1, "0");
  const intPart = digits.slice(0, digits.length - AGGREGATE_MARGIN_SCALE);
  const fracPart = digits.slice(digits.length - AGGREGATE_MARGIN_SCALE);
  return `${negative ? "-" : ""}${intPart}.${fracPart}`;
}
