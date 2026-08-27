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

/** 全局横幅：跨整个窗口宽度、压在导航与顶栏之上，且没有关闭按钮。
 *  应用侧用它挂「当前是演示数据」这类躲不开的声明（Codex #8）。 */
export const 带全局横幅: Story = {
  args: {
    ...Default.args,
    banner: (
      <div
        role="status"
        className="flex items-center justify-center gap-2 border-b-2 border-warning bg-warning/15 px-4 py-2 text-center text-sm font-semibold text-warning"
      >
        <span aria-hidden="true">⚠</span>
        <span>当前展示的是演示数据（Fake 连接器），非真实运营数据</span>
      </div>
    ),
  },
};
