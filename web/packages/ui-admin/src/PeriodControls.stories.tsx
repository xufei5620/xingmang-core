import type { Meta, StoryObj } from "@storybook/react-vite";
import { PeriodControls } from "./PeriodControls";

const meta = {
  title: "Admin/PeriodControls",
  component: PeriodControls,
  parameters: { layout: "padded" },
  args: {
    day: "2026-08-27",
    granularity: "week",
    period: {
      day: "2026-08-27",
      granularity: "week",
      from: "2026-08-24",
      to: "2026-08-30",
    },
    onDayChange: () => undefined,
    onGranularityChange: () => undefined,
  },
} satisfies Meta<typeof PeriodControls>;

export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {};

export const WaitingForServer: Story = {
  args: { day: "", granularity: "month", period: undefined },
};

export const FinanceResolved: Story = {
  args: {
    day: "2026-08-27",
    granularity: "month",
    period: {
      day: "2026-08-27",
      granularity: "month",
      from: "2026-08-01",
      to: "2026-08-31",
    },
    dateLabel: "统计日期",
    dateAriaLabel: "统计日期",
    granularityAriaLabel: "统计模式",
  },
};

export const Narrow320: Story = {
  args: FinanceResolved.args,
  decorators: [
    (Story) => (
      <div className="w-80 max-w-full">
        <Story />
      </div>
    ),
  ],
};

export const DarkMode: Story = {
  globals: { theme: "dark" },
};
