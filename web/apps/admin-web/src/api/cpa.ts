import { apiClient, type ApiClient } from "./client";
import type { ListOptions } from "./platform";
import type { CountBody } from "./users";

/** 一个**可能缺席**的定点金额，标度显式给出（httpapi/profit_daily.go
 *  `moneyItem`，UI 交接 §13 的 `Money` 形状）。
 *
 *  与 `api/users.ts` 的 `AmountBody` 不同：那里的金额已经是币种自己的最小
 *  单位（分），这里的 CPA 折算成本是 `money.MicroScale`（微 USD，10^-6），
 *  必须带 `scale` 字段——否则前端只能把 6 硬编码进某个格式化调用，
 *  一旦后端改了标度，所有金额会悄悄错 100 倍且不报错（moneyItem 的原注释）。
 *  `null` = 未配价，不是 $0（宪法 12 条）。 */
export interface MoneyBody {
  amount_minor: string;
  currency: string;
  scale: number;
}

/** `GET /api/v1/platforms/cpa/keys` 的一行（httpapi/cpa_keys.go cpaKeyUsageItem）。
 *
 *  `api_key_hash` 是 usage_events.api_key_hash 的原样只读值——单向哈希，
 *  从不还原成真实 key（宪法 7 条；connectors/cpa 包文档「从不挂载」清单）。 */
export interface CPAKeyUsageItem {
  api_key_hash: string;
  /** 空串 = api_key_aliases 里没有这个哈希的别名登记，正常状态，不是错误。 */
  alias: string;
  request_count: number;
  tokens_in: number;
  tokens_out: number;
  tokens_cache_read: number;
  tokens_cache_creation: number;
  cost: MoneyBody | null;
  /** > 0 表示 cost 是**部分**合计——这把 key 当天用过某个未配价的模型，
   *  cost 不是这把 key 的完整支出（宪法 12 条：禁止裸数字冒充完整数据）。 */
  unpriced_request_count: number;
  /** null 表示这把 key 当天没有任何请求落在返回的这一页里。 */
  last_used_at: string | null;
}

export interface CPAKeyUsagePage {
  business_day: string;
  items: CPAKeyUsageItem[];
  total_key_count: number;
  /** 目前后端从不截断这个端点的结果（截断的是 cpa.keys.usage 观测里的
   *  小样本，不是这个端点）；字段仍然带上，界面据此显示"还有 N 条"而不是
   *  假设永远是 false。 */
  truncated: boolean;
  snapshot: {
    observed_at: string;
    source: string;
    watermark: string;
    is_partial: boolean;
  };
}

export interface ListCPAKeysOptions extends ListOptions {
  /** UTC 业务日 YYYY-MM-DD；不传 = 服务端按 UTC 今天解释。 */
  day?: string;
}

/** 读取 CPA 逐 API Key 的当日用量（`platform.users.read`）。
 *
 *  与 platformusers 系列端点是两条完全不同的链路：CPA 没有终端用户身份，
 *  这里一行 = 一把 API Key（见 ScopeCPAKeysRead 在后端的注释）。 */
export async function listCPAKeys(
  options: ListCPAKeysOptions = {},
  client: ApiClient = apiClient,
): Promise<CPAKeyUsagePage> {
  const body = await client.get<CPAKeyUsagePage>("/api/v1/platforms/cpa/keys", {
    searchParams: { ...(options.day ? { day: options.day } : {}) },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return { ...body, items: body.items ?? [] };
}

// --- cpa.* 观测的 value 形状（读 GET /api/v1/metrics，不是专门端点）---
//
// 概览与渠道保障两页复用 /api/v1/metrics（ops.read），value 形状对应
// connectors/cpa/observations.go 写入的四条观测；这里只声明**读侧**关心的
// 字段子集，不是完整契约的镜像。

export interface CPARequestsValue {
  business_day?: string;
  total_request_count?: number;
  by_provider?: Record<string, number>;
  by_model?: Record<string, number>;
}

export interface CPACostValue {
  business_day?: string;
  total_cost_minor_units?: number;
  currency?: string;
  total_omitted_reason?: string;
  unpriced_request_count?: number;
  unpriced_models?: string[];
}

export interface CPAAccountAnomaly {
  account_key: string;
  display_account: string;
  provider: string;
  disabled: boolean;
  status: string;
  state: string;
  action: string;
  action_reason: string;
}

export interface CPAAccountsHealthValue {
  run_id?: string;
  account_count?: number;
  disabled_count?: number;
  anomaly_count?: number;
  anomalies?: CPAAccountAnomaly[];
  truncated?: boolean;
}

export const CPA_REQUESTS_METRIC_KEY = "cpa.requests.daily";
export const CPA_COST_METRIC_KEY = "cpa.cost.daily";
export const CPA_KEYS_METRIC_KEY = "cpa.keys.usage";
export const CPA_ACCOUNTS_METRIC_KEY = "cpa.accounts.health";

/** `CountBody` 只在这个模块里被当类型用（`api/users.ts` 已经导出了实现），
 *  重新导出一次是为了让 CPA 面板只需要从一个模块导入。 */
export type { CountBody };
