import type { InputHTMLAttributes } from "react";
import { cx } from "./cx";

export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  /** 校验失败态：danger 描边 + aria-invalid */
  invalid?: boolean;
}

export function Input({ invalid = false, className, ...rest }: InputProps) {
  return (
    <input
      {...rest}
      aria-invalid={invalid || undefined}
      className={cx(
        "h-(--xm-control-h-md) w-full rounded-md border bg-surface px-3 text-sm text-fg",
        "placeholder:text-fg-muted focus:outline-2 focus:outline-accent",
        "disabled:cursor-not-allowed disabled:opacity-50 read-only:bg-surface-muted",
        invalid ? "border-danger" : "border-edge",
        className,
      )}
    />
  );
}
