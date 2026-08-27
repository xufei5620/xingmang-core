import type { Meta, StoryObj } from "@storybook/react-vite";
import { Badge } from "@xingmang/ui-primitives";
import { StatTile } from "./StatTile";

const meta = {
  title: "Admin/StatTile",
  component: StatTile,
} satisfies Meta<typeof StatTile>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = {
  args: {
    label: "紧急",
    value: "3",
    note: "未解决的严重（critical）告警",
  },
};

/** 零不等于「没接」：有数据源、当前确实是 0 的时候，数字照常显示,
 *  副行换成一句把这件事说清楚的话。 */
export const 零: Story = {
  args: {
    label: "紧急",
    value: "0",
    note: "当前没有未解决的严重告警",
  },
};

/** 还没有数据源的格子：主数位是「—」并弱化，右上角挂「未接入」,
 *  副行说清楚什么上线之后它才会有数。
 *
 *  显示 0 会被读成「今天没有到期项」——而事实是我们还没接这条线。 */
export const 未接入: Story = {
  args: {
    label: "今日到期",
    value: "—",
    unavailable: true,
    note: "审批、重试与轮换到期；随 Foundation-B 与后台任务页上线",
    status: <Badge tone="neutral">未接入</Badge>,
  },
};

/** 带入口的格子。 */
export const 带入口: Story = {
  args: {
    label: "紧急",
    value: "2",
    note: "未解决的严重（critical）告警",
    link: (
      <a href="#" className="text-xs font-medium text-accent hover:underline">
        查看全部告警 →
      </a>
    ),
  },
};

/** 四格并排（运营工作台顶部的样子）。 */
export const 四格: Story = {
  args: { label: "紧急", value: "3", note: "未解决的严重告警" },
  render: () => (
    <div className="grid w-256 max-w-full grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <StatTile label="紧急" value="3" note="未解决的严重（critical）告警" />
      <StatTile
        label="今日到期"
        value="—"
        unavailable
        note="随 Foundation-B 与后台任务页上线"
        status={<Badge tone="neutral">未接入</Badge>}
      />
      <StatTile
        label="阻塞"
        value="—"
        unavailable
        note="随支付接入（M3）上线"
        status={<Badge tone="neutral">未接入</Badge>}
      />
      <StatTile label="最近恢复" value="4" note="最近 24 小时自动恢复的告警" />
    </div>
  ),
};

export const DarkMode: Story = {
  args: { label: "紧急", value: "3", note: "未解决的严重告警" },
  globals: { theme: "dark" },
};
