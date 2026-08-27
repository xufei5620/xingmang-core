import type { Meta, StoryObj } from "@storybook/react-vite";
import { NavItemDisabled, NavSection } from "./Nav";

const meta = {
  title: "Admin/Nav",
  component: NavSection,
} satisfies Meta<typeof NavSection>;
export default meta;
type Story = StoryObj<typeof meta>;

const linkClass = "block rounded-md px-3 py-2 text-fg-muted hover:bg-surface-muted";
const activeClass = "block rounded-md bg-surface-muted px-3 py-2 font-medium text-accent";

/** 一段导航：段标题 + 若干条目。 */
export const Default: Story = {
  args: {
    title: "全局",
    children: (
      <>
        <span className={activeClass}>运营总览</span>
        <NavItemDisabled label="告警中心" hint="即将上线" title="告警中心随 XM-0033 上线" />
        <span className={linkClass}>审计事件</span>
      </>
    ),
  },
};

/** 被管平台段：Registry 里有的可点，没有的显示为未接入并带里程碑标签。
 *
 *  未接入的条目**留在导航上**而不是隐藏（规格 §12 惯例）——看不见的东西，
 *  运营会以为平台压根不管这摊事。 */
export const 被管平台: Story = {
  args: {
    title: "被管平台",
    children: (
      <>
        <span className={activeClass}>Sub2API</span>
        <NavItemDisabled label="NewAPI" hint="未接入·M1" />
        <NavItemDisabled label="CPA" hint="未接入·M4" />
        <NavItemDisabled label="开票系统" hint="契约草案·XM-0028" />
        <NavItemDisabled label="支付" hint="未接入·M3" />
        <NavItemDisabled label="服务器" hint="未接入·M2" />
        <NavItemDisabled label="模型保障" hint="未接入·M1.5" />
      </>
    ),
  },
};

/** 读不到注册表时的样子。
 *
 *  刻意不显示成「未接入」：那是拿一次 403 或一次加载中，去断言平台没有接入。
 *  不知道就说不知道。 */
export const 注册表读取失败: Story = {
  args: {
    title: "被管平台",
    children: (
      <>
        <NavItemDisabled
          label="Sub2API"
          hint="读取失败"
          title="服务注册表读取失败，无法判断该平台是否已接入"
        />
        <NavItemDisabled
          label="NewAPI"
          hint="读取失败"
          title="服务注册表读取失败，无法判断该平台是否已接入"
        />
      </>
    ),
  },
};
