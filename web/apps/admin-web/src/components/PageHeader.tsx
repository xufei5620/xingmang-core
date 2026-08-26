import { Button } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";

export interface PageHeaderProps {
  title: string;
  description?: ReactNode;
  /** 传了就显示刷新按钮。看板的数据是拉取来的，人得能主动要一次新的。 */
  onRefresh?: () => void;
  refreshing?: boolean;
}

export function PageHeader({ title, description, onRefresh, refreshing }: PageHeaderProps) {
  return (
    <header className="mb-4 flex items-start justify-between gap-4">
      <div className="min-w-0">
        <h2 className="text-base font-semibold text-fg">{title}</h2>
        {description ? <p className="mt-1 text-xs text-fg-muted">{description}</p> : null}
      </div>
      {onRefresh ? (
        <Button variant="secondary" size="sm" onClick={onRefresh} loading={refreshing}>
          刷新
        </Button>
      ) : null}
    </header>
  );
}
