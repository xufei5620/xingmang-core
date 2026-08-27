import { formatLocalClock } from "@xingmang/ui-admin";
import { Button } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";

export interface PageHeaderProps {
  title: string;
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
  description,
  onRefresh,
  refreshing,
  lastRefreshedAt,
  actions,
}: PageHeaderProps) {
  return (
    <header className="mb-4 flex items-start justify-between gap-4">
      <div className="min-w-0">
        <h2 className="text-base font-semibold text-fg">{title}</h2>
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
