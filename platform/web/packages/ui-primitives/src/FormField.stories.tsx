import type { Meta, StoryObj } from "@storybook/react-vite";
import { FormField } from "./FormField";
import { Input } from "./Input";

const meta = {
  title: "Primitives/FormField",
  component: FormField,
  args: {
    label: "名称",
    htmlFor: "name",
    children: <Input id="name" placeholder="请输入名称" />,
  },
} satisfies Meta<typeof FormField>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {};
export const Disabled: Story = {
  args: { children: <Input id="name" disabled placeholder="不可编辑" /> },
};
export const DarkMode: Story = { globals: { theme: "dark" } };
export const LongText: Story = {
  args: {
    label: "这是一个非常非常长的字段标签用于验证换行与控件对齐",
    error: "这是一段非常非常长的错误说明，用来检查错误文案是否能完整展示。",
    children: <Input id="name" invalid defaultValue="非法值" />,
  },
};
