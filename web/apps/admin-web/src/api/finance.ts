/** 成本看板供数的 API 客户端（XM-0037d，后端 §8.5 + UI 交接 §13）。
 *
 *  两个端点当前是同一个粒度（一个上游账号一行），差别在投影：
 *  渠道看**钱**（收入/成本/毛利/毛利率），上游看**供给**（余额/可用天数/充值成本率）。
 *
 *  三条贯穿本文件的纪律，都是后端形状的直接映射：
 *
 *  1. **金额可空**。`null` = 给不出（覆盖不全或币种混杂），**不是 0**。
 *     把它渲染成 ¥0.00 会让「今天还没入账」看起来像「今天没赚钱」。
 *  2. **金额带 scale**。线上是 scale-6 微单位，而币种的最小单位是 2 位——
 *     用 `formatScaledMinorUnits` 而不是 `formatMinorUnits`，后者会差一万倍。
 *  3. **可用天数给不出时带得出原因**。`days === null` 时 `reason` 非空，
 *     前端必须把那个原因显示出来：一个没有解释的「—」会被读成 bug。
 */

import { apiClient, type ApiClient } from "./client";
import { appApiConfig, type PlatformApiConfig } from "./config";

/** §13 的 `Money`：大整数按字符串传（超 2^53 不丢精度）。 */
export interface Money {
  amountMinor: string;
  currency: string;
  /** 定点标度。后端带它出来，正是为了前端不把 6 硬编码在某处。 */
  scale: number;
}

/** 一段窗口的覆盖率——金额可解释的前提（宪法 12 条）。 */
export interface SummaryCoverage {
  rowCount: number;
  revenueKnownRows: number;
  costKnownRows: number;
  /** 账号级聚合行数（后端 `account:` 哨兵）。这些行没有独立的令牌下钻。 */
  accountGrainRows: number;
  mixedCurrency: boolean;
  /** 两侧都覆盖满且币种单一——只有这时金额才是可断言的。 */
  complete: boolean;
}

/** §13 的 `Observed` 语义。两侧各一个观测时刻，取窗口内最旧的那个。 */
export interface SummaryObserved {
  costObservedAt: string | null;
  revenueObservedAt: string | null;
  updatedAt: string | null;
  source: string;
}

/** 可用天数给不出的原因（§10.4）。 */
export type RunwayReason =
  | "not_applicable"
  | "no_balance"
  | "balance_stale"
  | "no_consumption"
  | "currency_mismatch"
  | "";

export type RunwayLevel = "critical" | "warning" | "serious" | "healthy" | "";

/** 可用天数（§10.4）。`days === null` 时 `reason` 说明为什么。 */
export interface Runway {
  days: number | null;
  level: RunwayLevel;
  reason: RunwayReason;
  windowDays: number;
  coveredDays: number;
  dailyAverage: Money | null;
  balance: Money | null;
  balanceObservedAt: string | null;
}

/** §13 的 ChannelSummary（逐渠道的钱）。 */
export interface ChannelSummary {
  id: string;
  name: string;
  systemType: string;
  accessMethod: string;
  metered: boolean;
  baseUrl: string;
  /** 空串 = 未归属（后端四桶的第三桶）。 */
  platformId: string;
  credentialRef: string;
  rechargeRatio: string;
  rechargeCostRate: string;
  /** 分组倍率（§10.2 + §13 的 groupRate）。
   *
   *  ⚠️ **它不是成本的一部分，前端绝不能拿它去乘任何金额。**
   *  §10.2 的原话是「分组倍率独立存储 / 展示，不并入 recharge_ratio，
   *  前端不重复乘算」——后端已经一次都没乘过它，这里再乘一遍就成了
   *  「重复乘算」本身。
   *
   *  后端**没配就不出这个字段**，所以它是 `undefined` 而不是空串：
   *  「这条渠道没有分组倍率」是多数渠道的正常状态，不是 1。 */
  groupRate?: string;
  businessDayTz: string;
  status: string;
  tokenCount: number;
  usageRevenue: Money | null;
  supplyCost: Money | null;
  grossProfit: Money | null;
  /** 定点十进制字符串；收入 ≤ 0 时为 null（没有收入谈不上毛利率）。 */
  grossMargin: string | null;
  coverage: SummaryCoverage;
  observed: SummaryObserved;
  runway: Runway;
}

/** §13 的 UpstreamSummary（逐上游的供给）。 */
export interface UpstreamSummary {
  id: string;
  name: string;
  /** 供应商归并键。今天它与账号一一对应（后端唯一索引使然）。 */
  supplierKey: string;
  systemType: string;
  accessMethod: string;
  baseUrl: string;
  rechargeCostRate: string;
  /** 同 ChannelSummary.groupRate：不参与任何计算，没配就 undefined。 */
  groupRate?: string;
  credentialRef: string;
  status: string;
  tokenCount: number;
  usageRevenue: Money | null;
  supplyCost: Money | null;
  grossProfit: Money | null;
  coverage: SummaryCoverage;
  observed: SummaryObserved;
  runway: Runway;
}

/** 可用天数的覆盖率（§12 拍板要求「标注覆盖率边界」）。 */
export interface RunwayCoverage {
  total: number;
  known: number;
  reasons: Record<string, number>;
}

/** 三档预警阈值。**名字的严重程度与数值方向相反**（SoloAI 既有命名）：
 *  天数越少越严重，所以 critical 是最紧的一档，serious 反而最松。 */
export interface RunwayThresholds {
  criticalDays: number;
  warningDays: number;
  seriousDays: number;
}

export interface ChannelSummaryPage {
  items: ChannelSummary[];
  from: string;
  to: string;
}

export interface UpstreamSummaryPage {
  items: UpstreamSummary[];
  from: string;
  to: string;
  runwayCoverage: RunwayCoverage;
  runwayThresholds: RunwayThresholds;
}

/** 线上的 snake_case 形状。私有——只在本文件里映射一次。 */
interface RawMoney {
  amount_minor?: string;
  currency?: string;
  scale?: number;
}

interface RawCoverage {
  row_count?: number;
  revenue_known_rows?: number;
  cost_known_rows?: number;
  account_grain_rows?: number;
  mixed_currency?: boolean;
  complete?: boolean;
}

interface RawObserved {
  cost_observed_at?: string | null;
  revenue_observed_at?: string | null;
  updated_at?: string | null;
  source?: string;
}

interface RawRunway {
  days?: number | null;
  level?: string;
  reason?: string;
  window_days?: number;
  covered_days?: number;
  daily_average?: RawMoney | null;
  balance?: RawMoney | null;
  balance_observed_at?: string | null;
}

interface RawChannel {
  id?: string;
  name?: string;
  system_type?: string;
  access_method?: string;
  metered?: boolean;
  base_url?: string;
  platform_id?: string;
  credential_ref?: string;
  recharge_ratio?: string;
  recharge_cost_rate?: string;
  group_rate?: string;
  business_day_tz?: string;
  status?: string;
  token_count?: number;
  usage_revenue?: RawMoney | null;
  supply_cost?: RawMoney | null;
  gross_profit?: RawMoney | null;
  gross_margin?: string | null;
  coverage?: RawCoverage;
  observed?: RawObserved;
  runway?: RawRunway;
}

interface RawUpstream extends RawChannel {
  supplier_key?: string;
}

interface RawChannelPage {
  items?: RawChannel[];
  from?: string;
  to?: string;
}

interface RawUpstreamPage {
  items?: RawUpstream[];
  from?: string;
  to?: string;
  runway_coverage?: { total?: number; known?: number; reasons?: Record<string, number> };
  runway_thresholds?: {
    critical_days?: number;
    warning_days?: number;
    serious_days?: number;
  };
}

/** 金额映射。**null 原样保留**——那是「给不出」，不是 0。 */
function money(raw: RawMoney | null | undefined): Money | null {
  if (!raw || raw.amount_minor === undefined || raw.amount_minor === null) return null;
  return {
    amountMinor: raw.amount_minor,
    currency: raw.currency ?? "",
    // scale 缺省不补 6：补一个猜出来的标度正是这个字段要避免的事。
    // 缺了就让 formatScaledMinorUnits 去说「数值异常」。
    scale: raw.scale ?? Number.NaN,
  };
}

function coverage(raw: RawCoverage | undefined): SummaryCoverage {
  return {
    rowCount: raw?.row_count ?? 0,
    revenueKnownRows: raw?.revenue_known_rows ?? 0,
    costKnownRows: raw?.cost_known_rows ?? 0,
    accountGrainRows: raw?.account_grain_rows ?? 0,
    mixedCurrency: raw?.mixed_currency ?? false,
    // complete 缺省为 false：拿不准时按「不完整」处理，
    // 页面因此会显示覆盖率提示而不是一个笃定的数（宪法 12 条）。
    complete: raw?.complete ?? false,
  };
}

function observed(raw: RawObserved | undefined): SummaryObserved {
  return {
    costObservedAt: raw?.cost_observed_at ?? null,
    revenueObservedAt: raw?.revenue_observed_at ?? null,
    updatedAt: raw?.updated_at ?? null,
    source: raw?.source ?? "",
  };
}

function runway(raw: RawRunway | undefined): Runway {
  return {
    days: raw?.days ?? null,
    level: (raw?.level ?? "") as RunwayLevel,
    reason: (raw?.reason ?? "") as RunwayReason,
    windowDays: raw?.window_days ?? 0,
    coveredDays: raw?.covered_days ?? 0,
    dailyAverage: money(raw?.daily_average),
    balance: money(raw?.balance),
    balanceObservedAt: raw?.balance_observed_at ?? null,
  };
}

function toChannel(raw: RawChannel): ChannelSummary {
  return {
    id: raw.id ?? "",
    name: raw.name ?? "",
    systemType: raw.system_type ?? "",
    accessMethod: raw.access_method ?? "",
    metered: raw.metered ?? false,
    baseUrl: raw.base_url ?? "",
    platformId: raw.platform_id ?? "",
    credentialRef: raw.credential_ref ?? "",
    rechargeRatio: raw.recharge_ratio ?? "",
    rechargeCostRate: raw.recharge_cost_rate ?? "",
    // 缺席时保持 undefined，**不折成空串**：后端刻意用「不出这个字段」
    // 表达「没有分组倍率」，折成 "" 会让它看起来像一个被清空的值。
    ...(raw.group_rate ? { groupRate: raw.group_rate } : {}),
    businessDayTz: raw.business_day_tz ?? "",
    status: raw.status ?? "",
    tokenCount: raw.token_count ?? 0,
    usageRevenue: money(raw.usage_revenue),
    supplyCost: money(raw.supply_cost),
    grossProfit: money(raw.gross_profit),
    // 毛利率的 null **不折成空串**：两者在这里恰好同义，
    // 但保持 null 让「后端说给不出」与「后端没给这个字段」在类型上仍是一件事。
    grossMargin: raw.gross_margin ?? null,
    coverage: coverage(raw.coverage),
    observed: observed(raw.observed),
    runway: runway(raw.runway),
  };
}

function toUpstream(raw: RawUpstream): UpstreamSummary {
  const base = toChannel(raw);
  return {
    id: base.id,
    name: base.name,
    supplierKey: raw.supplier_key ?? "",
    systemType: base.systemType,
    accessMethod: base.accessMethod,
    baseUrl: base.baseUrl,
    rechargeCostRate: base.rechargeCostRate,
    ...(base.groupRate ? { groupRate: base.groupRate } : {}),
    credentialRef: base.credentialRef,
    status: base.status,
    tokenCount: base.tokenCount,
    usageRevenue: base.usageRevenue,
    supplyCost: base.supplyCost,
    grossProfit: base.grossProfit,
    coverage: base.coverage,
    observed: base.observed,
    runway: base.runway,
  };
}

export interface SummaryOptions {
  /** 业务日区间，两个要同时给（都不给则后端只取今天）。 */
  from?: string;
  to?: string;
  signal?: AbortSignal;
}

function windowParams(options: SummaryOptions): Record<string, string | undefined> {
  return { from: options.from, to: options.to };
}

/** 逐渠道的收入 / 成本 / 毛利（§13 ChannelSummary）。 */
export async function listChannelSummaries(
  options: SummaryOptions = {},
  client: ApiClient = apiClient,
  config: PlatformApiConfig = appApiConfig,
): Promise<ChannelSummaryPage> {
  const body = await client.get<RawChannelPage>("/api/v1/finance/channels/summary", {
    searchParams: { environment: config.environment, ...windowParams(options) },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return {
    items: (body.items ?? []).map(toChannel),
    from: body.from ?? "",
    to: body.to ?? "",
  };
}

/** 逐上游的余额 / 可用天数 / 充值成本率（§13 UpstreamSummary + §10.4）。 */
export async function listUpstreamSummaries(
  options: SummaryOptions = {},
  client: ApiClient = apiClient,
  config: PlatformApiConfig = appApiConfig,
): Promise<UpstreamSummaryPage> {
  const body = await client.get<RawUpstreamPage>("/api/v1/finance/upstreams/summary", {
    searchParams: { environment: config.environment, ...windowParams(options) },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return {
    items: (body.items ?? []).map(toUpstream),
    from: body.from ?? "",
    to: body.to ?? "",
    runwayCoverage: {
      total: body.runway_coverage?.total ?? 0,
      known: body.runway_coverage?.known ?? 0,
      reasons: body.runway_coverage?.reasons ?? {},
    },
    runwayThresholds: {
      criticalDays: body.runway_thresholds?.critical_days ?? 0,
      warningDays: body.runway_thresholds?.warning_days ?? 0,
      seriousDays: body.runway_thresholds?.serious_days ?? 0,
    },
  };
}
