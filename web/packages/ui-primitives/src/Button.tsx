import type { ButtonHTMLAttributes } from "react";
import { cx } from "./cx";

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: "primary" | "secondary" | "ghost" | "danger";
  size?: "sm" | "md" | "lg";
  /** 进行中：禁用并显示 aria-busy（写按钮防重复提交，规格 §7.7-12） */
  loading?: boolean;
}

const variantClass: Record<NonNullable<ButtonProps["variant"]>, string> = {
  primary: "bg-accent text-accent-fg hover:bg-accent-strong",
  secondary: "bg-surface text-fg border border-edge hover:bg-surface-muted",
  ghost: "bg-transparent text-fg hover:bg-surface-muted",
  danger: "bg-danger text-danger-fg hover:opacity-90",
};

const sizeClass: Record<NonNullable<ButtonProps["size"]>, string> = {
  sm: "h-(--xm-control-h-sm) px-2 text-xs",
  md: "h-(--xm-control-h-md) px-3 text-sm",
  lg: "h-(--xm-control-h-lg) px-4 text-base",
};

export function Button({
  variant = "primary",
  size = "md",
  loading = false,
  disabled,
  className,
  children,
  ...rest
}: ButtonProps) {
  return (
    <button
      {...rest}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      className={cx(
        "inline-flex items-center justify-center gap-2 rounded-md font-medium",
        "transition-colors disabled:cursor-not-allowed disabled:opacity-50",
        variantClass[variant],
        sizeClass[size],
        className,
      )}
    >
      {loading ? (
        <span
          aria-hidden
          className="size-3.5 animate-spin rounded-full border-2 border-current border-t-transparent"
        />
      ) : null}
      {children}
    </button>
  );
}
