import type { ReactNode } from "react";
import { Button } from "@xingmang/ui-primitives";

export interface AdminShellProps {
  /** 侧边导航内容（由应用注入自己的 NavLink，包本身不依赖 router） */
  nav: ReactNode;
  title?: string;
  user?: { name: string };
  onLogout?: () => void;
  /** 全局横幅槽位：跨整个窗口宽度、压在导航与顶栏之上。
   *
   *  刻意做成插槽而不是 `message?: string`：横幅的内容与判定属于应用
   *  （壳不知道什么是「演示数据」），壳只负责保证它躲不开——放在最外层，
   *  而不是主内容区里跟着页面一起滚走。 */
  banner?: ReactNode;
  children: ReactNode;
}

/** 管理后台外壳：左侧导航 + 顶栏 + 主内容区（规格 §7.3 ui-admin/AdminShell）。
 *  桌面宽屏优先（规格 §7.7-13）。 */
export function AdminShell({
  nav,
  title = "星芒统一控制平台",
  user,
  onLogout,
  banner,
  children,
}: AdminShellProps) {
  return (
    <div className="flex min-h-screen flex-col bg-canvas text-fg font-sans">
      {banner}
      <div className="flex min-h-0 flex-1">
        <aside className="flex w-56 shrink-0 flex-col border-r border-edge bg-surface">
          <div className="flex h-14 items-center border-b border-edge px-4 text-sm font-semibold">
            {title}
          </div>
          <nav aria-label="主导航" className="flex-1 space-y-1 p-2 text-sm">
            {nav}
          </nav>
        </aside>
        <div className="flex min-w-0 flex-1 flex-col">
          <header className="flex h-14 items-center justify-end gap-3 border-b border-edge bg-surface px-4">
            {user ? <span className="text-sm text-fg-muted">{user.name}</span> : null}
            {onLogout ? (
              <Button variant="ghost" size="sm" onClick={onLogout}>
                退出登录
              </Button>
            ) : null}
          </header>
          <main className="min-w-0 flex-1 p-6">{children}</main>
        </div>
      </div>
    </div>
  );
}
