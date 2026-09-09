/** 告警页的「时间范围」筛选（XM-ALERTS-TAB-DURATION）。
 *
 *  原型逐字要「时间：最近 7 天」。它是**对已取回的那一批做的收窄**，不是查询
 *  条件：平台页一次取最近至多 200 条（含已解决），选「最近 7 天」是从这 200 条
 *  里挑，而不是让服务端去更远的历史里找。措辞与页面说明必须说清这一点，否则
 *  选了「最近 30 天」却看不到第 201 条会被当成数据丢了（宪法 12 条）。
 */

export const ALERT_WINDOW_PARAM = "alert_window";

export type AlertWindowKey = "24h" | "7d" | "30d" | "all";

export interface AlertWindowOption {
  key: AlertWindowKey;
  label: string;
  /** 相对当前时刻回看多少毫秒；`all` 不回看。 */
  spanMs: number | null;
}

const HOUR = 3_600_000;

export const ALERT_WINDOW_OPTIONS: readonly AlertWindowOption[] = [
  { key: "24h", label: "最近 24 小时", spanMs: 24 * HOUR },
  { key: "7d", label: "最近 7 天", spanMs: 7 * 24 * HOUR },
  { key: "30d", label: "最近 30 天", spanMs: 30 * 24 * HOUR },
  { key: "all", label: "不限时间", spanMs: null },
];

/** 默认不限时间：平台页本来就只有最近 200 条，再默认掐掉一段会让人以为告警
 *  少了。要收窄是主动的选择，不是页面替人做的假设。 */
export const DEFAULT_ALERT_WINDOW: AlertWindowKey = "all";

export function parseAlertWindow(raw: string | null): AlertWindowKey {
  const value = (raw ?? "").trim();
  return ALERT_WINDOW_OPTIONS.some((option) => option.key === value)
    ? (value as AlertWindowKey)
    : DEFAULT_ALERT_WINDOW;
}

export function alertWindowLabel(key: AlertWindowKey): string {
  return ALERT_WINDOW_OPTIONS.find((option) => option.key === key)?.label ?? key;
}

/** 判断一条告警是否落在窗口内。
 *
 *  用 `last_seen_at` 而不是 `opened_at`：一条两个月前开、五分钟前还在响的告警，
 *  「最近 24 小时」里必须看得见——它正是此刻要处理的那条。按开始时间过滤会把
 *  最该看的长期故障藏起来。取不出时间的一律**保留**：过滤器不该吃掉它读不懂
 *  的记录。 */
export function alertWithinWindow(
  alert: { opened_at: string; last_seen_at: string },
  key: AlertWindowKey,
  now: number,
): boolean {
  const option = ALERT_WINDOW_OPTIONS.find((item) => item.key === key);
  if (!option || option.spanMs === null) return true;
  const seen = Date.parse(alert.last_seen_at || alert.opened_at);
  if (Number.isNaN(seen)) return true;
  return now - seen <= option.spanMs;
}

/** 「持续」的人话形式：一条告警从首次发现到恢复（或到此刻）有多久。
 *
 *  原型这一列的用处是让人一眼分出「刚抖了一下」和「已经烧了一小时」。所以
 *  取整到分钟就够，且**不做四舍五入**——把 1 小时 59 分说成 2 小时会让人以为
 *  自己晚了一分钟。时间读不出来时返回「—」，不猜 0。 */
export function formatAlertDuration(openedAt: string, endAt: string | null, now: number): string {
  const start = Date.parse(openedAt);
  if (Number.isNaN(start)) return "—";
  const end = endAt ? Date.parse(endAt) : now;
  const ms = (Number.isNaN(end) ? now : end) - start;
  if (ms < 0) return "—";
  const minutes = Math.floor(ms / 60_000);
  if (minutes < 1) return "不到 1 分钟";
  const days = Math.floor(minutes / 1440);
  const hours = Math.floor((minutes % 1440) / 60);
  const rest = minutes % 60;
  if (days > 0) return hours > 0 ? `${days} 天 ${hours} 小时` : `${days} 天`;
  if (hours > 0) return rest > 0 ? `${hours} 小时 ${rest} 分` : `${hours} 小时`;
  return `${rest} 分`;
}

/** 排序用的毫秒数。未恢复的告警按「到此刻」算，于是它们自然排在同样时长的
 *  已恢复告警前面（同一时长里，还在烧的更该先看）。 */
export function alertDurationMs(openedAt: string, endAt: string | null, now: number): number | null {
  const start = Date.parse(openedAt);
  if (Number.isNaN(start)) return null;
  const end = endAt ? Date.parse(endAt) : now;
  const ms = (Number.isNaN(end) ? now : end) - start;
  return ms < 0 ? null : ms;
}
