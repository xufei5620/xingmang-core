import type { BadgeTone } from "@xingmang/ui-primitives";
import {
  describeServiceStatus,
  formatUtcTimestamp,
  platformNavSpec,
  type FreshnessContract,
} from "@xingmang/ui-admin";
import {
  alertAgeAnchor,
  describeFireCount,
  FIRE_COUNT_MEANING,
  type AlertItem,
} from "../api/alerts";
import type { ApprovalItem } from "../api/approvals";
import { jobKindLabel, type JobRunItem } from "../api/jobs";
import type { MetricItem, ServiceItem } from "../api/platform";
import { describeSeverity, describeStatus, sortForDisplay } from "./alerts";
import { groupByRisk, isEffectivelyExpired, voteProgress } from "./approvals";
import { metricLabel } from "./metrics";
import { PLATFORM_CATALOG, pendingBadge, platformOfMetricKey } from "./platforms";

/** 「最近恢复」的回看窗口（原型副标题逐字：「最近 24 小时」）。 */
export const RECOVERED_WINDOW_HOURS = 24;

/** 「审批到期」这一格向前看多久。
 *
 *  用滚动 24 小时而不是「今天」：审批单的 `expires_at` 是一个时刻，而「今天」
 *  要先定按哪个时区切日（宪法 14 条）。这一格没有业务日语义可依，硬挑一个时区
 *  会让同一批单在两台机器上数出不同的结果——同「最近恢复」那格的窗口口径。 */
export const APPROVAL_DUE_WINDOW_HOURS = 24;

/** 「凭据到期」今天缺的是什么。**抽成一份给两处共用**。
 *
 *  这句话有过两份副本：`WORK_CATEGORIES.expiring` 与 `focusRows()` 的「安全」行。
 *  上一次订正只改了前者，于是首屏那一行继续写着「随『人员与权限』页上线」——
 *  而 `/identity` 早就建成了，那句话把人指向一个根本不缺的东西。共用一份之后，
 *  下一次订正不可能只改一半。 */
const CREDENTIAL_EXPIRY_GAP =
  "凭据模型里还没有到期时间：CredentialRef 只登记 secret://<scope>/<name>，" +
  "不记录签发与轮换到期。";

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
    blockedBy: `${CREDENTIAL_EXPIRY_GAP}要先给凭据加到期元数据，这一格才有得算。`,
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
  /** 悬停说明：这一行的数字有什么读起来不直观的地方。缺省不挂。 */
  hint?: string;
  /** 绝对时刻那一行（告警行才有）：打开 / 确认 / 最近评估，一律
   *  `formatUtcTimestamp` 带 UTC 后缀。
   *
   *  `meta` 里的「已持续 13 小时」是相对时长，它回答不了「我确认之后它还在
   *  不在响」——负责人看着一条已确认的告警说「这个没有带时间，我感觉好像我确认
   *  之后还在告警」，而界面上确实没有任何一个时刻能让他核对。 */
  timeline?: string;
  /** 被合并进这一行的明细（同 kind 的已放弃作业）。**只有合并行才有**，
   *  单条不带（也不显示「×1」），否则界面上会多出一堆没有内容的展开箭头。 */
  children?: WorkItem[];
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

/** 「最近评估」那个时刻怎么念。
 *
 *  `last_seen_at` 是告警引擎最近一轮判定「条件仍成立」的时刻（TouchAlert 每轮
 *  更新），不是「最近一次发生」；措辞跟着 `FIRE_COUNT_MEANING` 的口径叫「评估」，
 *  不叫「最近触发」。 */
function lastEvaluatedText(alert: Pick<AlertItem, "last_seen_at">): string {
  return `最近评估 ${formatUtcTimestamp(alert.last_seen_at)}`;
}

/** 未处理的活跃告警 → 待处理事项。
 *
 *  **不收已确认的。** 已确认（ACKNOWLEDGED）的告警仍然活着、每轮仍在 +1，但它
 *  已经有人认领——把它和没人管的混排在一起、还挂着「警告 / 严重」的徽章，
 *  人会以为自己刚才那一下确认没生效。那一组走 `acknowledgedWorkItems`，在列表
 *  底部折叠着。已解决的更不是待办。
 *
 *  按严重度与最近发现排序——与告警页同一条排序规则，两处不能各排各的。 */
export function workItemsFromAlerts(alerts: readonly AlertItem[], now: Date): WorkItem[] {
  return sortForDisplay(
    alerts.filter((alert) => alert.status !== "RESOLVED" && alert.status !== "ACKNOWLEDGED"),
  ).map((alert) => {
    const severity = describeSeverity(alert.severity);
    const age = alertAgeAnchor(alert);
    return {
      id: alert.id,
      categoryId: "incidents",
      categoryLabel: severity.label,
      tone: severity.tone,
      title: alert.title,
      // 「已持续」优先用 first_opened_at；那个字段今天还不存在，所以退回
      // opened_at 并把「恢复后重新打开会重新计时」挂到 hint 上（见
      // api/alerts 的 alertAgeAnchor）——一个系统性偏小的数字不加说明地摆
      // 出来，与摆一个错数字没有区别。
      meta: `${alert.rule_key} · ${alert.environment} · 已持续 ${ageText(age.since, now)}`,
      // 相对时长之外再给两个绝对时刻：「已持续 13 小时」核对不了任何事，
      // 「打开 09-08 13:31 UTC · 最近评估 09-09 02:31 UTC」才能拿去和自己的
      // 记忆、和 IM 记录对时间。
      timeline: `打开 ${formatUtcTimestamp(alert.opened_at)} · ${lastEvaluatedText(alert)}`,
      // **不是「触发 N 次」。** fire_count 是每 60 秒重评一轮、条件仍成立就
      // +1 的轮数（见 api/alerts 的 fire_count 契约注释）；措辞与告警中心、
      // 平台告警面板共用 describeFireCount 一份，三处不各说各的。
      due: describeFireCount(alert).combined,
      to: "/alerts",
      hint: [FIRE_COUNT_MEANING, age.hint].filter(Boolean).join(" "),
    };
  });
}

/** 已确认那一组的标题。组头带条数：折叠着的时候人只看得见这一行。 */
export const ACKNOWLEDGED_GROUP_TITLE = "已确认，等待自愈";

export function acknowledgedGroupHeading(count: number): string {
  return `${ACKNOWLEDGED_GROUP_TITLE}（${count} 条）`;
}

/** 已确认、但条件仍然成立的告警 → 折叠组里的明细行。
 *
 *  判据只看 `status === "ACKNOWLEDGED"`，不看 `acknowledged_at` 有没有值：
 *  确认时刻是伴随字段，后端 AcknowledgeAlert 一起写，但已解决的告警也可能带着
 *  当年的确认时刻——它不是「等待自愈」，它已经愈了。
 *
 *  徽章不再是严重度（「警告 / 严重」是催人动手的语气，而这一组恰恰是「已经有人
 *  在管」），改用状态口径的「已确认」（与告警中心状态列同一份 describeStatus）。
 *  严重度退到 meta 里仍然可见——不是不重要，是不再催。
 *
 *  明细行要回答的是「我确认之后它还在不在响」：写出**确认时刻**与**最近评估
 *  时刻**，后者晚于前者就是「仍在成立」的证据。确认时刻缺失时明说缺失，不拿
 *  「—」或别的时刻冒充。 */
export function acknowledgedWorkItems(alerts: readonly AlertItem[], now: Date): WorkItem[] {
  return sortForDisplay(alerts.filter((alert) => alert.status === "ACKNOWLEDGED")).map(
    (alert) => {
      const status = describeStatus("ACKNOWLEDGED");
      const severity = describeSeverity(alert.severity);
      const age = alertAgeAnchor(alert);
      const acknowledged =
        alert.acknowledged_at === null || alert.acknowledged_at === ""
          ? "已确认（后端没有记录确认时刻）"
          : `已确认 ${formatUtcTimestamp(alert.acknowledged_at)}`;
      return {
        id: alert.id,
        categoryId: "incidents",
        categoryLabel: status.label,
        tone: status.tone,
        title: alert.title,
        meta: `${severity.label} · ${alert.rule_key} · ${alert.environment} · 已持续 ${ageText(age.since, now)}`,
        timeline: `打开 ${formatUtcTimestamp(alert.opened_at)} · ${acknowledged} · 仍在成立：${lastEvaluatedText(alert)}`,
        due: describeFireCount(alert).combined,
        to: "/alerts",
        hint: [status.hint, FIRE_COUNT_MEANING, age.hint].filter(Boolean).join(" "),
      };
    },
  );
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
export function workItemsFromJobRuns(
  runs: readonly JobRunItem[],
  now: Date,
  options: { truncated?: boolean } = {},
): WorkItem[] {
  const discarded = runs.filter((run) => run.state === "discarded");

  /** 放弃的时刻优先用 finalized_at（River 定终态的时刻）；缺了就退回最近
   *  一次尝试，再退回创建时刻——不显示"0 秒前"这种编出来的新鲜。 */
  const discardedAt = (run: JobRunItem): string =>
    run.finalized_at ?? run.attempted_at ?? run.created_at;

  const single = (run: JobRunItem): WorkItem => ({
    id: `job-${run.id}`,
    categoryId: "jobs",
    categoryLabel: "已放弃",
    tone: "danger" as const,
    title: `${jobKindLabel(run.kind)} 重试 ${run.max_attempts} 次后放弃`,
    // 错误原文可能很长且带栈，这里只放一行定位信息；正文在后台任务页看。
    meta: `${run.queue} · 共失败 ${run.error_count} 次 · ${ageText(discardedAt(run), now)}前`,
    due: "需人工处理",
    // 直接落到后台任务页的「多次失败任务」页签（state=discarded 那个），
    // 而不是页面首屏——点进去还要自己找是多余的一步。
    to: "/jobs?sub=repeated",
  });

  // 按 kind 分组：**不按 queue**。生产上 288 条 card_sync 与几条别的任务同在
  // default 队列里，按队列合并会把它们并成一行，等于换一种方式丢信息。
  const groups = new Map<string, JobRunItem[]>();
  for (const run of discarded) {
    const bucket = groups.get(run.kind);
    if (bucket) bucket.push(run);
    else groups.set(run.kind, [run]);
  }

  const items = [...groups.entries()].map(([kind, group]) => {
    // 明细按放弃时刻倒序：展开第一眼看到的应当是最近那一次。
    const detail = [...group]
      .sort((a, b) => discardedAt(b).localeCompare(discardedAt(a)))
      .map(single);
    // 只有一条时**逐字保持原样**：不出现「×1」这种噪声，也不给一个展开箭头
    // 后面空无一物。
    if (detail.length === 1) return detail[0] as WorkItem;

    const newest = discardedAt(group[0] as JobRunItem);
    const oldest = newest;
    const span = group.reduce(
      (acc, run) => {
        const at = discardedAt(run);
        return { newest: at > acc.newest ? at : acc.newest, oldest: at < acc.oldest ? at : acc.oldest };
      },
      { newest, oldest },
    );
    // 队列去重后列出来：同一 kind 正常只在一个队列上，真出现两个也不能替它
    // 挑一个说。
    const queues = [...new Set(group.map((run) => run.queue))].join("、");
    // `+` 号只标"还不止这些"这一层粒度；「只取了 N 条」的整句由
    // truncationNote 一处说，两边不各写一份（见 CREDENTIAL_EXPIRY_GAP 的教训）。
    const more = options.truncated ? "+" : "";
    return {
      id: `job-kind-${kind}`,
      categoryId: "jobs",
      categoryLabel: "已放弃",
      tone: "danger" as const,
      title: `${jobKindLabel(kind)} 已放弃 ×${detail.length}${more}`,
      meta: `${queues} · 最近 ${ageText(span.newest, now)}前 · 最早 ${ageText(span.oldest, now)}前`,
      due: "需人工处理",
      to: "/jobs?sub=repeated",
      children: detail,
    } satisfies WorkItem;
  });

  // 条数多的排在前面（288 条的那一类必须第一眼看见）；条数相同的**保持取数
  // 顺序**，那已经是服务端按放弃时刻倒序给的，这里不再自己排一遍。单条行没有
  // children，按 1 参与排序。
  const weight = (item: WorkItem): number => item.children?.length ?? 1;
  return items
    .map((item, index) => ({ item, index }))
    .sort((a, b) => weight(b.item) - weight(a.item) || a.index - b.index)
    .map((entry) => entry.item);
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

/** 顶部四格里「紧急」的口径：**未处理**的严重告警。
 *
 *  刻意只数 critical：把 warning 也算进「紧急」，这个数就永远不会小,
 *  于是它不再能回答「现在要不要放下手里的事」。
 *
 *  **已确认的不算，已静默的算。** 两者都活着、都没解决，但问的是「要不要放下
 *  手里的事」：已确认意味着有人认领了，再数进去这个数就永远降不下来，负责人按
 *  了确认之后看到「紧急 1」只会得出「确认没生效」的结论；静默则相反——它没人
 *  认领、只是被捂住了嘴，静默期间「紧急」掉到 0 正是最容易出事的时候。
 *  已确认的那一条在「我的待处理」底部的折叠组里仍然看得见。 */
export function urgentCount(alerts: readonly AlertItem[]): number {
  return alerts.filter(
    (a) => a.status !== "RESOLVED" && a.status !== "ACKNOWLEDGED" && a.severity === "critical",
  ).length;
}

/** 顶部四格里「审批到期」的口径：`APPROVAL_DUE_WINDOW_HOURS` 内到期的待审批单。
 *
 *  ## 这一格为什么只数审批
 *
 *  它以前叫「今日到期」，数的是原型说的**审批 + 重试 + 轮换到期**三样，副行写着
 *  「随 Foundation-B 与后台任务页上线」。那句话今天是错的（审批中心 XM-0030 已
 *  启用，后台任务页与 /jobs/runs 也早就在），但把三样合成一个数同样是错的：
 *
 *  - **重试不是到期。** 后台任务的 `scheduled_at` 是「下一次什么时候再试」，不是
 *    一条截止线；而且工作台只查 `state=discarded`——那些已经把重试用尽了，根本
 *    没有未来时刻可数。已放弃的任务在同一屏的「失败任务」一类里逐条列着。
 *  - **轮换到期今天一个数都算不出来**（见 CREDENTIAL_EXPIRY_GAP）。把它算进合计
 *    等于让这个数**恒偏低**，而偏低的数与正确的数长得一模一样。
 *
 *  所以这一格收窄成「审批到期」：数一件有真正截止时刻的事，并且对这件事是完整的。
 *
 *  ## 已过期的不算
 *
 *  与 `workItemsFromApprovals` 同一条判据（`isEffectivelyExpired`）：库里的 status
 *  会滞后于 `expires_at`，而一张已经过期的单谁也批不动了——把它算进「快到期了，
 *  去看一眼」的计数里，只会让人白跑一趟。`expires_at` 解析不出来的同样不算：
 *  我们无法断言它什么时候到期，就不能替它断言「快到了」。 */
export function approvalsDueSoonCount(items: readonly ApprovalItem[], now: Date): number {
  const until = now.getTime() + APPROVAL_DUE_WINDOW_HOURS * 3600 * 1000;
  return items.filter((item) => {
    if (item.status !== "PENDING" || isEffectivelyExpired(item, now)) return false;
    const at = Date.parse(item.expires_at);
    return Number.isFinite(at) && at <= until;
  }).length;
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
      // 这一行以前写着「随『人员与权限』页上线」，而 /identity 早已建成——
      // 一句指错方向的话比没有话更坏：它让人去等一个已经到货的东西。两件事
      // 各有各的缺口，都不在页面上：凭据到期缺的是数据模型（同上面的
      // WORK_CATEGORIES.expiring，共用 CREDENTIAL_EXPIRY_GAP 一份文案），
      // 权限异常缺的是判定规则——员工账号有角色，但全仓没有任何一处算过
      // 「哪个角色组合算异常」，没有规则就没有异常可报。
      detail: `${CREDENTIAL_EXPIRY_GAP}权限异常则还没有判定规则：员工账号与角色读得到，但没有任何一条规则说什么算异常。`,
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
  /** 这一格**为什么**是这个状态。缺省不渲染（没有要解释的东西就不说话）。
   *
   *  一个四个字的徽章回答不了「黄灯亮了两周是怎么回事」。原因从这一行自己的
   *  数据里得出（哪几条指标 is_partial、哪几条从未采到值），不是把后端的事实
   *  再抄一份到前端。 */
  freshnessNote?: string;
  /** 悬停里的工程证据：相关指标的 watermark 原样文本。**只展示不解析**——
   *  它是连接器私有的自由文本（NewAPI 那条里确实带着
   *  `subscription:unavailable_over_http`），解析它等于在前端复刻一份后端事实。 */
  freshnessEvidence?: string;
  /** 这一行说的是什么范围。只有会被误读的行才挂（今天是开票系统那一行）。 */
  scopeNote?: string;
}

/** 新鲜度从最差到最好的顺序。**与后端 `ops.Observation.Freshness` 同一个顺序**
 *  （internal/platform/ops/freshness.go：失败 > 未初始化 > 延迟 > 部分 > 新鲜）。
 *
 *  这一份是副本，不是真相源——后端没有导出有序清单，所以 labels.reconcile 的
 *  「新鲜度优先级」一组从 Go 函数体里把顺序机械推导出来，与本表逐值对账；后端
 *  调整优先级而这里没跟，那一组会红。**顺序不要再抄第三份。**
 *
 *  以前这里是 `{ uninitialized: 0, failed: 1, … }`，把「未初始化」排在「同步
 *  失败」**之前**，与后端正好相反。后果不是排版问题：Sub2API 有一条结构上永远
 *  采不到数的指标（订阅制上游没有钱包余额，`sub2api.channels.balance` 每轮都
 *  写一行没有观测时刻的记录），于是那一格恒为中性灰的「未初始化」，**哪怕其余
 *  六条指标全部同步失败也不会变红**——对该平台的任何后续故障失明。后端为同一
 *  件事专门修过（XM-0031，原话：否则正在发生的故障会被显示成中性的「尚未接
 *  入」），前端在汇总时又反着实现了一遍。 */
export const FRESHNESS_PRIORITY: readonly string[] = [
  "failed",
  "uninitialized",
  "stale",
  "partial",
  "fresh",
];

const FRESHNESS_RANK: Record<string, number> = Object.fromEntries(
  FRESHNESS_PRIORITY.map((state, index) => [state, index]),
);

/** 前端不认识的状态排在**比 failed 还差**的位置。
 *
 *  这不是防御性编程的客套：以前的兜底写的是 `?? 0`，而 0 恰好是当时最差的那
 *  档（uninitialized），所以它「碰巧」是对的——把 0 让给 failed 之后，同一个
 *  `?? 0` 就变成「与同步失败并列」，再加上 `rank < worstRank` 的严格小于，先
 *  到的 failed 会把一个后端新增的、我们完全不认识的状态吃掉。
 *
 *  显式给一个更差的哨兵，是让这个条件为自己负责：未知状态必须冒出来让人去查
 *  （describeFreshness 对它已经是 warning 语气 +「未知状态（x）」的措辞）。 */
const UNKNOWN_STATE_RANK = -1;

const UNINITIALIZED: FreshnessContract = {
  state: "uninitialized",
  staleness_seconds: null,
  threshold_seconds: 0,
  is_partial: false,
  observed_at: null,
  last_success: null,
  last_error_code: "",
};

/** 新鲜度取**最差**的那一条，不取平均也不取最新。
 *
 *  一个平台有五条指标、其中一条已经两小时没更新时，矩阵上那一格必须显示
 *  「延迟」。取最新的那条会把这件事盖掉，而它正是人来这一屏要找的东西。 */
function worstFreshness(metrics: readonly MetricItem[]): FreshnessContract {
  let worst: FreshnessContract | undefined;
  for (const metric of metrics) {
    const rank = FRESHNESS_RANK[metric.freshness.state] ?? UNKNOWN_STATE_RANK;
    const worstRank = worst
      ? (FRESHNESS_RANK[worst.state] ?? UNKNOWN_STATE_RANK)
      : Number.POSITIVE_INFINITY;
    if (rank < worstRank) worst = metric.freshness;
  }
  return worst ?? UNINITIALIZED;
}

/** 这一格为什么是这个状态，以及有什么工程证据可放进悬停。
 *
 *  只解释两种今天真的会让人困惑的状态：
 *
 *  - **partial**：NewAPI 那格的黄灯永远亮着，界面上一个字的说明都没有。原因
 *    从数据里发现——点名该平台里哪几条 `is_partial`，再加一句通用解释。**不**
 *    在前端复述「NewAPI 上游没有订阅订单接口」这条后端事实，那是第二份副本；
 *    真正的机器码原因要等后端的 `partial_reason` 字段（见交接文档 follow_up）。
 *  - **uninitialized**：「这个平台一条指标都没有」（服务器）与「有指标在采、
 *    其中一条从未采到值」（Sub2API 的渠道余额）是两回事，而今天两者显示成同
 *    一个中性灰徽章。这个区分**不需要新字段**，从有没有指标行就能得出。
 *
 *  措辞**止步于事实**：不说「不适用」。要断言「订阅制上游结构上就没有钱包
 *  余额」，前端唯一的自有手段是硬编一张指标键名单——那是禁止的第二份副本，
 *  而且一条刚上线还没采到值的新指标与它长得一模一样。
 *
 *  `subject` 是「一条指标都没有」那句话的主语：平台行说「这个平台」，开票行
 *  说「开票的只读数据通道」——开票不是平台（同一格的 scopeNote 正在说它是只读
 *  数据对接），这句话不能把它叫成平台。 */
function describeWhy(
  worst: FreshnessContract,
  own: readonly MetricItem[],
  subject: string,
): Pick<MatrixRow, "freshnessNote" | "freshnessEvidence"> {
  if (worst.state === "partial") {
    const partials = own.filter((m) => m.freshness.is_partial);
    const names = partials.map((m) => metricLabel(m.metric_key)).join("、");
    const who = names === "" ? "其中一部分指标" : names;
    const evidence = partials
      .filter((m) => m.watermark !== "")
      .map((m) => `${metricLabel(m.metric_key)}：${m.watermark}`)
      .join("；");
    return {
      freshnessNote:
        `${who}这一轮采集成功了，但其中一部分数据上游给不出来，` +
        "显示的数值可能偏小；这不是故障，也不会自己好转。",
      ...(evidence === ""
        ? {}
        : { freshnessEvidence: `采集水位线（连接器原文）：${evidence}` }),
    };
  }
  if (worst.state === "uninitialized") {
    if (own.length === 0) return { freshnessNote: `${subject}还没有任何指标在采。` };
    // 走到这里 never 至少有一条：own 非空时 worst 就是 own 里某一条的 freshness
    // （worstFreshness 只在 own 为空时才用 UNINITIALIZED 兜底），worst 是
    // uninitialized 就意味着那一条自己是 uninitialized。所以这里不设空数组分支
    // ——设了也走不到，只会让下一个人以为存在第三种情形。
    const never = own
      .filter((m) => m.freshness.state === "uninitialized")
      .map((m) => metricLabel(m.metric_key));
    return {
      freshnessNote:
        `有指标在采，但「${never.join("、")}」从未采到值。` +
        "「还没接上」与「每轮都在采、每轮都采到空」在这里长得一样，要看这条指标本身才分得清。",
    };
  }
  return {};
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
    const freshness = worstFreshness(own);
    const why = describeWhy(freshness, own, "这个平台");
    return {
      key: spec.serviceType,
      label: spec.label,
      stage: platformNavSpec(spec.serviceType)?.stage ?? "—",
      statusLabel: status ? status.label : pendingBadge(spec.plan),
      statusTone: status ? status.tone : "neutral",
      freshness,
      events,
      observedAt: latestObservedAt(own),
      to: `/platforms/${spec.serviceType}`,
      ...why,
    };
  });

  rows.push(invoiceRow({ services, metrics, alerts: active }));

  return rows;
}

/** 开票系统那一行。
 *
 *  **和平台行走同一段计算**，不再是三个写死的字面量。今天算出来仍然是
 *  「未接入 / 未初始化 / 0」——那正是要的结果：诚实不等于换个结论。区别在于
 *  将来只读通道真的接上了、或者接上之后挂了，这一行会跟着变，而写死的版本
 *  只会一直说同样三个词（`invoice.requests.daily` / `invoice.amount.daily`
 *  已经在 ops 的 registeredMetrics 白名单里，所以这是一条真通路）。
 *
 *  与 `NON_PLATFORM_SERVICE_TYPES`（platforms.ts 里显式把 invoice 挡在侧栏平台
 *  段外）**不矛盾，两处问的不是同一个问题**：侧栏问「哪些是平台」，答案是开票
 *  不是；矩阵问「我管的这些系统现在怎么样」，答案里有开票。别把其中一处当成
 *  bug 顺手改掉。 */
function invoiceRow({ services, metrics, alerts }: MatrixInput): MatrixRow {
  const own = metrics.filter((m) => platformOfMetricKey(m.metric_key) === INVOICE_SERVICE_TYPE);
  const instances = services.filter((s) => s.service_type === INVOICE_SERVICE_TYPE);
  const events = alerts.filter(
    (a) => platformOfMetricKey(a.source_metric_key) === INVOICE_SERVICE_TYPE,
  ).length;

  const registered = instances[0];
  const status = registered ? describeServiceStatus(registered.status) : undefined;
  // 三级口径，与平台行同构：有登记实例就用 Registry 状态；没实例但已经有
  // invoice.* 指标在采，说「未登记」（数据先到、登记没跟上，也是一种事实）；
  // 两样都没有才是「未接入」——今天走的是这一支。
  const statusLabel = status ? status.label : own.length > 0 ? "未登记" : "未接入";
  const freshness = worstFreshness(own);
  // 主语不是「这个平台」：开票不是平台，同一格的 scopeNote 正说着它是只读数据对接
  const why = describeWhy(freshness, own, "开票的只读数据通道");

  return {
    key: INVOICE_SERVICE_TYPE,
    label: "开票系统",
    stage: "契约前置",
    statusLabel,
    statusTone: status ? status.tone : "neutral",
    freshness,
    events,
    observedAt: latestObservedAt(own),
    // 开票不是平台（ADMIN-IA §5.2）：它的入口在治理段「跨平台财务」的
    // 「开票集成」子页签，以及各平台「支付与财务」里的「开票」
    to: "/finance?sub=invoicing",
    ...why,
    scopeNote: INVOICE_SCOPE_NOTE,
  };
}

/** 开票在登记簿与指标键里的 service_type。 */
const INVOICE_SERVICE_TYPE = "invoice";

/** 开票那一行说的是什么。
 *
 *  这一行写着「未接入」，而点它的链接进去是一个**能用的、嵌着开票管理端的
 *  页面**（那条线 09-02 就上线了）。行说的是只读数据对接，链接去的是管理端
 *  嵌入——今天格子里没有任何东西告诉人这是两回事，于是它每天都在误导人。 */
const INVOICE_SCOPE_NOTE =
  "这一行说的是只读数据对接（invoice.* 指标与登记簿实例）；" +
  "点链接进去的是嵌到平台里的开票管理端，那条线已经能用——两者不是一回事。";

