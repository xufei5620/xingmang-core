import * as TabsPrimitive from "@radix-ui/react-tabs";
import type { ReactNode } from "react";
import { cx } from "./cx";

export interface TabItem {
  value: string;
  label: string;
  content: ReactNode;
  disabled?: boolean;
}

export interface TabsProps {
  items: TabItem[];
  value?: string;
  defaultValue?: string;
  onValueChange?: (value: string) => void;
  className?: string;
}

export function Tabs({
  items,
  value,
  defaultValue,
  onValueChange,
  className,
}: TabsProps) {
  return (
    <TabsPrimitive.Root
      value={value}
      defaultValue={defaultValue ?? items[0]?.value}
      onValueChange={onValueChange}
      className={cx("flex flex-col gap-3", className)}
    >
      <TabsPrimitive.List className="flex gap-1 border-b border-edge">
        {items.map((item) => (
          <TabsPrimitive.Trigger
            key={item.value}
            value={item.value}
            disabled={item.disabled}
            className={cx(
              "-mb-px border-b-2 border-transparent px-3 py-2 text-sm font-medium text-fg-muted",
              "outline-none focus:outline-2 focus:outline-accent",
              "data-[state=active]:border-accent data-[state=active]:text-fg",
              "disabled:cursor-not-allowed disabled:opacity-50",
            )}
          >
            {item.label}
          </TabsPrimitive.Trigger>
        ))}
      </TabsPrimitive.List>
      {items.map((item) => (
        <TabsPrimitive.Content
          key={item.value}
          value={item.value}
          className="text-sm text-fg"
        >
          {item.content}
        </TabsPrimitive.Content>
      ))}
    </TabsPrimitive.Root>
  );
}
