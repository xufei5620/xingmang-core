import * as DialogPrimitive from "@radix-ui/react-dialog";
import type { ReactNode } from "react";
import { cx } from "./cx";

export interface DialogProps {
  trigger: ReactNode;
  title: string;
  description?: string;
  children?: ReactNode;
  open?: boolean;
  defaultOpen?: boolean;
  onOpenChange?: (open: boolean) => void;
}

export function Dialog({
  trigger,
  title,
  description,
  children,
  open,
  defaultOpen,
  onOpenChange,
}: DialogProps) {
  return (
    <DialogPrimitive.Root
      open={open}
      defaultOpen={defaultOpen}
      onOpenChange={onOpenChange}
    >
      <DialogPrimitive.Trigger asChild>{trigger}</DialogPrimitive.Trigger>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="fixed inset-0 z-50 bg-fg/40" />
        <DialogPrimitive.Content
          {...(description ? {} : { "aria-describedby": undefined })}
          className={cx(
            "fixed top-1/2 left-1/2 z-50 w-[min(32rem,calc(100%-2rem))]",
            "-translate-x-1/2 -translate-y-1/2",
            "rounded-lg border border-edge bg-surface p-6 shadow-md",
          )}
        >
          <div className="mb-4 flex items-start justify-between gap-4">
            <div className="flex flex-col gap-1">
              <DialogPrimitive.Title className="text-sm font-medium text-fg">
                {title}
              </DialogPrimitive.Title>
              {description ? (
                <DialogPrimitive.Description className="text-xs text-fg-muted">
                  {description}
                </DialogPrimitive.Description>
              ) : null}
            </div>
            <DialogPrimitive.Close
              aria-label="关闭"
              className={cx(
                "inline-flex h-(--xm-control-h-sm) items-center justify-center px-2",
                "rounded-md text-fg-muted hover:bg-surface-muted",
                "outline-none focus:outline-2 focus:outline-accent",
              )}
            >
              <svg aria-hidden viewBox="0 0 16 16" className="size-3.5">
                <path
                  fill="currentColor"
                  d="M3.22 3.22a.75.75 0 0 1 1.06 0L8 6.94l3.72-3.72a.75.75 0 1 1 1.06 1.06L9.06 8l3.72 3.72a.75.75 0 1 1-1.06 1.06L8 9.06l-3.72 3.72a.75.75 0 1 1-1.06-1.06L6.94 8 3.22 4.28a.75.75 0 0 1 0-1.06z"
                />
              </svg>
            </DialogPrimitive.Close>
          </div>
          <div className="text-sm text-fg">{children}</div>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}
