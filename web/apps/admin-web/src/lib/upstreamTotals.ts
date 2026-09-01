import type { Money } from "../api/finance";

/** 一组金额的合计。算不出来时说明为什么，而不是给一个数。 */
export type MoneyTotal =
  | { kind: "ok"; total: bigint; currency: string; scale: number }
  | { kind: "empty" }
  | { kind: "mixed-currency" }
  | { kind: "mixed-scale" }
  | { kind: "malformed" };

/** 把若干金额加起来（上游管理顶部两格）。
 *
 *  **币种或标度不一致时不给合计**，与渠道表的 `channelTotal` 同一条规矩:
 *  把不同币种的最小单位加起来是纯粹的错数，而它看起来完全正常。
 *  标度也一样——scale-6 的微单位与 scale-2 的分直接相加会差一万倍。
 *
 *  `null` 的金额（后端说「给不出」）**跳过而不是当 0**：它们不参与合计,
 *  由调用方结合覆盖率去说「这个数只包含 N 条里的 M 条」。
 *
 *  全程 BigInt（宪法 13 条）。 */
export function sumMoney(amounts: readonly (Money | null | undefined)[]): MoneyTotal {
  let total = 0n;
  let currency = "";
  let scale = -1;
  let counted = 0;

  for (const m of amounts) {
    if (!m) continue;
    if (!/^-?\d+$/.test(m.amountMinor)) return { kind: "malformed" };
    if (!Number.isFinite(m.scale)) return { kind: "malformed" };

    if (counted === 0) {
      currency = m.currency;
      scale = m.scale;
    } else if (m.currency !== currency) {
      return { kind: "mixed-currency" };
    } else if (m.scale !== scale) {
      return { kind: "mixed-scale" };
    }

    total += BigInt(m.amountMinor);
    counted += 1;
  }

  if (counted === 0) return { kind: "empty" };
  return { kind: "ok", total, currency, scale };
}

/** 合计算不出来时的一句话。**说清是哪一种**——
 *  「没有可合计的数据」与「币种不一致」要采取的下一步完全不同。 */
export function describeMissingTotal(kind: Exclude<MoneyTotal["kind"], "ok">): string {
  switch (kind) {
    case "empty":
      return "本页账号在这个业务日窗口里还没有可用的汇总数";
    case "mixed-currency":
      return "本页账号的币种不一致，合计没有意义";
    case "mixed-scale":
      return "汇总里的金额标度不一致，合计不可信";
    case "malformed":
      return "汇总里有算不了的金额，不给合计";
  }
}

/** 合计只覆盖了几条。
 *
 *  必须显示出来：一个只算了 3 条里 1 条的合计，看起来和算全了的一模一样
 *  （宪法 12 条：覆盖率是金额可解释的前提）。 */
export function coveredCount(amounts: readonly (Money | null | undefined)[]): number {
  return amounts.filter((m) => Boolean(m)).length;
}

/** 按真实 instant 取一组 RFC3339 时间里最旧的那个，并保留原始字符串作为页面证据。
 *
 *  不能直接比较字符串：`01:00-07:00` 实际比 `08:30+02:00` 更新，
 *  但词典序恰好相反。解析不出的时间不替任何金额背书。
 *
 *  上游管理页（账号粒度与供应商分组粒度）和渠道管理页的余额观测时刻
 *  共用这一条规则，抽成一份避免三处各写一遍、各自在边界上漂开。 */
export function oldestActualTimestamp(timestamps: readonly (string | null | undefined)[]): string | null {
  let oldest: { raw: string; instant: number } | null = null;
  for (const raw of timestamps) {
    if (!raw) continue;
    const instant = Date.parse(raw);
    if (!Number.isFinite(instant)) continue;
    if (oldest === null || instant < oldest.instant) oldest = { raw, instant };
  }
  return oldest?.raw ?? null;
}
