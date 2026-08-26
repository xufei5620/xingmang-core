import type { ReactNode } from "react";
import { cx } from "./cx";

/** 徽章语气。名字按「语义」而不是按颜色取——业务代码不该知道 danger 是红的，
 *  换主题时只改 tokens.css，不动调用点（规格 §7.7：禁止硬编码颜色）。 */
export type BadgeTone = "neutral" | "info" | "success" | "warning" | "danger";

export interface BadgeProps {
  tone?: BadgeTone;
  children: ReactNode;
  /** 悬停说明。徽章文案很短，完整解释放这里而不是撑宽布局。 */
  title?: string;
  className?: string;
}

/** 语气 → 令牌类。底色用 `/10` 这类透明度修饰符由 color-mix 从令牌算出，
 *  仍然只有令牌一个色源，不引入新色值。 */
const toneClass: Record<BadgeTone, string> = {
  neutral: "border-edge bg-surface-muted text-fg-muted",
  info: "border-accent bg-accent/10 text-accent",
  success: "border-success bg-success/10 text-success",
  // warning 用 text-fg 而不是 text-warning：琥珀色字压在浅底上对比度不足，
  // 靠底色与描边传达语气，文字保持可读（无障碍优先于配色一致）
  warning: "border-warning bg-warning/15 text-fg",
  danger: "border-danger bg-danger/10 text-danger",
};

/** 状态徽章：一个短标签 + 一种语气。用于服务状态、数据新鲜度等场景。 */
export function Badge({ tone = "neutral", children, title, className }: BadgeProps) {
  return (
    <span
      title={title}
      className={cx(
        "inline-flex items-center gap-1 whitespace-nowrap rounded-sm border",
        "px-1.5 py-0.5 text-xs font-medium",
        toneClass[tone],
        className,
      )}
    >
      {children}
    </span>
  );
}
