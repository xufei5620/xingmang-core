import type { SparkSample } from "@xingmang/ui-admin";
import type { MetricHistoryItem, MetricItem } from "../api/platform";
import { formatBasisPointsPercent, formatCount, formatMinorUnits, toIntegerValue } from "./money";

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

/** metric_key → 友好名。键取自各 connector 的 Metric* 常量
 *  （connectors/sub2api/contract.go、connectors/newapi/contract.go）。 */
const METRIC_LABELS: Record<string, string> = {
  "sub2api.users.total": "Sub2API 用户数",
  "sub2api.users.balance": "Sub2API 用户余额",
  "sub2api.revenue.daily": "Sub2API 日收入",
  "sub2api.cost.daily": "Sub2API 日成本",
  "sub2api.channels.balance": "Sub2API 渠道余额",
  "newapi.users.total": "NewAPI 用户数",
  "newapi.recharge.daily": "NewAPI 日充值",
  "newapi.subscription.daily": "NewAPI 日订阅",
  "newapi.channels.status": "NewAPI 渠道状态",
  "newapi.models.usage": "NewAPI 模型用量",
  // XM-OVERVIEW-UI：请求审计线（reqlog）按业务日/滚动窗口聚合出的调用量三件套，
  // 以及 Sub2API 的逐渠道状态（订阅型上游没有余额，渠道健康改看这一条）。
  "sub2api.channels.status": "Sub2API 渠道状态",
  "sub2api.requests.daily": "Sub2API 调用量（日）",
  "sub2api.requests.success_rate_24h": "Sub2API 成功率（24h）",
  "sub2api.requests.trend_7d": "Sub2API 调用量趋势（7 日）",
  "newapi.requests.daily": "NewAPI 调用量（日）",
  "newapi.requests.success_rate_24h": "NewAPI 成功率（24h）",
  "newapi.requests.trend_7d": "NewAPI 调用量趋势（7 日）",
  // XM-PAY1：资金概览卡消费的按日按状态资金汇总（XM-PAY0 新增指标）。
  "sub2api.payments.daily": "Sub2API 支付日汇总",
  "newapi.payments.daily": "NewAPI 支付日汇总",
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
  "newapi.users.total": (v) => ({ raw: toIntegerValue(v["total_users"]), kind: "count" }),
  "newapi.recharge.daily": (v) => ({
    raw: toIntegerValue(v["amount_minor_units"]),
    kind: "money",
  }),
  "newapi.subscription.daily": (v) => ({
    raw: toIntegerValue(v["amount_minor_units"]),
    kind: "money",
  }),
  // 渠道状态的主数值是渠道条数，与 sub2api.channels.balance 同一条理由：
  // 余额可能币种不一、还可能压根没配，合计不成立（见 NewApiChannelRow.balanceMinorUnits）
  "newapi.channels.status": (v) => ({ raw: channelCountOf(v), kind: "count" }),
  "newapi.models.usage": (v) => ({
    raw: toIntegerValue(v["total_request_count"]),
    kind: "count",
  }),
  // XM-OVERVIEW-UI：调用量的主数值取 request_count，与卡片主位显示的数一致。
  // success_rate_24h 不在这里——它的主数值是个万分比（bp），既不是金额也不是
  // 单纯计数，套 kind: "money" | "count" 的哪一种都会让 formatPrimary 给出
  // 一个看着正常、实际单位错了的数字；那条指标的卡片文案完全由下面的自定义
  // RENDERERS 给出，并且不挂迷你趋势图（见 PlatformOverviewPanel 的
  // MetricTile sparkline={false}），所以没有 metricPrimaryValue/
  // metricSeriesValue 会被调用到它头上的路径。trend_7d 同理不登记：
  // 它的 value 是一个内嵌逐日数组，根本不是「单个主数值」这个形状，
  // 走的是 readRequestsTrendDays / toTrendSparkSamples 另一条解析管线。
  "sub2api.channels.status": (v) => ({ raw: channelCountOf(v), kind: "count" }),
  "sub2api.requests.daily": (v) => ({ raw: toIntegerValue(v["request_count"]), kind: "count" }),
  "newapi.requests.daily": (v) => ({ raw: toIntegerValue(v["request_count"]), kind: "count" }),
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
  return bigintToSafeNumber(raw);
}

/** bigint → 安全 number；超出 Number.MAX_SAFE_INTEGER 范围时返回 null。
 *  换算成 number 已经不准的值，不该被当作任何图表纵轴或数值计算的输入
 *  （见下方 metricSeriesValue 与请求量趋势 toTrendSparkSamples 共用同一条纪律）。 */
function bigintToSafeNumber(raw: bigint | null): number | null {
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

// --- NewAPI 渠道状态 ---

/** NewAPI 渠道状态指标里的一行（写入形状见 connectors/newapi/contract.go）。 */
export interface NewApiChannelRow {
  channelId: string;
  name: string;
  type: string;
  /** null 表示上游没给这个字段——显示成「未知」，不默认当作启用。 */
  enabled: boolean | null;
  /** undefined = **未配置余额**（键不存在）；null = 值不是合法整数最小单位。
   *
   *  三态而不是两态：`undefined` 与 `0` 在业务上是相反的两件事——前者是
   *  「这个渠道本来就不按余额计费」，后者是「配了，而且已经花光了，要立刻处理」。
   *  契约层为此把余额做成可空并在 nil 时**不写这个键**，显示层必须把这个
   *  区分接住，否则契约那一层的努力到这里就白费了。 */
  balanceMinorUnits: bigint | null | undefined;
  currency: string;
  modelCount: bigint | null;
  /** 错误率，ppm 整数。null 表示取不到可信数值。 */
  errorRatePPM: bigint | null;
  latencyMS: bigint | null;
}

function readNewApiChannelRow(raw: Record<string, unknown>): NewApiChannelRow {
  const enabled = raw["enabled"];
  return {
    channelId: readString(raw, "channel_id") ?? "",
    name: readString(raw, "name") ?? "",
    type: readString(raw, "type") ?? "",
    enabled: typeof enabled === "boolean" ? enabled : null,
    // 键不存在 → undefined（未配置）；键在但值不合法 → null（数值异常）。
    // `in` 判断而不是 `?? undefined`：后者会把一个显式的 null 也读成未配置。
    balanceMinorUnits:
      "balance_minor_units" in raw ? toIntegerValue(raw["balance_minor_units"]) : undefined,
    currency: currencyOf(raw),
    modelCount: toIntegerValue(raw["model_count"]),
    errorRatePPM: toIntegerValue(raw["error_rate_ppm"]),
    latencyMS: toIntegerValue(raw["latency_ms"]),
  };
}

/** 从 NewAPI 渠道状态指标的 value 里解析出逐渠道明细。
 *  卡片摘要与渠道表共用这一个解析函数，避免两处各解析一遍再解析出分歧。 */
/** ⚠️ XM-0052 起**暂时没有消费者**：渠道管理页换成按上游账号出行之后,
 *  这份逐渠道的启停 / 错误率 / 延迟在界面上没有去处了（概览卡片只给聚合数）。
 *  **刻意不删**——它解析的正是渠道保障（M1.5）要用的那批数据,
 *  那一片落地时直接接上即可。 */
export function readNewApiChannelRows(
  value: Record<string, unknown> | null,
): NewApiChannelRow[] {
  const raw = value?.["channels"];
  if (!Array.isArray(raw)) return [];
  return (raw as unknown[])
    .filter((c): c is Record<string, unknown> => typeof c === "object" && c !== null)
    .map(readNewApiChannelRow);
}

/** NewAPI 渠道状态指标的键。渠道表要从指标列表里挑出这一条。 */
export const NEWAPI_CHANNELS_METRIC_KEY = "newapi.channels.status";

// --- Sub2API 渠道状态（XM-OVERVIEW-UI）---

/** Sub2API 渠道状态指标里的一行（写入形状见 XM-OVERVIEW-UI 交接文档）。
 *
 *  与 ChannelRow（sub2api.channels.balance）**不是同一个形状**：Sub2API 的
 *  上游账号是订阅型，没有钱包余额，channels.balance 永远是空数组——渠道
 *  健康要看的是这条 status 指标。字段也完全不同：没有 token_valid，
 *  多了一个 status 字符串（active / error / …），且键名是 `name` 不是
 *  `channel_name`。两个类型分开定义，不要试图合并成一个可选字段的超集：
 *  合并后调用方要自己猜「这次是哪个平台写的」，猜错了字段就会静默取到
 *  undefined。 */
export interface Sub2ApiChannelStatusRow {
  channelId: string;
  name: string;
  /** 上游原样状态字符串。不在解析这一层把它归类成「可用/不可用」——
   *  那是业务判断（active 才算可用），交给 lib/overview 的健康行计算去做，
   *  解析函数只管把契约里给的字段原样搬过来。 */
  status: string;
  currency: string;
}

function readSub2ApiChannelStatusRow(raw: Record<string, unknown>): Sub2ApiChannelStatusRow {
  return {
    channelId: readString(raw, "channel_id") ?? "",
    name: readString(raw, "name") ?? "",
    status: readString(raw, "status") ?? "",
    currency: currencyOf(raw),
  };
}

/** 从 Sub2API 渠道状态指标的 value 里解析出逐渠道明细。
 *  上游健康卡与（将来若需要的）渠道表共用这一个解析函数。 */
export function readSub2ApiChannelStatusRows(
  value: Record<string, unknown> | null,
): Sub2ApiChannelStatusRow[] {
  const raw = value?.["channels"];
  if (!Array.isArray(raw)) return [];
  return (raw as unknown[])
    .filter((c): c is Record<string, unknown> => typeof c === "object" && c !== null)
    .map(readSub2ApiChannelStatusRow);
}

/** Sub2API 渠道状态指标的键。上游健康卡要从指标列表里挑出这一条。 */
export const SUB2API_CHANNEL_STATUS_METRIC_KEY = "sub2api.channels.status";

// --- 请求量：日调用量 / 24h 成功率 / 7 日趋势（XM-OVERVIEW-UI）---

/** 「近 7 日调用量」趋势指标（`*.requests.trend_7d`）里的一天。 */
export interface RequestsTrendDay {
  day: string;
  requestCount: bigint | null;
  successCount: bigint | null;
  /** true 表示这一天没有可信数据。即便 request_count 字段仍带着数字
   *  （后端约定不给可空整数），也不能当成真实读数画进折线——
   *  与「同步失败样本不进折线」是同一条纪律（宪法 12 条）。 */
  missing: boolean;
}

function readRequestsTrendDay(raw: Record<string, unknown>): RequestsTrendDay {
  return {
    day: readString(raw, "day") ?? "",
    requestCount: toIntegerValue(raw["request_count"]),
    successCount: toIntegerValue(raw["success_count"]),
    missing: raw["missing"] === true,
  };
}

/** 从「近 7 日调用量」趋势指标的 value 里解析出逐日明细。
 *
 *  契约保证按天升序给 7 个元素，这里仍按 day 字符串（YYYY-MM-DD，字典序
 *  与时间序一致）再排一次防御——契约一旦哪天漂了乱序，页面画出来的至少
 *  不是一条随机跳动的锯齿线。 */
export function readRequestsTrendDays(value: Record<string, unknown> | null): RequestsTrendDay[] {
  const raw = value?.["days"];
  if (!Array.isArray(raw)) return [];
  const days = (raw as unknown[])
    .filter((d): d is Record<string, unknown> => typeof d === "object" && d !== null)
    .map(readRequestsTrendDay);
  return [...days].sort((a, b) => (a.day < b.day ? -1 : a.day > b.day ? 1 : 0));
}

/** 逐日明细 → 折线样本，喂给 ui-admin 的 Sparkline（与卡片迷你趋势图、
 *  底部大图同一个绘制组件——另写一套画法，断点/失败的视觉语义迟早会漂开）。
 *
 *  这条管线与 toSparkSamples**不是同一回事**：toSparkSamples 的输入是分次
 *  采集的历史观测序列（GET /metrics/history），这里的输入是**同一条观测**
 *  内嵌的 7 天数组，横轴直接用日历日（UTC 当天零点），没有 synced_at 可用。
 *
 *  missing 映射到 failed：那一天没有可信数据，把 request_count 当正常读数
 *  连进折线就是伪造（宪法 12 条）。day 解析不出合法日期的样本直接丢弃——
 *  NaN 进了路径字符串会让整条折线消失。 */
export function toTrendSparkSamples(days: readonly RequestsTrendDay[]): SparkSample[] {
  const samples: SparkSample[] = [];
  for (const d of days) {
    const at = Date.parse(`${d.day}T00:00:00Z`);
    if (!Number.isFinite(at)) continue;
    samples.push({
      at,
      value: bigintToSafeNumber(d.requestCount),
      failed: d.missing,
      observedAt: at,
    });
  }
  return samples;
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

/** 渠道状态是个聚合指标，值里是一个数组，单独处理。
 *
 *  主数值是渠道条数，补充说明给「启用 / 异常」两个数——这三个数正是运营
 *  在总览上要一眼看到的东西（「几个渠道、几个开着、几个不对劲」）。
 *
 *  不给合计余额：币种可能不一致，而且部分渠道压根没配余额，
 *  一个把「未配置」当 0 加进去的合计是纯粹的错数。 */
function renderNewApiChannelStatus(value: Record<string, unknown>): ValueRender {
  const enabled = toIntegerValue(value["enabled_channel_count"]);
  const unhealthy = toIntegerValue(value["unhealthy_channel_count"]);

  return {
    primary: `${formatPrimary(metricPrimaryValue(NEWAPI_CHANNELS_METRIC_KEY, value), "")} 个渠道`,
    secondary: joinParts([
      enabled === null ? undefined : `启用 ${formatCount(enabled)}`,
      // 0 个异常也要说出来：不显示与「没算过」在页面上长得一样，
      // 而「查过了，都正常」是一条真正的信息
      unhealthy === null ? undefined : `异常 ${formatCount(unhealthy)}`,
    ]),
  };
}

/** Sub2API 渠道状态是个聚合指标，值里是一个数组，单独处理（与
 *  renderNewApiChannelStatus 同一个理由，字段名不同：这条只有 active 与否，
 *  没有 enabled/error_rate_ppm）。 */
function renderSub2ApiChannelStatus(value: Record<string, unknown>): ValueRender {
  const rows = readSub2ApiChannelStatusRows(value);
  const active = rows.filter((r) => r.status === "active").length;
  return {
    primary: `${formatPrimary(metricPrimaryValue(SUB2API_CHANNEL_STATUS_METRIC_KEY, value), "")} 个渠道`,
    secondary:
      rows.length > 0
        ? joinParts([`可用 ${formatCount(active)}`, `不可用 ${formatCount(rows.length - active)}`])
        : undefined,
  };
}

/** avg_duration_ms 是 `int | null`：null 表示这天没有可信的平均耗时，
 *  **不是 0**——0 毫秒是个会骗人的默认值（宪法 12 条同一条纪律）。 */
function formatAvgDurationMs(value: unknown): string {
  if (value === null) return "平均耗时未知";
  const n = toIntegerValue(value);
  return n === null ? "平均耗时未知" : `平均耗时 ${formatCount(n)}ms`;
}

/** 日调用量（`*.requests.daily`）的渲染，sub2api / newapi 共用同一套逻辑，
 *  按 key 参数化只是为了让 metricPrimaryValue 取到与自己对应的主数值口径。 */
function renderRequestsDaily(key: string): Renderer {
  return (v) => ({
    primary: `${formatPrimary(metricPrimaryValue(key, v), "")} 次`,
    secondary: joinParts([
      readString(v, "day") ? `业务日 ${readString(v, "day")}` : undefined,
      `成功 ${formatCount(v["success_count"])}`,
      `失败 ${formatCount(v["failure_count"])}`,
      formatAvgDurationMs(v["avg_duration_ms"]),
    ]),
  });
}

/** 24h 成功率（`*.requests.success_rate_24h`）的渲染。
 *
 *  success_rate_bp 为 null 时**不是 0%**——契约明说 null 表示这个滚动窗口
 *  内压根没有请求，成功率无意义（除以零）。显示「0.00%」会被读成「全部失败」，
 *  是比「未初始化显示 0」更隐蔽的一种编数据，因为它是一个看起来完全合理的
 *  百分比。这里必须把「没有请求」与「有请求但全失败」分开说清楚。 */
function renderSuccessRate24h(v: Record<string, unknown>): ValueRender {
  const bp = toIntegerValue(v["success_rate_bp"]);
  if (bp === null) {
    return {
      primary: NO_VALUE,
      secondary: joinParts(["24h 内无请求，成功率无意义", `请求 ${formatCount(v["request_count"])} 次`]),
      unavailable: true,
    };
  }
  return {
    primary: formatBasisPointsPercent(bp),
    secondary: joinParts([
      `请求 ${formatCount(v["request_count"])} 次`,
      `成功 ${formatCount(v["success_count"])} 次`,
    ]),
  };
}

/** 「近 7 日调用量」趋势指标（`*.requests.trend_7d`）的兜底文字渲染。
 *
 *  这条指标在概览页走的是专门的折线组件（readRequestsTrendDays +
 *  toTrendSparkSamples），不经过 presentMetric；这里仍然登记一个 RENDERERS
 *  条目，只是为了防御：如果它将来出现在某个通用指标表格里，好歹显示一句
 *  「7 天里有几天缺数据」，而不是 renderUnknown 那句「值形状未知：days」——
 *  那句话对认识这个指标的人没有意义，还会让人怀疑是不是解析坏了。 */
function renderRequestsTrend7d(value: Record<string, unknown>): ValueRender {
  const days = readRequestsTrendDays(value);
  if (days.length === 0) {
    return { primary: NO_VALUE, secondary: "值形状未知：days", unavailable: true };
  }
  const missingCount = days.filter((d) => d.missing).length;
  const total = days.reduce((sum, d) => (d.missing ? sum : sum + (d.requestCount ?? 0n)), 0n);
  return {
    primary: `${formatCount(total)} 次`,
    secondary: joinParts([
      `近 ${days.length} 天合计`,
      missingCount > 0 ? `${missingCount} 天缺数据` : undefined,
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
  "newapi.users.total": (v) => ({
    primary: formatPrimary(metricPrimaryValue("newapi.users.total", v), ""),
    secondary: joinParts([
      `活跃 ${formatCount(v["active_users"])}`,
      v["balance_minor_units"] === undefined
        ? undefined
        : `余额 ${formatMinorUnits(v["balance_minor_units"], currencyOf(v))}`,
    ]),
  }),
  "newapi.recharge.daily": (v) => ({
    primary: formatPrimary(metricPrimaryValue("newapi.recharge.daily", v), currencyOf(v)),
    secondary: joinParts([
      readString(v, "day") ? `业务日 ${readString(v, "day")}` : undefined,
      v["order_count"] === undefined ? undefined : `${formatCount(v["order_count"])} 单`,
    ]),
  }),
  "newapi.subscription.daily": (v) => ({
    primary: formatPrimary(metricPrimaryValue("newapi.subscription.daily", v), currencyOf(v)),
    secondary: joinParts([readString(v, "day") ? `业务日 ${readString(v, "day")}` : undefined]),
  }),
  "newapi.channels.status": renderNewApiChannelStatus,
  "newapi.models.usage": (v) => ({
    primary: `${formatPrimary(metricPrimaryValue("newapi.models.usage", v), "")} 次请求`,
    secondary: joinParts([
      v["model_count"] === undefined ? undefined : `${formatCount(v["model_count"])} 个模型`,
    ]),
  }),
  // XM-OVERVIEW-UI
  "sub2api.channels.status": renderSub2ApiChannelStatus,
  "sub2api.requests.daily": renderRequestsDaily("sub2api.requests.daily"),
  "newapi.requests.daily": renderRequestsDaily("newapi.requests.daily"),
  "sub2api.requests.success_rate_24h": renderSuccessRate24h,
  "newapi.requests.success_rate_24h": renderSuccessRate24h,
  "sub2api.requests.trend_7d": renderRequestsTrend7d,
  "newapi.requests.trend_7d": renderRequestsTrend7d,
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

// --- 支付按日按状态资金汇总（XM-PAY1，消费 XM-PAY0 新增的 sub2api.payments.daily
// / newapi.payments.daily）---

/** 某个归一化分桶的笔数与金额（`by_status` 的一条）。null 表示这个字段本身
 *  不是合法整数——与"这个桶没出现"是两回事：后者体现为 byStatus 里压根
 *  没有这个键（见 DailyPaymentSummary.ByStatus 的契约注释："上游这一天
 *  没有落进某个桶的订单，那个桶的键就不出现"，前端解析原样保留这个空缺，
 *  不补一个 {count:0, amountMinor:0n} 冒充"确认过是零"）。 */
export interface PaymentBucketAmount {
  count: bigint | null;
  amountMinor: bigint | null;
}

/** 资金概览卡消费的整份指标 value（见 payments.read.v1 契约"ops.metric_
 *  observation 观测"一节的 JSON 示例）。 */
export interface PaymentsDailySummary {
  /** 业务日，null 表示字段缺失或形状不对（不猜）。 */
  day: string | null;
  /** 本次汇总的合约币种；空串表示上游没给。 */
  currency: string;
  /** 键是四个归一化分桶之一；上游这天没有的桶，键就不出现。 */
  byStatus: Partial<Record<"succeeded" | "pending" | "failed" | "refunded", PaymentBucketAmount>>;
  /** null 表示未知（规格 §12），不是 0——两平台的语义都可能是"确实不知道"。 */
  feeMinorUnits: bigint | null;
  /** 恒为 null：净现金流公式尚未确定，见 XM-PAY0 交接文档，前端不现算。 */
  netMinorUnits: bigint | null;
}

function readPaymentBucketAmount(raw: unknown): PaymentBucketAmount | null {
  if (typeof raw !== "object" || raw === null) return null;
  const record = raw as Record<string, unknown>;
  return {
    count: toIntegerValue(record["count"]),
    amountMinor: toIntegerValue(record["amount_minor_units"]),
  };
}

/** 解析 sub2api.payments.daily / newapi.payments.daily 指标的 value。
 *
 *  与本文件其余 read* 函数同一条纪律：只做形状解析，不做业务判断——
 *  「NewAPI 的 refunded 桶永远不出现」这类判断留给消费方（组件层），
 *  这里原样把"这个桶存在与否"透传出去。 */
export function readPaymentsDailySummary(
  value: Record<string, unknown> | null,
): PaymentsDailySummary {
  const day = readString(value ?? {}, "day") ?? null;
  const currency = currencyOf(value ?? {});
  const byStatus: PaymentsDailySummary["byStatus"] = {};
  const rawByStatus = value?.["by_status"];
  if (rawByStatus && typeof rawByStatus === "object") {
    for (const bucket of ["succeeded", "pending", "failed", "refunded"] as const) {
      const parsed = readPaymentBucketAmount((rawByStatus as Record<string, unknown>)[bucket]);
      if (parsed) byStatus[bucket] = parsed;
    }
  }
  return {
    day,
    currency,
    byStatus,
    feeMinorUnits: toIntegerValue(value?.["fee_minor_units"]),
    netMinorUnits: toIntegerValue(value?.["net_minor_units"]),
  };
}
