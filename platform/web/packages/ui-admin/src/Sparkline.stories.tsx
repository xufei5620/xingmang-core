import type { Meta, StoryObj } from "@storybook/react-vite";
import { Sparkline } from "./Sparkline";
import type { SparkSample } from "./sparklineGeometry";

const HOUR = 3_600_000;
const START = Date.UTC(2026, 7, 26, 0, 0, 0);

/** 造一条 24 小时序列。fail 里的下标记为同步失败（值保持上一次成功的读数），
 *  partial 里的下标记为「只拿到部分数据」（数值可能偏小）。 */
function series(values: number[], fail: number[] = [], partial: number[] = []): SparkSample[] {
  return values.map((value, i) => ({
    at: START + i * HOUR,
    value,
    failed: fail.includes(i),
    partial: partial.includes(i),
  }));
}

const rising = [12, 14, 13, 17, 19, 18, 22, 26, 25, 29, 33, 31, 36, 40, 38, 44, 47, 45, 51, 55, 54, 60, 63, 66];

const meta = {
  title: "Admin/Sparkline",
  component: Sparkline,
  parameters: { layout: "padded" },
  // 卡片里的实际宽度就是这个量级，太宽会看不出真实观感
  decorators: [(Story) => <div className="w-64">{Story()}</div>],
} satisfies Meta<typeof Sparkline>;

export default meta;
type Story = StoryObj<typeof meta>;

export const 上升趋势: Story = {
  args: { samples: series(rising), label: "近 24 小时日收入趋势" },
};

export const 含同步失败: Story = {
  args: {
    // 第 8、9、17 小时同步失败：折线在此断开，断点用 danger 令牌色标出
    samples: series(rising, [8, 9, 17]),
    label: "近 24 小时日收入趋势（含 3 次同步失败）",
  },
};

export const 含部分数据: Story = {
  args: {
    // 第 5~7 小时只拿到部分数据：这几段画成虚线并叠一个空心竖标，
    // 图下方还有一行看得见的文字说明——不能只靠颜色区分完整性（Codex #5）
    samples: series(rising, [], [5, 6, 7]),
    label: "近 24 小时日收入趋势（含 3 个部分数据点）",
  },
};

export const 失败与部分数据混合: Story = {
  args: {
    samples: series(rising, [8, 9], [15, 16]),
    label: "近 24 小时日收入趋势（含同步失败与部分数据）",
  },
};

export const 数值恒定: Story = {
  args: { samples: series(new Array(24).fill(1000)), label: "近 24 小时渠道数趋势" },
};

export const 数据不足: Story = {
  args: { samples: series([42]), label: "近 24 小时用户数趋势" },
};

export const 全部失败: Story = {
  args: {
    samples: series(rising, [...rising.keys()]),
    label: "近 24 小时用户数趋势（全部同步失败）",
  },
};
