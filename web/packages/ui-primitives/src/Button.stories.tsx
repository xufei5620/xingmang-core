import type { Meta, StoryObj } from "@storybook/react-vite";
import { Button } from "./Button";

const meta = {
  title: "Primitives/Button",
  component: Button,
} satisfies Meta<typeof Button>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = { args: { children: "保存" } };
export const Loading: Story = { args: { children: "提交中", loading: true } };
export const Disabled: Story = { args: { children: "不可用", disabled: true } };
export const Danger: Story = { args: { children: "删除", variant: "danger" } };
export const Secondary: Story = { args: { children: "取消", variant: "secondary" } };
export const Compact: Story = { args: { children: "紧凑", size: "sm" } };
export const LongText: Story = {
  args: { children: "这是一个非常非常长的按钮文案用于验证溢出表现是否可控" },
};
