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
  "接码在当前环境未启用（XM_SMS_MODE=off）。配好供应商清单与密钥引用后会自动出现。";

function translateUnmounted(error: unknown): never {
  if (looksLikeUnmountedRoute(error)) {
    throw new FeatureNotMountedError(error, SMS_NOT_MOUNTED_DESCRIPTION);
  }
  throw error;
}

export interface SMSProvider {
  provider: string;
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
