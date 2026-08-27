import type { Meta, StoryObj } from "@storybook/react-vite";
import { EmptyState } from "@xingmang/ui-primitives";
import { AdminShell } from "./AdminShell";
import { NavItemDisabled, NavSection } from "./Nav";

const meta = {
  title: "Admin/AdminShell",
  component: AdminShell,
  parameters: { layout: "fullscreen" },
} satisfies Meta<typeof AdminShell>;
export default meta;
type Story = StoryObj<typeof meta>;

const linkClass = "block rounded-md px-3 py-2 text-fg-muted";
const activeClass = "block rounded-md bg-surface-muted px-3 py-2 font-medium text-accent";

/** 三段式导航（ADMIN-IA v2）：全局 / 被管平台 / 平台治理。
 *  应用侧把这些 span 换成自己的 NavLink，壳本身不依赖 router。 */
const nav = (
  <>
    <NavSection title="全局">
      <span className={activeClass}>运营总览</span>
      <NavItemDisabled label="告警中心" hint="即将上线" />
      <span className={linkClass}>审计事件</span>
    </NavSection>
    <NavSection title="被管平台">
      <span className={linkClass}>Sub2API</span>
      {/* NewAPI 自 XM-0035 起页面已建（内容来自 newapi.* 指标），
          所以它在这里是普通条目而不是禁用占位；禁用的样子看 CPA 那条 */}
      <span className={linkClass}>NewAPI</span>
      <NavItemDisabled label="CPA" hint="未接入·M4" />
      <NavItemDisabled label="开票系统" hint="契约草案·XM-0028" />
    </NavSection>
    <NavSection title="平台治理">
      <span className={linkClass}>注册表</span>
      <NavItemDisabled label="财务中心" hint="未接入·M3" />
      <span className={linkClass}>设置</span>
    </NavSection>
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
