import type { ReactNode } from "react";
import { cx } from "@xingmang/ui-primitives";

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
      <h3 className="px-3 pb-1 text-xs font-semibold tracking-wider text-nav-fg-muted">{title}</h3>
      <div className="space-y-0.5">{children}</div>
    </div>
  );
}

const navItemBase =
  "relative flex items-center justify-between gap-2 rounded-md px-3 py-2 transition-colors";

/** 导航项的类名。
 *
 *  由包给出而不是各应用自己拼：导航在深色轨道上，用的是 nav-* 那组不随主题
 *  翻转的令牌；应用侧照着内容区的 surface/fg 拼一份，换主题时就会一半亮一半暗。
 *
 *  选中态的左侧亮边用伪元素而不是 border-left：加一条边框会把文字整体推右 2px，
 *  点击导航时整栏文字轻微跳动。 */
export function navItemClass({ isActive }: { isActive: boolean }): string {
  return cx(
    navItemBase,
    isActive
      ? "bg-nav-active-bg font-medium text-nav-active-fg before:absolute before:inset-y-1 before:left-0 before:w-0.5 before:rounded-full before:bg-nav-accent before:content-['']"
      : "text-nav-fg-muted hover:bg-nav-hover hover:text-nav-fg",
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
 *  但必须明确地点不动。两件事都要做到，所以既不能删，也不能做成链接。
 *
 *  「点不动」靠 hint 文案与光标表达，不靠调低透明度：深色轨道上的次要文字
 *  本来就只有 6:1，再压一层 opacity 会掉到 AA 线下，于是「未接入」这条最需要
 *  被读到的信息反而成了最看不清的一条。 */
export function NavItemDisabled({ label, hint, title }: NavItemDisabledProps) {
  return (
    <span
      aria-disabled="true"
      title={title}
      className={cx(navItemBase, "cursor-not-allowed text-nav-fg-muted")}
    >
      <span className="truncate">{label}</span>
      <span className="shrink-0 text-xs">{hint}</span>
    </span>
  );
}
