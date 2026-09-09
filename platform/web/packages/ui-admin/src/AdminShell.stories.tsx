import type { Meta, StoryObj } from "@storybook/react-vite";
import {
  EmptyState as EmptyStateView,
  ErrorState as ErrorStateView,
  LoadingState as LoadingStateView,
  PermissionDenied as PermissionDeniedView,
} from "@xingmang/ui-primitives";
import { AdminShell } from "./AdminShell";
import { ContextStrip } from "./ContextStrip";
import { NavItemDisabled, NavItemLabel, NavSection, NavSectionCollapsible, navItemClass } from "./Nav";
import { navStageHint, NAV_GROUPS, PLATFORM_NAV_ITEMS } from "./navigation";

/** 侧栏由 navigation.ts 渲染，与真实应用同一份数据（ADMIN-IA v3 §一）。
 *
 *  这一点是 XM-0042 的产出之一：以前这个故事里的导航是手抄的，于是文档改了名
 *  之后 Storybook 还在展示旧的三段式。设计评审看的是 Storybook，而 Storybook
 *  在骗人——那比没有 Storybook 更糟。
 *
 *  应用侧把这些 span 换成自己的 NavLink，壳本身不依赖 router;
 *  类名一律走 navItemClass——深色轨道上的配色由包决定，调用方不自己拼。 */
const nav = (
  <>
    {NAV_GROUPS.map((group) => {
      const items =
        group.id === "platforms" ? (
          // 平台段在真实应用里由 Registry 驱动。这里给一份典型状态:
          // 已登记的不挂标签，未接入的挂里程碑——两种都要能看见
          PLATFORM_NAV_ITEMS.map((platform, index) => (
            <span key={platform.serviceType} className={navItemClass({ isActive: false })}>
              <NavItemLabel
                label={platform.label}
                hint={index < 2 ? undefined : `未接入·${platform.stage}`}
              />
            </span>
          ))
        ) : (
          group.items.map((item) => (
            <span
              key={item.path}
              className={navItemClass({ isActive: item.path === "/dashboard" })}
            >
              <NavItemLabel label={item.label} hint={navStageHint(item)} />
            </span>
          ))
        );

      return group.collapsible ? (
        <NavSectionCollapsible key={group.id} title={group.title} hint={group.stage}>
          {items}
        </NavSectionCollapsible>
      ) : (
        <NavSection key={group.id} title={group.title}>
          {items}
        </NavSection>
      );
    })}
  </>
);

const contextStrip = (
  <ContextStrip
    crumbs={[
      { key: "section", label: "全局" },
      { key: "page", label: "运营工作台" },
    ]}
    environment={{
      label: "环境 由服务端解析",
      tone: "info",
      hint: "前端未显式指定 environment，读取范围由服务端按调用者所在环境决定",
    }}
  />
);

const base = {
  nav,
  contextStrip,
  user: { name: "产品负责人" },
  onLogout: () => {},
  children: <p className="text-sm">页面内容</p>,
};

const meta = {
  title: "Admin/AdminShell",
  component: AdminShell,
  parameters: { layout: "fullscreen" },
} satisfies Meta<typeof AdminShell>;
export default meta;
type Story = StoryObj<typeof meta>;

export const Default: Story = { args: base };

/** 「扩展能力」展开的样子。原型里这一组默认收起，进入 `#/ext/*` 时自动展开。
 *  收起不是「藏起来」：标题与「后置」标签始终在场，人能看出还有这么一段。 */
export const 扩展能力展开: Story = {
  args: {
    ...base,
    nav: (
      <NavSectionCollapsible title="扩展能力" hint="后置" open>
        {NAV_GROUPS.find((g) => g.id === "ext")?.items.map((item) => (
          <span key={item.path} className={navItemClass({ isActive: false })}>
            <NavItemLabel label={item.label} hint={navStageHint(item)} />
          </span>
        ))}
      </NavSectionCollapsible>
    ),
  },
};

/** 读不到服务注册表时平台段的样子。
 *  刻意不显示成「未接入」：那是拿一次 403 或一次加载中，去断言平台没有接入。 */
export const 注册表读取失败: Story = {
  args: {
    ...base,
    nav: (
      <NavSection title="平台">
        {PLATFORM_NAV_ITEMS.map((platform) => (
          <NavItemDisabled
            key={platform.serviceType}
            label={platform.label}
            hint="读取失败"
            title="服务注册表读取失败，无法判断该平台是否已接入"
          />
        ))}
      </NavSection>
    ),
  },
};

/** 数据还在路上时壳保持不动：导航、面包屑、环境标识都在原位，只有主内容区在转。
 *  壳跟着一起闪，人会以为整页重载了。 */
export const Loading: Story = {
  args: { ...base, children: <LoadingStateView label="正在读取指标…" /> },
};

export const Empty: Story = {
  args: {
    ...base,
    children: (
      <EmptyStateView title="这个环境还没采到任何指标" description="等待采集任务首次运行" />
    ),
  },
};

/** 出错时横幅与内容区各说各的：横幅说「这一屏的数据是什么性质」，内容区说
 *  「这一次请求怎么了」。合成一条就会丢掉其中一件事。 */
export const Error: Story = {
  args: {
    ...base,
    banner: (
      <div
        role="status"
        className="flex items-center justify-center gap-2 border-b-2 border-warning bg-warning/15 px-4 py-2 text-center text-sm font-semibold text-warning"
      >
        <span aria-hidden="true">⚠</span>
        <span>当前展示的是演示数据（Fake 连接器），非真实运营数据</span>
      </div>
    ),
    children: <ErrorStateView message="读取 /api/v1/metrics 失败：503 upstream_unavailable" />,
  },
};

/** 无权访问时导航仍然完整：把入口藏掉，人会以为这个功能根本不存在。
 *  服务端才是最终裁决者，前端隐藏不构成安全控制。 */
export const PermissionDenied: Story = {
  args: { ...base, children: <PermissionDeniedView permission="ops.read" /> },
};

/** 超长标题、导航项、面包屑与用户名都必须截断在自己的格子里:
 *  左栏不许被撑宽，顶栏不许换行。 */
export const LongText: Story = {
  args: {
    ...base,
    title: "星芒统一控制平台 · 生产环境 · 华东一区只读副本",
    contextStrip: (
      <ContextStrip
        crumbs={[
          { key: "section", label: "平台" },
          { key: "page", label: "Sub2API 订阅转 API 售卖与计费（华东一区只读副本）" },
        ]}
        environment={{ label: "环境 production-cn-east-1-readonly", tone: "info" }}
      />
    ),
    nav: (
      <NavSection title="平台">
        <span className={navItemClass({ isActive: true })}>
          <NavItemLabel label="Sub2API 订阅转 API 售卖与计费（华东一区）" />
        </span>
        <NavItemDisabled label="服务器（Server Agent 待接入，ADR-015）" hint="未接入·M2" />
      </NavSection>
    ),
    user: { name: "王小明（平台运营 · 只读）" },
  },
};

/** 窄屏（1024px）：左栏定宽不压缩，内容区自己收窄。
 *  横向溢出只允许发生在表格容器内部，不能撑宽整个页面（§11.2）。 */
export const Compact: Story = {
  args: base,
  decorators: [
    (Story) => (
      <div className="w-256 max-w-full overflow-hidden border border-edge">
        <Story />
      </div>
    ),
  ],
};

/** 暗色下左栏比画布再暗一档：两者同色的话左栏就消失了。 */
export const DarkMode: Story = {
  args: base,
  globals: { theme: "dark" },
};
