import { FreshnessBadge, FreshnessNote } from "@xingmang/ui-admin";
import { cx } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";
import type { MetricItem } from "../api/platform";
import { presentMetric } from "../lib/metrics";

export interface MetricCardProps {
  item: MetricItem;
  /** 趋势图槽位。做成插槽而不是卡片自己去拉历史：卡片因此仍然是纯展示组件，
   *  渲染它不需要 QueryClient，趋势失败也天然溢不出来。 */
  trend?: ReactNode;
}

/** 单个指标卡片。
 *
 *  结构上把「数值」和「新鲜度」焊在一起：徽章与数据时间是卡片的固定部件，
 *  不是可选项——规格 §9.1 不允许出现一张只有数字的卡片。 */
export function MetricCard({ item, trend }: MetricCardProps) {
  const shown = presentMetric(item);
  return (
    <article className="flex flex-col gap-2 rounded-lg border border-edge bg-surface p-4 shadow-sm">
      <header className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <h3 className="truncate text-sm font-medium text-fg" title={shown.label}>
            {shown.label}
          </h3>
          <p className="truncate font-mono text-xs text-fg-muted" title={item.metric_key}>
            {item.metric_key}
          </p>
        </div>
        <FreshnessBadge freshness={item.freshness} />
      </header>

      <p
        className={cx(
          "text-2xl font-semibold tabular-nums",
          // 没有可信数值时弱化：一个灰掉的「未初始化」不会被当成读数
          shown.unavailable ? "text-fg-muted" : "text-fg",
        )}
      >
        {shown.primary}
      </p>
      {shown.secondary ? <p className="text-xs text-fg-muted">{shown.secondary}</p> : null}

      {/* 趋势画在数值下方而不是背景里：叠在数字后面的折线会降低数字的对比度，
          而这里数字才是主角 */}
      {trend ? <div className="min-h-10">{trend}</div> : null}

      <div className="mt-auto border-t border-edge pt-2">
        <FreshnessNote freshness={item.freshness} />
        <p className="text-xs text-fg-muted">
          来源 {item.source || "—"}
          {item.watermark ? ` · 水位 ${item.watermark}` : null}
        </p>
      </div>
    </article>
  );
}
