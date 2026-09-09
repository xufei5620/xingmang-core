import * as SelectPrimitive from "@radix-ui/react-select";
import { cx } from "./cx";

export interface SelectOption {
  value: string;
  label: string;
  disabled?: boolean;
}

export interface SelectProps {
  options: SelectOption[];
  value?: string;
  defaultValue?: string;
  onValueChange?: (value: string) => void;
  placeholder?: string;
  disabled?: boolean;
  invalid?: boolean;
  name?: string;
  className?: string;
  "aria-label"?: string;
  "aria-labelledby"?: string;
}

export function Select({
  options,
  value,
  defaultValue,
  onValueChange,
  placeholder = "请选择",
  disabled = false,
  invalid = false,
  name,
  className,
  "aria-label": ariaLabel,
  "aria-labelledby": ariaLabelledBy,
}: SelectProps) {
  return (
    <SelectPrimitive.Root
      value={value}
      defaultValue={defaultValue}
      onValueChange={onValueChange}
      disabled={disabled}
      name={name}
    >
      <SelectPrimitive.Trigger
        aria-label={ariaLabel}
        aria-labelledby={ariaLabelledBy}
        aria-invalid={invalid || undefined}
        className={cx(
          "inline-flex w-full items-center justify-between gap-2",
          "h-(--xm-control-h-md) rounded-md border bg-surface px-3 text-sm text-fg",
          "outline-none focus:outline-2 focus:outline-accent",
          "disabled:cursor-not-allowed disabled:opacity-50",
          "data-placeholder:text-fg-muted",
          invalid ? "border-danger" : "border-edge",
          className,
        )}
      >
        <SelectPrimitive.Value placeholder={placeholder} />
        <SelectPrimitive.Icon>
          <svg aria-hidden viewBox="0 0 16 16" className="size-3.5 shrink-0 text-fg-muted">
            <path
              fill="currentColor"
              d="M4.2 6.2a.75.75 0 0 1 1.06 0L8 8.94l2.74-2.74a.75.75 0 1 1 1.06 1.06l-3.27 3.27a.75.75 0 0 1-1.06 0L4.2 7.26a.75.75 0 0 1 0-1.06z"
            />
          </svg>
        </SelectPrimitive.Icon>
      </SelectPrimitive.Trigger>
      <SelectPrimitive.Portal>
        <SelectPrimitive.Content
          position="popper"
          sideOffset={4}
          className={cx(
            "z-50 min-w-(--radix-select-trigger-width) overflow-hidden",
            "rounded-md border border-edge bg-surface shadow-md",
          )}
        >
          <SelectPrimitive.Viewport className="p-1">
            {options.map((opt) => (
              <SelectPrimitive.Item
                key={opt.value}
                value={opt.value}
                disabled={opt.disabled}
                className={cx(
                  "relative flex cursor-default items-center rounded-sm px-2 py-1.5 text-sm text-fg",
                  "outline-none select-none",
                  "data-highlighted:bg-surface-muted",
                  "data-disabled:pointer-events-none data-disabled:opacity-50",
                )}
              >
                <SelectPrimitive.ItemText>{opt.label}</SelectPrimitive.ItemText>
              </SelectPrimitive.Item>
            ))}
          </SelectPrimitive.Viewport>
        </SelectPrimitive.Content>
      </SelectPrimitive.Portal>
    </SelectPrimitive.Root>
  );
}
