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
 *  实际说了什么。
 *
 *  两个调用方共用这一份口径：「充值订单」页签的 `PaymentStatusRollupTable`
 *  与「资金概览」页签的「最近事件」四行（XM-SUB2API-RECENT-EVENTS）。同一个
 *  口径两份实现迟早分叉，所以分桶、求和、失败判据只在这里写一次。 */

/** 一个桶的金额为什么给不出。空串 = 金额可用。 */
export type PaymentAmountGap = "" | "no-amount" | "currency-mismatch";

export interface PaymentBucketRollup {
  /** 归一化桶名，与订单表「状态」列的可筛选值同一个空间。 */
  bucket: string;
  count: number;
  /** 金额之和；`null` = 给不出（原因见 `amountGap`），**不是 0**。 */
  minorUnits: bigint | null;
  /** 桶内金额的币种；币种不一致（`amountGap === "currency-mismatch"`）时为空串。 */
  currency: string;
  /** 桶内**实际贡献了金额**的条目出现过的币种，去重后按字母序。 */
  currencies: string[];
  amountGap: PaymentAmountGap;
  /** 落进这个桶的上游原始状态，按字母序，供人核对归类是否合理。 */
  rawStatuses: string[];
}

export interface PaymentStatusRollup {
  buckets: PaymentBucketRollup[];
  /** 汇总覆盖的订单总数（所有桶之和，含「未知」）。 */
  totalCount: number;
  /** 整份汇总里金额的唯一币种；没有任何金额、或币种不止一种时为空串。 */
  currency: string;
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

/** 累加中的桶。币种用集合记，**只记真正贡献了金额的那些条目**——一条金额
 *  缺席的记录说自己是 USD，并不能证明这个桶里有 USD 的钱。 */
interface BucketAccumulator {
  bucket: string;
  count: number;
  sum: bigint | null;
  currencies: Set<string>;
  rawStatuses: string[];
}

export function rollupPaymentStatuses(
  stats: Readonly<Record<string, PlatformOrderStat>>,
): PaymentStatusRollup {
  const byBucket = new Map<string, BucketAccumulator>();
  const unknown: string[] = [];
  const allCurrencies = new Set<string>();
  let totalCount = 0;

  for (const [rawStatus, stat] of Object.entries(stats)) {
    const bucket = bucketLabel(rawStatus);
    if (bucket === "未知") unknown.push(rawStatus);
    totalCount += stat.count;
    const minor = amountMinor(stat.amount);
    let entry = byBucket.get(bucket);
    if (!entry) {
      entry = { bucket, count: 0, sum: null, currencies: new Set(), rawStatuses: [] };
      byBucket.set(bucket, entry);
    }
    entry.count += stat.count;
    entry.rawStatuses.push(rawStatus);
    if (minor !== null) {
      entry.sum = (entry.sum ?? 0n) + minor;
      // 币种空串（上游没说）也当作一个独立取值：把「不知道是什么币」的钱
      // 与已知 CNY 的钱加在一起，等于替上游认定了币种。
      const currency = stat.amount?.currency ?? "";
      entry.currencies.add(currency);
      allCurrencies.add(currency);
    }
  }

  const finish = (entry: BucketAccumulator): PaymentBucketRollup => {
    const currencies = [...entry.currencies].sort();
    entry.rawStatuses.sort();
    // 跨币种 fail closed：币种不止一种就**不给合计**，也不做隐式换算——
    // 与「资金概览」的 aggregateChannelMoney（currency-mismatch → money=null）
    // 同一条纪律。一边 fail closed 一边偷偷相加，两个数字迟早互相拆台。
    //
    // 走到这一支说明上游在同一个窗口里给出了不止一种币种的金额；今天的
    // payments.read.v1 对整份 stats_by_status 只发一个合约币种（见契约
    // 「跨币种处理」一节，非合约币种的金额被排除并置 is_partial），所以这是
    // 一道防御性的闸门，不是当下的常态分支——但闸门必须在，口径才不依赖
    // 「上游现在恰好只发一种币」这个会变的前提。
    if (currencies.length > 1) {
      return {
        bucket: entry.bucket,
        count: entry.count,
        minorUnits: null,
        currency: "",
        currencies,
        amountGap: "currency-mismatch",
        rawStatuses: entry.rawStatuses,
      };
    }
    return {
      bucket: entry.bucket,
      count: entry.count,
      minorUnits: entry.sum,
      currency: currencies[0] ?? "",
      currencies,
      amountGap: entry.sum === null ? "no-amount" : "",
      rawStatuses: entry.rawStatuses,
    };
  };

  // 四个已知桶按固定顺序（与订单表的筛选选项一致），「未知」永远排最后：
  // 顺序固定，人才能在两天的截图之间对着同一行看。
  const ordered: PaymentBucketRollup[] = [];
  for (const bucket of STATUS_BUCKET_OPTIONS) {
    const entry = byBucket.get(bucket);
    if (entry) ordered.push(finish(entry));
  }
  const unknownBucket = byBucket.get("未知");
  if (unknownBucket) ordered.push(finish(unknownBucket));

  const currencies = [...allCurrencies];
  return {
    buckets: ordered,
    totalCount,
    currency: currencies.length === 1 ? currencies[0]! : "",
    unknownStatuses: unknown.sort(),
  };
}

// ---------------------------------------------------------------------------
// 「最近事件」四行的取值口径（XM-SUB2API-RECENT-EVENTS）
// ---------------------------------------------------------------------------

/** 一行的可信程度。四挡刻意区分「确认过的零」与「说不清是不是零」——
 *  把后者显示成 0，正是宪法 12 条要防的那种「看起来有结论」。 */
export type PaymentBucketCoverage =
  /** 金额与笔数都完整可信。 */
  | "known"
  /** 区间内确认没有落进这个桶的订单：0 笔、0 元，是结论不是缺席。 */
  | "confirmed-zero"
  /** 笔数可信，金额不完整（上游排除了非合约币种/未识别渠道的金额）。 */
  | "partial"
  /** 连笔数都说不清；不显示数字。 */
  | "unknown";

export interface PaymentBucketCell {
  bucket: string;
  /** `null` = 笔数说不清（不是 0）。 */
  count: number | null;
  /** `null` = 金额给不出（不是 0）。 */
  minorUnits: bigint | null;
  currency: string;
  coverage: PaymentBucketCoverage;
  /** 金额为什么给不出/不完整的逐字说明；空串 = 无需说明。 */
  amountNote: string;
}

/** 上游把整份汇总标记为不完整时的说明。原因见契约「跨币种处理」一节
 *  （Sub2API：命中非合约币种；NewAPI：未识别的支付渠道）——两者都是
 *  「笔数照计、金额被排除」，所以合计只会偏低，方向是确定的。 */
const PARTIAL_AMOUNT_NOTE =
  "覆盖不全：上游有未计入金额的订单（非合约币种或未识别渠道），笔数仍完整，合计只会偏低";

const PARTIAL_ABSENT_NOTE =
  "覆盖不全：这个分桶在本次汇总里没有出现，无法确认它真的是零";

const CURRENCY_MISMATCH_NOTE = "币种不一致，合计给不出；不做隐式换算";

const NO_AMOUNT_NOTE = "上游没有给出这个分桶的金额（不是 0）";

/** 把一个归一化桶摊成「最近事件」表里的一行。
 *
 *  只做取值判断，不决定这一行叫什么——行名（成功充值/待处理/失败/退款）是
 *  原型措辞，只服务「资金概览」一个调用方，留在组件里。 */
export function presentPaymentBucket(
  rollup: PaymentStatusRollup,
  bucket: string,
  isPartial: boolean,
): PaymentBucketCell {
  const entry = rollup.buckets.find((item) => item.bucket === bucket);

  if (!entry) {
    // 桶整个缺席。`is_partial=false` 时这是**确认过的零**：汇总覆盖整个查询
    // 区间，区间里没有一笔落进这个桶——与 PaymentSummaryCards 的 BucketCard
    // 同一条判据（契约原话：没有落进某个桶的订单，那个桶的键就不出现）。
    if (isPartial) {
      return {
        bucket,
        count: null,
        minorUnits: null,
        currency: "",
        coverage: "unknown",
        amountNote: PARTIAL_ABSENT_NOTE,
      };
    }
    return {
      bucket,
      count: 0,
      minorUnits: 0n,
      currency: rollup.currency,
      coverage: "confirmed-zero",
      amountNote: "",
    };
  }

  if (entry.amountGap === "currency-mismatch") {
    return {
      bucket,
      count: entry.count,
      minorUnits: null,
      currency: "",
      coverage: "partial",
      amountNote: CURRENCY_MISMATCH_NOTE,
    };
  }

  if (entry.amountGap === "no-amount") {
    return {
      bucket,
      count: entry.count,
      minorUnits: null,
      currency: "",
      coverage: "partial",
      amountNote: NO_AMOUNT_NOTE,
    };
  }

  return {
    bucket,
    count: entry.count,
    minorUnits: entry.minorUnits,
    currency: entry.currency,
    coverage: isPartial ? "partial" : "known",
    amountNote: isPartial ? PARTIAL_AMOUNT_NOTE : "",
  };
}
