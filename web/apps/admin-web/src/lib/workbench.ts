import type { BadgeTone } from "@xingmang/ui-primitives";
import { describeServiceStatus, platformNavSpec, type FreshnessContract } from "@xingmang/ui-admin";
import type { AlertItem } from "../api/alerts";
import type { MetricItem, ServiceItem } from "../api/platform";
import { describeSeverity, sortForDisplay } from "./alerts";
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
    blockedBy: "审批链随 Foundation-B（XM-0030）上线",
  },
  {
    id: "jobs",
    label: "失败任务",
    blockedBy: "后台任务页与 River 查询端点尚未建",
  },
  {
    id: "finance",
    label: "财务异常",
    blockedBy: "退款冻结与对账异常随支付接入（M3）上线",
  },
  {
    id: "expiring",
    label: "即将到期",
    blockedBy: "凭据轮换到期随「人员与权限」页上线",
  },
  {
    id: "changes",
    label: "待评审变更",
    blockedBy: "变更单随 Foundation-B 上线",
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

function ageText(fromIso: string, now: Date): string {
  const seconds = Math.max(0, Math.round((now.getTime() - Date.parse(fromIso)) / 1000));
  if (!Number.isFinite(seconds)) return "—";
  if (seconds < 60) return `${seconds} 秒`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} 小时`;
  return `${Math.floor(seconds / 86400)} 天`;
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

