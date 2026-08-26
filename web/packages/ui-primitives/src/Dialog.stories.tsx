import type { Meta, StoryObj } from "@storybook/react-vite";
import { Button } from "./Button";
import { Dialog } from "./Dialog";

const meta = {
  title: "Primitives/Dialog",
  component: Dialog,
  args: {
    trigger: <Button>打开</Button>,
    title: "确认删除",
    children: "删除后无法恢复，请确认已备份相关数据。",
  },
} satisfies Meta<typeof Dialog>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {};
export const Disabled: Story = {
  args: { trigger: <Button disabled>打开</Button> },
};
export const DarkMode: Story = {
  globals: { theme: "dark" },
  args: { defaultOpen: true },
};
export const LongText: Story = {
  args: {
    title: "这是一个非常非常长的对话框标题用于验证换行与关闭按钮布局是否可控",
    children:
      "这是一段非常非常长的对话框正文，用来检查在窄宽度下正文是否能正常换行而不会撑破面板或遮挡关闭按钮。".repeat(
        4,
      ),
  },
};
