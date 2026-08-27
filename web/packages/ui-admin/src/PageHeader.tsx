import type { ReactNode } from "react";
import { Button } from "@xingmang/ui-primitives";
import { formatLocalClock } from "./freshness";

export interface PageHeaderProps {
  title: string;
  /** 与标题同一层的状态与阶段标签（ServiceStatusBadge、「未接入·M3」这类）。
   *
   *  §11.2 要求标题、状态、阶段标签同层：状态挪到下一行或右侧，人就会先把
   *  标题读成「这个页面是什么」，再单独去找「它现在什么情况」——而这两件事
   *  在运营台上永远是一起看的。 */
  status?: ReactNode;
  description?: ReactNode;
  /** 传了就显示刷新按钮。看板的数据是拉取来的，人得能主动要一次新的。 */
  onRefresh?: () => void;
  refreshing?: boolean;
  /** 最近一次成功取到数据的时刻（毫秒）。
   *
   *  自动轮询让页面「自己会动」，于是必须回答一个新问题：这一屏是什么时候的？
   *  没有这行字，人分不清「数值没变」和「轮询已经停了」。 */
  lastRefreshedAt?: number | undefined;
  /** 标题右侧的附加内容（徽章、说明等）。 */
  actions?: ReactNode;
}

export function PageHeader({
  title,
  status,
  description,
  onRefresh,
  refreshing,
  lastRefreshedAt,
  actions,
}: PageHeaderProps) {
  return (
    <header className="mb-4 flex items-start justify-between gap-4">
      <div className="min-w-0">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
          <h2 className="flex min-w-0 items-center gap-2 text-base font-semibold text-fg">
            {/* 靛蓝亮边，同导航选中项与指标卡顶边，是这套设计仅有的三处标识 */}
            <span
              aria-hidden="true"
              className="h-4 w-0.5 shrink-0 rounded-full bg-linear-to-b from-accent to-transparent"
            />
            <span className="truncate">{title}</span>
          </h2>
          {status}
        </div>
        {description ? <p className="mt-1 text-xs text-fg-muted">{description}</p> : null}
      </div>
      <div className="flex shrink-0 items-center gap-3">
        {actions}
        {lastRefreshedAt ? (
          <span className="text-xs text-fg-muted tabular-nums">
            最后刷新 {formatLocalClock(lastRefreshedAt)}
          </span>
        ) : null}
        {onRefresh ? (
          <Button variant="secondary" size="sm" onClick={onRefresh} loading={refreshing}>
            刷新
          </Button>
        ) : null}
      </div>
    </header>
  );
}
