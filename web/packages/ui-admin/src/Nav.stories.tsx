import type { Meta, StoryObj } from "@storybook/react-vite";
import { NavItemDisabled, NavItemLabel, NavSection, NavSectionCollapsible, navItemClass } from "./Nav";
import { navStageHint, GLOBAL_NAV_ITEMS, NAV_GROUPS, PLATFORM_NAV_ITEMS } from "./navigation";

const meta = {
  title: "Admin/Nav",
  component: NavSection,
} satisfies Meta<typeof NavSection>;
export default meta;
type Story = StoryObj<typeof meta>;

const item = (label: string, hint?: string, active = false) => (
  <span key={label} className={navItemClass({ isActive: active })}>
    <NavItemLabel label={label} hint={hint} />
  </span>
);

/** 一段导航：段标题 + 若干条目。条目来自 ADMIN-IA v3 的可执行副本
 *  (navigation.ts)，不是手抄的——手抄的那份在 XM-0042 之前已经漂了。 */
export const Default: Story = {
  args: {
    title: "全局",
    children: GLOBAL_NAV_ITEMS.map((navItem) =>
      item(navItem.label, navStageHint(navItem), navItem.path === "/dashboard"),
    ),
  },
};

/** 平台段：4 个平台一律可点（原型把它们全画成可进入的），右侧标签说明
 *  「点进去有没有真数据」。
 *
 *  未接入的条目**留在导航上**而不是隐藏（规格 §12 惯例）——看不见的东西，
 *  运营会以为平台压根不管这摊事；而灰掉它只能表达「不能点」，表达不了
 *  「能看结构、还没有数据」，后者才是服务器与 CPA 现在的状态。 */
export const 平台: Story = {
  args: {
    title: "平台",
    children: PLATFORM_NAV_ITEMS.map((platform, index) =>
      item(platform.label, index < 2 ? undefined : `未接入·${platform.stage}`, index === 0),
    ),
  },
};

/** 治理段：七页里有四页还只是路由位，右侧「未建·<阶段>」把这件事说出来。 */
export const 平台治理: Story = {
  args: {
    title: "平台治理",
    children: (NAV_GROUPS.find((g) => g.id === "governance")?.items ?? []).map((navItem) =>
      item(navItem.label, navStageHint(navItem)),
    ),
  },
};

/** 读不到注册表时的样子。
 *
 *  刻意不显示成「未接入」：那是拿一次 403 或一次加载中，去断言平台没有接入。
 *  不知道就说不知道。 */
export const 注册表读取失败: Story = {
  args: {
    title: "平台",
    children: PLATFORM_NAV_ITEMS.slice(0, 2).map((platform) => (
      <NavItemDisabled
        key={platform.serviceType}
        label={platform.label}
        hint="读取失败"
        title="服务注册表读取失败，无法判断该平台是否已接入"
      />
    )),
  },
};

/** 可折叠的「扩展能力」段（原型里唯一一个）。默认收起、带「后置」标签，
 *  进入 `#/ext/*` 时自动展开。收起 ≠ 隐藏：标题始终在场。 */
export const 扩展能力: Story = {
  render: () => (
    <div className="w-60 bg-nav-surface p-2 text-nav-fg">
      <NavSectionCollapsible title="扩展能力" hint="后置">
        {(NAV_GROUPS.find((g) => g.id === "ext")?.items ?? []).map((navItem) =>
          item(navItem.label, navStageHint(navItem)),
        )}
      </NavSectionCollapsible>
    </div>
  ),
  args: { title: "扩展能力", children: null },
};
