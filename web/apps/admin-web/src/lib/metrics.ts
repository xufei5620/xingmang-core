import type { SparkSample } from "@xingmang/ui-admin";
import type { MetricHistoryItem, MetricItem } from "../api/platform";
import { formatCount, formatMinorUnits, toIntegerValue } from "./money";

/** 指标在卡片上的呈现结果。 */
export interface MetricPresentation {
  /** 友好名。未登记的 metric_key 直接用键名，不编一个好听的假名字。 */
  label: string;
  /** 主数字（或「未初始化」这类占位文案）。 */
  primary: string;
  /** 补充说明，例如业务日、单数、币种。 */
  secondary?: string;
  /** true 表示这里没有可信数值，卡片应弱化显示，不要看着像个正常读数。 */
  unavailable: boolean;
}

/** metric_key → 友好名。键取自 connectors/sub2api/contract.go 的指标常量。 */
const METRIC_LABELS: Record<string, string> = {
  "sub2api.users.total": "Sub2API 用户数",
  "sub2api.users.balance": "Sub2API 用户余额",
  "sub2api.revenue.daily": "Sub2API 日收入",
  "sub2api.cost.daily": "Sub2API 日成本",
  "sub2api.channels.balance": "Sub2API 渠道余额",
};

/** 没有可信数值时主位显示的占位符。 */
const NO_VALUE = "—";

function readString(value: Record<string, unknown>, key: string): string | undefined {
  const v = value[key];
  return typeof v === "string" && v.length > 0 ? v : undefined;
}

function currencyOf(value: Record<string, unknown>): string {
  return readString(value, "currency") ?? "";
}

// --- 主数值：卡片正文与趋势图共用的取值口径 ---

/** 主数值的原始形态。
 *
 *  刻意停在 bigint 而不是直接给字符串：卡片要格式化后的文本，趋势图要能比大小
 *  的数，两者对同一个「主数值」的需求不同，但取的必须是**同一个字段**——
 *  否则卡片显示日收入、趋势画的却是订单数，没人看得出来。 */
export interface MetricPrimaryValue {
  /** 整数最小单位（金额）或整数计数；null 表示取不到可信数值。 */
  raw: bigint | null;
  kind: "money" | "count";
  /** 兜底渲染时主数值取自哪个字段；已登记的指标为 undefined。 */
  field?: string;
}

type PrimaryReader = (value: Record<string, unknown>) => MetricPrimaryValue;

/** 渠道余额的「主数值」是渠道条数——逐渠道余额在渠道明细页看。
 *  不拿合计余额当主数值：币种不一致时那个数根本不成立（见下方 renderChannelBalance）。 */
function channelCountOf(value: Record<string, unknown>): bigint | null {
  const declared = toIntegerValue(value["channel_count"]);
  if (declared !== null) return declared;
  const raw = value["channels"];
  return Array.isArray(raw) ? BigInt(raw.length) : null;
}

const PRIMARY_READERS: Record<string, PrimaryReader> = {
  "sub2api.users.total": (v) => ({ raw: toIntegerValue(v["total_users"]), kind: "count" }),
  "sub2api.users.balance": (v) => ({
    raw: toIntegerValue(v["balance_minor_units"]),
    kind: "money",
  }),
  "sub2api.revenue.daily": (v) => ({
    raw: toIntegerValue(v["amount_minor_units"]),
    kind: "money",
  }),
  "sub2api.cost.daily": (v) => ({ raw: toIntegerValue(v["amount_minor_units"]), kind: "money" }),
  "sub2api.channels.balance": (v) => ({ raw: channelCountOf(v), kind: "count" }),
};

/** 未登记指标的主数值：认得出金额就按金额，认得出整数就按计数，都认不出给 null。 */
function unknownPrimary(value: Record<string, unknown>): MetricPrimaryValue {
  for (const [key, raw] of Object.entries(value)) {
    const n = toIntegerValue(raw);
    if (key.endsWith("_minor_units") && n !== null) return { raw: n, kind: "money", field: key };
  }
  for (const [key, raw] of Object.entries(value)) {
    if (key === "currency") continue;
    const n = toIntegerValue(raw);
    if (n !== null) return { raw: n, kind: "count", field: key };
  }
  return { raw: null, kind: "count" };
}

/** 取某个指标的主数值。卡片与趋势图都走这里，口径因此只有一处。 */
export function metricPrimaryValue(
  metricKey: string,
  value: Record<string, unknown> | null,
): MetricPrimaryValue {
  return (PRIMARY_READERS[metricKey] ?? unknownPrimary)(value ?? {});
}

/** 主数值 → 趋势图纵轴用的 number。
 *
 *  这是全项目里唯一允许把金额落到 number 的地方，因为它算的是**像素坐标**，
 *  不是钱：结果只用于画线，绝不回头当金额显示（宪法 13 条）。
 *  超出安全整数范围时返回 null——那种值换算成 number 已经不准了，
 *  与其画一条错的线，不如显示「暂无趋势」。 */
export function metricSeriesValue(
  metricKey: string,
  value: Record<string, unknown> | null,
): number | null {
  const { raw } = metricPrimaryValue(metricKey, value);
  if (raw === null) return null;
  if (raw > BigInt(Number.MAX_SAFE_INTEGER) || raw < BigInt(Number.MIN_SAFE_INTEGER)) return null;
  return Number(raw);
}

/** 历史观测序列 → 趋势图样本。
 *
 *  横轴**一律**用 synced_at（我们采集的时刻），不是 observed_at：
 *  XM-0024 的失败样本会保留上一次成功的 observed_at，拿它当横轴，10:05、10:10
 *  两次连续失败就会全部堆回 10:00 那个成功点上，红色的失败区间凭空消失——
 *  历史表建立「那段是红的」这个语义，靠的就是这一条（Codex #3）。
 *
 *  observed_at 不丢，作为点的附加「数据时间」跟着样本走：两者含义不同，
 *  「什么时候采的」和「数据本身是什么时候的」都得留着，只是不能混用。
 *
 *  synced_at 解析不出来的样本才丢弃：NaN 进了坐标计算会让整条路径消失，
 *  而这种样本本身就是数据完整性事故，不该被 observed_at 顶替着蒙混过去。 */
export function toSparkSamples(
  metricKey: string,
  items: MetricHistoryItem[],
): SparkSample[] {
  const samples: SparkSample[] = [];
  for (const item of items) {
    const at = Date.parse(item.synced_at);
    if (!Number.isFinite(at)) continue;
    const observed = item.observed_at === null ? Number.NaN : Date.parse(item.observed_at);
    samples.push({
      at,
      value: metricSeriesValue(metricKey, item.value),
      // 除了 ok 一律按失败处理：不认识的状态不能默认当成一次成功观测
      failed: item.status !== "ok",
      // 部分数据可能偏小，交给折线用虚线画出来，而不是混进正常实线里
      partial: item.is_partial === true,
      observedAt: Number.isFinite(observed) ? observed : null,
    });
  }
  return samples;
}

// --- 渠道明细 ---

/** 渠道余额指标里的一行（连接器写入形状见 connectors/sub2api/contract.go）。 */
export interface ChannelRow {
  channelId: string;
  channelName: string;
  /** null 表示余额值不是合法整数最小单位，界面显示「数值异常」。 */
  balanceMinorUnits: bigint | null;
  currency: string;
  /** null 表示上游没给这个字段——显示成「未知」，不默认当作有效。 */
  tokenValid: boolean | null;
}

function readChannelRow(raw: Record<string, unknown>): ChannelRow {
  const tokenValid = raw["token_valid"];
  return {
    channelId: readString(raw, "channel_id") ?? "",
    channelName: readString(raw, "channel_name") ?? "",
    balanceMinorUnits: toIntegerValue(raw["balance_minor_units"]),
    currency: currencyOf(raw),
    tokenValid: typeof tokenValid === "boolean" ? tokenValid : null,
  };
}

/** 从渠道余额指标的 value 里解析出逐渠道明细。
 *  卡片摘要与渠道明细页共用这一个解析函数，避免两处各解析一遍再解析出分歧。 */
export function readChannelRows(value: Record<string, unknown> | null): ChannelRow[] {
  const raw = value?.["channels"];
  if (!Array.isArray(raw)) return [];
  return (raw as unknown[])
    .filter((c): c is Record<string, unknown> => typeof c === "object" && c !== null)
    .map(readChannelRow);
}

/** 渠道余额合计。
 *
 *  币种不一致时返回 null：把不同币种的最小单位加在一起是纯粹的错数，
 *  显示一个「合计」等于替人做了一个他没同意的换算。 */
export function channelTotal(rows: ChannelRow[]): { total: bigint; currency: string } | null {
  if (rows.length === 0) return null;
  const currencies = new Set(rows.map((r) => r.currency));
  if (currencies.size !== 1) return null;
  let total = 0n;
  for (const r of rows) {
    if (r.balanceMinorUnits === null) return null;
    total += r.balanceMinorUnits;
  }
  return { total, currency: [...currencies][0] ?? "" };
}

// --- 卡片渲染 ---

interface ValueRender {
  primary: string;
  secondary?: string;
  unavailable?: boolean;
}

type Renderer = (value: Record<string, unknown>) => ValueRender;

function joinParts(parts: Array<string | undefined>): string | undefined {
  const kept = parts.filter((p): p is string => Boolean(p));
  return kept.length > 0 ? kept.join(" · ") : undefined;
}

/** 主数值 → 展示文本。null 由 formatMinorUnits / formatCount 统一显示「数值异常」。 */
function formatPrimary(primary: MetricPrimaryValue, currency: string): string {
  return primary.kind === "money"
    ? formatMinorUnits(primary.raw, currency)
    : formatCount(primary.raw);
}

/** 渠道余额是个聚合指标，值里是一个数组，单独处理。 */
function renderChannelBalance(value: Record<string, unknown>): ValueRender {
  const rows = readChannelRows(value);
  const sum = channelTotal(rows);
  const invalidTokens = rows.filter((r) => r.tokenValid === false).length;

  return {
    primary: `${formatPrimary(metricPrimaryValue("sub2api.channels.balance", value), "")} 个渠道`,
    secondary: joinParts([
      sum ? `合计 ${formatMinorUnits(sum.total, sum.currency)}` : undefined,
      invalidTokens > 0 ? `${invalidTokens} 个渠道令牌失效` : undefined,
    ]),
  };
}

const RENDERERS: Record<string, Renderer> = {
  "sub2api.users.total": (v) => ({
    primary: formatPrimary(metricPrimaryValue("sub2api.users.total", v), ""),
    secondary: joinParts([`活跃 ${formatCount(v["active_users"])}`]),
  }),
  "sub2api.users.balance": (v) => ({
    primary: formatPrimary(metricPrimaryValue("sub2api.users.balance", v), currencyOf(v)),
    secondary: joinParts([`透支 ${formatMinorUnits(v["overdraft_minor_units"], currencyOf(v))}`]),
  }),
  "sub2api.revenue.daily": (v) => ({
    primary: formatPrimary(metricPrimaryValue("sub2api.revenue.daily", v), currencyOf(v)),
    secondary: joinParts([
      readString(v, "day") ? `业务日 ${readString(v, "day")}` : undefined,
      v["order_count"] === undefined ? undefined : `${formatCount(v["order_count"])} 单`,
    ]),
  }),
  "sub2api.cost.daily": (v) => ({
    primary: formatPrimary(metricPrimaryValue("sub2api.cost.daily", v), currencyOf(v)),
    secondary: joinParts([readString(v, "day") ? `业务日 ${readString(v, "day")}` : undefined]),
  }),
  "sub2api.channels.balance": renderChannelBalance,
};

/** 未登记指标的兜底渲染：认得出金额就按金额显示，认得出整数就按计数显示，
 *  都认不出就明说「值形状未知」并列出字段名——比瞎猜一个数字诚实。 */
function renderUnknown(value: Record<string, unknown>): ValueRender {
  const primary = unknownPrimary(value);
  if (primary.raw === null) {
    const keys = Object.keys(value);
    return {
      primary: NO_VALUE,
      secondary: keys.length > 0 ? `值形状未知：${keys.join("、")}` : "值为空",
      unavailable: true,
    };
  }
  return {
    primary: formatPrimary(primary, currencyOf(value)),
    ...(primary.kind === "count" && primary.field ? { secondary: primary.field } : {}),
  };
}

/** 指标 → 卡片呈现内容。
 *
 *  第一条分支就是宪法 12 条：从未采集过就绝不显示数字。后端此时 value 里可能
 *  仍带着 0，直接渲染就会变成「余额 ¥0.00」这种理直气壮的假数据。 */
export function presentMetric(item: MetricItem): MetricPresentation {
  const label = METRIC_LABELS[item.metric_key] ?? item.metric_key;

  if (item.freshness.state === "uninitialized") {
    return { label, primary: "未初始化", secondary: "从未成功采集", unavailable: true };
  }

  const value = item.value ?? {};
  const render = (RENDERERS[item.metric_key] ?? renderUnknown)(value);
  return {
    label,
    primary: render.primary,
    ...(render.secondary === undefined ? {} : { secondary: render.secondary }),
    unavailable: render.unavailable ?? false,
  };
}

/** 指标友好名（表头、筛选器等只要名字的地方）。 */
export function metricLabel(metricKey: string): string {
  return METRIC_LABELS[metricKey] ?? metricKey;
}

/** 渠道余额指标的键。渠道明细页要从指标列表里挑出这一条。 */
export const CHANNEL_BALANCE_METRIC_KEY = "sub2api.channels.balance";
