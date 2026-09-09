import type { Meta, StoryObj } from "@storybook/react-vite";
import { Select } from "./Select";

const options = [
  { value: "prod", label: "生产" },
  { value: "staging", label: "预发" },
  { value: "dev", label: "开发" },
];

const meta = {
  title: "Primitives/Select",
  component: Select,
  args: { "aria-label": "环境", options, placeholder: "请选择环境" },
} satisfies Meta<typeof Select>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {};
export const Disabled: Story = { args: { disabled: true } };
export const DarkMode: Story = { globals: { theme: "dark" } };
export const LongText: Story = {
  args: {
    options: [
      {
        value: "long",
        label: "这是一个非常非常长的选项文案用于验证下拉与触发器溢出是否可控",
      },
      ...options,
    ],
  },
};
