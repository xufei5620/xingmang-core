import type { Meta, StoryObj } from "@storybook/react-vite";
import { Button } from "./Button";
import { Tooltip } from "./Tooltip";

const meta = {
  title: "Primitives/Tooltip",
  component: Tooltip,
  args: {
    content: "危险操作，不可撤销",
    children: <Button variant="danger">删除</Button>,
    delayDuration: 0,
  },
} satisfies Meta<typeof Tooltip>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {};
export const Disabled: Story = {
  args: { children: <Button disabled>删除</Button> },
};
export const DarkMode: Story = { globals: { theme: "dark" } };
export const LongText: Story = {
  args: {
    content: "这是一段非常非常长的提示文案，用来检查悬浮层在窄宽度下是否换行而不是撑出视口。",
  },
};
