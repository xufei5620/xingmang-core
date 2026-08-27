import type { Meta, StoryObj } from "@storybook/react-vite";
import { PageState } from "./PageState";

const meta = {
  title: "Admin/PageState",
  component: PageState,
} satisfies Meta<typeof PageState>;
export default meta;
type Story = StoryObj<typeof meta>;

/** 默认：空态。空的是什么只有调用方知道，所以 title 必须自己传——
 *  一句「暂无数据」什么也没说明。 */
export const Default: Story = {
  args: {
    kind: "empty",
    title: "这个环境还没有登记任何服务",
    description: "用右上角的「登记服务」按钮登记第一个",
  },
};

export const Loading: Story = { args: { kind: "loading" } };

export const Empty: Story = {
  args: {
    kind: "empty",
    title: "没有匹配的告警",
    description: "换个条件，或清除筛选看全部",
  },
};

/** 错误态带错误码与 request_id：报障时要能和服务端日志对上。
 *  重试按钮只在重试有意义时出现。 */
export const Error: Story = {
  args: {
    kind: "error",
    message: "读取 /api/v1/metrics 失败：503 upstream_unavailable（错误码 UNAVAILABLE）",
    footnote: "request_id: req-8f21c0",
    onRetry: () => {},
  },
};

/** 无权访问**不给重试按钮**：权限不足重试一万次都是同一个答案。
 *  「前端隐藏不构成安全控制」这句话必须在——它提醒读代码的人别把它当权限门。 */
export const PermissionDenied: Story = {
  args: { kind: "denied", permission: "ops.read", footnote: "request_id: req-9c02aa" },
};

/** 「未接入」不是「空」。
 *
 *  空是「读到了，里面没有」，下一步是去造一条数据；未接入是「这块我们还没建」,
 *  下一步是等排期。把后者显示成前者，就是让运营以为「这个平台今天没有告警」。 */
export const 未接入: Story = {
  args: {
    kind: "unavailable",
    description: "退款冻结与对账异常随支付接入（M3）上线",
  },
};

/** 超长文案必须能换行，不能把容器撑破。 */
export const LongText: Story = {
  args: {
    kind: "error",
    message:
      "读取 /api/v1/platforms/sub2api/requests 失败：上游请求审计系统在 30 秒内没有响应，可能是 reqlog 控制台不可达、也可能是这次查询的时间窗太大导致上游超时（错误码 UPSTREAM_TIMEOUT）",
    footnote: "request_id: req-0f4a2c9d8b7e6a5f4c3b2a1908f7e6d5",
    onRetry: () => {},
  },
};

/** 紧凑：嵌在表格或卡片内部时用，留白收窄一档。 */
export const Compact: Story = {
  args: {
    kind: "empty",
    title: "没有渠道",
    description: "这次观测里 channels 为空数组",
    compact: true,
  },
  decorators: [
    (Story) => (
      <div className="w-96 max-w-full rounded-lg border border-edge p-2">
        <Story />
      </div>
    ),
  ],
};

export const DarkMode: Story = {
  args: {
    kind: "error",
    message: "读取失败：503 upstream_unavailable",
    onRetry: () => {},
  },
  globals: { theme: "dark" },
};
