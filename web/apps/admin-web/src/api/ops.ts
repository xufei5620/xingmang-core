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
  /** "fake" | "real" | ""。
   *
   *  **空串是「本进程不知道」，不是「没配置」**（XM-OPS-TRUTH 子片 A 起的语义）。
   *  为什么不知道，由下一个字段说；不要拿 `=== "fake"` 之类的判断把空串落进
   *  未定义分支。 */
  effective_mode: string;
  /** 上一行那个答案是**从哪来的**："database" | "unknown" | ""。
   *
   *  - `"database"`：`core.connector_config` 里有这一行，`effective_mode` 是确定值。
   *  - `"unknown"`：库里没有这一行。那一轮实际按 **worker 进程**的
   *    `XM_SUB2API_MODE` / `XM_NEWAPI_MODE` 缺省跑，而 platform-api 容器根本没有
   *    这两个键（它不跑同步），所以它诚实地说「我不知道」而不是猜一个 fake。
   *  - `""`：`config_available` 为 false，两个字段一起是空的。
   *
   *  **永远不会是 `"env"`**：那是 worker 才答得出的来源，这个端点答不出。
   *
   *  可选是为了老后端还没有这一列的那段部署窗口——缺席时
   *  `describeSyncMode` 退回只看 `effective_mode` 的旧口径。 */
  effective_mode_source?: string;
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

/** 某一类失败作业最近那一条的错误投影（Go 侧 `opsFailedJobErrorBody`）。
 *
 *  `truncated` / `original_length` 一起给：一条被截断的错误如果不说自己被截断了，
 *  读的人会以为上游就说了这么多。null 表示那条作业没有记错误。 */
export interface OpsFailedJobError {
  at: string;
  message: string;
  truncated: boolean;
  original_length: number;
}

/** 某一类后台作业在回看窗口内的失败摘要（Go 侧 `opsFailedJobKindBody`）。
 *
 *  这一段存在的理由是 2026-09-08 现场那一格：card_sync 24 小时内 288 条
 *  discarded，工作台「我的待处理」只取最新 20 条再自己按类型分组，于是那一行
 *  只能写「×20+」——一个既不是真数、又看不出真数有多大的数字。合并所需的
 *  **条数、最早与最近时刻、类型**由后端一次算好，前端不再自己数。 */
export interface OpsFailedJobKind {
  kind: string;
  /** 窗口内这一类失败了几次。 */
  count: number;
  /** 窗口内最早一次。 */
  first_at: string;
  /** 窗口内最近一次。 */
  last_at: string;
  /** 跳去 `/api/v1/jobs/runs` 定位那一条用。 */
  last_run_id: number;
  /** 最近那条作业的尝试次数——**不是本类的失败总数**，那是 `count`。 */
  error_count: number;
  last_error: OpsFailedJobError | null;
}

/** `failed_jobs_by_kind` 那一段的三态，逐字对齐后端的 `opsFailedJobs*` 常量。
 *
 *  **前端要 switch 这个字段，不要判 `failed_jobs_by_kind` 是不是 null。**
 *  这一格此前用同一个 JSON `null` 表达两件完全不同的事：「这个部署没接 jobs
 *  数据源」和「接了，但这次查库失败了」。前者是良性的部署事实，后者是「运行
 *  保障页这一格正瞎着」——要人去看。有了这个字段，null 才只剩「没有数据」
 *  一个意思，为什么没有由它说。 */
export type OpsFailedJobsStatus = "ok" | "not_wired" | "query_failed";

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
  /** null 表示这一段**没有数据**，为什么没有由 `failed_jobs_status` 说；
   *  空数组表示查过了、窗口内一条失败作业都没有。两者不是一回事。 */
  failed_jobs_by_kind: OpsFailedJobKind[] | null;
  failed_jobs_status: OpsFailedJobsStatus;
  /** 上一段的回看窗口，让「288 次」能说成「24 小时内 288 次」而不是一个
   *  没有量纲的数。 */
  failed_jobs_window_hours: number;
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
