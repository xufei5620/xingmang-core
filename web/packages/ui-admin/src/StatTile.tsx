import type { ReactNode } from "react";
import { cx } from "@xingmang/ui-primitives";

export interface StatTileProps {
  label: string;
  /** 主数位的**展示文本**。没有可信数值时传 `—` 并置 `unavailable`。 */
  value: string;
  /** true = 这一格还没有数据源，主数位弱化，不冒充成一个读数。 */
  unavailable?: boolean;
  /** 副行：这个数怎么来的、包含什么。**必填**——一个孤零零的「3」
   *  在运营台上没有意义，人第一个问题永远是「哪三个」。 */
  note: string;
  /** 右上角状态槽（未接入徽章等）。 */
  status?: ReactNode;
  /** 底部入口槽（「查看全部告警 →」）。 */
  link?: ReactNode;
}

/** 工作台顶部的计数格(原型 `tile`，运营工作台的 `grid g4`)。
 *
 *  与 MetricCard 刻意不同：**没有新鲜度徽章**。这些格子数的是告警与待办,
 *  不是采集来的指标——告警是对数字的判读，不是数字本身，给它套一个新鲜度
 *  会是编造出来的语义（与 AlertSummaryCard 同一条理由）。
 *
 *  靛蓝亮边也不给：整套设计只在导航选中项、页面标题、指标卡三处出现
 *  (原型设计系统 Visual signature)，多了就从标识变成装饰。 */
export function StatTile({ label, value, unavailable = false, note, status, link }: StatTileProps) {
  return (
    <article className="flex flex-col gap-1 rounded-lg border border-edge bg-surface p-4 shadow-sm">
      <header className="flex items-start justify-between gap-2">
        <h3 className="truncate text-sm font-medium text-fg-muted" title={label}>
          {label}
        </h3>
        {status}
      </header>
      <p
        className={cx(
          "text-2xl font-semibold tabular-nums",
          unavailable ? "text-fg-muted" : "text-fg",
        )}
      >
        {value}
      </p>
      <p className="text-xs text-fg-muted">{note}</p>
      {link ? <div className="mt-auto pt-2">{link}</div> : null}
    </article>
  );
}
