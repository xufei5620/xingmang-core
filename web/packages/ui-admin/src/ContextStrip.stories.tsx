import type { Meta, StoryObj } from "@storybook/react-vite";
import { Badge } from "@xingmang/ui-primitives";
import { ContextStrip } from "./ContextStrip";

const meta = {
  title: "Admin/ContextStrip",
  component: ContextStrip,
  parameters: { layout: "fullscreen" },
  decorators: [
    (Story) => (
      <div className="bg-canvas pb-8">
        <Story />
      </div>
    ),
  ],
} satisfies Meta<typeof ContextStrip>;
export default meta;
type Story = StoryObj<typeof meta>;

const serverResolved = {
  label: "环境 由服务端解析",
  tone: "info" as const,
  hint: "前端未显式指定 environment，读取范围由服务端按调用者所在环境决定（跨环境读取本就不允许）",
};

const platformCrumbs = [
  { key: "section", label: "被管平台" },
  { key: "page", label: "Sub2API" },
];

export const Default: Story = {
  args: { crumbs: platformCrumbs, environment: serverResolved },
};

/** 位置永远先于数据到达：路由一切换面包屑就成立，不必等任何请求。
 *  所以这里没有「面包屑加载中」——只有环境标识可能还没读到。 */
export const Loading: Story = {
  args: {
    crumbs: platformCrumbs,
    environment: { label: "环境 读取中…", tone: "neutral" },
  },
};

/** 认不出的路径就不显示面包屑：说错一次，它以后就不能被当成坐标用了。
 *  环境标识仍然在场——那件事跟你在哪一页无关。 */
export const Empty: Story = {
  args: { environment: serverResolved },
};

/** 环境读不到时明说读不到，不猜一个 production。 */
export const Error: Story = {
  args: {
    crumbs: platformCrumbs,
    environment: {
      label: "环境 读取失败",
      tone: "danger",
      hint: "GET /api/v1/services 失败：503 upstream_unavailable",
    },
    note: "下方内容可能来自缓存",
  },
};

/** 无权读取当前页时位置照旧显示：人得知道自己站在哪，才知道该去申请什么权限。 */
export const PermissionDenied: Story = {
  args: {
    crumbs: [
      { key: "section", label: "全局" },
      { key: "page", label: "审计事件" },
    ],
    environment: serverResolved,
    note: "需要权限：audit.read",
  },
};

/** 超长层级名截断在自己的格子里，环境标识不被挤下去。 */
export const LongText: Story = {
  args: {
    crumbs: [
      { key: "section", label: "被管平台" },
      { key: "platform", label: "Sub2API 订阅转 API 售卖与计费（华东一区只读副本）" },
      { key: "page", label: "渠道令牌与余额明细 · 按上游分组 · 仅显示令牌失效项" },
    ],
    environment: { label: "环境 production-cn-east-1-readonly", tone: "info" },
    note: "只读视图",
  },
};

/** 最小形态：只有位置，没有环境与说明。 */
export const Compact: Story = {
  args: {
    crumbs: [
      { key: "section", label: "平台治理" },
      { key: "page", label: "设置" },
    ],
  },
};

export const DarkMode: Story = {
  args: {
    ...Default.args,
    actions: <Badge tone="warning">阶段 1</Badge>,
  },
  globals: { theme: "dark" },
};
