import type { ReactNode } from "react";

export interface NavSectionProps {
  /** 段标题。三段式导航的段名来自 ADMIN-IA：全局 / 被管平台 / 平台治理。 */
  title: string;
  children: ReactNode;
}

/** 侧边导航的一段。
 *
 *  段标题做成不可点、不可折叠的小号标签：三段是固定结构（ADMIN-IA 一、三段式
 *  总树），一个能折起来的分组头会让人以为这一段可有可无，而「被管平台」恰恰是
 *  这次重构里的主体。 */
export function NavSection({ title, children }: NavSectionProps) {
  return (
    <div className="mb-4 last:mb-0">
      <h3 className="px-3 pb-1 text-xs font-semibold tracking-wider text-fg-muted">{title}</h3>
      <div className="space-y-1">{children}</div>
    </div>
  );
}

export interface NavItemDisabledProps {
  label: string;
  /** 右侧状态标签，例如「即将上线」「未接入·M1」。 */
  hint: string;
  /** 悬停说明：为什么现在点不了。 */
  title?: string;
}

/** 禁用态导航项。
 *
 *  用 <span aria-disabled> 而不是 <a> 或 <button>：一个没有目标的链接会被键盘
 *  与读屏当成可达入口，点下去什么都不发生，比看不见更让人困惑。
 *
 *  它必须在场——规格 §12 惯例要求未接入的东西显示为未接入而不是隐藏——
 *  但必须明确地点不动。两件事都要做到，所以既不能删，也不能做成链接。 */
export function NavItemDisabled({ label, hint, title }: NavItemDisabledProps) {
  return (
    <span
      aria-disabled="true"
      title={title}
      className="flex cursor-not-allowed items-center justify-between gap-2 rounded-md px-3 py-2 text-fg-muted opacity-60"
    >
      <span className="truncate">{label}</span>
      <span className="shrink-0 text-xs">{hint}</span>
    </span>
  );
}
