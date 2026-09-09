import * as LabelPrimitive from "@radix-ui/react-label";
import {
  cloneElement,
  isValidElement,
  useId,
  type HTMLAttributes,
  type ReactElement,
  type ReactNode,
} from "react";
import { cx } from "./cx";

export interface FormFieldProps {
  label: string;
  htmlFor?: string;
  error?: string;
  hint?: string;
  required?: boolean;
  children: ReactNode;
  className?: string;
}

export function FormField({
  label,
  htmlFor,
  error,
  hint,
  required = false,
  children,
  className,
}: FormFieldProps) {
  const errorId = useId();
  const hintId = useId();
  const existingDescribedBy =
    isValidElement(children) && typeof children.props === "object" && children.props !== null
      ? (children.props as { "aria-describedby"?: string })["aria-describedby"]
      : undefined;
  const describedBy =
    [existingDescribedBy, error ? errorId : null, hint && !error ? hintId : null]
      .filter(Boolean)
      .join(" ") || undefined;

  const control = isValidElement(children)
    ? cloneElement(children as ReactElement<HTMLAttributes<HTMLElement>>, {
        ...(htmlFor ? { id: htmlFor } : {}),
        ...(describedBy ? { "aria-describedby": describedBy } : {}),
      })
    : children;

  return (
    <div className={cx("flex flex-col gap-1", className)}>
      <LabelPrimitive.Root htmlFor={htmlFor} className="text-sm font-medium text-fg">
        {label}
        {required ? (
          <span aria-hidden className="text-danger">
            {" "}
            *
          </span>
        ) : null}
      </LabelPrimitive.Root>
      {control}
      {hint && !error ? (
        <p id={hintId} className="text-xs text-fg-muted">
          {hint}
        </p>
      ) : null}
      {error ? (
        <p id={errorId} role="alert" className="text-xs text-danger">
          {error}
        </p>
      ) : null}
    </div>
  );
}
