import type { Meta, StoryObj } from "@storybook/react-vite";
import { EmptyState } from "@xingmang/ui-primitives";
import { AdminShell } from "./AdminShell";

const meta = {
  title: "Admin/AdminShell",
  component: AdminShell,
  parameters: { layout: "fullscreen" },
} satisfies Meta<typeof AdminShell>;
export default meta;
type Story = StoryObj<typeof meta>;

const nav = (
  <>
    <span className="block rounded-md bg-surface-muted px-3 py-2 font-medium text-accent">
      运营总览
    </span>
    <span className="block rounded-md px-3 py-2 text-fg-muted">告警中心（后续）</span>
  </>
);

export const Default: Story = {
  args: {
    nav,
    user: { name: "产品负责人" },
    onLogout: () => {},
    children: <EmptyState title="暂无数据" description="等待 Sub2API 接入" />,
  },
};
