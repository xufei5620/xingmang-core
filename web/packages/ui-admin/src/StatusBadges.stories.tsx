import type { Meta, StoryObj } from "@storybook/react-vite";
import type { FreshnessContract } from "./freshness";
import { FreshnessBadge, FreshnessNote, ServiceStatusBadge } from "./StatusBadges";

const base: FreshnessContract = {
  state: "fresh",
  staleness_seconds: 42,
  threshold_seconds: 1800,
  is_partial: false,
  observed_at: "2026-08-26T10:00:00Z",
  last_success: "2026-08-26T10:00:00Z",
  last_error_code: "",
};

const meta = {
  title: "Admin/FreshnessBadge",
  component: FreshnessBadge,
} satisfies Meta<typeof FreshnessBadge>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Fresh: Story = { args: { freshness: base } };
export const Stale: Story = {
  args: { freshness: { ...base, state: "stale", staleness_seconds: 7200 } },
};
export const Partial: Story = { args: { freshness: { ...base, state: "partial", is_partial: true } } };
export const Failed: Story = {
  args: {
    freshness: { ...base, state: "failed", last_error_code: "upstream_timeout" },
  },
};
export const Uninitialized: Story = {
  args: {
    freshness: {
      ...base,
      state: "uninitialized",
      observed_at: null,
      last_success: null,
      staleness_seconds: null,
    },
  },
};

/** 徽章 + 说明行的实际组合形态，以及服务状态徽章。 */
export const AllStates: Story = {
  name: "全部状态一览",
  args: { freshness: base },
  render: () => (
    <div className="flex flex-col gap-4 bg-surface p-4">
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
        <div key={f.state} className="flex flex-col gap-1">
          <FreshnessBadge freshness={f} />
          <FreshnessNote freshness={f} />
        </div>
      ))}
      <div className="flex gap-2">
        <ServiceStatusBadge status="active" />
        <ServiceStatusBadge status="degraded" />
        <ServiceStatusBadge status="retired" />
      </div>
    </div>
  ),
};
