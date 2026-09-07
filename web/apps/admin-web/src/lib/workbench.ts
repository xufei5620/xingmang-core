import type { BadgeTone } from "@xingmang/ui-primitives";
import { describeServiceStatus, platformNavSpec, type FreshnessContract } from "@xingmang/ui-admin";
import type { AlertItem } from "../api/alerts";
import type { ApprovalItem } from "../api/approvals";
import { jobKindLabel, type JobRunItem } from "../api/jobs";
import type { MetricItem, ServiceItem } from "../api/platform";
import { describeSeverity, sortForDisplay } from "./alerts";
import { groupByRisk, isEffectivelyExpired, voteProgress } from "./approvals";
import { PLATFORM_CATALOG, pendingBadge, platformOfMetricKey } from "./platforms";

/** 「最近恢复」的回看窗口（原型副标题逐字：「最近 24 小时」）。 */
export const RECOVERED_WINDOW_HOURS = 24;

/** 待处理事项的分类（原型运营工作台的筛选条，逐字按顺序）。
 *
 *  分类是 IA，先按原型定下来；每一类今天有没有数据源是另一回事，由 `source`
 *  说明。摆一个永远空的分类而不说为什么，人会以为「这一类现在没有问题」——
 *  而事实是我们还没接这条线。 */
export interface WorkCategory {
  id: string;
  label: string;
  /** 这一类今天的数据来源；undefined = 还没有源。 */
  source?: string;
  /** 没有源时，说清楚什么东西上线之后它才会有内容。 */
  blockedBy?: string;
}

export const WORK_CATEGORIES: readonly WorkCategory[] = [
  {
    id: "incidents",
    label: "故障",
    // 原型这一类的条目指向 `g/alerts/incidents`，也就是「告警与故障」页。
    // 故障事件（Incident）是 ADMIN-IA §5.1 要新建的对象，现在还没有,
    // 所以这一类暂由**活跃告警**充数——这件事必须在界面上说出来,
    // 不能让人以为看到的已经是收敛过的故障单
    source: "活跃告警（Incident 对象未建，暂由告警充数）",
  },
  {
    id: "approvals",
    label: "待审批",
    // XM-WORKBENCH-APPROVALS：这一格接上了 `GET /api/v1/approvals?status=PENDING`。
    // 它原来写着「后端已启用、这一格的取数还没接」——那句话现在过期了，而一个
    // 说「还没接」的占位比缺功能更糟：它让人不去看本来就有的数据，于是等着人
    // 投票的 L3/L4 动作在首屏彻底不可见。
    //
    // 只收**此刻真的还等着人投票**的单（见 workItemsFromApprovals）：已过期的
    // 不算待办，哪怕库里仍记着 PENDING。
    source: "审批中心里仍等着投票的单（已过期的不算，见 workItemsFromApprovals）",
  },
  {
    id: "jobs",
    label: "失败任务",
    // XM-WORKBENCH-JOBS：这里原来写着「后台任务页与 River 查询端点尚未建」。
    // 那句话在 XM-JOBS0 交付时就过期了——/api/v1/jobs/runs 一直都在，后台
    // 任务页也一直都在。一个说"还没接"的占位比缺功能更糟：它让人不去看
    // 本来就有的数据，于是失败的后台任务在首屏彻底不可见。
    source: "后台任务里已放弃的运行记录（重试用尽，不会再跑）",
  },
  {
    id: "finance",
    label: "财务异常",
    blockedBy: "退款冻结与对账异常随支付接入（M3）上线",
  },
  {
    id: "expiring",
    label: "即将到期",
    // 「人员与权限」页（/identity）早就建成了，所以这句话也不能再那么写。
    // 真正的缺口在更下面一层：**凭据模型里根本没有到期这个概念**——
    // secrets.CredentialRef 只有 scope/name 两个字段，全仓找不到任何
    // ExpiresAt / RotatedAt。没有到期时间，就没有「即将到期」可算。
    blockedBy:
      "凭据模型里还没有到期时间：CredentialRef 只登记 secret://<scope>/<name>，" +
      "不记录签发与轮换到期。要先给凭据加到期元数据，这一格才有得算。",
  },
  {
    id: "changes",
    label: "待评审变更",
    // 同上：Foundation-B 已交付，这一格等的不是它。变更单本体今天**不在平台里**
    // （没有 change_request 表、没有只读端点、没有 change.* Action），真实的
    // 变更单是仓库 docs/change-requests/ 下的 CR-xxxx。要不要搬进平台登记
    // 待产品负责人裁定——逐字同 ChangesPage 的 TAB_SOURCE.requests。
    blockedBy:
      "变更单本体今天不在平台里：真实的变更单是仓库 docs/change-requests/ 下的 " +
      "CR-xxxx（Markdown），后台没有读它的路径。要不要搬进平台登记待产品负责人裁定。",
  },
];

export interface WorkItem {
  id: string;
  categoryId: string;
  categoryLabel: string;
  tone: BadgeTone;
  title: string;
  /** 「来源 · 负责人 · 已持续」那一行。 */
  meta: string;
  /** 右侧那一列：什么时候要处理完 / 现在什么进度。 */
  due: string;
  to: string;
}

/** 把一段秒数说成人话。`ageText`（过去多久）与 `untilText`（还有多久）共用这
 *  一份单位阶梯：两处各写一套，迟早会长成「3 小时」和「3小时」两种写法。 */
function durationText(seconds: number): string {
  if (!Number.isFinite(seconds)) return "—";
  const whole = Math.max(0, Math.round(seconds));
  if (whole < 60) return `${whole} 秒`;
  if (whole < 3600) return `${Math.floor(whole / 60)} 分钟`;
  if (whole < 86400) return `${Math.floor(whole / 3600)} 小时`;
  return `${Math.floor(whole / 86400)} 天`;
}

function ageText(fromIso: string, now: Date): string {
  return durationText((now.getTime() - Date.parse(fromIso)) / 1000);
}

/** 距离某个未来时刻还有多久；时刻解析不出来时返回 null。
 *
 *  **不在这里退回一个占位字符串**：调用方要拼的是「N 小时后到期」，拿到 "—"
 *  会拼出「—后到期」这种既不是时间也不是说明的句子。让它显式地判一次。 */
function untilText(toIso: string, now: Date): string | null {
  const at = Date.parse(toIso);
  if (!Number.isFinite(at)) return null;
  return durationText((at - now.getTime()) / 1000);
}

/** 活跃告警 → 待处理事项。
 *
 *  只取活跃的（已解决的不是待办），按严重度与最近发现排序——与告警页同一条
 *  排序规则，两处不能各排各的。 */
export function workItemsFromAlerts(alerts: readonly AlertItem[], now: Date): WorkItem[] {
  return sortForDisplay(alerts.filter((alert) => alert.status !== "RESOLVED")).map((alert) => {
    const severity = describeSeverity(alert.severity);
    return {
      id: alert.id,
      categoryId: "incidents",
      categoryLabel: severity.label,
      tone: severity.tone,
      title: alert.title,
      meta: `${alert.rule_key} · ${alert.environment} · 已持续 ${ageText(alert.opened_at, now)}`,
      // 「触发 N 次」比「首次何时」更能区分「抖了一下」和「一直在响」
      due: `触发 ${alert.fire_count} 次`,
      to: "/alerts",
    };
  });
}

/** 已放弃的后台任务 → 待处理事项。
 *
 *  **只收 `discarded`，不收 `retryable`。** 这不是图省事：「我的待处理」是
 *  "我必须动手的事"，而一个 retryable 的任务不需要任何人动手——它会自己再试，
 *  试成了就消失，试到用尽就变成 discarded 出现在这里。把它也列进来会让这张
 *  清单抖动（一条目跳出来又自己消失），而一张会抖的待办清单，人很快就不看了。
 *  重试中的任务在后台任务页「失败与重试」页签里，那里才是看过程的地方。
 *
 *  也不收 `cancelled`：那是有人主动取消的，不是失败。 */
export function workItemsFromJobRuns(runs: readonly JobRunItem[], now: Date): WorkItem[] {
  return runs
    .filter((run) => run.state === "discarded")
    .map((run) => {
      // 放弃的时刻优先用 finalized_at（River 定终态的时刻）；缺了就退回最近
      // 一次尝试，再退回创建时刻——不显示"0 秒前"这种编出来的新鲜。
      const at = run.finalized_at ?? run.attempted_at ?? run.created_at;
      return {
        id: `job-${run.id}`,
        categoryId: "jobs",
        categoryLabel: "已放弃",
        tone: "danger" as const,
        title: `${jobKindLabel(run.kind)} 重试 ${run.max_attempts} 次后放弃`,
        // 错误原文可能很长且带栈，这里只放一行定位信息；正文在后台任务页看。
        meta: `${run.queue} · 共失败 ${run.error_count} 次 · ${ageText(at, now)}前`,
        due: "需人工处理",
        // 直接落到后台任务页的「多次失败任务」页签（state=discarded 那个），
        // 而不是页面首屏——点进去还要自己找是多余的一步。
        to: "/jobs?sub=repeated",
      };
    });
}

/** 风险等级的色调。**与 components/ApprovalQueue.tsx 里的 RISK_TONE 是同一张表**
 *  ——那一份在组件内私有，本片不改那个文件，所以这里照抄一份。将来加档位时两处
 *  要一起改：同一个 L3 在工作台上是黄的、在审批队列上是红的，人会以为是两回事。 */
const APPROVAL_RISK_TONE: Readonly<Record<string, BadgeTone>> = {
  L2: "warning",
  L3: "warning",
  L4: "danger",
};

/** 待审批的动作 → 待处理事项（XM-WORKBENCH-APPROVALS）。
 *
 *  两道过滤缺一不可：
 *
 *  1. **只收 PENDING。** 取数那一层已经带了 `status=PENDING`，这里仍然再滤一遍：
 *     这是个纯函数，调用方换个取法（比如为了省一次请求而复用不带筛选的列表）
 *     时，它不该悄悄把已驳回、已执行的单也列成待办。
 *  2. **过期的不收，哪怕库里仍记着 PENDING。** 过期是定时任务 ExpirePending 写
 *     回去的，库里的 status 会滞后，而服务端在执行那一刻才按 expires_at 判
 *     （见 lib/approvals 的 isEffectivelyExpired）。照抄 status 会让首屏混进一批
 *     谁也批不动的单——「我的待处理」是"我必须动手的事"，摆一件动不了的事进来，
 *     人翻两次就不再信这张清单，这与「重试中的任务不列进来」是同一条理由。
 *
 *  排序复用审批队列页那一套（groupByRisk：L4 → L3 → L2，组内先到期的在前），
 *  两处不能各排各的——同一批单在两个页面上排成两个样子，人会以为看的是两份
 *  数据。 */
export function workItemsFromApprovals(items: readonly ApprovalItem[], now: Date): WorkItem[] {
  const waiting = items.filter(
    (item) => item.status === "PENDING" && !isEffectivelyExpired(item, now),
  );
  return groupByRisk(waiting).flatMap((group) =>
    group.items.map((item) => {
      const remaining = untilText(item.expires_at, now);
      return {
        id: `approval-${item.id}`,
        categoryId: "approvals",
        // 徽章上放风险等级而不是「待审批」：这一屏上唯一要当场判断的是「先看
        // 哪一张」，而 L4 与 L2 的差别正是答案。不认识的等级照原样显示成中性
        // 徽章，不丢弃——静默吞掉一整档待审批比显示一个陌生的等级名危险得多。
        categoryLabel: item.risk_level,
        tone: APPROVAL_RISK_TONE[item.risk_level] ?? "neutral",
        title: `${item.action_id}@${item.action_version}`,
        // 与审批队列卡片的落款同一行内容：谁提交的、还差几票。「还差几票」比
        // 「何时提交」更能回答「我现在能不能推动它」，而票数措辞（含「票数够了
        // 但还缺一张特权票」那种）只有 voteProgress 一份，不在这里另写。
        meta: `${item.requester_id} 提交 · ${voteProgress(item, now)}`,
        // 右侧那一列问的是「什么时候要处理完」——审批单的答案就是到期时刻。
        due: remaining === null ? "到期时间未知" : `${remaining}后到期`,
        // 直达「操作与审批」页的「待审批」子页签：投票、执行、撤回都在那里，
        // 落到页面首屏还要自己找是多余的一步。
        to: "/actions?sub=pending",
      };
    }),
  );
}

/** 「我的待处理」三条数据源各自的取数上限（XM-WORKBENCH-TRUNCATION）。 */
export const ACTIVE_ALERTS_LIMIT = 200;
export const WORK_JOBS_LIMIT = 20;
/** 待审批只取一屏够看的量。完整队列在「操作与审批」页——那边按等级分组，
 *  也只有那边能投票和执行；这一格是入口，不是清单。服务端上界是 100
 *  （httpapi/approvals.go 的 maxApprovalLimit），这个数远在其下。 */
export const WORK_APPROVALS_LIMIT = 20;

/** 这一屏是不是没显示全，以及该怎么说。
 *
 *  **三个判据都由服务端给，前端不自己算**（XM-ALERTS-LIST-TRUNCATED）：
 *  - 告警：`/api/v1/alerts` 的 `truncated`。调用方传的 limit 与真正生效的
 *    limit 可能不是一个数（不传、或传得比服务端上界还大都会被钳），拿自己传
 *    的数去比会**永远判不出截断**。
 *  - 后台任务：`/api/v1/jobs/runs` 的 `next_before`——它本来就是游标分页的
 *    "还有下一页"，比数个数可靠。
 *  - 待审批：`/api/v1/approvals` 的 `truncated`，与告警同一条理由（服务端算的
 *    是"返回条数 >= 生效上限"，生效上限只有服务端知道）。
 *
 *  仍然只说"可能"：服务端的判据是"返回条数正好等于生效上限"，恰好等于时也
 *  可能就是恰好这么多。含糊不好，但假装看到的是全部更糟——一张"没有待处理
 *  事项"的清单如果其实被截断了，人会据此收工。
 *
 *  返回 null 表示没有截断，界面据此不显示这一行（而不是显示一句"没有截断"，
 *  那是噪声）。 */
export function truncationNote(input: {
  activeCategoryId: string;
  alertsTruncated: boolean;
  jobsTruncated: boolean;
  approvalsTruncated: boolean;
}): string | null {
  const parts: string[] = [];
  const showAlerts = input.activeCategoryId === "" || input.activeCategoryId === "incidents";
  const showApprovals = input.activeCategoryId === "" || input.activeCategoryId === "approvals";
  const showJobs = input.activeCategoryId === "" || input.activeCategoryId === "jobs";
  if (showAlerts && input.alertsTruncated) {
    parts.push(`活跃告警只取了 ${ACTIVE_ALERTS_LIMIT} 条`);
  }
  if (showApprovals && input.approvalsTruncated) {
    parts.push(`待审批的单只取了 ${WORK_APPROVALS_LIMIT} 条`);
  }
  if (showJobs && input.jobsTruncated) {
    parts.push(`已放弃的后台任务只取了 ${WORK_JOBS_LIMIT} 条`);
  }
  if (parts.length === 0) return null;
  return `这一屏可能不是全部：${parts.join("，")}。完整清单在各自的页面里。`;
}

/** 顶部四格里「紧急」的口径：未解决的严重告警。
 *
 *  刻意只数 critical：把 warning 也算进「紧急」，这个数就永远不会小,
 *  于是它不再能回答「现在要不要放下手里的事」。 */
export function urgentCount(alerts: readonly AlertItem[]): number {
  return alerts.filter((a) => a.status !== "RESOLVED" && a.severity === "critical").length;
}

/** 顶部四格里「最近恢复」的口径：近 24 小时内自动恢复的告警。 */
export function recentlyRecoveredCount(alerts: readonly AlertItem[], now: Date): number {
  const since = now.getTime() - RECOVERED_WINDOW_HOURS * 3600 * 1000;
  return alerts.filter((a) => {
    if (a.status !== "RESOLVED" || !a.resolved_at) return false;
    const at = Date.parse(a.resolved_at);
    return Number.isFinite(at) && at >= since;
  }).length;
}

export interface FocusRow {
  domain: string;
  /** 有源时是计数；无源时是 undefined，由界面显示「未接入」而不是 0。 */
  count?: number;
  tone: BadgeTone;
  state: string;
  detail: string;
}

/** 运营焦点：按领域分开列，**绝不合成一个总健康分**。
 *
 *  交接文档 §9.1 明令禁止合成分数，原型也把可靠性 / 财务 / 安全画成三行。
 *  理由不是审美：一个 87 分的看板没法回答「我现在该去修哪个」，而把三条
 *  相互无关的信号平均起来，任何一条恶化都会被另外两条稀释掉。 */
export function focusRows(alerts: readonly AlertItem[]): FocusRow[] {
  const active = alerts.filter((a) => a.status !== "RESOLVED");
  const critical = active.filter((a) => a.severity === "critical").length;
  const warning = active.filter((a) => a.severity === "warning").length;

  const reliability: FocusRow =
    critical > 0
      ? { domain: "可靠性", count: critical + warning, tone: "danger", state: "严重", detail: `${critical} 条严重告警未解决` }
      : warning > 0
        ? { domain: "可靠性", count: warning, tone: "warning", state: "需关注", detail: `${warning} 条警告未解决` }
        : { domain: "可靠性", count: 0, tone: "success", state: "正常", detail: "当前没有未解决的告警" };

  return [
    reliability,
    {
      domain: "财务",
      tone: "neutral",
      state: "未接入",
      detail: "对账异常与退款冻结随支付接入（M3）上线",
    },
    {
      domain: "安全",
      tone: "neutral",
      state: "未接入",
      detail: "凭据到期与权限异常随「人员与权限」页上线",
    },
  ];
}

export interface MatrixRow {
  key: string;
  label: string;
  /** ADMIN-IA §一 的阶段列。 */
  stage: string;
  /** 关键状态：已登记的用 Registry 状态，没登记的用平台目录的接入标签。 */
  statusLabel: string;
  statusTone: BadgeTone;
  /** 该平台指标里最差的那一条新鲜度；没有指标时是「未初始化」。 */
  freshness: FreshnessContract;
  /** 活动事件数（未解决告警）。 */
  events: number;
  /** 最近观测时刻（该平台全部指标里最新的一个 observed_at）。 */
  observedAt: string | null;
  to: string;
}

/** 新鲜度取**最差**的那一条，不取平均也不取最新。
 *
 *  一个平台有五条指标、其中一条已经两小时没更新时，矩阵上那一格必须显示
 *  「延迟」。取最新的那条会把这件事盖掉，而它正是人来这一屏要找的东西。 */
const FRESHNESS_RANK: Record<string, number> = {
  uninitialized: 0,
  failed: 1,
  stale: 2,
  partial: 3,
  fresh: 4,
};

const UNINITIALIZED: FreshnessContract = {
  state: "uninitialized",
  staleness_seconds: null,
  threshold_seconds: 0,
  is_partial: false,
  observed_at: null,
  last_success: null,
  last_error_code: "",
};

function worstFreshness(metrics: readonly MetricItem[]): FreshnessContract {
  let worst: FreshnessContract | undefined;
  for (const metric of metrics) {
    const rank = FRESHNESS_RANK[metric.freshness.state] ?? 0;
    const worstRank = worst ? (FRESHNESS_RANK[worst.state] ?? 0) : Number.POSITIVE_INFINITY;
    if (rank < worstRank) worst = metric.freshness;
  }
  return worst ?? UNINITIALIZED;
}

function latestObservedAt(metrics: readonly MetricItem[]): string | null {
  let latest: string | null = null;
  for (const metric of metrics) {
    const at = metric.freshness.observed_at;
    if (at && (latest === null || at > latest)) latest = at;
  }
  return latest;
}

export interface MatrixInput {
  services: readonly ServiceItem[];
  metrics: readonly MetricItem[];
  alerts: readonly AlertItem[];
}

/** 平台状态矩阵。
 *
 *  行来自平台目录（4 个平台），再按原型补一行「开票系统」——原型的矩阵里
 *  确实有这一行，它指向治理段的「跨平台财务 / 开票集成」，与「开票不是平台」
 *  并不矛盾：矩阵回答的是「我管的这些系统现在怎么样」，不是侧栏的平台清单。
 *
 *  每一格都只由真实数据得出，凑不出来的显示成「未接入 / 未初始化」而不是 0。 */
export function platformMatrixRows({ services, metrics, alerts }: MatrixInput): MatrixRow[] {
  const active = alerts.filter((a) => a.status !== "RESOLVED");

  const rows: MatrixRow[] = PLATFORM_CATALOG.map((spec) => {
    const own = metrics.filter((m) => platformOfMetricKey(m.metric_key) === spec.serviceType);
    const instances = services.filter((s) => s.service_type === spec.serviceType);
    const events = active.filter(
      (a) => platformOfMetricKey(a.source_metric_key) === spec.serviceType,
    ).length;

    // 已登记的用 Registry 的实际状态；没登记的说「未登记 / 未接入·Mx」,
    // 不说「正常」——没有实例可谈的时候，「正常」是编出来的。
    // 状态文案走 describeServiceStatus，与资源目录页同一套口径:
    // 同一个 degraded 在两页上显示成两种说法，人会以为是两回事
    const registered = instances[0];
    const status = registered ? describeServiceStatus(registered.status) : undefined;
    return {
      key: spec.serviceType,
      label: spec.label,
      stage: platformNavSpec(spec.serviceType)?.stage ?? "—",
      statusLabel: status ? status.label : pendingBadge(spec.plan),
      statusTone: status ? status.tone : "neutral",
      freshness: worstFreshness(own),
      events,
      observedAt: latestObservedAt(own),
      to: `/platforms/${spec.serviceType}`,
    };
  });

  rows.push({
    key: "invoice",
    label: "开票系统",
    stage: "契约前置",
    statusLabel: "未接入",
    statusTone: "neutral",
    freshness: UNINITIALIZED,
    events: 0,
    observedAt: null,
    // 开票不是平台（ADMIN-IA §5.2）：它的入口在治理段「跨平台财务」的
    // 「开票集成」子页签，以及各平台「支付与财务」里的「开票」
    to: "/finance?sub=invoicing",
  });

  return rows;
}

