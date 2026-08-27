import type { FreshnessContract } from "@xingmang/ui-admin";
import { apiClient, type ApiClient } from "./client";
import { appApiConfig, type PlatformApiConfig } from "./config";
import { newRequestId } from "../lib/ids";

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

// --- 指标历史（GET /api/v1/metrics/history）---

/** 一条历史观测（后端按 observed_at 升序返回）。
 *
 *  与 MetricItem 的区别：历史条目**没有** freshness——新鲜度是「相对现在有多旧」，
 *  对一条历史样本问这个问题没有意义。趋势图要的是 status 与 value。 */
export interface MetricHistoryItem {
  /** null 表示上游没给观测时刻，只能用 synced_at 定位。 */
  observed_at: string | null;
  synced_at: string;
  status: string;
  is_partial: boolean;
  watermark: string;
  last_error_code: string;
  value: Record<string, unknown> | null;
}

/** 趋势图默认回看窗口。24 小时对齐总览卡片上「近一天」的说法。 */
export const METRIC_HISTORY_HOURS = 24;

export interface MetricHistoryOptions extends ListOptions {
  hours?: number;
}

/** 读取单个指标的历史观测序列，供迷你趋势图使用。 */
export async function listMetricHistory(
  metricKey: string,
  options: MetricHistoryOptions = {},
  client: ApiClient = apiClient,
  config: PlatformApiConfig = appApiConfig,
): Promise<MetricHistoryItem[]> {
  const body = await client.get<ListResponse<MetricHistoryItem>>("/api/v1/metrics/history", {
    searchParams: {
      metric_key: metricKey,
      hours: String(options.hours ?? METRIC_HISTORY_HOURS),
      // environment 的传法与 listMetrics 一致：显式配了才传，
      // 猜一个只会换来 403（见 config.ts 的说明）
      ...envParams(config),
    },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return body.items ?? [];
}

// --- 审计事件（GET /api/v1/audit/events，需 audit.read）---

/** 一条审计事件（字段对齐 audit.Event 与规格 §4.4）。 */
export interface AuditEventItem {
  sequence: number;
  occurred_at: string;
  principal_id: string;
  principal_type: string;
  action_id: string;
  action_version: string;
  action_run_id: string;
  resource_type: string;
  resource_id: string;
  environment: string;
  request_id: string;
  result: string;
  error_code: string;
  /** null 表示「没有前态」（新建），与「前态是空对象」不是一回事。 */
  before_summary: Record<string, unknown> | null;
  after_summary: Record<string, unknown> | null;
  event_hash: string;
  prev_hash: string;
}

export interface AuditPage {
  items: AuditEventItem[];
  /** 下一页的游标（下一条 sequence 上界）；null 表示没有更多了。 */
  nextBefore: number | null;
}

/** 审计页每次拉多少条。 */
export const AUDIT_PAGE_SIZE = 50;

export interface AuditListOptions extends ListOptions {
  limit?: number;
  /** 只取 sequence 小于该值的事件；不传表示从最新一条开始。 */
  beforeSeq?: number;
}

interface AuditResponse {
  items: AuditEventItem[] | null;
  next_before?: number;
}

/** 按序号倒序列出审计事件（最新的在前）。 */
export async function listAuditEvents(
  options: AuditListOptions = {},
  client: ApiClient = apiClient,
): Promise<AuditPage> {
  const body = await client.get<AuditResponse>("/api/v1/audit/events", {
    searchParams: {
      limit: String(options.limit ?? AUDIT_PAGE_SIZE),
      // 首页不传 before_seq：传 0 会被当成「只要 sequence < 0 的事件」，
      // 也就是一条都没有
      before_seq: options.beforeSeq === undefined ? undefined : String(options.beforeSeq),
    },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  // 契约：next_before 为 0 或缺失都表示到底了。0 是合法的 falsy 值，
  // 必须显式判，不能靠 `body.next_before || null` —— 那样写没错，但下一个人
  // 读到时分不清是「刻意」还是「顺手」
  const next = body.next_before;
  return {
    items: body.items ?? [],
    nextBefore: next === undefined || next <= 0 ? null : next,
  };
}

// --- 写路径（POST /api/v1/actions/{id}/versions/{version}/execute）---

/** Action 执行结果。
 *
 *  字段名照抄 httpapi.executeActionResponse：`action_run_id` + `result`。 */
interface ExecuteActionResponse {
  action_run_id?: string;
  /** 后端若改名成 run_id 也认——只用于显示，认宽一点不会误导人。 */
  run_id?: string;
  result?: unknown;
}

export interface ActionRun {
  /** 规格 §5.8：所有写接口返回 action_run_id，界面必须把它显示出来。 */
  runId: string;
  result: unknown;
}

export interface ExecuteActionInput {
  actionId: string;
  version: string;
  params: Record<string, unknown>;
  /** 不传则现生成一个。显式传是为了让「同一次提交重试」能带同一个 ID。 */
  requestId?: string;
}

/** 执行一个 Action。
 *
 *  请求体只有 `params`：后端 ExecuteActionHandler 用 DisallowUnknownFields 解析，
 *  多塞一个 request_id 会 400。request_id 走 `X-Request-ID` 头
 *  （httpapi/middleware.go 的 RequestID 中间件读它，写进审计事件与访问日志）。 */
export async function executeAction(
  input: ExecuteActionInput,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  const requestId = input.requestId ?? newRequestId();
  const path = `/api/v1/actions/${encodeURIComponent(input.actionId)}/versions/${encodeURIComponent(input.version)}/execute`;
  const body = await client.post<ExecuteActionResponse>(
    path,
    { params: input.params },
    { requestId, ...(options.signal ? { signal: options.signal } : {}) },
  );
  return { runId: body.action_run_id ?? body.run_id ?? "", result: body.result };
}

/** 登记服务（registry.service.create@1，Permission = registry.service.manage）。 */
export function createService(
  params: Record<string, string>,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "registry.service.create", version: "1", params },
    options,
    client,
  );
}

/** 上报一次服务观测（registry.service.observe@1）。
 *
 *  这是「数据活了」的那一下：服务的新鲜度会从「未初始化」变成有时间戳。 */
export function observeService(
  params: Record<string, string>,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "registry.service.observe", version: "1", params },
    options,
    client,
  );
}
