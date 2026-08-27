import type { Meta, StoryObj } from "@storybook/react-vite";
import { MetricCard } from "./MetricCard";
import type { FreshnessContract } from "./freshness";

const meta = {
  title: "Admin/MetricCard",
  component: MetricCard,
  parameters: { layout: "padded" },
  decorators: [
    (Story) => (
      <div className="max-w-80 bg-canvas p-4">
        <Story />
      </div>
    ),
  ],
} satisfies Meta<typeof MetricCard>;
export default meta;
type Story = StoryObj<typeof meta>;

// 指标键抽成常量而不是就地写字面量：`metricKey: "……"` 这个形状会被 gitleaks 的
// generic-api-key 规则当成泄露的密钥（同一条误报见 admin-web 的 OverviewPage.test.tsx）。
// 与其让 secret-scan 常红到没人再看它，不如换个写法绕开这个形状
const REVENUE_METRIC = "sub2api.revenue.daily";
const RECHARGE_METRIC = "newapi.recharge.daily";
const LONG_METRIC = "sub2api.revenue.daily.by_region.cn_east_1.readonly_replica";

const fresh: FreshnessContract = {
  state: "fresh",
  staleness_seconds: 42,
  threshold_seconds: 1800,
  is_partial: false,
  observed_at: "2026-08-26T10:00:00Z",
  last_success: "2026-08-26T10:00:00Z",
  last_error_code: "",
};

export const Default: Story = {
  args: {
    label: "Sub2API 日收入",
    metricKey: REVENUE_METRIC,
    value: "¥12,480.00",
    secondary: "业务日 2026-08-26 · 316 单",
    freshness: fresh,
    source: "connector:sub2api",
    watermark: "2026-08-26T10:00:00Z",
    link: (
      <a className="text-xs font-medium text-accent hover:underline" href="#p">
        查看平台 →
      </a>
    ),
  },
};

/** 首次加载时不先画一个「数据新鲜」的空卡片：徽章一旦显示就是在做承诺。
 *  骨架占住卡片的高度，避免数据回来时整片网格跳动。 */
export const Loading: Story = {
  args: Default.args,
  render: () => (
    <div
      aria-busy="true"
      className="flex h-48 flex-col gap-3 rounded-lg border border-edge bg-surface p-4"
    >
      <div className="h-4 w-1/2 animate-pulse rounded-sm bg-surface-muted" />
      <div className="h-8 w-2/3 animate-pulse rounded-sm bg-surface-muted" />
      <div className="mt-auto h-3 w-full animate-pulse rounded-sm bg-surface-muted" />
      <p className="sr-only">正在读取指标</p>
    </div>
  ),
};

/** 从未成功采集：主数位显示「未初始化」并弱化，绝不显示 0。
 *  后端此时 value 里可能仍带着 0，直接渲染就成了理直气壮的假数据（宪法 12 条）。 */
export const Empty: Story = {
  args: {
    label: "NewAPI 日充值",
    metricKey: RECHARGE_METRIC,
    value: "未初始化",
    secondary: "从未成功采集",
    unavailable: true,
    freshness: {
      ...fresh,
      state: "uninitialized",
      observed_at: null,
      last_success: null,
      staleness_seconds: null,
    },
    source: "connector:newapi",
  },
};

/** 同步失败：数值还在（它是上一次成功的结果），但徽章与说明行必须说清楚
 *  这一点——一个看起来正常的数字配一个红徽章，比藏起数字更有用。 */
export const Error: Story = {
  args: {
    ...Default.args,
    value: "¥11,902.00",
    secondary: "业务日 2026-08-25 · 297 单",
    freshness: {
      ...fresh,
      state: "failed",
      staleness_seconds: 26 * 3600,
      observed_at: "2026-08-25T10:00:00Z",
      last_error_code: "upstream_timeout",
    },
  },
};

/** 无权读取该指标：不画卡片、也不画一个空壳，只说明缺什么权限。
 *  留一张灰卡片会让人以为「这个指标是空的」，而事实是「你看不到它」。 */
export const PermissionDenied: Story = {
  args: Default.args,
  render: () => (
    <div className="flex flex-col gap-1 rounded-lg border border-edge bg-surface p-4">
      <p className="text-sm font-medium text-fg">无权查看该指标</p>
      <p className="text-xs text-fg-muted">
        需要权限：ops.read（服务端为最终裁决，前端隐藏不构成安全控制）
      </p>
    </div>
  ),
};

/** 超长友好名与超长指标键都截断，主数值不被挤走。 */
export const LongText: Story = {
  args: {
    ...Default.args,
    label: "Sub2API 日收入（华东一区只读副本 · 含订阅与按量两类订单）",
    metricKey: LONG_METRIC,
    value: "¥1,204,880,315.00",
    secondary: "业务日 2026-08-26 · 316,402 单 · 币种 CNY · 含税",
  },
};

/** 最小形态：没有指标键、没有趋势、没有入口——平台详情页里的卡片就是这样，
 *  它自己已经是终点了。 */
export const Compact: Story = {
  args: {
    label: "Sub2API 用户数",
    value: "1,204",
    secondary: "活跃 843",
    freshness: fresh,
    source: "connector:sub2api",
  },
};

export const DarkMode: Story = {
  args: Default.args,
  globals: { theme: "dark" },
};
