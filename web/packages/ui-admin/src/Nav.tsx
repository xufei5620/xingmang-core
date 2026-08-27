import { useEffect, useState, type ReactNode } from "react";
import { cx } from "@xingmang/ui-primitives";

const sectionTitleClass = "px-3 pb-1 text-xs font-semibold tracking-wider text-nav-fg-muted";

export interface NavSectionProps {
  /** 段标题。四个分组的段名来自 ADMIN-IA v3 §一：全局 / 平台 / 平台治理 / 扩展能力。 */
  title: string;
  children: ReactNode;
}

/** 侧边导航的一段（不可折叠的那三段）。
 *
 *  段标题做成不可点、不可折叠的小号标签：前三段是固定结构（ADMIN-IA v3 §一），
 *  一个能折起来的分组头会让人以为这一段可有可无，而「平台」恰恰是这次重构里的主体。
 *  唯一可折叠的是「扩展能力」——原型自己就把它画成 `<details>`，见下。 */
export function NavSection({ title, children }: NavSectionProps) {
  return (
    <div className="mb-4 last:mb-0">
      <h3 className={sectionTitleClass}>{title}</h3>
      <div className="space-y-0.5">{children}</div>
    </div>
  );
}

export interface NavSectionCollapsibleProps extends NavSectionProps {
  /** 分组本身的阶段标签。原型给「扩展能力」标的是「后置」。 */
  hint?: string;
  /** 置 true 时展开。原型的行为是默认收起、进入 `#/ext/*` 时自动展开，所以这个值
   *  跟着路由走。人手动折起来之后不会被下一次重渲染强行掰开——只有它**从 false
   *  变成 true**（也就是刚走进这一段）才重新展开。 */
  open?: boolean;
}

/** 可折叠的一段（只有「扩展能力」用）。
 *
 *  用原生 `<details>/<summary>` 而不是自己拿 button 加 state 拼：折叠展开的键盘与
 *  读屏行为浏览器已经做对了，重做一遍只会漏掉其中一半。
 *
 *  段标题仍然是 `<h3>`(放在 summary 里面，HTML 规范允许 summary 含一个标题元素),
 *  否则这一段在标题层级里凭空消失，靠标题跳转的读屏用户就找不到它了。 */
export function NavSectionCollapsible({
  title,
  hint,
  open: openProp,
  children,
}: NavSectionCollapsibleProps) {
  const [open, setOpen] = useState(openProp ?? false);
  useEffect(() => {
    if (openProp) setOpen(true);
  }, [openProp]);

  return (
    <details
      open={open}
      onToggle={(event) => setOpen(event.currentTarget.open)}
      className="mb-4 last:mb-0"
    >
      <summary className="flex cursor-pointer items-center justify-between gap-2 rounded-md px-3 hover:bg-nav-hover">
        <h3 className={cx(sectionTitleClass, "px-0")}>{title}</h3>
        {hint ? <span className="shrink-0 pb-1 text-xs text-nav-fg-muted">{hint}</span> : null}
      </summary>
      <div className="space-y-0.5">{children}</div>
    </details>
  );
}

const navItemBase =
  "relative flex items-center justify-between gap-2 rounded-md px-3 py-2 transition-colors";

/** 导航项的类名。
 *
 *  由包给出而不是各应用自己拼：导航在深色轨道上，用的是 nav-* 那组不随主题
 *  翻转的令牌；应用侧照着内容区的 surface/fg 拼一份，换主题时就会一半亮一半暗。
 *
 *  选中态的左侧亮边用伪元素而不是 border-left：加一条边框会把文字整体推右 2px,
 *  点击导航时整栏文字轻微跳动。 */
export function navItemClass({ isActive }: { isActive: boolean }): string {
  return cx(
    navItemBase,
    isActive
      ? "bg-nav-active-bg font-medium text-nav-active-fg before:absolute before:inset-y-1 before:left-0 before:w-0.5 before:rounded-full before:bg-nav-accent before:content-['']"
      : "text-nav-fg-muted hover:bg-nav-hover hover:text-nav-fg",
  );
}

export interface NavItemLabelProps {
  label: string;
  /** 右侧状态标签，例如「未接入·M4」「未建·F-B」「读取失败」。 */
  hint?: string;
}

/** 导航项的内容：标签 + 右侧状态。
 *
 *  单独抽出来是因为可点与不可点两种条目的**内容**必须长得一样，只有外层元素不同
 *  (`NavLink` vs `<span aria-disabled>`)。以前两边各拼一份，于是禁用项有状态标签、
 *  可点项没有——而「服务器 未接入·M2 但可以点进去看骨架」恰恰是最需要标签的那种。
 *
 *  hint 留在可访问名里（不加 aria-hidden）：「未接入·M2」是这一条最该被读到的信息，
 *  藏起来等于只让看得见的人知道。 */
export function NavItemLabel({ label, hint }: NavItemLabelProps) {
  return (
    <>
      <span className="truncate">{label}</span>
      {hint ? <span className="shrink-0 text-xs">{hint}</span> : null}
    </>
  );
}

export interface NavItemDisabledProps extends NavItemLabelProps {
  /** 右侧状态标签。禁用项必须给出原因，所以这里不是可选的。 */
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
      <NavItemLabel label={label} hint={hint} />
    </span>
  );
}
