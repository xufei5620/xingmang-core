import type { Meta, StoryObj } from "@storybook/react-vite";
import { FreshnessBadge, FreshnessNote } from "./StatusBadges";
import type { FreshnessContract } from "./freshness";

const meta = {
  title: "Admin/FreshnessBadge",
  component: FreshnessBadge,
  decorators: [
    (Story) => (
      <div className="bg-surface p-4">
        <Story />
      </div>
    ),
  ],
} satisfies Meta<typeof FreshnessBadge>;
export default meta;
type Story = StoryObj<typeof meta>;

const base: FreshnessContract = {
  state: "fresh",
  staleness_seconds: 42,
  threshold_seconds: 1800,
  is_partial: false,
  observed_at: "2026-08-26T10:00:00Z",
  last_success: "2026-08-26T10:00:00Z",
  last_error_code: "",
};

/** 徽章与说明行是一对：徽章给量级判断，说明行给数据时间与落后多久（§9.1）。 */
export const Default: Story = {
  args: { freshness: base },
  render: (args) => (
    <div className="flex flex-col gap-1">
      <FreshnessBadge {...args} />
      <FreshnessNote freshness={args.freshness} />
    </div>
  ),
};

/** 还没取到新鲜度时留骨架，不先画一个「数据新鲜」：
 *  徽章一旦显示就是在对人做承诺，猜一次就够毁掉它的全部意义。 */
export const Loading: Story = {
  args: { freshness: base },
  render: () => (
    <span
      aria-busy="true"
      aria-label="正在读取数据新鲜度"
      className="inline-block h-5 w-20 animate-pulse rounded-sm bg-surface-muted"
    />
  ),
};

/** 从未成功采集。用 neutral 而不是告警色：这通常意味着「还没接上」，不是故障。
 *  真正的保护在数值位置显示「未初始化」而不是 0。 */
export const Empty: Story = {
  args: {
    freshness: {
      ...base,
      state: "uninitialized",
      observed_at: null,
      last_success: null,
      staleness_seconds: null,
    },
  },
  render: Default.render,
};

/** 同步失败：错误码进悬停说明，省得再去翻日志。 */
export const Error: Story = {
  args: {
    freshness: { ...base, state: "failed", last_error_code: "upstream_timeout" },
  },
  render: Default.render,
};

/** 无权读取时不显示徽章，只说明缺什么权限：一个灰徽章会被读成
 *  「这个指标没数据」，而事实是「你看不到它」。 */
export const PermissionDenied: Story = {
  args: { freshness: base },
  render: () => (
    <p className="text-xs text-fg-muted">
      无权查看数据新鲜度 · 需要权限：ops.read（服务端为最终裁决）
    </p>
  ),
};

/** 前端不认识的状态一律显示出来并按 warning 处理：后端将来新增状态时，
 *  宁可显示「未知状态」让人来查，也不能把它渲染成看起来正常的样子。 */
export const LongText: Story = {
  args: {
    freshness: {
      ...base,
      state: "degraded_partial_backfill_in_progress",
      last_error_code: "connector_backfill_window_exceeded_retry_scheduled",
    },
  },
  render: Default.render,
};

/** 表格行里的形态：五种状态并排，确认高度一致、不撑行。 */
export const Compact: Story = {
  args: { freshness: base },
  render: () => (
    <div className="flex flex-wrap items-center gap-2">
      {(
        [
          base,
          { ...base, state: "partial", is_partial: true },
          { ...base, state: "stale", staleness_seconds: 7200 },
          { ...base, state: "failed", last_error_code: "upstream_timeout" },
          {
            ...base,
            state: "uninitialized",
            observed_at: null,
            last_success: null,
            staleness_seconds: null,
          },
        ] satisfies FreshnessContract[]
      ).map((f) => (
        <FreshnessBadge key={f.state} freshness={f} />
      ))}
    </div>
  ),
};

export const DarkMode: Story = {
  args: { freshness: base },
  globals: { theme: "dark" },
  render: Compact.render,
};
