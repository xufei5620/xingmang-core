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
import { executeAction, type ActionRun, type ListOptions } from "./platform";

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

// ============================================================================
// XM-0048 成本登记簿（读 + 四个写 Action）
//
// 与上面的看板供数**同文件不同层**：上面是按业务日窗口聚合出来的投影
// （收入/成本/毛利/可用天数），下面是登记簿本身——「这个上游账号是怎么配的」。
// 放在一起是因为它们共用 finance.read 与同一批领域概念（倍率、凭据引用、
// 接入方式）；分成两个文件，读的人会以为那是两套互不相干的东西。
// ============================================================================


/** 登记簿端点的**线上原始金额形状**（snake_case）。
 *
 *  与上半部分的 `Money` 是同一个概念的两种形态：那边是映射之后的展示模型,
 *  这边是批次/代理资产端点原样吐出来的。**没有在这里也加一层映射**——
 *  那两个端点只有一个消费者（行展开区），映射层的价值全在「多处消费时形状一致」,
 *  为一个消费者建一层，只是多一个会漂的地方。
 *
 *  `scale` 与 `currency` 都要：展示时交给 `formatScaledMinorUnits`,
 *  它按 scale 降到币种的最小单位。把 6 硬编码在某个格式化函数里,
 *  一旦与后端漂开，所有金额差一万倍且看起来完全正常（宪法 13 条）。 */
export interface MoneyItem {
  amount_minor: string;
  currency: string;
  scale: number;
}

/** 一条令牌映射（成本侧键 ↔ 收入侧键）。 */
export interface TokenMappingItem {
  upstream_token_id: string;
  own_account_id: string;
  /** **引用**不是凭据（ADR-014）。空串=这条映射没有每令牌凭据，那是正常状态。 */
  credential_ref: string;
  updated_at: string;
}

/** 登记簿一行：一个上游账号。 */
export interface UpstreamAccountItem {
  id: string;
  system_type: string;
  access_method: string;
  base_url: string;
  /** 只有引用，永不回明文。 */
  credential_ref: string;
  /** 规范存储量（除数，定点十进制字符串）。空串=未配置（订阅型本就没有倍率）。 */
  recharge_ratio: string;
  /** 展示投影 = 1 / recharge_ratio。**只拿它显示，绝不用它反算成本**。 */
  recharge_cost_rate: string;
  currency: string;
  /** 业务日切日时区。展示金额与日期时按它解释，不按浏览器时区（宪法 14 条）。 */
  business_day_tz: string;
  /** 空串 = 未配对。台账的归属从这里取值。 */
  platform_id: string;
  status: string;
  environment: string;
  /** true = 走「实扣 ÷ 倍率」的计量口径；false = 订阅摊销口径。
   *  由后端算好，前端不按 access_method 再判一次——那个判断散到几个页面
   *  之后迟早有一个漏掉新枚举值。 */
  metered: boolean;
  token_mappings: TokenMappingItem[];
  created_at: string;
  updated_at: string;
}

export interface SubscriptionBatchItem {
  id: string;
  upstream_account_id: string;
  paid: MoneyItem;
  surcharge: MoneyItem;
  refunded: MoneyItem;
  cost_basis: MoneyItem;
  account_share: MoneyItem;
  daily_amortization: MoneyItem | null;
  currency: string;
  starts_on: string;
  expires_on: string;
  effective_days: number;
  refunded_on: string | null;
  terminated_on: string | null;
  account_count: number;
  proxy_asset_id: string | null;
}

export interface ProxyAssetItem {
  id: string;
  paid: MoneyItem;
  surcharge: MoneyItem;
  refunded: MoneyItem;
  cost_basis: MoneyItem;
  account_share: MoneyItem;
  daily_amortization: MoneyItem | null;
  currency: string;
  opened_on: string;
  expires_on: string;
  effective_days: number;
  refunded_on: string | null;
  terminated_on: string | null;
  shared_account_count: number;
  buy_platform: string;
  buy_address: string;
  /** 引用而非凭据：代理的账号密码从不进库。 */
  credential_ref: string;
  /** false ⇒ daily_amortization 是**已知的 0**，不是未知——
   *  「这份代理今天没在服务」与「没算出来」是两回事。 */
  mounted: boolean;
  environment: string;
}

interface ListResponse<T> {
  items: T[] | null;
}

interface SubscriptionListResponse<T> extends ListResponse<T> {
  truncated?: boolean;
  limit?: number;
  as_of?: string;
}

export interface SubscriptionBatchPage {
  items: SubscriptionBatchItem[];
  truncated: boolean;
  limit: number;
  as_of: string;
}

export interface ProxyAssetPage {
  items: ProxyAssetItem[];
  truncated: boolean;
  limit: number;
  as_of: string;
}

/** 读取成本登记簿需要的权限。
 *
 *  **不复用 ops.read**：登记簿列的是每个上游账号的凭据引用、充值倍率与令牌
 *  映射。倍率是商业条款（我们从上游拿到几折），比看板上的余额数字敏感一个量级
 *  （internal/platform/finance/permissions.go）。 */
export const FINANCE_READ_PERMISSION = "finance.read";

/** 三个写权限。**刻意分开**，依据是爆炸半径而不是整齐:
 *  改倍率直接决定毛利报表长什么样，写错令牌映射会把成本记到别的渠道上
 *  （两条渠道一个虚高一个虚低，合计却完全正确——最难从总数上看出来的一类错误）。 */
export const UPSTREAM_ACCOUNT_MANAGE_PERMISSION = "finance.upstream_account.manage";
export const RECHARGE_RATIO_MANAGE_PERMISSION = "finance.recharge_ratio.manage";
export const TOKEN_MAP_MANAGE_PERMISSION = "finance.token_map.manage";
/** 订阅批次与代理资产共同使用的既有写权限。刻意不进入 DEFAULT_SCOPES。 */
export const SUBSCRIPTION_MANAGE_PERMISSION = "finance.subscription.manage";

/** 哪些平台有「上游管理」这一格。
 *
 *  只有 sub2api / newapi。服务器那一格也叫 `suppliers`，标签是「供应商与采购」——
 *  说的是机器与机房，不是上游 API 供应商。按 tab.value 分发时必须先过这道判定,
 *  否则服务器页会渲染出一张 API 成本登记簿，而且看起来完全正常。 */
const PLATFORMS_WITH_UPSTREAM_REGISTRY = new Set(["sub2api", "newapi"]);

export function platformHasUpstreamRegistry(serviceType: string): boolean {
  return PLATFORMS_WITH_UPSTREAM_REGISTRY.has(serviceType);
}

/** 三个只读查询的 react-query key。
 *
 *  抽成常量而不是在各处写字面量：登记簿页与它的行展开区分属两个组件，
 *  写完之后要失效的是**同一个** key——两处各写一遍字符串，
 *  改动一处就会变成「写成功了但表没刷新」，而这种 bug 只在真机上看得见。
 *
 *  (顺带避开 gitleaks 的 generic-api-key 误报：`queryKey: "长横线串"`
 *  正是它的匹配形状，而本仓库禁止用 allowlist 消音。) */
export const UPSTREAM_ACCOUNTS_QUERY = "finance-upstream-accounts";
export const SUBSCRIPTION_BATCHES_QUERY = "finance-subscription-batches";
export const PROXY_ASSETS_QUERY = "finance-proxy-assets";
/** 上游汇总（本文件上半部分的 `listUpstreamSummaries`）。
 *  与登记簿分开的 key：改倍率要刷登记簿，但不会立刻改变已入账的窗口汇总。 */
export const UPSTREAM_SUMMARY_QUERY = "finance-upstream-summary";

export async function listUpstreamAccounts(
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<UpstreamAccountItem[]> {
  const body = await client.get<ListResponse<UpstreamAccountItem>>(
    "/api/v1/finance/upstream-accounts",
    { ...(options.signal ? { signal: options.signal } : {}) },
  );
  return body.items ?? [];
}

export async function listSubscriptionBatches(
  upstreamAccountId: string,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<SubscriptionBatchPage> {
  const body = await client.get<SubscriptionListResponse<SubscriptionBatchItem>>(
    "/api/v1/finance/subscription-batches",
    {
      searchParams: { upstream_account_id: upstreamAccountId },
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
  return {
    items: body.items ?? [],
    truncated: body.truncated ?? false,
    limit: body.limit ?? 0,
    as_of: body.as_of ?? "",
  };
}

export async function listProxyAssets(
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ProxyAssetPage> {
  const body = await client.get<SubscriptionListResponse<ProxyAssetItem>>(
    "/api/v1/finance/proxy-assets",
    { ...(options.signal ? { signal: options.signal } : {}) },
  );
  return {
    items: body.items ?? [],
    truncated: body.truncated ?? false,
    limit: body.limit ?? 0,
    as_of: body.as_of ?? "",
  };
}

export interface RegisterSubscriptionBatchParams {
  upstream_account_id: string;
  paid_minor: string;
  surcharge_minor: string;
  currency: string;
  starts_on: string;
  expires_on: string;
  account_count: number;
  proxy_asset_id?: string;
}

export interface ProxyAssetCreateParams {
  paid_minor: string;
  surcharge_minor: string;
  currency: string;
  opened_on: string;
  expires_on: string;
  shared_account_count: number;
  buy_platform?: string;
  buy_address?: string;
  credential_ref?: string;
  mounted?: boolean;
}

export interface ProxyAssetEditParams {
  proxy_asset_id: string;
  buy_platform: string;
  buy_address: string;
  credential_ref: string;
  mounted: boolean;
}

export type SetProxyAssetParams = ProxyAssetCreateParams | ProxyAssetEditParams;

/** 新增一笔不可变订阅批次。参数逐个复制，运行时也不会透传白名单外字段。 */
export function registerSubscriptionBatch(
  params: RegisterSubscriptionBatchParams,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  const allowed: Record<string, unknown> = {
    upstream_account_id: params.upstream_account_id,
    paid_minor: params.paid_minor,
    surcharge_minor: params.surcharge_minor,
    currency: params.currency,
    starts_on: params.starts_on,
    expires_on: params.expires_on,
    account_count: params.account_count,
    ...(params.proxy_asset_id ? { proxy_asset_id: params.proxy_asset_id } : {}),
  };
  return executeAction(
    { actionId: "finance.subscription_batch.register", version: "1", params: allowed },
    options,
    client,
  );
}

/** 登记 / 修改代理资产。编辑分支主动排除金额、币种、期间与共享账号数。 */
export function setProxyAsset(
  params: SetProxyAssetParams,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  const editing = "proxy_asset_id" in params && Boolean(params.proxy_asset_id);
  const allowed: Record<string, unknown> = editing
    ? {
        proxy_asset_id: (params as ProxyAssetEditParams).proxy_asset_id,
        buy_platform: params.buy_platform ?? "",
        buy_address: params.buy_address ?? "",
        credential_ref: params.credential_ref ?? "",
        mounted: params.mounted ?? false,
      }
    : {
        paid_minor: (params as ProxyAssetCreateParams).paid_minor,
        surcharge_minor: (params as ProxyAssetCreateParams).surcharge_minor,
        currency: (params as ProxyAssetCreateParams).currency,
        opened_on: (params as ProxyAssetCreateParams).opened_on,
        expires_on: (params as ProxyAssetCreateParams).expires_on,
        shared_account_count: (params as ProxyAssetCreateParams).shared_account_count,
        ...(params.buy_platform !== undefined ? { buy_platform: params.buy_platform } : {}),
        ...(params.buy_address !== undefined ? { buy_address: params.buy_address } : {}),
        ...(params.credential_ref !== undefined
          ? { credential_ref: params.credential_ref }
          : {}),
        ...(params.mounted !== undefined ? { mounted: params.mounted } : {}),
      };
  return executeAction(
    { actionId: "finance.proxy_asset.set", version: "1", params: allowed },
    options,
    client,
  );
}

/** 登记 / 修改上游账号(`finance.upstream_account.set@1`)。
 *
 *  写路径唯一入口是 Action（宪法 2 条）。`upstream_account_id` 留空 = 新建。 */
export function setUpstreamAccount(
  params: Record<string, string>,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "finance.upstream_account.set", version: "1", params },
    options,
    client,
  );
}

/** 修改充值倍率(`finance.recharge_ratio.set@1`)。
 *
 *  `reason` 是**必填**的：倍率是唯一会改变成本口径的字段，改完不会报错,
 *  只会让台账从那一刻起静静地错着。没有理由的改动在事后复盘时与手滑不可区分。 */
export function setRechargeRatio(
  params: { upstream_account_id: string; recharge_ratio: string; reason: string },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "finance.recharge_ratio.set", version: "1", params },
    options,
    client,
  );
}

/** 维护令牌映射(`finance.token_map.set@1`)。
 *
 *  参数：`upstream_account_id`、`upstream_token_id`、`own_account_id` 必填,
 *  `credential_ref` 可选（newapi 侧走账号级会话，没有每令牌凭据）。
 *
 *  收 `Record<string, string>` 而不是逐字段的类型：可选字段**不传**与传空串
 *  在这个 Action 上是两回事，用 `credential_ref?: string` 表达不了「省略」,
 *  组装的责任交给 lib/upstreamForm.buildTokenMapParams，那里有测试盯着。 */
export function setTokenMapping(
  params: Record<string, string>,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "finance.token_map.set", version: "1", params },
    options,
    client,
  );
}

/** 移除令牌映射(`finance.token_map.remove@1`)。`reason` 必填，理由同倍率。 */
export function removeTokenMapping(
  params: { upstream_account_id: string; upstream_token_id: string; reason: string },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "finance.token_map.remove", version: "1", params },
    options,
    client,
  );
}

/** 接入方式的展示口径。
 *
 *  三种方式的成本口径完全不同，不能只显示一个英文枚举值让人自己去猜。 */
export function describeAccessMethod(raw: string): { label: string; hint: string } {
  switch (raw) {
    case "upstream_key":
      return { label: "上游中转", hint: "按每令牌实扣 ÷ 倍率折算成本（计量口径）" };
    case "official_api":
      return { label: "官方 API", hint: "直连官方，成本口径待接（设计稿 §3.1 占位）" };
    case "subscription_account":
      return { label: "订阅账号", hint: "按订阅批次摊销到每天（不适用充值倍率）" };
    default:
      // 不认识的枚举值原样显示 + 标注，不猜：前端不认识不等于配置错了
      return { label: raw || "—", hint: `前端不认识这个接入方式：${raw}` };
  }
}

/** 凭据的展示口径。**永远只说状态，不显示值**（ADR-014、宪法 7 条）。 */
export function describeCredential(ref: string): { label: string; configured: boolean; hint: string } {
  if (!ref) {
    return {
      label: "未配置",
      configured: false,
      hint: "这条没有凭据引用；对 newapi 侧走账号级会话的映射来说这是正常状态",
    };
  }
  return { label: "已配置", configured: true, hint: `凭据引用 ${ref}；平台永不持有明文` };
}
