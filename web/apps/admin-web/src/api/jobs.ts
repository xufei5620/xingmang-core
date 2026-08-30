/** 后台任务页的 API 层（XM-JOBS0，ADMIN-IA §2.2 `g/jobs`）。
 *
 *  两个只读端点，都需要 ops.read：
 *   - GET /api/v1/jobs/overview —— 周期任务目录 + 队列积压 + worker 心跳快照；
 *   - GET /api/v1/jobs/runs —— river_job 的分页运行记录，按 kind / state 筛选。
 *
 *  字段形状对齐 internal/platform/httpapi/jobs.go 的响应 DTO，不是 river_job
 *  的直接序列化——后端已经把 args 过滤成白名单、错误文案截了断，这一层原样
 *  转发那个已经安全的结构，不再猜测或补全字段。 */
import { apiClient, type ApiClient } from "./client";
import { appApiConfig, type PlatformApiConfig } from "./config";
import type { ListOptions } from "./platform";

/** river_job_state 的七个取值（后端 jobs.RunState）。 */
export type JobRunState =
  | "available"
  | "cancelled"
  | "completed"
  | "discarded"
  | "retryable"
  | "running"
  | "scheduled";

/** 最近一次失败尝试（httpapi jobRunErrorBody）。已经过截断。 */
export interface JobRunError {
  at: string;
  message: string;
  truncated: boolean;
  original_length: number;
}

/** 一条 river_job 记录（httpapi jobRunItem）。 */
export interface JobRunItem {
  id: number;
  kind: string;
  queue: string;
  state: JobRunState;
  attempt: number;
  max_attempts: number;
  created_at: string;
  scheduled_at: string;
  attempted_at: string | null;
  finalized_at: string | null;
  /** null 表示还没有一次完整的尝试可以算耗时——不是 0 毫秒。 */
  duration_ms: number | null;
  error_count: number;
  last_error: JobRunError | null;
  /** 只含白名单字段（environment/source/business_day/run_id/
   *  approval_envelope_sha256 之类），绝不是整段 River args。 */
  args: Record<string, unknown>;
}

/** 一个已注册周期任务的目录项（httpapi jobScheduleBody）。
 *
 *  **没有 enabled 字段**：platform-api 进程读不到 platform-worker 手上的
 *  运行时配置，任何「已启用/已停用」的断言都会是装出来的事实。activity
 *  只描述「river_job 里看到了什么」，不是配置真相。 */
export interface JobScheduleStatus {
  id: string;
  kind: string;
  queue: string;
  /** 生产要改这个任务的周期得改哪个环境变量（不是这条周期的当前值）。 */
  schedule_config_env: string;
  side_effect_class: string;
  last_run: JobRunItem | null;
  /** 从最近两次出现的时间差推出来的观测值，不是配置值。 */
  observed_interval_seconds: number | null;
  next_run_estimated_at: string | null;
  activity: "activity_observed" | "no_recent_activity" | "never_observed";
}

/** 一个队列的积压快照（httpapi jobQueueBacklogBody）。
 *  completed_24h 只统计最近 24 小时——River 自带的清理任务会删掉更早的记录。 */
export interface JobQueueBacklogRow {
  queue: string;
  available: number;
  running: number;
  retryable: number;
  scheduled: number;
  completed_24h: number;
  discarded: number;
}

/** worker 心跳投影（httpapi jobWorkerHeartbeatBody）。
 *  known=false 表示 river_job 里从未出现过心跳记录，不是「0 秒前」。 */
export interface JobWorkerHeartbeat {
  last_seen_at: string | null;
  seconds_ago: number | null;
  state: JobRunState | "";
  environment: string;
  known: boolean;
}

/** GET /api/v1/jobs/overview 的响应体。 */
export interface JobsOverview {
  environment: string;
  generated_at: string;
  schedules: JobScheduleStatus[];
  queue_backlog: JobQueueBacklogRow[];
  worker_heartbeat: JobWorkerHeartbeat;
}

/** 拉取周期任务目录、队列积压与 worker 心跳的一次性快照。
 *
 *  environment 跟随调用者身份（跨环境读取后端会拒），与 /api/v1/metrics 同一条
 *  规则——即便这批数据今天其实是平台级、不按环境拆分的（见后端
 *  jobs.QueryStore 的文件头注释），这个参数在契约上仍然存在，为未来某个
 *  任务的 args 真的带上 environment 时留一条能自动生效的路径。 */
export async function getJobsOverview(
  options: ListOptions = {},
  client: ApiClient = apiClient,
  config: PlatformApiConfig = appApiConfig,
): Promise<JobsOverview> {
  return client.get<JobsOverview>("/api/v1/jobs/overview", {
    searchParams: { environment: config.environment },
    ...(options.signal ? { signal: options.signal } : {}),
  });
}

/** 每页默认条数，跟随后端 jobs.DefaultRunsLimit。 */
export const JOB_RUNS_PAGE_SIZE = 50;

export interface JobRunPage {
  items: JobRunItem[];
  /** 下一页的游标（下一条 id 上界）；null 表示没有更多了。 */
  nextBefore: number | null;
}

export interface ListJobRunsOptions extends ListOptions {
  /** 不传或空数组 = 不筛类型；多个值按「其中任意一个」匹配（服务端一次查询完成，
   *  不必自己拼多条游标各翻各的页）。 */
  kinds?: readonly string[];
  /** 不传 = 不筛状态。 */
  state?: JobRunState;
  /** 只取 id 小于该值的记录；不传表示从最新一条开始。 */
  before?: number;
  limit?: number;
}

interface JobRunsResponse {
  items: JobRunItem[] | null;
  next_before?: number;
}

/** 按可选的 kinds / state 游标分页列出最近的运行记录（最新在前）。 */
export async function listJobRuns(
  options: ListJobRunsOptions = {},
  client: ApiClient = apiClient,
  config: PlatformApiConfig = appApiConfig,
): Promise<JobRunPage> {
  const body = await client.get<JobRunsResponse>("/api/v1/jobs/runs", {
    searchParams: {
      environment: config.environment,
      kind: options.kinds && options.kinds.length > 0 ? options.kinds.join(",") : undefined,
      state: options.state,
      // 首页不传 before：传 0 会被当成「只要 id < 0 的记录」，也就是一条都没有
      before: options.before === undefined ? undefined : String(options.before),
      limit: String(options.limit ?? JOB_RUNS_PAGE_SIZE),
    },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  // 契约：next_before 为 0 或缺失都表示到底了（与 /api/v1/audit/events 同一条约定）。
  const next = body.next_before;
  return {
    items: body.items ?? [],
    nextBefore: next === undefined || next <= 0 ? null : next,
  };
}

/** 六个已注册周期任务 + 一个手动触发任务的中文名（后端 job kind 常量，
 *  internal/platform/jobs 的 *JobKind）。
 *
 *  未知 kind 不隐藏也不报错（见 jobKindLabel）：后端加了新任务而前端还没
 *  跟上时，显示原始 kind 仍然是有用的信息。 */
const JOB_KIND_LABELS: Readonly<Record<string, string>> = {
  platform_heartbeat: "平台心跳",
  sub2api_sync: "Sub2API 同步",
  newapi_sync: "NewAPI 同步",
  finance_cost_sync: "成本采集",
  retention_prune: "保留期清理",
  alert_evaluate: "告警评估",
  audit_archive_manual: "审计归档（手动触发）",
};

/** 把 job kind 翻成中文名；未知 kind 原样返回。 */
export function jobKindLabel(kind: string): string {
  return JOB_KIND_LABELS[kind] ?? kind;
}

/** 「同步批次」页签覆盖的三个数据连接器同步任务——严格来说是「从上游/自己的
 *  账本读数据进平台」的那一类，与心跳、清理、告警评估（平台内部维护）区分开。 */
export const SYNC_JOB_KINDS: readonly string[] = [
  "sub2api_sync",
  "newapi_sync",
  "finance_cost_sync",
];

const JOB_STATE_LABELS: Readonly<Record<JobRunState, string>> = {
  available: "待执行",
  running: "运行中",
  retryable: "等待重试",
  scheduled: "计划中",
  completed: "已完成",
  discarded: "多次失败（已放弃）",
  cancelled: "已取消",
};

/** 把 river_job 状态翻成中文名；未知状态显式暴露而不是隐藏
 *  （理由同 describeFreshness：前端不认识的状态不该被静默按「正常」处理）。 */
export function jobStateLabel(state: string): string {
  return JOB_STATE_LABELS[state as JobRunState] ?? `未知状态（${state}）`;
}
