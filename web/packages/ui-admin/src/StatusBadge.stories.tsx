import type { Meta, StoryObj } from "@storybook/react-vite";
import { Badge } from "@xingmang/ui-primitives";
import { ServiceStatusBadge } from "./StatusBadges";

const meta = {
  title: "Admin/StatusBadge",
  component: ServiceStatusBadge,
  decorators: [
    (Story) => (
      <div className="bg-surface p-4">
        <Story />
      </div>
    ),
  ],
} satisfies Meta<typeof ServiceStatusBadge>;
export default meta;
type Story = StoryObj<typeof meta>;

/** 服务状态：active / degraded / retired（后端 registry.ServiceStatus）。
 *  语气与文案一起给——颜色不是唯一的状态表达（§11.1）。 */
export const Default: Story = { args: { status: "active" } };

/** 注册表还没读到时不敢说任何状态：拿一次「加载中」去断言服务的状态，
 *  和拿一次 403 去断言平台没接是同一个错误。 */
export const Loading: Story = {
  args: { status: "active" },
  render: () => <Badge tone="neutral">读取中</Badge>,
};

/** 已退役：neutral 而不是 danger——它不是故障，是一个正常的终态。 */
export const Empty: Story = { args: { status: "retired" } };

/** 前端不认识的状态值原样带出并按 warning 处理，不静默降级成 neutral：
 *  后端新增状态时，宁可显示「未知」让人来查。 */
export const Error: Story = { args: { status: "quarantined" } };

/** 无权读取时说明缺什么权限，不给一个中性徽章冒充「正常」。 */
export const PermissionDenied: Story = {
  args: { status: "active" },
  render: () => (
    <p className="text-xs text-fg-muted">
      无权查看服务状态 · 需要权限：registry.read（服务端为最终裁决）
    </p>
  ),
};

/** 超长未知状态值不换行、不撑破表格行。 */
export const LongText: Story = {
  args: { status: "degraded_pending_manual_verification_after_credential_rotation" },
};

/** 表格行里的形态：三种已知状态并排，确认高度一致。 */
export const Compact: Story = {
  args: { status: "active" },
  render: () => (
    <div className="flex flex-wrap items-center gap-2">
      <ServiceStatusBadge status="active" />
      <ServiceStatusBadge status="degraded" />
      <ServiceStatusBadge status="retired" />
    </div>
  ),
};

export const DarkMode: Story = {
  args: { status: "active" },
  globals: { theme: "dark" },
  render: Compact.render,
};
