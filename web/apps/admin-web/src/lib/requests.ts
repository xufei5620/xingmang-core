import type { BadgeTone } from "@xingmang/ui-primitives";
import type { RequestSummary } from "../api/requests";

/** 请求详情页面的展示口径。
 *
 *  抽成纯函数是为了让这些判断能被单测钉住——它们大多是「空值该显示成什么」，
 *  而空值恰恰是这条数据里最常见、也最容易被渲染成 "undefined" 的东西
 *  （令牌映射不到用户名、上游没记 TTFB、连接中断没有状态码）。 */

/** 令牌映射不到用户名时的显示文案。
 *
 *  必须是一句话而不是空白：空白会被读成「这条请求没有用户」，
 *  而事实是「有用户，只是我们没查到它叫什么」——后者要去 NewAPI 后台
 *  拿 token_prefix 反查，前者什么也不用做。两种处置完全不同。 */
export const UNMAPPED_USER_TEXT = "未映射";

/** 上游没给该字段时的占位符。 */
export const MISSING_VALUE_TEXT = "—";

/** reqlog 没记到状态码（连接中断）时的显示文案。 */
export const NO_STATUS_TEXT = "无响应";

export function formatUsername(username: string): string {
  return username.trim() === "" ? UNMAPPED_USER_TEXT : username;
}

export function isUnmappedUser(username: string): boolean {
  return username.trim() === "";
}

/** HTTP 状态码 → 文案与语气。
 *
 *  status === 0 归入 danger 而不是 neutral：那是一次**没成功**的请求
 *  （reqlog 连状态码都没记到，通常是连接中断）。画成中性色会让它在一屏
 *  绿色里看不出来，而它恰恰是最该被看见的那一类。 */
export function describeStatus(status: number): { label: string; tone: BadgeTone } {
  if (status === 0) return { label: NO_STATUS_TEXT, tone: "danger" };
  if (status >= 200 && status <= 299) return { label: String(status), tone: "success" };
  if (status === 429) return { label: "429", tone: "warning" };
  if (status >= 400 && status <= 499) return { label: String(status), tone: "warning" };
  if (status >= 500) return { label: String(status), tone: "danger" };
  // 1xx/3xx 走到这里说明上游行为超出预期，显式标出来而不是当成正常
  return { label: String(status), tone: "neutral" };
}

/** 毫秒 → 人读的时长。
 *
 *  一秒以内保留毫秒（排查延迟时那几十毫秒是有意义的），
 *  超过一秒转成秒并保留一位小数（"18.4 s" 比 "18420 ms" 好判断量级）。 */
export function formatMillis(ms: number | null | undefined): string {
  if (ms === null || ms === undefined || !Number.isFinite(ms) || ms < 0) {
    return MISSING_VALUE_TEXT;
  }
  if (ms < 1000) return `${Math.round(ms)} ms`;
  return `${(ms / 1000).toFixed(1)} s`;
}

/** TTFB 的显示。
 *
 *  与 formatMillis 分开一个函数，只为守住一条区分：**null 不等于 0**。
 *  null 是「没测」（非流式请求通常没有），0 是一个合法观测值（缓存命中）。
 *  合并成一个 `ttfb || "—"` 会把缓存命中显示成「没测」，
 *  而缓存命中率恰恰是这一页最有价值的观察之一。 */
export function formatTTFB(ttfbMs: number | null): string {
  if (ttfbMs === null) return MISSING_VALUE_TEXT;
  return formatMillis(ttfbMs);
}

/** 字节数 → 人读的大小。截断提示要显示「共多少」，靠它。 */
export function formatBytes(bytes: number | null | undefined): string {
  if (bytes === null || bytes === undefined || !Number.isFinite(bytes) || bytes < 0) {
    return MISSING_VALUE_TEXT;
  }
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}

/** token 三元组的一行摘要：输入 / 输出 / 缓存。
 *
 *  缓存命中数**单列**而不是并进输入：它们计费口径不同，
 *  加在一起会得到一个既不是用量也不是成本的数字。 */
export function formatTokens(item: RequestSummary): string {
  return `${item.tokens_in} / ${item.tokens_out} / ${item.tokens_cache}`;
}

/** 消息角色 → 中文标签。
 *
 *  认不出的角色**原样显示**而不是归到「其他」：上游协议随时会长出新角色
 *  （tool / developer / function），把它们统一画成「其他」，
 *  读的人就分不出一条工具调用和一条开发者指令。 */
export function describeRole(role: string): { label: string; tone: BadgeTone } {
  switch (role) {
    case "system":
      return { label: "系统", tone: "neutral" };
    case "user":
      return { label: "用户", tone: "info" };
    case "assistant":
      return { label: "助手", tone: "success" };
    case "tool":
      return { label: "工具", tone: "warning" };
    default:
      return { label: role || "未知角色", tone: "warning" };
  }
}

/** 截断提示文案。未截断时返回空串（调用方据此不渲染那一行）。 */
export function truncationNote(truncated: boolean, originalBytes: number, shownBytes: number): string {
  if (!truncated) return "";
  return `内容过长，只显示了前 ${formatBytes(shownBytes)}（原文共 ${formatBytes(originalBytes)}）`;
}

/** 保留期提示。
 *
 *  一直显示而不是只在空结果时显示：人翻不到某天的请求时，第一反应是
 *  「是不是筛错了」，而真正的原因常常是那天已经出了保留窗口。
 *  这句话摆在筛选条旁边，比在空状态里补一句更早地回答那个问题。 */
export function retentionNote(days: number): string {
  if (!Number.isFinite(days) || days <= 0) return "";
  return `请求审计只保留最近 ${days} 天，更早的记录已被清理`;
}

/** UTF-8 字节数（浏览器侧）。用于「显示了多少」这一半的计算。 */
export function utf8Bytes(text: string): number {
  return new TextEncoder().encode(text).length;
}
