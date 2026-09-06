import type { PlatformOrderStat } from "../api/finance";
import { bucketLabel, STATUS_BUCKET_OPTIONS } from "../components/platformOrdersColumns";

/** 按归一化分桶汇总 `stats_by_status`（XM-PAY-STATUS-ROLLUP）。
 *
 *  为什么值得做：`stats_by_status` 是**整个查询区间**的汇总，而下面那张表只有
 *  已翻到的那几页。页面一直把它解析出来又丢掉，于是「今天一共成功了多少笔、
 *  多少钱」这个最常被问的问题，只能靠人把表翻到底再自己加——翻到一半得到的
 *  数字还是错的，而且看不出是错的。
 *
 *  上游给的键是**原始状态字面量**（不归一化），所以这里按 `bucketLabel` 归拢，
 *  同时把落进每个桶的原始状态原样带出来：归一化是为了好读，不是为了掩盖上游
 *  实际说了什么。 */

export interface PaymentBucketRollup {
  /** 归一化桶名，与订单表「状态」列的可筛选值同一个空间。 */
  bucket: string;
  count: number;
  /** 金额之和；`null` = 这个桶里没有一条给出了金额（不是 0）。 */
  minorUnits: bigint | null;
  currency: string;
  /** 落进这个桶的上游原始状态，按字母序，供人核对归类是否合理。 */
  rawStatuses: string[];
}

export interface PaymentStatusRollup {
  buckets: PaymentBucketRollup[];
  /** 汇总覆盖的订单总数（所有桶之和，含「未知」）。 */
  totalCount: number;
  /** 归不进四个已知桶的原始状态。非空时页面必须显示出来——
   *  上游加了新状态而我们悄悄丢掉，比显示一个陌生的桶名危险得多。 */
  unknownStatuses: string[];
}

function amountMinor(amount: PlatformOrderStat["amount"] | undefined): bigint | null {
  if (!amount || amount.minor_units === null) return null;
  try {
    return BigInt(amount.minor_units);
  } catch {
    // 解析不出来的金额当作「这一条没给金额」，而不是 0：把读不懂的值算成 0
    // 会让合计静悄悄变小（宪法 13 条：金额只走整数，读不出就说读不出）。
    return null;
  }
}

export function rollupPaymentStatuses(
  stats: Readonly<Record<string, PlatformOrderStat>>,
): PaymentStatusRollup {
  const byBucket = new Map<string, PaymentBucketRollup>();
  const unknown: string[] = [];
  let totalCount = 0;

  for (const [rawStatus, stat] of Object.entries(stats)) {
    const bucket = bucketLabel(rawStatus);
    if (bucket === "未知") unknown.push(rawStatus);
    totalCount += stat.count;
    const existing = byBucket.get(bucket);
    const minor = amountMinor(stat.amount);
    if (existing) {
      existing.count += stat.count;
      if (minor !== null) existing.minorUnits = (existing.minorUnits ?? 0n) + minor;
      if (!existing.currency && stat.amount?.currency) existing.currency = stat.amount.currency;
      existing.rawStatuses.push(rawStatus);
    } else {
      byBucket.set(bucket, {
        bucket,
        count: stat.count,
        minorUnits: minor,
        currency: stat.amount?.currency ?? "",
        rawStatuses: [rawStatus],
      });
    }
  }

  for (const entry of byBucket.values()) entry.rawStatuses.sort();

  // 四个已知桶按固定顺序（与订单表的筛选选项一致），「未知」永远排最后：
  // 顺序固定，人才能在两天的截图之间对着同一行看。
  const ordered: PaymentBucketRollup[] = [];
  for (const bucket of STATUS_BUCKET_OPTIONS) {
    const entry = byBucket.get(bucket);
    if (entry) ordered.push(entry);
  }
  const unknownBucket = byBucket.get("未知");
  if (unknownBucket) ordered.push(unknownBucket);

  return { buckets: ordered, totalCount, unknownStatuses: unknown.sort() };
}
