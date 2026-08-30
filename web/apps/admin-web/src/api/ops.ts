/** 运行保障总览的 API 层（XM-OPS0）。
 *
 *  后端只给一个只读聚合端点：`GET /api/v1/ops/overview`。它把控制平面自己的
 *  健康状况拼成一份快照——worker 心跳、两条采集链路（sub2api/newapi）、两个
 *  连接器健康检查、告警投递渠道是否配置、保留期清理任务与数据库连通性。
 *
 *  这一层只做「按契约取数」，展示判断（状态徽章、新鲜度文案、依赖列怎么拼）
 *  一律不放在这里，见 lib/ops.ts——与 alerts.ts / lib/alerts.ts 同一条分工。 */
import { apiClient, type ApiClient } from "./client";
import { appApiConfig, type PlatformApiConfig } from "./config";

/** 五个新鲜度状态，逐字对齐后端 `internal/platform/ops/freshness.go` 的 `ops.State`。
 *
 *  与 `@xingmang/ui-admin` 的 `FreshnessState` 是同一份后端契约的两处前端副本
 *  （那边的 `FreshnessContract.state` 收成了 `string`，因为要同时服务好几个
 *  指标契约）；这里收紧成字面量联合类型，坏值能在 typecheck 就地暴露。两者
 *  取值集合必须一致——`describeFreshness` 因此可以直接吃这里的 `.state`。 */
export type OpsFreshnessState = "uninitialized" | "failed" | "stale" | "partial" | "fresh";

/** 单个指标的新鲜度契约，形状与 `@xingmang/ui-admin` 的 `FreshnessContract` 逐字一致。 */
export interface OpsFreshness {
  state: OpsFreshnessState;
  /** null = 从未观测过。 */
  staleness_seconds: number | null;
  threshold_seconds: number;
  is_partial: boolean;
  observed_at: string | null;
  last_success: string | null;
  /** 健康时为空串。 */
  last_error_code: string;
}

/** 一个指标的快照：取值 + 这份取值有多新鲜。value 是未类型化的 JSON——
 *  不同 metric_key 的形状不同，读取时按字段防御式取（见 lib/ops.ts）。 */
export interface OpsMetricSnapshot {
  metric_key: string;
  source: string;
  value: Record<string, unknown>;
  freshness: OpsFreshness;
}

export interface OpsBuildInfo {
  version: string;
  commit: string;
  environment: string;
}

/** 一条采集链路（sub2api 或 newapi 的凭据/同步配置）。 */
export interface OpsSyncPipeline {
  /** "sub2api_sync" | "newapi_sync" */
  kind: string;
  /** "sub2api" | "newapi" */
  platform: string;
  /** 只有这个部署压根没挂载凭据模块时才是 false——与「挂载了但还没配置」不是一回事,
   *  两者混着显示会让人以为配一下就能用，实际上这个部署没有这个模块。 */
  config_available: boolean;
  /** "fake" | "real" | ""（仅当 config_available 为 false 时为空）。 */
  effective_mode: string;
  config_updated_at: string | null;
  /** 这一行的新鲜度取自哪个指标，例如 "sub2api.channels.status"。 */
  sample_metric_key: string;
  source: string;
  freshness: OpsFreshness;
}

export interface OpsAlertDelivery {
  telegram_configured: boolean;
  webhook_configured: boolean;
}

export interface OpsDatabaseStatus {
  connected: boolean;
}

/** `GET /api/v1/ops/overview` 的响应体。
 *
 *  sync_pipelines 与 connector_health 按契约**始终**是 2 个元素（sub2api 在前，
 *  newapi 在后），从不为 null——这一顺序是契约保证，不是经验假设。 */
export interface OpsOverview {
  build: OpsBuildInfo;
  worker_heartbeat: OpsMetricSnapshot;
  sync_pipelines: OpsSyncPipeline[];
  connector_health: OpsMetricSnapshot[];
  alert_delivery: OpsAlertDelivery;
  retention: OpsMetricSnapshot;
  database: OpsDatabaseStatus;
}

/** 取控制平面运行保障总览。只读、无分页、无筛选——一次请求换一份完整快照。
 *
 *  environment 跟随调用者身份的配置，不接受调用方指定：跨环境读取本来就该被
 *  后端拒绝，前端也不该假装能选（与 alerts.ts 的 listAlerts 同一条道理）。 */
export async function getOpsOverview(
  client: ApiClient = apiClient,
  config: PlatformApiConfig = appApiConfig,
  signal?: AbortSignal,
): Promise<OpsOverview> {
  return client.get<OpsOverview>("/api/v1/ops/overview", {
    searchParams: { environment: config.environment },
    ...(signal ? { signal } : {}),
  });
}
