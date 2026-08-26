import { Badge } from "@xingmang/ui-primitives";
import {
  describeFreshness,
  describeServiceStatus,
  formatFreshnessDetail,
  formatFreshnessNote,
  type FreshnessContract,
} from "./freshness";

export interface FreshnessBadgeProps {
  freshness: FreshnessContract;
  className?: string;
}

/** 数据新鲜度徽章（规格 §9.1 / 宪法 12 条）。
 *
 *  任何展示后端数值的地方都必须挨着一个它——这是「禁止裸数字冒充实时完整数据」
 *  在 UI 层的落点。失败时把错误码也带进悬停说明，省得再翻日志。 */
export function FreshnessBadge({ freshness, className }: FreshnessBadgeProps) {
  const d = describeFreshness(freshness.state);
  const hint = freshness.last_error_code
    ? `${d.hint}（错误码 ${freshness.last_error_code}）`
    : d.hint;
  return (
    <Badge tone={d.tone} title={hint} className={className}>
      {d.label}
    </Badge>
  );
}

/** 新鲜度说明行：数据时间 + 落后多久（精确秒数与阈值在 title 里）。 */
export function FreshnessNote({ freshness }: { freshness: FreshnessContract }) {
  return (
    <p className="text-xs text-fg-muted" title={formatFreshnessDetail(freshness)}>
      {formatFreshnessNote(freshness)}
      {freshness.is_partial && freshness.state !== "partial" ? "（数据不完整）" : null}
    </p>
  );
}

/** 服务状态徽章（active / degraded / retired）。 */
export function ServiceStatusBadge({ status }: { status: string }) {
  const d = describeServiceStatus(status);
  return (
    <Badge tone={d.tone} title={d.hint}>
      {d.label}
    </Badge>
  );
}
