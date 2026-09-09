/** 告警的展示口径（XM-0033）。
 *
 *  抽成纯函数是为了能直接断言这些规则本身：一个把 SILENCED 显示成绿色
 *  「已解决」的映射，在界面上和正确实现长得几乎一样，只有测试看得出来。
 *
 *  颜色**只经 BadgeTone**，不出现任何色值（宪法：前端禁止硬编码颜色）。 */
import type { BadgeTone } from "@xingmang/ui-primitives";
import type {
  AlertItem,
  AlertNotifyStatus,
  AlertSeverity,
  AlertStatus,
  SilenceItem,
  SilenceState,
} from "../api/alerts";

export interface Display {
  label: string;
  tone: BadgeTone;
  /** 鼠标悬停时的解释。徽章只有两三个字，语义靠它补全。 */
  hint: string;
}

/** 严重度的展示口径（规格 §9.3「严重度」）。 */
export function describeSeverity(severity: AlertSeverity | string): Display {
  switch (severity) {
    case "critical":
      return { label: "严重", tone: "danger", hint: "critical：需要立即处置" };
    case "warning":
      return { label: "警告", tone: "warning", hint: "warning：需要关注，未必要立即动手" };
    case "info":
      return { label: "提示", tone: "info", hint: "info：仅供知悉" };
    default:
      // 未知严重度按最高级别显示，不按最低。
      // 后端加了新级别而前端没跟上时，把它显示成「提示」会让一条可能是
      // 灾难级的告警安静地躺在列表底部——不认识就当严重的处理才是安全的默认。
      return { label: severity || "未知", tone: "danger", hint: `未知严重度 ${severity}：按最高级别显示` };
  }
}

/** 告警状态的展示口径（规格 §9.3 的五态）。 */
export function describeStatus(status: AlertStatus | string): Display {
  switch (status) {
    case "OPEN":
      return { label: "未处理", tone: "danger", hint: "OPEN：已触发，还没有人认领" };
    case "REOPENED":
      return { label: "复发", tone: "danger", hint: "REOPENED：解决之后 24 小时内又发生了" };
    case "ACKNOWLEDGED":
      return { label: "已确认", tone: "info", hint: "ACKNOWLEDGED：有人认领了，问题仍在" };
    case "SILENCED":
      // **不能是 success**：静默不是解决。这条告警仍然活着，只是被捂住了嘴，
      // 窗口过期后会转回 OPEN 并重新投递。绿色会让人以为问题已经没了。
      return { label: "已静默", tone: "neutral", hint: "SILENCED：静默窗口内不投递；窗口过期后条件仍成立会转回未处理" };
    case "RESOLVED":
      return { label: "已解决", tone: "success", hint: "RESOLVED：触发条件不再成立，自动恢复" };
    default:
      return { label: status || "未知", tone: "warning", hint: `未知状态 ${status}` };
  }
}

/** 投递状态的展示口径（规格 §9.3「通知投递状态」）。 */
export function describeNotifyStatus(status: AlertNotifyStatus | string): Display {
  switch (status) {
    case "delivered":
      return { label: "已投递", tone: "success", hint: "至少一个渠道确认收下了" };
    case "failed":
      return { label: "投递失败", tone: "danger", hint: "上一轮投递失败，下一轮会自动重试" };
    case "pending":
      // 中性而不是绿色：没投递出去就是没投递出去。一个渠道都没配的部署
      // 会让告警永远停在这个状态——那是真话，不该被粉饰成「排队中，马上就好」。
      return { label: "未投递", tone: "neutral", hint: "还没投递出去；若一直是这个状态，多半是没有配置任何告警渠道" };
    default:
      return { label: status || "未知", tone: "warning", hint: `未知投递状态 ${status}` };
  }
}

/** 一条告警是否可以被「确认」。
 *
 *  与后端 AcknowledgeAlert 的 WHERE 子句同一条规则：只有 OPEN / REOPENED。
 *  前端隐藏按钮**不构成安全控制**（服务端仍会拒绝），这里只是不让人点一个
 *  必然失败的按钮。 */
export function canAcknowledge(alert: AlertItem): boolean {
  return alert.status === "OPEN" || alert.status === "REOPENED";
}

export interface SeverityCounts {
  critical: number;
  warning: number;
  info: number;
  total: number;
}

/** 按严重度统计**活跃**告警，供运营总览的告警卡使用。
 *
 *  已解决的不计入：那张卡回答的是「现在有什么要处理」。
 *  静默的**计入**——它仍然活着，只是不投递；把它从计数里去掉会让
 *  「静默期间总览显示零告警」，而那正是最容易出事的时候。 */
export function countBySeverity(alerts: AlertItem[]): SeverityCounts {
  const counts: SeverityCounts = { critical: 0, warning: 0, info: 0, total: 0 };
  for (const a of alerts) {
    if (a.status === "RESOLVED") continue;
    counts.total += 1;
    if (a.severity === "critical") counts.critical += 1;
    else if (a.severity === "warning") counts.warning += 1;
    else if (a.severity === "info") counts.info += 1;
  }
  return counts;
}

/** 列表排序：严重度降序 → 最近发现降序。
 *
 *  「最严重的在最上面」是唯一合理的默认：一屏装不下时被挤到下面的，
 *  必须是最不要紧的那些。同级按最近发现排，让还在持续的排在前面。 */
export function sortForDisplay(alerts: AlertItem[]): AlertItem[] {
  const rank: Record<string, number> = { critical: 0, warning: 1, info: 2 };
  return [...alerts].sort((a, b) => {
    const ra = rank[a.severity] ?? 0; // 未知严重度按最高排（与 describeSeverity 一致）
    const rb = rank[b.severity] ?? 0;
    if (ra !== rb) return ra - rb;
    return b.last_seen_at.localeCompare(a.last_seen_at);
  });
}

/** 静默窗口三态的展示口径（Go 侧 httpapi/alert_silences.go 的 state）。
 *
 *  「生效中」用 danger 而不是 success：一个正在生效的静默窗口意味着
 *  **告警此刻是哑的**，那是需要被看见的状态，不是一切正常。绿色会让人
 *  一眼扫过去以为没事——而静默期间恰恰是最容易出事的时候。 */
export function describeSilenceState(state: SilenceState | string): Display {
  switch (state) {
    case "active":
      return { label: "生效中", tone: "danger", hint: "此刻正在压着告警：命中的规则不会投递" };
    case "scheduled":
      return { label: "未开始", tone: "info", hint: "窗口还没到开始时间，暂时不影响任何告警" };
    case "expired":
      return { label: "已过期", tone: "neutral", hint: "窗口已经结束，条件仍成立的告警会转回未处理并重新投递" };
    default:
      return { label: state || "未知", tone: "warning", hint: `未知状态 ${state}：后端可能新增了取值` };
  }
}

/** 全局窗口（rule_key 为空串）的显示名。 */
export const GLOBAL_SILENCE_RULE_LABEL = "全部规则（全局）";

/** 还要压多久（只对生效中的窗口有意义）。
 *
 *  两个时刻**都来自服务端**（ends_at 与响应里的 as_of），不碰浏览器时钟：
 *  一台快五分钟的机器会把「还剩 3 分钟」算成「已经结束」，而这一行正是
 *  运营用来判断「要不要等它自己过期」的依据。
 *
 *  两者任一缺失或不可解析时返回空串——宁可不显示，也不显示一个算错的数。 */
export function formatSilenceRemaining(endsAt: string, asOf: string): string {
  if (!endsAt || !asOf) return "";
  const end = Date.parse(endsAt);
  const now = Date.parse(asOf);
  if (Number.isNaN(end) || Number.isNaN(now)) return "";
  const minutes = Math.ceil((end - now) / 60000);
  if (minutes <= 0) return "";
  if (minutes < 60) return `还剩 ${minutes} 分钟`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) {
    const rest = minutes % 60;
    return rest === 0 ? `还剩 ${hours} 小时` : `还剩 ${hours} 小时 ${rest} 分钟`;
  }
  const days = Math.floor(hours / 24);
  const restHours = hours % 24;
  return restHours === 0 ? `还剩 ${days} 天` : `还剩 ${days} 天 ${restHours} 小时`;
}

/** 静默列表排序：生效中在最前，然后按结束时间升序（最快解除的在前）。
 *
 *  「哪些此刻在压着告警」是这一页唯一要一眼看出来的东西，所以它们置顶。
 *  同组内按 ends_at 升序，是因为运营接下来要问的是「还要压多久」——
 *  快到期的排前面，才排得上处理顺序。 */
export function sortSilencesForDisplay(items: SilenceItem[]): SilenceItem[] {
  const rank: Record<string, number> = { active: 0, scheduled: 1, expired: 2 };
  return [...items].sort((a, b) => {
    const ra = rank[a.state] ?? 3; // 未知态排最后：它不是「现在压着」的证据
    const rb = rank[b.state] ?? 3;
    if (ra !== rb) return ra - rb;
    return a.ends_at.localeCompare(b.ends_at);
  });
}

/** 静默时长的可选项。
 *
 *  给固定档而不是自由输入分钟数：自由输入的第一个失败形态是有人填了
 *  99999，等于永久关掉这条规则而没有任何东西会提醒他解除。上限 7 天
 *  与后端的 maxSilenceMinutes 一致，超出会被当场拒绝。 */
export const SILENCE_DURATION_OPTIONS = [
  { value: "30", label: "30 分钟" },
  { value: "60", label: "1 小时" },
  { value: "240", label: "4 小时" },
  { value: "720", label: "12 小时" },
  { value: "1440", label: "1 天" },
  { value: "4320", label: "3 天" },
  { value: "10080", label: "7 天（上限）" },
];
