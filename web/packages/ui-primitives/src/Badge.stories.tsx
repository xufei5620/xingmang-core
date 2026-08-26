import type { Meta, StoryObj } from "@storybook/react-vite";
import { Badge } from "./Badge";

const meta = {
  title: "Primitives/Badge",
  component: Badge,
} satisfies Meta<typeof Badge>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Neutral: Story = { args: { children: "未初始化" } };
export const Info: Story = { args: { children: "开发环境", tone: "info" } };
export const Success: Story = { args: { children: "运行中", tone: "success" } };
export const Warning: Story = { args: { children: "数据延迟", tone: "warning" } };
export const Danger: Story = { args: { children: "同步失败", tone: "danger" } };
export const WithTitle: Story = {
  args: { children: "数据延迟", tone: "warning", title: "落后 3 小时，超过 30 分钟阈值" },
};
