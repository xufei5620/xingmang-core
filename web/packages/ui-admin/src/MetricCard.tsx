import type { ReactNode } from "react";
import { cx } from "@xingmang/ui-primitives";
import type { FreshnessContract } from "./freshness";
import { FreshnessBadge, FreshnessNote } from "./StatusBadges";

export interface MetricCardProps {
  /** 友好名。 */
  label: string;
  /** 指标键。等宽显示，供人核对这张卡到底画的是哪个指标。 */
  metricKey?: string;
  /** 主数值的**展示文本**。卡片不做取值与格式化——同一个 metric_key 在不同
   *  平台的口径由应用侧的 presentMetric 统一决定，卡片再算一遍就会出现两套口径。 */
  value: string;
  /** true 表示这里没有可信数值（未初始化、值形状未知等），主数位弱化显示。 */
  unavailable?: boolean;
  secondary?: string | undefined;
  freshness: FreshnessContract;
  source?: string | undefined;
  watermark?: string | undefined;
  /** 趋势图槽位。做成插槽而不是卡片自己去拉历史：卡片因此仍然是纯展示组件，
   *  渲染它不需要 QueryClient，趋势失败也天然溢不出来。 */
  trend?: ReactNode;
  /** 平台入口槽位（「查看平台 →」）。理由同 trend：卡片不认识路由，
   *  链接由调用方给——平台详情页自己就是终点，那里渲染卡片时不传。 */
  link?: ReactNode;
}

/** 单个指标卡片。
 *
 *  结构上把「数值」和「新鲜度」焊在一起：徽章与数据时间是卡片的固定部件，
 *  不是可选项——规格 §9.1 不允许出现一张只有数字的卡片。 */
export function MetricCard({
  label,
  metricKey,
  value,
  unavailable = false,
  secondary,
  freshness,
  source,
  watermark,
  trend,
  link,
}: MetricCardProps) {
  return (
    <article className="relative flex flex-col gap-2 overflow-hidden rounded-lg border border-edge bg-surface p-4 shadow-sm">
      {/* 顶边靛蓝亮边：整套设计只在导航选中项、页面标题与指标卡三处出现
          （原型设计系统 Visual signature），普通卡片与表格保持安静 */}
      <span
        aria-hidden="true"
        className="absolute inset-x-0 top-0 h-0.5 bg-linear-to-r from-accent to-transparent"
      />
      <header className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <h3 className="truncate text-sm font-medium text-fg" title={label}>
            {label}
          </h3>
          {metricKey ? (
            <p className="truncate font-mono text-xs text-fg-muted" title={metricKey}>
              {metricKey}
            </p>
          ) : null}
        </div>
        <FreshnessBadge freshness={freshness} />
      </header>

      <p
        className={cx(
          "text-2xl font-semibold tabular-nums",
          // 没有可信数值时弱化：一个灰掉的「未初始化」不会被当成读数
          unavailable ? "text-fg-muted" : "text-fg",
        )}
      >
        {value}
      </p>
      {secondary ? <p className="text-xs text-fg-muted tabular-nums">{secondary}</p> : null}

      {/* 趋势画在数值下方而不是背景里：叠在数字后面的折线会降低数字的对比度，
          而这里数字才是主角 */}
      {trend ? <div className="min-h-10">{trend}</div> : null}

      <div className="mt-auto border-t border-edge pt-2">
        <FreshnessNote freshness={freshness} />
        <p className="text-xs text-fg-muted">
          来源 {source || "—"}
          {watermark ? ` · 水位 ${watermark}` : null}
        </p>
        {/* 平台入口排在新鲜度之后：先让人看清这个数可不可信，再请他点进去 */}
        {link ? <div className="mt-2">{link}</div> : null}
      </div>
    </article>
  );
}
