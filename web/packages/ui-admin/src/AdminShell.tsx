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
  /** 面包屑与环境提示条（见 ContextStrip）。在顶栏之下、主内容之上，
   *  跟着壳而不是跟着页面：它说明的是「你在哪、这屏数据是什么性质」，
   *  换页时这两件事都还在。 */
  contextStrip?: ReactNode;
  /** 顶栏左侧的全局搜索。不传时渲染占位（阶段 2 才实装），
   *  留成插槽是为了到时候只换这一处，不动壳的布局。 */
  search?: ReactNode;
  children: ReactNode;
}

/** 顶栏搜索占位。
 *
 *  用 <span aria-disabled> 而不是 <input> 或 <button>：一个能聚焦、能输入却
 *  什么都搜不到的框比没有框更糟——人会以为是搜索坏了。理由同 NavItemDisabled，
 *  位置必须先占住（§11.2 顶栏含全局搜索），但必须明确地点不动。 */
function SearchPlaceholder() {
  return (
    <span
      aria-disabled="true"
      title="全局搜索属于 UI 交接阶段 2，当前只占位"
      className="flex h-8 w-80 max-w-full cursor-not-allowed items-center justify-between gap-2 rounded-md border border-edge bg-surface-muted px-2 text-xs text-fg-muted"
    >
      <span className="truncate">搜索平台、渠道、审计事件（阶段 2）</span>
      <kbd className="shrink-0 rounded-sm border border-edge px-1 font-mono text-xs">Ctrl K</kbd>
    </span>
  );
}

/** 管理后台外壳：深色左侧导航 + 顶栏 + 主内容区（规格 §7.3 ui-admin/AdminShell）。
 *  桌面宽屏优先（规格 §7.7-13）。
 *
 *  左栏用 nav-* 令牌而不是 surface/fg：深色导航配浅色内容区是这套设计的固定
 *  结构（UI 交接文档 §11.1），不是「暗色模式下的样子」——所以它不跟着主题翻转，
 *  也就不能复用会翻转的那组令牌。 */
export function AdminShell({
  nav,
  title = "星芒统一控制平台",
  user,
  onLogout,
  banner,
  contextStrip,
  search,
  children,
}: AdminShellProps) {
  return (
    <div className="flex min-h-screen flex-col bg-canvas text-fg font-sans">
      {banner}
      <div className="flex min-h-0 flex-1">
        <aside className="flex w-60 shrink-0 flex-col border-r border-nav-edge bg-nav-surface text-nav-fg">
          <div className="flex h-14 shrink-0 items-center gap-2 border-b border-nav-edge px-4">
            {/* 靛蓝亮边：整套设计只在导航、页面标题、指标卡三处出现，
                多了就从标识变成装饰（原型设计系统 Visual signature） */}
            <span
              aria-hidden="true"
              className="h-5 w-0.5 shrink-0 rounded-full bg-linear-to-b from-nav-accent to-transparent"
            />
            <span className="truncate text-sm font-semibold">{title}</span>
          </div>
          <nav aria-label="主导航" className="min-h-0 flex-1 overflow-y-auto p-2 text-sm">
            {nav}
          </nav>
        </aside>
        <div className="flex min-w-0 flex-1 flex-col">
          <header className="flex h-14 shrink-0 items-center gap-3 border-b border-edge bg-surface px-4">
            {search ?? <SearchPlaceholder />}
            <div className="ml-auto flex shrink-0 items-center gap-3">
              {user ? <span className="text-sm text-fg-muted">{user.name}</span> : null}
              {onLogout ? (
                <Button variant="ghost" size="sm" onClick={onLogout}>
                  退出登录
                </Button>
              ) : null}
            </div>
          </header>
          {contextStrip}
          <main className="min-w-0 flex-1 p-6">{children}</main>
        </div>
      </div>
    </div>
  );
}
