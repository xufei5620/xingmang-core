import { Badge, type BadgeTone } from "@xingmang/ui-primitives";
import { riskLevel, riskLevelText } from "../lib/labels";

/** 风险等级的语气。**只经 BadgeTone**，不出现任何色值（宪法：前端禁止硬编码颜色）。
 *
 *  L2/L3 同为 warning、L4 单独 danger：会不会造成不可逆后果是这条线的分界，
 *  而不是「数字更大就更红」。 */
const RISK_TONE: Readonly<Record<string, BadgeTone>> = {
  L0: "neutral",
  L1: "info",
  L2: "warning",
  L3: "warning",
  L4: "danger",
};

export function riskTone(level: string): BadgeTone {
  return RISK_TONE[level] ?? "neutral";
}

/** 风险等级徽章：`L3 高风险`，悬停给出「要几票、能不能自批、多久过期」。
 *
 *  **一处实现，两页共用**（操作与审批页的目录/执行记录/等级表，以及审批队列）。
 *  这两页此前各有一份语气表和一份「等级怎么写」的逻辑，改一处不改另一处时
 *  同一个 L4 会在两页长得不一样——而这两页的人恰恰要对着同一张单说话。
 *
 *  等级串在最前面且逐字保留：ADR-003、审批策略、运维口头交流用的都是它。
 *  认不出来的等级只显示原串（等级表以后可能加档）——把一个更危险的新等级
 *  兜底成某个已知等级，比不翻译危险得多，所以文案一律走 riskLevelText。 */
export function RiskBadge({ level }: { level: string }) {
  return (
    <Badge tone={riskTone(level)} title={riskLevel(level)?.hint}>
      {riskLevelText(level)}
    </Badge>
  );
}
