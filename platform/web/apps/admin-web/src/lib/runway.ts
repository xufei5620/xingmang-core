import type { Runway, RunwayThresholds } from "../api/finance";

/** 可用天数（§10.4）在屏幕上的说法。
 *
 *  抽成一份，是因为它现在有**两个**消费者：平台概览的可用天数卡
 *  （`FinanceSummaryCards`）与渠道管理表的「余额 / 有效期」列。
 *  两处各写一份文案的下场是可预见的——同一个 `no_balance`，
 *  概览说「余额采集未接通」而表格说「暂无数据」，读的人会以为那是两件事。
 *
 *  与 XM-0049 的告警规则 R5 也是同一批档位：那边判要不要响，这边判显示成什么
 *  颜色，共用后端给的 `level` 就不会各判各的。 */

/** 可用天数给不出时的说法。**每种都有自己的话**——一个统一的「暂无数据」
 *  会让「这条渠道不该有余额」和「余额采集还没接通」看起来是同一件事。 */
export const RUNWAY_REASON_TEXT: Record<string, string> = {
  not_applicable: "订阅型渠道没有余额，可用天数对它无意义",
  no_balance: "尚未读到上游余额（余额采集未接通）",
  balance_stale: "余额观测已过期，不显示伪精确天数",
  no_consumption: "窗口内没有已知消耗，除不出天数",
  currency_mismatch: "余额与消耗币种不一致，不做换算",
};

/** 三档预警的语气。**名字的严重程度与数值方向相反**（后端沿用 SoloAI 命名）：
 *  天数越少越严重，critical 最紧、serious 最松。 */
export const RUNWAY_TONE: Record<string, "danger" | "warning" | "info" | "success"> = {
  critical: "danger",
  warning: "warning",
  serious: "info",
  healthy: "success",
};

/** 给不出天数时的一句话。
 *
 *  认不出来的 reason **不编一句话**，返回 null 让调用方显示「—」：
 *  后端将来加一种新原因时，一个含糊的兜底文案会把它伪装成已知情况。 */
export function runwayReasonText(runway: Runway): string | null {
  return RUNWAY_REASON_TEXT[runway.reason] ?? null;
}

/** 窗口与档位的说明。金额旁边必须能看到它是按什么窗口算的（§10.4）。 */
export function runwayNote(runway: Runway, thresholds: RunwayThresholds): string {
  const window = `近 ${runway.windowDays} 个完整业务日（实到 ${runway.coveredDays} 天）`;
  const tiers = `告警档 ≤${thresholds.criticalDays}/${thresholds.warningDays}/${thresholds.seriousDays} 天`;
  return `${window} · ${tiers}`;
}
