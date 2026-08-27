import type { Meta, StoryObj } from "@storybook/react-vite";
import {
  EmptyState as EmptyStateView,
  ErrorState as ErrorStateView,
  LoadingState as LoadingStateView,
  PermissionDenied as PermissionDeniedView,
} from "@xingmang/ui-primitives";
import { AdminShell } from "./AdminShell";
import { ContextStrip } from "./ContextStrip";
import { NavItemDisabled, NavSection, navItemClass } from "./Nav";

const meta = {
  title: "Admin/AdminShell",
  component: AdminShell,
  parameters: { layout: "fullscreen" },
} satisfies Meta<typeof AdminShell>;
export default meta;
type Story = StoryObj<typeof meta>;

/** 三段式导航（ADMIN-IA v2）：全局 / 被管平台 / 平台治理。
 *  应用侧把这些 span 换成自己的 NavLink，壳本身不依赖 router。
 *  类名一律走 navItemClass——深色轨道上的配色由包决定，调用方不自己拼。 */
const nav = (
  <>
    <NavSection title="全局">
      <span className={navItemClass({ isActive: true })}>运营总览</span>
      <span className={navItemClass({ isActive: false })}>告警中心</span>
      <span className={navItemClass({ isActive: false })}>审计事件</span>
    </NavSection>
    <NavSection title="被管平台">
      <span className={navItemClass({ isActive: false })}>Sub2API</span>
      {/* NewAPI 自 XM-0035 起页面已建（内容来自 newapi.* 指标），
          所以它在这里是普通条目而不是禁用占位；禁用的样子看 CPA 那条 */}
      <span className={navItemClass({ isActive: false })}>NewAPI</span>
      <NavItemDisabled label="CPA" hint="未接入·M4" />
      <NavItemDisabled label="开票系统" hint="契约草案·XM-0028" />
    </NavSection>
    <NavSection title="平台治理">
      <span className={navItemClass({ isActive: false })}>注册表</span>
      <NavItemDisabled label="财务中心" hint="未接入·M3" />
      <span className={navItemClass({ isActive: false })}>设置</span>
    </NavSection>
  </>
);

const contextStrip = (
  <ContextStrip
    crumbs={[
      { key: "section", label: "全局" },
      { key: "page", label: "运营总览" },
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

export const Default: Story = { args: base };

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

/** 超长标题、导航项、面包屑与用户名都必须截断在自己的格子里：
 *  左栏不许被撑宽，顶栏不许换行。 */
export const LongText: Story = {
  args: {
    ...base,
    title: "星芒统一控制平台 · 生产环境 · 华东一区只读副本",
    contextStrip: (
      <ContextStrip
        crumbs={[
          { key: "section", label: "被管平台" },
          { key: "page", label: "Sub2API 订阅转 API 售卖与计费（华东一区只读副本）" },
        ]}
        environment={{ label: "环境 production-cn-east-1-readonly", tone: "info" }}
      />
    ),
    nav: (
      <NavSection title="被管平台">
        <span className={navItemClass({ isActive: true })}>
          Sub2API 订阅转 API 售卖与计费（华东一区）
        </span>
        <NavItemDisabled label="开票系统（Codex 线，契约冻结中）" hint="契约草案·XM-0028" />
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
