import type { Meta, StoryObj } from "@storybook/react-vite";
import { Tabs } from "./Tabs";

const items = [
  { value: "overview", label: "概览", content: "概览内容：环境健康与待办。" },
  { value: "logs", label: "日志", content: "日志内容：最近一次同步结果。" },
  { value: "settings", label: "设置", content: "设置内容：连接器开关。" },
];

const meta = {
  title: "Primitives/Tabs",
  component: Tabs,
  args: { defaultValue: "overview", items },
} satisfies Meta<typeof Tabs>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {};
export const Disabled: Story = {
  args: {
    items: items.map((item) =>
      item.value === "logs" ? { ...item, disabled: true } : item,
    ),
  },
};
export const DarkMode: Story = { globals: { theme: "dark" } };
export const LongText: Story = {
  args: {
    items: [
      {
        value: "long",
        label: "这是一个非常非常长的标签文案用于验证横向溢出",
        content: "长标签对应的内容区。",
      },
      ...items,
    ],
  },
};
