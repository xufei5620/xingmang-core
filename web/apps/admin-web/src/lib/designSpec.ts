import { tokens } from "@xingmang/design-tokens";

/** 「界面规范」页（XM-DESIGN0）的静态规格数据与纯函数。
 *
 *  这一页与后台其余每一页都不同：它**不读任何后端端点**。屏幕上的每一个色块、
 *  每一个按钮、每一张表都是仓库里真实组件的当场渲染，权威在
 *  `@xingmang/design-tokens` 与 `@xingmang/ui-primitives` / `@xingmang/ui-admin`。
 *  所以这里放的不是「将来会有的数据」，而是**从哪个真身取、怎么摆**。
 *
 *  一条纪律贯穿全文件：**不抄色值**。令牌名单从 `tokens` 推导，当前值在页面上
 *  用 `getComputedStyle` 实读；读不到就如实显示「读不到」，绝不退回一个写死的
 *  十六进制——那会让这一页自己成为第二份会漂移的色值定义，而它恰恰是那条
 *  「禁止硬编码颜色/圆角/阴影」红线的说明面。 */

export interface TokenRef {
  /** `tokens`（design-tokens/src/index.ts）里的语义名，如 `color.accent`。 */
  name: string;
  /** CSS 变量名，如 `--xm-color-accent`。取不出来时为 null。 */
  variable: string | null;
  /** 原始引用文本，如 `var(--xm-color-accent)`。取不出变量名时用它说明原因。 */
  reference: string;
}

/** `var(--x)` → `--x`。不是**单一** var() 引用（组合值、字面量）时返回 null。
 *
 *  只认单一引用而不做宽松匹配：这一页要拿变量名去 `getComputedStyle` 实读，
 *  从 `1px solid var(--a)` 里挑出 `--a` 读回来的是「那一个颜色」而不是这条声明
 *  本身的值——显示出来是错的，而错得看不出来。宁可承认取不出。 */
export function cssVariableOf(reference: string): string | null {
  const matched = /^var\((--[\w-]+)\)$/.exec(reference.trim());
  return matched ? (matched[1] as string) : null;
}

/** 把 `tokens` 的一组引用摊平成页面可渲染的清单。
 *
 *  遍历 `tokens` 而不是自己列一份名单：名单一旦手抄，design-tokens 新增一个
 *  语义色，这一页就会安静地少展示一条——而「少了一条」在一屏色卡里看不出来。 */
export function tokenRefs(prefix: string, group: Readonly<Record<string, string>>): TokenRef[] {
  return Object.entries(group).map(([key, reference]) => ({
    name: `${prefix}.${key}`,
    variable: cssVariableOf(reference),
    reference,
  }));
}

/** `getComputedStyle().getPropertyValue()` 读回来的原始串 → 可显示的值。
 *
 *  空串 / 全空白一律返回 null，由调用方渲染成「读不到」：浏览器对一个**没有
 *  定义**的自定义属性返回的正是空串，把它显示成空白格子的话，一个被误删的
 *  令牌在这一页上看起来跟正常的一模一样。 */
export function resolvedTokenValue(raw: string | null | undefined): string | null {
  if (raw === null || raw === undefined) return null;
  const trimmed = raw.trim();
  return trimmed === "" ? null : trimmed;
}

export const COLOR_TOKEN_REFS: readonly TokenRef[] = tokenRefs("color", tokens.color);
export const NAV_TOKEN_REFS: readonly TokenRef[] = tokenRefs("nav", tokens.nav);
export const FONT_TOKEN_REFS: readonly TokenRef[] = tokenRefs("font", tokens.font);
export const RADIUS_TOKEN_REFS: readonly TokenRef[] = tokenRefs("radius", tokens.radius);
export const SHADOW_TOKEN_REFS: readonly TokenRef[] = tokenRefs("shadow", tokens.shadow);

/** 令牌的「用在哪儿 / 为什么是这个取值」。
 *
 *  内容取自 tokens.css 里已有的注释，不新编理由。没有注释可依据的令牌就**不给
 *  说明**——编一句「用于卡片背景」这种同义反复，比留空更糟：它看起来像是有据。 */
export const TOKEN_NOTES: Readonly<Record<string, string>> = {
  "color.fgMuted":
    "取 gray-600 而不是 500：500 压在白底上只有 3.9:1，全站的说明文字、表头、次要标签都走它，达不到 AA 就是全站达不到。",
  "color.accentSoft": "选中行与软底标签的底色。",
  "color.edgeStrong": "输入控件这类「需要被摸到」的边。",
  "color.tableStripe":
    "斑马纹。比 surface-muted 再淡一档——拿 surface-muted 做隔行底色，整张表看起来会有一半像选中态。",
  "color.overlay": "对话框与抽屉的遮罩。",
  "nav.accent":
    "选中项左侧那道靛蓝亮边。整套设计只有三处用它：导航选中项、页面标题、指标卡顶边。",
  "nav.surface":
    "深色左侧导航自成一组令牌，**不随主题翻转**——浅色运营台配深色导航是这套设计在任何主题下的固定结构。",
};

/** 4px 基准的间距刻度（tokens.css 的 `--xm-space-*`）。
 *
 *  为什么这份名单在这里手写：`--xm-space-*` 与 `--xm-control-h-*` **没有**进
 *  design-tokens/src/index.ts 的 JS 映射表（那张表只收 color/nav/font/radius/shadow），
 *  也没有进 tailwind.css 的 @theme 映射，所以 JS 侧推导不出来。
 *
 *  但这里**只抄名字、不抄值**：页面按名字去 `getComputedStyle` 实读。tokens.css
 *  哪天删掉或改名一档，这一页会当场显示「读不到」而不是显示一个陈旧的旧值——
 *  名单漂了，页面自己会说出来。 */
export const SPACING_VARIABLES: readonly string[] = [
  "--xm-space-1",
  "--xm-space-2",
  "--xm-space-3",
  "--xm-space-4",
  "--xm-space-5",
  "--xm-space-6",
  "--xm-space-8",
  "--xm-space-10",
];

/** 控件高度三档（tokens.css 的 `--xm-control-h-*`）。Button/Input/Select 直接引用它们。 */
export const CONTROL_HEIGHT_VARIABLES: readonly string[] = [
  "--xm-control-h-sm",
  "--xm-control-h-md",
  "--xm-control-h-lg",
];

export interface TypeScaleEntry {
  /** 蓝图（DESIGN_BLUEPRINT 的「字体层级」卡）里的称呼。 */
  label: string;
  /** 平台里真实使用的类组合。改这里不会改样式——要改去改那个组件。 */
  className: string;
  /** 这一档现在由谁在用。写具体组件名，好让人去核对。 */
  usedBy: string;
  sample: string;
}

/** 字体层级。每一档都不是新定义的，而是从现有组件上摘下来的既成事实。 */
export const TYPE_SCALE: readonly TypeScaleEntry[] = [
  {
    label: "页面标题",
    className: "text-base font-semibold text-fg",
    usedBy: "ui-admin/PageHeader 的 h2",
    sample: "界面规范",
  },
  {
    label: "卡片标题",
    className: "text-sm font-medium text-fg",
    usedBy: "ui-admin/MetricCard 的 h3、ui-primitives/FormField 的标签",
    sample: "控制平面组件健康",
  },
  {
    label: "正文内容",
    className: "text-sm text-fg",
    usedBy: "ui-primitives/Tabs 的内容区默认字号",
    sample: "一段正文：页面里成段的说明与表格单元格走这一档。",
  },
  {
    label: "辅助文字",
    className: "text-xs text-fg-muted",
    usedBy: "PageHeader 的说明行、ui-admin/FreshnessNote",
    sample: "辅助说明：口径、来源与限制写在这一档。",
  },
  {
    label: "数字与时间",
    className: "font-mono text-xs text-fg tabular-nums",
    usedBy: "OpsPage 的 commit 与依赖列、formatUtcTimestamp 的显示位",
    sample: "2026-01-01 00:00:00 UTC",
  },
];

export interface RadiusUsage {
  /** `tokens.radius` 的键。 */
  token: string;
  /** Tailwind 侧的类名（tailwind.css 把 --radius-* 映射到了同名令牌）。 */
  className: string;
  usedBy: string;
}

/** 四档圆角各自的落点。 */
export const RADIUS_USAGE: readonly RadiusUsage[] = [
  { token: "sm", className: "rounded-sm", usedBy: "状态标签（ui-primitives/Badge）" },
  { token: "md", className: "rounded-md", usedBy: "按钮与输入控件（Button / Input / Select）" },
  { token: "lg", className: "rounded-lg", usedBy: "卡片与面板（MetricCard / PageState / DataTableV2 外框）" },
  { token: "xl", className: "rounded-xl", usedBy: "更大的说明面板（RunwayThresholdRulePanel）" },
];

/** 表格演示行。
 *
 *  **全站唯一允许出现演示行的地方**——因为这一格要说明的正是表格交互本身
 *  （排序、搜索、列显隐、密度、分页、行展开），而一张空表说明不了任何交互。
 *
 *  三条纪律钉在这份数据上：
 *  1. 行标识写成「示例行 A/B/C」，任何一张截图里都读不成业务对象；
 *  2. 不出现货币、百分比或带单位的计数——那三种形状会被读成读数（宪法 12 条）；
 *  3. 哈希用全 0，审计编号带 `demo` 前缀，时间是明显的整点合成值。
 *  designSpec.test.ts 对第 2 条有一条逐字段的断言。 */
export interface DesignDemoRow {
  id: string;
  name: string;
  /** 这一行演示哪一种行内详情：普通对象属性，还是证据字段。 */
  detailKind: "plain" | "evidence";
  /** 直接喂给 ui-admin 的 describeServiceStatus / ServiceStatusBadge。 */
  status: "active" | "degraded" | "retired";
  observedAt: string;
  source: string;
  hash: string;
  auditId: string;
}

export const DESIGN_DEMO_ROWS: readonly DesignDemoRow[] = [
  {
    id: "demo-a",
    name: "示例行 A",
    detailKind: "plain",
    status: "active",
    observedAt: "2026-01-01T00:00:00Z",
    source: "design-spec-demo",
    hash: `sha256:${"0".repeat(64)}`,
    auditId: "audit_demo_a",
  },
  {
    id: "demo-b",
    name: "示例行 B",
    detailKind: "evidence",
    status: "degraded",
    observedAt: "2026-01-02T03:04:05Z",
    source: "design-spec-demo",
    hash: `sha256:${"0".repeat(64)}`,
    auditId: "audit_demo_b",
  },
  {
    id: "demo-c",
    name: "示例行 C",
    detailKind: "plain",
    status: "retired",
    observedAt: "2026-01-03T06:07:08Z",
    source: "design-spec-demo",
    hash: `sha256:${"0".repeat(64)}`,
    auditId: "audit_demo_c",
  },
];

/** 「复杂组件」四件套各自在等谁。
 *
 *  这四个组件在仓库里**一个都不存在**（admin-web/src/components 下没有流程节点、
 *  日历单元格、预览框、时间线）。所以这一格保持诚实占位：先造一个空壳，等到
 *  真功能来的时候壳的形状多半是错的，而错壳比没有壳更难拆。 */
export const PENDING_COMPLEX_COMPONENTS: readonly { title: string; waitingFor: string }[] = [
  { title: "流程节点", waitingFor: "等审批链前端与「接口与自动化」（/ext/integration）落地，两处共用同一种节点。" },
  {
    title: "内容日历单元格",
    waitingFor:
      "属扩展能力段的只读蓝图（/ext/publishing）。按实施计划 §2.5，不得因为这一页存在就提前建组件。",
  },
  {
    title: "桌面与手机预览框",
    waitingFor: "属扩展能力段的只读蓝图（/ext/app），同上：随「应用与配置」实现，不预先造壳。",
  },
  { title: "告警时间线", waitingFor: "等告警详情页；时刻与事件的字段形状由那一片定。" },
];
