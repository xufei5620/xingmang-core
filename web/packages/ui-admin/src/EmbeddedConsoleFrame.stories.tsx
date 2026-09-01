import type { Meta, StoryObj } from "@storybook/react-vite";
import { EmbeddedConsoleFrame } from "./EmbeddedConsoleFrame";

const meta = {
  title: "Admin/EmbeddedConsoleFrame",
  component: EmbeddedConsoleFrame,
  parameters: {
    // 来源指向一个不存在的域名：Storybook 静态构建里没有真实的开票控制台可连,
    // 这里只演示壳本身的形状（无 sandbox、无来源展示的边框与圆角）,
    // 不代表真实加载效果——真实效果见 CR-0005 验证清单里的手工验证。
  },
} satisfies Meta<typeof EmbeddedConsoleFrame>;
export default meta;
type Story = StoryObj<typeof meta>;

/** Sub2API 支付与财务 → 开票。按平台过滤的开票管理视图。 */
export const Sub2Api: Story = {
  args: {
    origin: "https://invoice.example.test",
    path: "/embed/admin/sub2api",
    title: "开票",
  },
};

/** NewAPI 支付与财务 → 开票（CR-0005 推翻了此前「不补开票」的裁定）。 */
export const NewApi: Story = {
  args: {
    origin: "https://invoice.example.test",
    path: "/embed/admin/newapi",
    title: "开票",
  },
};

/** 治理 · 跨平台财务 → 开票集成：global 模式，展示系统设置与两平台源健康总览,
 *  不按平台过滤。 */
export const Global: Story = {
  args: {
    origin: "https://invoice.example.test",
    path: "/embed/admin/global",
    title: "开票",
  },
};
