/** 接码中心的 API 客户端（XM-SMS0）。
 *
 *  读走只读端点，写全部走 `sms.*` Action——后端没有第二条写路径，这里也不该有。
 *
 *  **完整号码与验证码只在带 sms.reveal 的响应里出现**：没有那个权限时后端
 *  根本不回这两个字段。前端不做「本地隐藏」那种假控制。
 */

import {
  apiClient,
  FeatureNotMountedError,
  looksLikeUnmountedRoute,
  type ApiClient,
} from "./client";
import { executeAction, type ActionRun, type ListOptions } from "./platform";

interface ListResponse<T> {
  items?: T[];
}

const SMS_NOT_MOUNTED_DESCRIPTION =
  "接码在当前环境未启用（XM_SMS_MODE=off）。开启后，供应商的密钥与启用开关都在管理后台里配。";

function translateUnmounted(error: unknown): never {
  if (looksLikeUnmountedRoute(error)) {
    throw new FeatureNotMountedError(error, SMS_NOT_MOUNTED_DESCRIPTION);
  }
  throw error;
}

export interface SMSProvider {
  provider: string;
  /** 标签与能力集来自服务端注册表（ADR-022）。页面按能力渲染按钮，
   *  接第三家供应商时前端一个字都不用改。 */
  label?: string;
  capabilities?: string[];
  /** 运营在后台开的开关。**与 verified 是两件事**：
   *  关着是运营的决定（去页面上打开），没验证是凭据的问题（去做连接测试）。
   *  买号要求两个都为 true。 */
  enabled: boolean;
  /** 是否做过成功的连接测试。**买号前必须为 true。** */
  verified: boolean;
  verified_at?: string;
  /** 供应商观察到的我方出口 IP（只有 62 会回）。上游做 IP 白名单时，
   *  这个值对不上就是后续全部 403 的原因。 */
  client_ip?: string;
  last_error?: string;
  /** 这家有没有取消/延长那五个动作。页面据此决定显不显示按钮，
   *  而不是按 provider 名字硬判。 */
  supports_lifecycle: boolean;
}

export interface SMSResource {
  resource_id: string;
  provider: string;
  /** 完整号码。没有 sms.reveal 时后端不回这个字段。 */
  phone?: string;
  phone_mask: string;
  service?: string;
  country?: string;
  status?: string;
  last_code_at?: string;
  expires_at?: string;
  synced_at?: string;
  /** 以下来自 Hero 官方 ActivationSchema（XM-SMS1）；62 没有。 */
  operator?: string;
  price_text?: string;
  verification_type?: string;
  /** 1 = 普通激活（20 分钟），2 = 租用（按小时）。 */
  subtype?: number;
  country_phone_code?: string;
  /** 平台自己的统一状态；status 仍是上游原话。 */
  state?: string;
  /** 「待收码但已过期」算成 expired 后的状态，页面显示用它。 */
  effective_state?: string;
}

export interface SMSOperation {
  operation_id: string;
  provider: string;
  kind: string;
  state: string;
  /** 上游订单/activation ID。unknown 时它是人去供应商侧对账的唯一抓手。 */
  provider_ref?: string;
  resource_id?: string;
  params_summary?: string;
  failure_reason?: string;
  needs_review: boolean;
  resolve_note?: string;
  /** 由**服务端**判定。前端不要按 state 自己推：迟早会推出一个
   *  「看起来该能重试」的不确定态，而重试就是再买一次。 */
  retry_allowed: boolean;
  started_at?: string;
  updated_at?: string;
}

export interface SMSCode {
  code_id: string;
  /** 没有 sms.reveal 时后端不回码本身，只回「有这么一条」。 */
  code?: string;
  resource_id: string;
  sender?: string;
  received_at?: string;
  created_at?: string;
}

export interface SMSCatalogItem {
  id: string;
  name?: string;
  service?: string;
  country?: string;
  /** 三个价格是**不同的事实**（Hero 的 default/retail/min），分开显示。
   *  缺失就是缺失，页面显示「—」，不补零也不补币种。 */
  default_price?: string;
  retail_price?: string;
  minimum_price?: string;
  available?: number;
}

async function get<T>(path: string, options: ListOptions, client: ApiClient): Promise<T[]> {
  const body = await client
    .get<ListResponse<T>>(path, { ...(options.signal ? { signal: options.signal } : {}) })
    .catch(translateUnmounted);
  return body.items ?? [];
}

export function listSMSProviders(
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<SMSProvider[]> {
  return get<SMSProvider>("/api/v1/sms/providers", options, client);
}

export function listSMSResources(
  provider = "",
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<SMSResource[]> {
  const query = provider ? `?provider=${encodeURIComponent(provider)}` : "";
  return get<SMSResource>(`/api/v1/sms/resources${query}`, options, client);
}

export function listSMSOperations(
  provider = "",
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<SMSOperation[]> {
  const query = provider ? `?provider=${encodeURIComponent(provider)}` : "";
  return get<SMSOperation>(`/api/v1/sms/operations${query}`, options, client);
}

export function listSMSCodes(
  resourceId: string,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<SMSCode[]> {
  return get<SMSCode>(
    `/api/v1/sms/resources/${encodeURIComponent(resourceId)}/codes`,
    options,
    client,
  );
}

export function listSMSCatalog(
  provider: string,
  filter: { platform_id?: string; country?: string; service?: string } = {},
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<SMSCatalogItem[]> {
  const params = new URLSearchParams({ provider });
  for (const [k, v] of Object.entries(filter)) {
    if (v) params.set(k, v);
  }
  return get<SMSCatalogItem>(`/api/v1/sms/catalog?${params.toString()}`, options, client);
}

/** 连接测试（`sms.provider.verify@1`）。
 *
 *  **不是纯本地无副作用**：成功会写下 verified_at，而买号前会检查它。 */
export function verifySMSProvider(
  provider: string,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "sms.provider.verify", version: "1", params: { provider } },
    options,
    client,
  );
}

/** 开关一家供应商（`sms.provider.set_enabled@1`）。
 *
 *  参数是 enabled 的**目标值**而不是「切换」：传切换时两个人同时点会变成
 *  一次开一次关，传目标值时同向的两次点击是幂等的。
 *
 *  **不影响验证事实**：开关是运营的意愿，验证是凭据的事实，两者不互相触发。 */
export function setSMSProviderEnabled(
  provider: string,
  enabled: boolean,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "sms.provider.set_enabled", version: "1", params: { provider, enabled } },
    options,
    client,
  );
}

/** 买号（`sms.number.purchase@1`）。**花真钱且不可退。**
 *
 *  `operation_id` 必须由调用方稳定生成：它是幂等键，重试要带同一个；
 *  而未决唯一索引会挡住「换一个 UUID 重发同一份请求」。 */
export function purchaseSMSNumbers(
  params: {
    provider: string;
    operation_id: string;
    quantity: number;
    goods_id?: string;
    first_number?: string;
    no_first_number?: string;
    service?: string;
    country?: number;
    operator?: string;
    max_price?: string;
    duration?: number;
    /** sms / call（官方 VerificationType）。call 是语音验证，另一种计费。 */
    verification_type?: string;
    /** 与 max_price 一起用：严格按这个价成交。 */
    fixed_price?: boolean;
  },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "sms.number.purchase", version: "1", params },
    options,
    client,
  );
}

/** 生命周期动作（`sms.resource.action@1`）。**只有 Hero 支持。** */
export function executeSMSResourceAction(
  params: { operation_id: string; resource_id: string; kind: string; duration?: number },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "sms.resource.action", version: "1", params },
    options,
    client,
  );
}

/** 人工核对一笔 unknown（`sms.operation.resolve@1`）。
 *
 *  **只改本地账本，绝不向上游重发。** note 必填：三个月后回看这条记录时，
 *  「谁凭什么判定它成功了」只有那句话回答得了。 */
export function resolveSMSOperation(
  params: { operation_id: string; outcome: "succeeded" | "failed"; resource_id?: string; note: string },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "sms.operation.resolve", version: "1", params },
    options,
    client,
  );
}


// ---- XM-SMS1：两家官方文档补齐后的扩展能力 ----
//
// 全部是**实时上游调用**，不是投影。路由按供应商分（/sms/hero/…、/sms/sms62/…），
// 共用路径会让页面在 62 上请求一个只有 Hero 有的东西。

async function getObject<T>(path: string, options: ListOptions, client: ApiClient): Promise<T> {
  return client
    .get<T>(path, { ...(options.signal ? { signal: options.signal } : {}) })
    .catch(translateUnmounted);
}

function qs(params: Record<string, string | number | boolean | undefined>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== "" && v !== false) p.set(k, String(v));
  }
  const encoded = p.toString();
  return encoded ? `?${encoded}` : "";
}

export interface HeroCountry {
  id: number;
  name_en: string;
  name_cn: string;
  name_ru: string;
  visible: boolean;
  retry: boolean;
}
export interface HeroService {
  code: string;
  name: string;
}
export interface HeroPriceRow {
  country: string;
  service: string;
  cost_text: string;
  count: number;
  physical_count: number;
}
export interface HeroTopCountry {
  country: number;
  price_text: string;
  retail_price_text: string;
  count: number;
}
export interface HeroCustomDuration {
  service: string;
  country: string;
  hours: number;
}
export interface HeroRentOffer {
  service: string;
  quantity: number;
  price_text: string;
  retail_price_text: string;
}
export interface HeroRentCountRow {
  country: string;
  hours: string;
  price_text: string;
  count: number;
}
export interface HeroHistoryItem {
  id: string;
  create_date: string;
  service: string;
  country: number;
  phone: string;
  more_codes: string;
  cost_text: string;
  status: number;
  phone_code: string;
  currency: number;
}
export interface HeroHistoryPage {
  items: HeroHistoryItem[];
  /** **这一页的**合计（官方如此），不是全量。 */
  page_sum_text: string;
  page_success_count: number;
  page: number;
  size: number;
  total: number;
  has_more: boolean;
}
export interface HeroStatsEntry {
  country: string;
  service: string;
  count: number;
  sum_text: string;
  raw?: Record<string, unknown>;
}
export interface HeroEmailDomain {
  name: string;
  cost_text: string;
  count: number;
}
export interface HeroExtendOption {
  duration: number;
  unit: string;
  price_text: string;
}
export interface HeroProlongRecord {
  duration: number;
  unit: string;
  price_text: string;
  created_at: string;
}
export interface SMSEmail {
  email_id: string;
  provider: string;
  external_id: string;
  site: string;
  email: string;
  /** 官方枚举 WAIT / CANCEL / SUCCESS。 */
  status: string;
  cost_text: string;
  currency: number;
  message?: string;
  /** 没有 sms.reveal 时后端不回 value，只回「有没有到」。 */
  has_value: boolean;
  value?: string;
  upstream_date?: string;
  synced_at?: string;
}
export interface SMS62GoodsDetail {
  id: string;
  name: string;
  price_text: string;
  country: string;
  stock: number;
  /** 这个商品可选的天数（三段 ID 的第三段）；上游没给就是空，自己填。 */
  durations: number[];
  /** 上游响应的真实字段名（只有名字）。官方没写这个接口的结构，
   *  映射错了的列会显示为空——对着这一行就知道该改哪个键。 */
  raw_keys?: string[];
}
export interface SMS62Order {
  order_id: string;
  goods_id: string;
  quantity: number;
  status: number;
  status_text: string;
  amount_text: string;
  created_at?: string;
}
export interface SMS62OrdersPage {
  items: SMS62Order[];
  page: number;
  page_size: number;
  total: number;
  /** 第一条订单的真实字段名（只有名字），理由同 SMS62GoodsDetail.raw_keys。 */
  raw_keys?: string[];
}

export const getHeroBalance = (o: ListOptions = {}, c: ApiClient = apiClient) =>
  getObject<{ balance_text: string }>("/api/v1/sms/hero/balance", o, c);
export const listHeroCountries = (o: ListOptions = {}, c: ApiClient = apiClient) =>
  get<HeroCountry>("/api/v1/sms/hero/countries", o, c);
export const listHeroServices = (country = 0, lang = "cn", o: ListOptions = {}, c: ApiClient = apiClient) =>
  get<HeroService>(`/api/v1/sms/hero/services${qs({ country: country || undefined, lang })}`, o, c);
export const getHeroOperators = (country = 0, o: ListOptions = {}, c: ApiClient = apiClient) =>
  getObject<{ by_country: Record<string, string[]> }>(
    `/api/v1/sms/hero/operators${qs({ country: country || undefined })}`,
    o,
    c,
  );
export const listHeroPrices = (service = "", country = 0, o: ListOptions = {}, c: ApiClient = apiClient) =>
  get<HeroPriceRow>(`/api/v1/sms/hero/prices${qs({ service, country: country || undefined })}`, o, c);
export const listHeroTopCountries = (service: string, byRank = false, o: ListOptions = {}, c: ApiClient = apiClient) =>
  get<HeroTopCountry>(`/api/v1/sms/hero/top-countries${qs({ service, by_rank: byRank })}`, o, c);
export const listHeroCustomDurations = (o: ListOptions = {}, c: ApiClient = apiClient) =>
  get<HeroCustomDuration>("/api/v1/sms/hero/custom-durations", o, c);
export const getHeroRentOffers = (country: number, hours: number, o: ListOptions = {}, c: ApiClient = apiClient) =>
  getObject<{ operators: Record<string, string>; services: HeroRentOffer[] }>(
    `/api/v1/sms/hero/rent-offers${qs({ country, hours })}`,
    o,
    c,
  );
export const listHeroRentCount = (service: string, country = 0, operator = "", o: ListOptions = {}, c: ApiClient = apiClient) =>
  get<HeroRentCountRow>(`/api/v1/sms/hero/rent-count${qs({ service, country: country || undefined, operator })}`, o, c);
export const getHeroHistory = (
  q: { from: string; to: string; page?: number; size?: number; search?: string },
  o: ListOptions = {},
  c: ApiClient = apiClient,
) => getObject<HeroHistoryPage>(`/api/v1/sms/hero/history${qs(q)}`, o, c);
export const getHeroStats = (date: string, o: ListOptions = {}, c: ApiClient = apiClient) =>
  getObject<{ date: string; items: HeroStatsEntry[] }>(`/api/v1/sms/hero/stats${qs({ date })}`, o, c);
export const listHeroEmailDomains = (site = "", o: ListOptions = {}, c: ApiClient = apiClient) =>
  get<HeroEmailDomain>(`/api/v1/sms/hero/email-domains${qs({ site })}`, o, c);

export const listUpstreamCodes = (resourceId: string, o: ListOptions = {}, c: ApiClient = apiClient) =>
  get<SMSCode>(`/api/v1/sms/resources/${encodeURIComponent(resourceId)}/upstream-codes`, o, c);
export const listExtendOptions = (
  resourceId: string,
  kind: "prolong" | "reactivate",
  o: ListOptions = {},
  c: ApiClient = apiClient,
) => get<HeroExtendOption>(`/api/v1/sms/resources/${encodeURIComponent(resourceId)}/extend-options${qs({ kind })}`, o, c);
export const listProlongHistory = (resourceId: string, o: ListOptions = {}, c: ApiClient = apiClient) =>
  get<HeroProlongRecord>(`/api/v1/sms/resources/${encodeURIComponent(resourceId)}/prolong-history`, o, c);

export const listSMSEmails = (o: ListOptions = {}, c: ApiClient = apiClient) =>
  get<SMSEmail>("/api/v1/sms/emails", o, c);
/** refresh=true 时先从上游读回最新状态（含收到的验证内容）——**人发起**，不轮询。 */
export const getSMSEmail = (emailId: string, refresh = false, o: ListOptions = {}, c: ApiClient = apiClient) =>
  getObject<SMSEmail>(
    `/api/v1/sms/emails/${encodeURIComponent(emailId)}${qs({ refresh: refresh ? 1 : undefined })}`,
    o,
    c,
  );

export const getSMS62GoodsDetail = (goodsId: string, o: ListOptions = {}, c: ApiClient = apiClient) =>
  getObject<SMS62GoodsDetail>(`/api/v1/sms/sms62/goods/${encodeURIComponent(goodsId)}`, o, c);
export const listSMS62Orders = (page = 1, pageSize = 20, o: ListOptions = {}, c: ApiClient = apiClient) =>
  getObject<SMS62OrdersPage>(`/api/v1/sms/sms62/orders${qs({ page, page_size: pageSize })}`, o, c);

/** 租用一个号（`sms.rent.purchase@1`）。**花真钱且按小时计费。** */
export function rentSMSNumber(
  params: {
    operation_id: string;
    service: string;
    country: number;
    duration_hours: number;
    operator?: string;
    currency?: number;
  },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "sms.rent.purchase", version: "1", params: { provider: "hero_sms", ...params } },
    options,
    client,
  );
}

/** 买邮箱接码（`sms.email.purchase@1`）。**花真钱。** count>1 走批量（上限 10）。 */
export function purchaseSMSEmails(
  params: { operation_id: string; site: string; domain: string; count?: number; service?: string },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "sms.email.purchase", version: "1", params: { provider: "hero_sms", ...params } },
    options,
    client,
  );
}

/** 邮箱取消 / 重下单（`sms.email.action@1`）。重下单可能花钱。 */
export function executeSMSEmailAction(
  params: { operation_id: string; email_id: string; kind: "email_cancel" | "email_reorder" },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction({ actionId: "sms.email.action", version: "1", params }, options, client);
}

/** 收藏（`sms.favorite.set@1` / `sms.favorite.remove@1`）。不花钱。 */
export function setSMSFavorite(
  params: { service: string; country: number; operator?: string },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "sms.favorite.set", version: "1", params: { provider: "hero_sms", ...params } },
    options,
    client,
  );
}
export function removeSMSFavorite(
  params: { service: string; country: number },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "sms.favorite.remove", version: "1", params: { provider: "hero_sms", ...params } },
    options,
    client,
  );
}


/** 向上游取一次码（`sms.code.fetch@1`）。
 *
 *  **人发起，不是后台轮询**：两家的限流都按密钥算。「还没有码」是正常状态，
 *  Action 成功、结果里 received=false；码本身不在结果里，走本地验证码端点。 */
export function fetchSMSCode(
  resourceId: string,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "sms.code.fetch", version: "1", params: { resource_id: resourceId } },
    options,
    client,
  );
}

/** 把在供应商后台下的单读进平台（`sms.order.import@1`）。**只读，不购买。**
 *  62 传订单 ID，Hero 传 activation ID。 */
export function importSMSOrder(
  params: { provider: string; upstream_ref: string },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction({ actionId: "sms.order.import", version: "1", params }, options, client);
}

// ---------- 路由规则（XM-SMS2 #5，ADR-022 决策 3） ----------

/** 一条路由规则：「服务 × 国家」→ 供应商优先级列表 + 单价上限。
 *  service / country 为 "*" 表示任意。命中顺序：精确 > 服务通配国家 >
 *  国家通配服务 > 全通配 > 没有规则时的默认顺序。 */
export interface SMSRoutingRule {
  rule_id: string;
  service: string;
  country: string;
  /** 优先级顺序，第一家优先；失败回落下一家，每家最多试一次。 */
  providers: string[];
  /** 十进制文本；空 = 不限。**按各家自己的币种比较，不折算。** */
  max_unit_price?: string;
  enabled: boolean;
  updated_at?: string;
}

export interface SMSRoutingRules {
  items: SMSRoutingRule[];
  /** 没有规则命中时要号按这个顺序试（= 装配顺序）。 */
  default_order: string[];
  wildcard: string;
}

export async function listSMSRoutingRules(
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<SMSRoutingRules> {
  const body = await client
    .get<Partial<SMSRoutingRules>>("/api/v1/sms/routing", {
      ...(options.signal ? { signal: options.signal } : {}),
    })
    .catch(translateUnmounted);
  return { items: body.items ?? [], default_order: body.default_order ?? [], wildcard: body.wildcard ?? "*" };
}

/** 新建或覆盖一条规则（`sms.routing.set@1`）。同「服务 × 国家」只有一条。
 *  不传 enabled 视为启用。 */
export function setSMSRoutingRule(
  input: { service: string; country: string; providers: string[]; max_unit_price?: string; enabled?: boolean },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  const params: Record<string, unknown> = {
    service: input.service,
    country: input.country,
    providers: input.providers,
  };
  if (input.max_unit_price) params.max_unit_price = input.max_unit_price;
  if (input.enabled !== undefined) params.enabled = input.enabled;
  return executeAction({ actionId: "sms.routing.set", version: "1", params }, options, client);
}

/** 删一条规则（`sms.routing.remove@1`）。可逆：再设一条同键的就回来了。 */
export function removeSMSRoutingRule(
  ruleId: string,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "sms.routing.remove", version: "1", params: { rule_id: ruleId } },
    options,
    client,
  );
}
