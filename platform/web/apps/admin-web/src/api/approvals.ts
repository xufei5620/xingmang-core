import {
  apiClient,
  FeatureNotMountedError,
  looksLikeUnmountedRoute,
  type ApiClient,
} from "./client";

/** 审批中心（`/api/v1/approvals*`，XM-0030b）。
 *
 *  后端契约见 internal/platform/httpapi/approvals.go。这一层只做取数与错误
 *  翻译，不做任何资格判断——能不能投票、能不能执行由服务端裁决（前端隐藏
 *  不构成安全控制）。 */

/** 审批相关的三个 scope（rolepermissions/rolemap.go）。
 *  写在这里是为了让「缺哪个权限」的提示能给出准确的名字。 */
export const APPROVAL_READ_PERMISSION = "approval.read";
export const APPROVAL_DECIDE_PERMISSION = "approval.decide";
export const APPROVAL_L4_PERMISSION = "approval.l4";

/** 审批中心整组端点在本环境没挂上时的说明。
 *
 *  与卡片/请求日志的「按环境变量可选挂载」不是同一回事，但表现一样：
 *  `router.go` 里 `if d.Approvals != nil` 为假时这组路由压根不存在，chi 回
 *  裸 404。这句话要说清楚**它不是坏了**，也不是「审批很慢」。
 *
 *  XM-0030-ENABLE 之后这句话必须改口径：`main.go` 现在**无条件**构造
 *  approval.Service 并注入，所以再看到 404 已经不是「等启用」了，而是这套
 *  前端在对一个**启用之前的旧后端**说话（典型场景：前端先发、platform-api
 *  还没滚上去）。写清这一点，人才知道该去看后端版本，而不是去等一个不会
 *  再来的排期。 */
const APPROVALS_NOT_MOUNTED_DESCRIPTION =
  "这套后台连上的 platform-api 还没有审批中心（整组 /api/v1/approvals 端点返回 404）。" +
  "审批中心自 XM-0030-ENABLE 起已在后端无条件启用，所以这通常意味着后端仍是启用之前的版本——" +
  "请确认 platform-api 已滚到含该变更的版本，而不是等待排期。" +
  "在此之前内核对 L2 及以上一律拒绝执行（ADVANCED_CONTROLS_REQUIRED），" +
  "不会有任何动作停在这里等审批。";

function translateUnmounted(error: unknown): never {
  if (looksLikeUnmountedRoute(error)) {
    throw new FeatureNotMountedError(error, APPROVALS_NOT_MOUNTED_DESCRIPTION);
  }
  throw error;
}

export type ApprovalStatus =
  | "PENDING"
  | "APPROVED"
  | "REJECTED"
  | "EXECUTED"
  | "EXPIRED"
  | "CANCELLED";

export type ApprovalVerdict = "APPROVE" | "REJECT";

export interface ApprovalDecision {
  approver_id: string;
  approver_type: string;
  verdict: ApprovalVerdict;
  comment: string;
  /** 投票那一刻此人是否持 approval.l4。**冻结在票上**：权限会变动，
   *  而「当时算不算特权票」是审计事实。 */
  privileged: boolean;
  created_at: string;
}

export interface ApprovalItem {
  id: string;
  action_id: string;
  action_version: string;
  risk_level: string;
  params: Record<string, unknown>;
  params_hash: string;
  requester_id: string;
  requester_type: string;
  reason: string;
  status: ApprovalStatus;
  created_at: string;
  expires_at: string;
  decided_at?: string;
  executed_at?: string;
  action_run_id?: string;
  decisions: ApprovalDecision[];
  /** 这个等级需要几票。后端按 Policy 算好给前端，**不让界面自己数**——
   *  票数规则是配置，散一份到前端迟早与后端分叉。 */
  votes_required: number;
  votes_cast: number;
  /** 这个等级是否需要至少一张特权票。为真且尚未收到时，界面要说清楚
   *  「票数够了但还缺一张特权票」——否则「两票齐了却还没批」看起来像故障。 */
  privileged_vote_required: boolean;
  privileged_vote_cast: boolean;
}

export interface ApprovalListResult {
  items: ApprovalItem[];
  limit: number;
  /** 取满上限：这一屏**可能**不是全部。 */
  truncated: boolean;
}

interface RawApprovalListResponse {
  items?: RawApprovalItem[] | null;
  limit?: number;
  truncated?: boolean;
}

interface RawApprovalItem extends Omit<ApprovalItem, "params" | "decisions"> {
  params?: Record<string, unknown> | null;
  decisions?: ApprovalDecision[] | null;
}

function normalize(raw: RawApprovalItem): ApprovalItem {
  return { ...raw, params: raw.params ?? {}, decisions: raw.decisions ?? [] };
}

export interface ListApprovalsOptions {
  status?: ApprovalStatus;
  limit?: number;
  signal?: AbortSignal;
}

/** 列出审批单。status 省略表示不过滤。 */
export async function listApprovals(
  options: ListApprovalsOptions = {},
  client: ApiClient = apiClient,
): Promise<ApprovalListResult> {
  const body = await client
    .get<RawApprovalListResponse>("/api/v1/approvals", {
      searchParams: {
        status: options.status,
        limit: options.limit === undefined ? undefined : String(options.limit),
      },
      ...(options.signal ? { signal: options.signal } : {}),
    })
    .catch(translateUnmounted);
  return {
    items: (body.items ?? []).map(normalize),
    limit: body.limit ?? 0,
    truncated: body.truncated ?? false,
  };
}

export async function getApproval(
  id: string,
  options: { signal?: AbortSignal } = {},
  client: ApiClient = apiClient,
): Promise<ApprovalItem> {
  const raw = await client
    .get<RawApprovalItem>(`/api/v1/approvals/${encodeURIComponent(id)}`, {
      ...(options.signal ? { signal: options.signal } : {}),
    })
    .catch(translateUnmounted);
  return normalize(raw);
}

/** 投一票。投票人取自身份令牌，请求体里不带——说自己是谁不算数。 */
export async function decideApproval(
  id: string,
  verdict: ApprovalVerdict,
  comment: string,
  client: ApiClient = apiClient,
): Promise<ApprovalItem> {
  const raw = await client.post<RawApprovalItem>(
    `/api/v1/approvals/${encodeURIComponent(id)}/decide`,
    { verdict, comment },
  );
  return normalize(raw);
}

/** 撤回自己的单。归属由服务端按 requester_id 判。 */
export async function cancelApproval(
  id: string,
  client: ApiClient = apiClient,
): Promise<ApprovalItem> {
  const raw = await client.post<RawApprovalItem>(
    `/api/v1/approvals/${encodeURIComponent(id)}/cancel`,
    {},
  );
  return normalize(raw);
}

export interface ApprovalExecutionResult {
  actionRunId: string;
}

/** 触发一张已批准的单。
 *
 *  **不传 params**：执行用的参数始终取自单上（后端 ExecuteApproved）。这里传
 *  一份只会让人误以为界面能改参数——改参数等于换一件事，要重新提交审批。 */
export async function executeApproval(
  id: string,
  client: ApiClient = apiClient,
): Promise<ApprovalExecutionResult> {
  const body = await client.post<{ action_run_id?: string }>(
    `/api/v1/approvals/${encodeURIComponent(id)}/execute`,
    {},
  );
  return { actionRunId: body.action_run_id ?? "" };
}
