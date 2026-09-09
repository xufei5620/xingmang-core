import { apiClient, type ApiClient } from "./client";

/** 操作与审批页（`/actions`，ADMIN-IA §2.2 `g/actions`）的三个数据源：
 *  操作目录、执行记录列表、执行记录详情。
 *
 *  「待审批」子页签走的是另一份客户端（api/approvals.ts，XM-0030b）——
 *  审批中心的后端已实装，只是还没在环境里启用，端点不存在时那一层会把裸 404
 *  翻成「未启用」而不是一个泛型报错。 */

/** Action 目录条目（`GET /api/v1/actions`，httpapi/actions.go actionSummary）。
 *
 *  本端点不挂在任何 scope 之后——目录本身不含实例数据，只是「有哪些声明」，
 *  真正的授权判定在执行时由内核裁决（httpapi/router.go 的注释）。 */
export interface ActionDefinitionItem {
  id: string;
  version: string;
  risk_level: string;
  permission: string;
  environments: string[];
  principal_types: string[];
  /** 当前 Foundation 阶段是否可执行；隐藏/灰显不构成安全控制，服务端仍会拒绝。 */
  executable: boolean;
  /** 不可执行时的原因；executable=true 时为空串。 */
  blocked_reason: string;
}

interface RawActionDefinitionItem {
  id: string;
  version: string;
  risk_level: string;
  permission: string;
  environments?: string[] | null;
  principal_types?: string[] | null;
  executable: boolean;
  blocked_reason?: string;
}

/** 列出已注册的 Action 声明（操作与审批页「操作目录」「风险与启用条件」
 *  两个子页签共用同一份数据，只是分组方式不同）。 */
export async function listActionDefinitions(
  options: { signal?: AbortSignal } = {},
  client: ApiClient = apiClient,
): Promise<ActionDefinitionItem[]> {
  const body = await client.get<{ items: RawActionDefinitionItem[] | null }>("/api/v1/actions", {
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return (body.items ?? []).map((raw) => ({
    id: raw.id,
    version: raw.version,
    risk_level: raw.risk_level,
    permission: raw.permission,
    environments: raw.environments ?? [],
    principal_types: raw.principal_types ?? [],
    executable: raw.executable,
    blocked_reason: raw.blocked_reason ?? "",
  }));
}

/** 一条跨 Action 执行记录（`GET /api/v1/actions/runs`，
 *  httpapi/actionruns.go actionRunItem）。不含 before/after——那部分只在
 *  详情端点、且调用者另有 audit.read 时才出现（见 ActionRunAuditSummary）。 */
export interface ActionRunItem {
  id: string;
  action_id: string;
  action_version: string;
  principal_id: string;
  principal_type: string;
  environment: string;
  request_id: string;
  risk_level: string;
  status: "succeeded" | "failed";
  /** status=succeeded 时为空串。 */
  error_code: string;
  duration_ms: number;
  /** RFC3339Nano，UTC。 */
  started_at: string;
  finished_at: string;
}

export interface ActionRunPage {
  items: ActionRunItem[];
  /** 空串表示已经翻到底。不透明字符串，不解析、不构造。 */
  nextCursor: string;
}

export type ActionRunStatusFilter = "" | "succeeded" | "failed";

export interface ActionRunListOptions {
  actionId?: string;
  status?: ActionRunStatusFilter;
  principal?: string;
  limit?: number;
  cursor?: string;
  signal?: AbortSignal;
}

/** 空串不进 URL：「不传」和「传空串」对后端不是一回事（见 client.ts）。 */
function omitEmpty(value: string | undefined): string | undefined {
  const trimmed = (value ?? "").trim();
  return trimmed === "" ? undefined : trimmed;
}

/** 每页拉多少条（不传 limit 时的默认值，须与 action.DefaultRunListLimit 一致，
 *  仅用于前端本地展示口径，服务端仍以自己的默认值为准）。 */
export const ACTION_RUN_PAGE_SIZE = 50;

/** 跨 Action 分页列出执行记录（操作与审批页「执行记录」子页签）。
 *
 *  环境范围：不接受 environment 参数——一律是调用者 Principal 的环境
 *  （规格 §20.5，与请求详情、审计事件同一条纪律）。 */
export async function listActionRuns(
  options: ActionRunListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRunPage> {
  const body = await client.get<{ items: ActionRunItem[] | null; next_cursor?: string }>(
    "/api/v1/actions/runs",
    {
      searchParams: {
        action_id: omitEmpty(options.actionId),
        status: omitEmpty(options.status),
        principal: omitEmpty(options.principal),
        limit: options.limit ? String(options.limit) : undefined,
        cursor: omitEmpty(options.cursor),
      },
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
  return { items: body.items ?? [], nextCursor: body.next_cursor ?? "" };
}

/** 执行记录详情附带的审计摘要（`GET /api/v1/actions/runs/{run_id}`，
 *  httpapi/actionruns.go actionRunAuditSummary）。只摘要、不含凭据——
 *  before/after 里出现什么由业务 Handler 写入时自行负责脱敏（规格 §4.4）。 */
export interface ActionRunAuditSummary {
  resource_type: string;
  resource_id: string;
  reason: string;
  /** null 表示「这个动作没有前后镜像」；非 null 但为空对象表示「记录了摘要，
   *  内容为空」——两者不是一回事。 */
  before_summary: Record<string, unknown> | null;
  after_summary: Record<string, unknown> | null;
  result: string;
  occurred_at: string;
}

/** 执行记录详情。`audit` 为 null 表示没有找到关联的审计事件（理论上不应
 *  发生——kernel.go 的 record/recordAudit 总是成对调用——但审计写入失败不
 *  回滚业务结果，界面必须能诚实呈现这种缺口，不能假装恒有）。 */
export interface ActionRunDetail {
  run: ActionRunItem;
  audit: ActionRunAuditSummary | null;
}

/** 读取单条执行记录详情。
 *
 *  权限：服务端要求 action.read + audit.read 都持有（详情带出的 before/after
 *  与审计事件本身同一档敏感度）。缺任一个都会 403，前端据此显示「缺哪个
 *  scope」而不是空白（ApiStateView 已处理 403 的展示）。 */
export async function getActionRun(
  runId: string,
  options: { signal?: AbortSignal } = {},
  client: ApiClient = apiClient,
): Promise<ActionRunDetail> {
  return client.get<ActionRunDetail>(`/api/v1/actions/runs/${encodeURIComponent(runId)}`, {
    ...(options.signal ? { signal: options.signal } : {}),
  });
}
