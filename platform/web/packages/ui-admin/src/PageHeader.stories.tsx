import type { Meta, StoryObj } from "@storybook/react-vite";
import { Badge, Button } from "@xingmang/ui-primitives";
import { PageHeader } from "./PageHeader";
import { FreshnessBadge, ServiceStatusBadge } from "./StatusBadges";
import type { FreshnessContract } from "./freshness";

const meta = {
  title: "Admin/PageHeader",
  component: PageHeader,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <div className="bg-canvas p-4">
        <Story />
      </div>
    ),
  ],
} satisfies Meta<typeof PageHeader>;
export default meta;
type Story = StoryObj<typeof meta>;

const fresh: FreshnessContract = {
  state: "fresh",
  staleness_seconds: 42,
  threshold_seconds: 1800,
  is_partial: false,
  observed_at: "2026-08-26T10:00:00Z",
  last_success: "2026-08-26T10:00:00Z",
  last_error_code: "",
};

/** 标题、状态、阶段标签同层（§11.2）：读标题时状态已经在眼里。 */
export const Default: Story = {
  args: {
    title: "Sub2API",
    status: (
      <>
        <ServiceStatusBadge status="active" />
        <Badge tone="neutral">已上线</Badge>
      </>
    ),
    description: "订阅转 API 的售卖与计费：用户数与余额、日收入/成本、渠道令牌与余额。",
    lastRefreshedAt: Date.parse("2026-08-26T10:00:00Z"),
    onRefresh: () => {},
  },
};

/** 刷新中：按钮自己禁用并标 aria-busy，标题与状态不动——
 *  页头跟着一起变灰会让人以为整页失效了。 */
export const Loading: Story = {
  args: { ...Default.args, refreshing: true },
};

/** 还没有任何数据时不显示「最后刷新」：一个空的时间戳比没有时间戳更误导。 */
export const Empty: Story = {
  args: {
    title: "运营总览",
    description: "这个环境还没采到任何指标",
    onRefresh: () => {},
  },
};

/** 取数失败时状态位说明失败，刷新按钮留着——人得能自己再试一次。 */
export const Error: Story = {
  args: {
    title: "运营总览",
    status: (
      <FreshnessBadge
        freshness={{ ...fresh, state: "failed", last_error_code: "upstream_timeout" }}
      />
    ),
    description: "下方数值来自上一次成功采集，不是当前值。",
    lastRefreshedAt: Date.parse("2026-08-26T08:00:00Z"),
    onRefresh: () => {},
  },
};

/** 无权读取时不给刷新按钮：一个点了必然 403 的按钮只是在浪费人的时间。
 *  但页面标题必须还在——否则人连自己在哪都不知道。 */
export const PermissionDenied: Story = {
  args: {
    title: "审计事件",
    status: <Badge tone="danger">无权访问</Badge>,
    description: "需要权限：audit.read（服务端为最终裁决，前端隐藏不构成安全控制）",
  },
};

/** 超长标题截断，状态标签不被挤走：状态是判断依据，不能因为标题长就看不见。 */
export const LongText: Story = {
  args: {
    title: "Sub2API 订阅转 API 售卖与计费平台 · 华东一区只读副本 · 生产环境",
    status: (
      <>
        <ServiceStatusBadge status="degraded" />
        <Badge tone="warning">阶段 2 · 运营工作台待建</Badge>
      </>
    ),
    description:
      "这一段说明刻意写得很长，用来确认它换行之后不会把右侧的刷新按钮与最后刷新时刻挤出可视区域，也不会把标题那一行顶开。",
    lastRefreshedAt: Date.parse("2026-08-26T10:00:00Z"),
    onRefresh: () => {},
    actions: <Button size="sm">导出</Button>,
  },
};

/** 密集页面上的最小形态：只有标题与状态，没有说明与操作。 */
export const Compact: Story = {
  args: { title: "注册表", status: <Badge tone="neutral">只读</Badge> },
};

export const DarkMode: Story = {
  args: Default.args,
  globals: { theme: "dark" },
};
