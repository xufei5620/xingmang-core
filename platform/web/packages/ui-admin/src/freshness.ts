import type { BadgeTone } from "@xingmang/ui-primitives";

/** 新鲜度契约形状，与 `GET /api/v1/metrics` 响应里的 `freshness` 一一对应
 *  （后端 `internal/platform/ops/freshness.go`）。
 *
 *  这里直接用 snake_case 而不是再套一层 camelCase 映射：多一层映射就多一处
 *  会漂移的定义，而这个形状是平台契约（规格 §9.1），不是某个页面的私有 DTO。 */
export interface FreshnessContract {
  state: string;
  /** null 表示无法计算（从未采集）。规格 §9.1：该值不落库，查询时动态算。 */
  staleness_seconds: number | null;
  threshold_seconds: number;
  is_partial: boolean;
  observed_at: string | null;
  last_success: string | null;
  last_error_code: string;
}

/** 后端已定义的五个主状态（`ops.State`）。 */
export type FreshnessState = "uninitialized" | "failed" | "stale" | "partial" | "fresh";

export interface StateDisplay {
  label: string;
  tone: BadgeTone;
  /** 悬停说明：徽章只有四个字，"为什么是这个状态"放这里。 */
  hint: string;
}

const freshnessDisplay: Record<FreshnessState, StateDisplay> = {
  // uninitialized 用 neutral 而不是告警色：从未采集通常意味着「还没接上」，
  // 不是故障。真正的保护不在徽章颜色，而在值的位置显示「未初始化」而非 0
  // （宪法 12 条：禁止裸数字冒充实时完整数据）
  uninitialized: { label: "未初始化", tone: "neutral", hint: "从未成功采集过，页面上没有可信数值" },
  failed: { label: "同步失败", tone: "danger", hint: "最近一次同步失败，下方数值是上一次成功的结果" },
  stale: { label: "数据延迟", tone: "warning", hint: "已超过该指标的新鲜度阈值" },
  partial: { label: "数据不完整", tone: "warning", hint: "采集成功但数据不全，数值可能偏小" },
  fresh: { label: "数据新鲜", tone: "success", hint: "在新鲜度阈值内且数据完整" },
};

/** 状态 → 文案与语气。
 *
 *  未知状态一律按 warning 处理而不是静默降级成 neutral：后端将来新增状态时，
 *  前端宁可显示「未知状态」让人来查，也不能把它渲染成看起来正常的样子。 */
export function describeFreshness(state: string): StateDisplay {
  const known = freshnessDisplay[state as FreshnessState];
  if (known) return known;
  return {
    label: `未知状态（${state}）`,
    tone: "warning",
    hint: "前端不认识这个新鲜度状态，请勿据此判断数据可信",
  };
}

/** 服务状态（后端 `registry.ServiceStatus`）。 */
export type ServiceStatus = "active" | "degraded" | "retired";

const serviceStatusDisplay: Record<ServiceStatus, StateDisplay> = {
  active: { label: "运行中", tone: "success", hint: "服务正常提供能力" },
  degraded: { label: "降级", tone: "warning", hint: "服务可用但能力受限" },
  retired: { label: "已下线", tone: "neutral", hint: "服务已退役，仅保留记录" },
};

/** 服务状态 → 文案与语气。未知值同样显式暴露，理由同 describeFreshness。 */
export function describeServiceStatus(status: string): StateDisplay {
  const known = serviceStatusDisplay[status as ServiceStatus];
  if (known) return known;
  return { label: `未知（${status}）`, tone: "warning", hint: "前端不认识这个服务状态" };
}

const MINUTE = 60;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/** 秒 → 人类可读的落后时长。
 *
 *  只保留一个量级（「3 小时」而不是「3 小时 12 分 5 秒」）：看板上要的是
 *  「旧到什么程度」的量级判断，精确值在 observed_at 里。 */
export function formatDuration(seconds: number): string {
  // 观测时间在未来时后端已钳到 0，这里再兜一次，避免出现「落后 -5 秒」
  const s = Number.isFinite(seconds) && seconds > 0 ? Math.floor(seconds) : 0;
  if (s < MINUTE) return `${s} 秒`;
  if (s < HOUR) return `${Math.floor(s / MINUTE)} 分钟`;
  if (s < DAY) return `${Math.floor(s / HOUR)} 小时`;
  return `${Math.floor(s / DAY)} 天`;
}

function pad2(n: number): string {
  return n < 10 ? `0${n}` : String(n);
}

/** RFC3339 时间串 → `YYYY-MM-DD HH:mm:ss UTC`。
 *
 *  一律显示 UTC 并写明时区后缀（宪法 14 条：时间库内 UTC，时区必须显式）。
 *  不走 toLocaleString：那会随运行机器的时区变来变去，两个人截图对不上，
 *  测试也不可复现。 */
export function formatUtcTimestamp(iso: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso; // 解析不了就原样显示，不吞掉信息
  return (
    `${d.getUTCFullYear()}-${pad2(d.getUTCMonth() + 1)}-${pad2(d.getUTCDate())} ` +
    `${pad2(d.getUTCHours())}:${pad2(d.getUTCMinutes())}:${pad2(d.getUTCSeconds())} UTC`
  );
}

/** 本地时区偏移，形如 `UTC+08:00`。 */
function localOffsetLabel(d: Date): string {
  // getTimezoneOffset 是「UTC 减本地」的分钟数，所以东八区返回 -480，符号要翻过来
  const minutes = -d.getTimezoneOffset();
  const sign = minutes < 0 ? "-" : "+";
  const abs = Math.abs(minutes);
  return `UTC${sign}${pad2(Math.floor(abs / 60))}:${pad2(abs % 60)}`;
}

/** RFC3339 时间串 → 本地时区的 `YYYY-MM-DD HH:mm:ss (UTC+08:00)`。
 *
 *  审计事件是「谁在什么时候做了什么」，读的人要拿它和自己的记忆、和 IM 记录
 *  对时间，所以列表里显示本地时间才有用；但**必须把时区写出来**，否则截图
 *  发给另一个时区的人就成了错的（宪法 14 条：时区必须显式）。
 *  权威的 UTC 值不丢，由调用方放进 title。
 *
 *  不走 toLocaleString：它的输出随运行环境的语言设置变来变去，
 *  两个人截图对不上，测试也不可复现。 */
export function formatLocalTimestamp(iso: string | null): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso; // 解析不了就原样显示，不吞掉信息
  return (
    `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ` +
    `${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())} ` +
    `(${localOffsetLabel(d)})`
  );
}

/** 毫秒时间戳 → 本地 `HH:mm:ss`。
 *
 *  只给「最后刷新于几点」这类当下发生的事用：人是拿它和手表对，秒级就够，
 *  年月日反而是噪音。**不用于数据时间**——那个必须带日期与时区
 *  （formatUtcTimestamp / formatLocalTimestamp）。 */
export function formatLocalClock(epochMs: number | null | undefined): string {
  if (epochMs === null || epochMs === undefined || !Number.isFinite(epochMs)) return "—";
  const d = new Date(epochMs);
  if (Number.isNaN(d.getTime())) return "—";
  return `${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`;
}

/** 新鲜度一行说明：「数据时间 … · 落后 …」。
 *
 *  规格 §9.1 要求数据时间与落后秒数都可见，所以这两段是一起产出的，
 *  调用方没法只显示其中一个。 */
export function formatFreshnessNote(freshness: FreshnessContract): string {
  if (!freshness.observed_at) return "数据时间 —（从未成功采集）";
  const observed = `数据时间 ${formatUtcTimestamp(freshness.observed_at)}`;
  if (freshness.staleness_seconds === null) return observed;
  return `${observed} · 落后 ${formatDuration(freshness.staleness_seconds)}`;
}

/** 精确到秒的落后量与阈值，放进 title。
 *
 *  正文写「落后 30 分钟」是为了一眼看出量级，但正好卡在阈值上时人得看到确切
 *  秒数才知道到底越没越线——所以精确值不能丢，只是挪到悬停里。 */
export function formatFreshnessDetail(freshness: FreshnessContract): string {
  const threshold = `阈值 ${freshness.threshold_seconds} 秒`;
  if (freshness.staleness_seconds === null) return `从未成功采集；${threshold}`;
  return `落后 ${freshness.staleness_seconds} 秒；${threshold}`;
}
