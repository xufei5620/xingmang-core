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

/** CR-0006 XM-INVCON1：已经拿到一份断言，组件应把它 postMessage 给 iframe。
 *  Storybook 静态构建里没有真实 iframe 内容可验证投递效果（同上方三个故事
 *  的既有说明——没有真实开票控制台可连），这里主要证明传入 `assertion`
 *  prop 不会让壳本身的渲染出任何岔子（不额外画一层遮罩，与不传时视觉上
 *  完全一致）。 */
export const WithAssertion: Story = {
  args: {
    origin: "https://invoice.example.test",
    path: "/embed/admin/sub2api",
    title: "开票",
    assertion: { assertion: "eyJhbGciOiJFZERTQSJ9.storybook-demo-payload.signature" },
  },
};

/** CR-0006 XM-INVCON1：`onAssertionNeeded` 只是一个回调 prop，不影响初始
 *  渲染——这里用一个空函数证明传了它之后组件仍正常呈现 iframe 壳，真实的
 *  "收到消息即回调"由 EmbeddedConsoleFrame.test.tsx 的 vitest 用例覆盖
 *  （jsdom 可以派发真实的 `message` 事件，Storybook 静态构建做不到）。 */
export const AssertionNeededCallback: Story = {
  args: {
    origin: "https://invoice.example.test",
    path: "/embed/admin/sub2api",
    title: "开票",
    onAssertionNeeded: () => {},
  },
};
