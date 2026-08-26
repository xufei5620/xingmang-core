import type { Meta, StoryObj } from "@storybook/react-vite";
import { Input } from "./Input";

const meta = {
  title: "Primitives/Input",
  component: Input,
} satisfies Meta<typeof Input>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = { args: { placeholder: "请输入名称" } };
export const Disabled: Story = { args: { disabled: true, value: "只读禁用" } };
export const Readonly: Story = { args: { readOnly: true, value: "readonly 值" } };
export const Invalid: Story = { args: { invalid: true, defaultValue: "非法值" } };
export const LongText: Story = {
  args: { defaultValue: "很长很长很长很长很长很长很长很长很长很长的输入内容" },
};
