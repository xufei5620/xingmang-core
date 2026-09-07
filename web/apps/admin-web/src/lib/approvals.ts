import type { BadgeTone } from "@xingmang/ui-primitives";
import type { ApprovalItem, ApprovalStatus } from "../api/approvals";

/** 审批队列的纯逻辑：分组、票数措辞、过期判定、可用动作。
 *
 *  与 lib/workbench.ts 同一条纪律——判断放在纯函数里，页面只负责摆。
 *  这一份尤其值得抽出来，因为「还差几票」和「过期了没有」都有不止一种
 *  说错的方式（见下面各自的注释）。 */

/** 队列按等级分组的顺序：先看最危险的。 */
export const RISK_GROUP_ORDER: readonly string[] = ["L4", "L3", "L2"];

export interface ApprovalGroup {
  riskLevel: string;
  items: ApprovalItem[];
}

/** 按风险等级分组，L4 → L3 → L2，其余等级按字典序排在后面。
 *
 *  不认识的等级**不丢弃**：等级表以后可能加档，静默吞掉一整组待审批比
 *  显示一个陌生的组名危险得多。 */
export function groupByRisk(items: readonly ApprovalItem[]): ApprovalGroup[] {
  const byLevel = new Map<string, ApprovalItem[]>();
  for (const item of items) {
    const bucket = byLevel.get(item.risk_level);
    if (bucket) {
      bucket.push(item);
    } else {
      byLevel.set(item.risk_level, [item]);
    }
  }
  const known = RISK_GROUP_ORDER.filter((level) => byLevel.has(level));
  const unknown = [...byLevel.keys()].filter((level) => !RISK_GROUP_ORDER.includes(level)).sort();
  return [...known, ...unknown].map((riskLevel) => ({
    riskLevel,
    items: (byLevel.get(riskLevel) ?? []).slice().sort(compareByUrgency),
  }));
}

/** 组内排序：先到期的排前面，同刻按提交时间。
 *  「谁快过期了」比「谁先提交」更该先看到。 */
function compareByUrgency(a: ApprovalItem, b: ApprovalItem): number {
  const byExpiry = Date.parse(a.expires_at) - Date.parse(b.expires_at);
  if (!Number.isNaN(byExpiry) && byExpiry !== 0) return byExpiry;
  return Date.parse(a.created_at) - Date.parse(b.created_at);
}

/** 一张单此刻**实际**是不是已经过期。
 *
 *  不能只看 `status`：`ExpirePending` 定时任务在 XM-0030c 之前根本没有人调，
 *  所以库里的 status 会一直停在 PENDING，而服务端在执行那一刻才按 expires_at
 *  判出过期。界面若照抄 status，队列看起来会比实际长——一屏「待审批」里混着
 *  一批其实谁也批不动的单。
 *
 *  终态的单不再谈过期：一张已执行的单的 expires_at 早就过去了，说它「已过期」
 *  是错的。 */
export function isEffectivelyExpired(item: ApprovalItem, now: Date): boolean {
  if (item.status !== "PENDING" && item.status !== "APPROVED") return false;
  const expiresAt = Date.parse(item.expires_at);
  if (Number.isNaN(expiresAt)) return false;
  return now.getTime() >= expiresAt;
}

/** 界面上该显示的状态：把「库里说 PENDING、其实已经过期」翻成 EXPIRED。 */
export function displayStatus(item: ApprovalItem, now: Date): ApprovalStatus {
  return isEffectivelyExpired(item, now) ? "EXPIRED" : item.status;
}

const STATUS_LABEL: Readonly<Record<ApprovalStatus, string>> = {
  PENDING: "待审批",
  APPROVED: "已批准",
  REJECTED: "已驳回",
  EXECUTED: "已执行",
  EXPIRED: "已过期",
  CANCELLED: "已撤回",
};

const STATUS_TONE: Readonly<Record<ApprovalStatus, BadgeTone>> = {
  PENDING: "warning",
  APPROVED: "info",
  REJECTED: "danger",
  EXECUTED: "success",
  EXPIRED: "neutral",
  CANCELLED: "neutral",
};

export function statusLabel(status: ApprovalStatus): string {
  return STATUS_LABEL[status] ?? status;
}

export function statusTone(status: ApprovalStatus): BadgeTone {
  return STATUS_TONE[status] ?? "neutral";
}

/** 「还差几票」的措辞。
 *
 *  三处讲究：
 *
 *  1. **票数够了但缺特权票**要单独说。只说「已 2/2 票」会让人以为该批了，
 *     而它还挂着——那看起来像故障。
 *  2. 票数取自后端算好的 `votes_required`，不在前端复算。票数规则是配置，
 *     散一份到前端迟早与后端分叉。
 *  3. 非 PENDING 的单不谈「还差几票」——已驳回的单差多少票都没有意义。
 */
export function voteProgress(item: ApprovalItem, now: Date): string {
  const status = displayStatus(item, now);
  if (status !== "PENDING") {
    return `${item.votes_cast}/${item.votes_required} 票`;
  }
  const missing = Math.max(0, item.votes_required - item.votes_cast);
  const needsPrivileged = item.privileged_vote_required && !item.privileged_vote_cast;
  if (missing > 0 && needsPrivileged) {
    return `还差 ${missing} 票，且其中至少一张须由 approval.l4 持有人投出`;
  }
  if (missing > 0) {
    return `还差 ${missing} 票（已 ${item.votes_cast}/${item.votes_required}）`;
  }
  if (needsPrivileged) {
    return `票数已满 ${item.votes_cast}/${item.votes_required}，但还缺一张 approval.l4 特权票`;
  }
  // 票数满、特权票也齐，却还是 PENDING：等级没配票数（Settle 对未配等级
  // 一律返回 PENDING）。说出来，别让人对着一张永远不动的单发呆。
  return "票数条件已满足但单未落定——多半是该风险等级未配置票数，请检查审批策略";
}

/** 当前登录者对这张单能做什么。
 *
 *  **门禁的分寸取决于前端知道什么**，这一点决定了下面每一条的写法：
 *
 *  - **记录自身的状态**（PENDING / APPROVED / 是否已过期）前端知道，按它藏
 *    按钮是准确的——一张已驳回的单上摆着「批准」按钮只会让人点出个错。
 *  - **归属**（谁提交的、我投过没有）前端也知道：principal_id 三种鉴权模式
 *    都拿得到（session.ts 的 currentPrincipalId）。
 *  - **权限**（approval.decide / approval.l4）前端**不知道**：scope 在任何模式
 *    下都不下发到前端（session.ts 的 currentUserRoles 注释）。所以这里
 *    **一律不按权限藏按钮**——猜错的两个方向都不好：藏错了让有权限的人找不到
 *    入口，没藏住则点了必然 403。权限交给服务端拒绝，错误就地显示。
 *
 *  身份判不出来时（principalId 为空串）只按状态判，不做自我判断——把「判不出
 *  来」当成「不是我」会把提交人自己的撤回按钮也藏掉。 */
export interface ApprovalAbilities {
  canVote: boolean;
  canExecute: boolean;
  canCancel: boolean;
  /** 状态与归属允许投票、但**这一条注定会被服务端拒**时的原因；
   *  其余情况为空串。目前只有「我已经投过票了」与「本等级不许自批」两条——
   *  它们都只依赖前端确实知道的信息。 */
  voteBlockedReason: string;
}

export function abilitiesFor(
  item: ApprovalItem,
  principalId: string,
  now: Date,
): ApprovalAbilities {
  const status = displayStatus(item, now);
  const known = principalId !== "";
  const isRequester = known && principalId === item.requester_id;
  const alreadyVoted = known && item.decisions.some((d) => d.approver_id === principalId);

  let reason = "";
  if (status === "PENDING") {
    if (alreadyVoted) {
      reason = "你已经在这张单上投过票了";
    } else if (isRequester && item.risk_level !== "L2") {
      // L3 及以上不许自批；L2 可以——单票即自批，等级本意如此。
      reason = `${item.risk_level} 不允许提交人给自己的单投票`;
    }
  }

  return {
    canVote: status === "PENDING" && reason === "",
    voteBlockedReason: reason,
    canExecute: status === "APPROVED",
    canCancel: status === "PENDING" && isRequester,
  };
}

/** 参数的展示行：把 params 摊平成有序的键值对，好让人一眼扫完。
 *
 *  按键名排序而不是按对象自身顺序：`params_hash` 算的就是排序后的 JSON，
 *  界面照同一个顺序摆，人肉核对参数时两边对得上。 */
export function paramRows(params: Record<string, unknown>): { key: string; value: string }[] {
  return Object.keys(params)
    .sort()
    .map((key) => ({ key, value: formatParamValue(params[key]) }));
}

function formatParamValue(value: unknown): string {
  if (value === null) return "null";
  if (value === undefined) return "—";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  return JSON.stringify(value);
}
