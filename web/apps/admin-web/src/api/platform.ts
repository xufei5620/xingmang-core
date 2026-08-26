import type { FreshnessContract } from "@xingmang/ui-admin";
import { apiClient, type ApiClient } from "./client";
import { appApiConfig, type PlatformApiConfig } from "./config";

/** `GET /api/v1/services` 的一条记录（httpapi/queries.go serviceItem）。 */
export interface ServiceItem {
  id: string;
  service_type: string;
  instance_id: string;
  environment: string;
  endpoint: string;
  owner: string;
  status: string;
  source_watermark: string;
  /** null 表示从未成功采集——前端据此显示「未初始化」而不是 0（规格 §9.1）。 */
  observed_at: string | null;
  stale_seconds: number | null;
}

/** `GET /api/v1/metrics` 的一条记录（httpapi/metrics.go metricItem）。
 *  freshness 不是可选字段：后端用结构本身保证「值必带新鲜度」，前端照抄这个约束。 */
export interface MetricItem {
  metric_key: string;
  source: string;
  environment: string;
  watermark: string;
  /** Go 侧是 map[string]any，可能序列化成 null——所以类型里必须允许 null。 */
  value: Record<string, unknown> | null;
  freshness: FreshnessContract;
}

interface ListResponse<T> {
  items: T[] | null;
}

export interface ListOptions {
  signal?: AbortSignal;
}

function envParams(config: PlatformApiConfig): Record<string, string | undefined> {
  return { environment: config.environment };
}

/** 列出某环境下的 Service。 */
export async function listServices(
  options: ListOptions = {},
  client: ApiClient = apiClient,
  config: PlatformApiConfig = appApiConfig,
): Promise<ServiceItem[]> {
  const body = await client.get<ListResponse<ServiceItem>>("/api/v1/services", {
    searchParams: envParams(config),
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return body.items ?? [];
}

/** 列出某环境下的指标观测及其新鲜度。 */
export async function listMetrics(
  options: ListOptions = {},
  client: ApiClient = apiClient,
  config: PlatformApiConfig = appApiConfig,
): Promise<MetricItem[]> {
  const body = await client.get<ListResponse<MetricItem>>("/api/v1/metrics", {
    searchParams: envParams(config),
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return body.items ?? [];
}

/** 服务采集的新鲜度阈值（秒）。
 *
 *  services 端点只给 observed_at / stale_seconds，没有阈值——registry 里就没有
 *  这个字段。所以「多久算旧」必须由前端定，并且**显示出来**：藏起来的常量
 *  等于藏起来的判断标准。1800 秒对齐 Sub2API 连接器的默认阈值
 *  （connectors/sub2api/contract.go: defaultStalenessThresholdSeconds）。 */
export const SERVICE_STALENESS_THRESHOLD_SECONDS = 1800;

/** 把 Service 的采集时间折算成统一的新鲜度形状，好复用同一个徽章。
 *
 *  只可能产出 uninitialized / stale / fresh：Service 记录不带同步状态与
 *  完整性标记，凭空造出 failed / partial 就是编数据。 */
export function serviceFreshness(service: ServiceItem): FreshnessContract {
  const base = {
    staleness_seconds: service.stale_seconds,
    threshold_seconds: SERVICE_STALENESS_THRESHOLD_SECONDS,
    is_partial: false,
    observed_at: service.observed_at,
    last_success: service.observed_at,
    last_error_code: "",
  };
  if (service.observed_at === null || service.stale_seconds === null) {
    return { ...base, state: "uninitialized", staleness_seconds: null, observed_at: null };
  }
  const state = service.stale_seconds >= SERVICE_STALENESS_THRESHOLD_SECONDS ? "stale" : "fresh";
  return { ...base, state };
}
