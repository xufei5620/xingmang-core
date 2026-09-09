/** 管理后台信息架构（ADMIN-IA v3）的**唯一可执行副本**。
 *
 *  这份数据同时驱动三处：侧栏（admin-web/router.tsx）、面包屑（admin-web/lib/breadcrumbs.ts）、
 *  Storybook（AdminShell.stories.tsx）。在 XM-0042 之前这三处各抄了一份 ADMIN-IA,
 *  于是漂了：侧栏写「注册表」、面包屑写「注册表」、Storybook 少一条「变更与审批」，
 *  而文档里这一页早改叫「资源目录」。抄三遍就会漂三次——所以只留一份。
 *
 *  为什么放在 ui-admin 而不是 admin-web:Storybook 只收 `packages/ * /src/**` 下的故事
 *  （apps/ui-storybook/.storybook/main.ts），放在应用里 Storybook 就够不着，
 *  「一份数据驱动 Storybook」这句话立刻不成立。
 *
 *  这里只放**信息架构**（有哪些分组、哪些条目、叫什么、去哪、有哪些子页签）。
 *  「这个平台在 Registry 里登记了没有」「点进去有没有内容」属于运行时事实，
 *  留在 admin-web/lib/platforms.ts —— 把它们混进来，这份数据就得依赖 API 类型，
 *  Storybook 也就渲染不了。
 *
 *  改导航的顺序：先改 docs/architecture/ADMIN-IA.md，再改这里，再改测试。 */

/** 页内子页签（ADMIN-IA §2.2）。
 *
 *  `id` 是 URL 里 `?sub=` 的取值，由本项目定（原型是 hash 路由，没有可直接复用的 id）；
 *  `label` 逐字照抄 ADMIN-IA，不得改写。 */
export interface NavSubTab {
  id: string;
  label: string;
}

/** 侧栏条目（ADMIN-IA §一 的一行）。 */
export interface NavItemSpec {
  /** 原型 id(`#/g/<id>`、`#/gov/<id>`、`#/ext/<id>`)。留着是为了能与原型逐条对账。 */
  id: string;
  /** 条目名称，逐字照抄 ADMIN-IA §一，不得改写。 */
  label: string;
  /** 平台真实路由（ADMIN-IA §四）。同时是 React Router 的路径与面包屑的键。 */
  path: string;
  /** ADMIN-IA §一 的「阶段」列。未实装的条目用它拼出侧栏右侧的 `未建·<阶段>`。 */
  stage: string;
  /** 页面是否已实装。false = 本片只建了路由位与占位页。 */
  built: boolean;
  /** 页内子页签，逐字按 ADMIN-IA §2.2；空数组表示这一页没有子页签。 */
  subTabs: readonly NavSubTab[];
}

export interface NavGroupSpec {
  id: "global" | "platforms" | "governance" | "ext";
  /** 分组标题，逐字照抄 ADMIN-IA §一。 */
  title: string;
  /** 原型里这一组是可折叠 `<details>`，默认收起（只有「扩展能力」是）。 */
  collapsible: boolean;
  /** 分组本身的阶段标签（只有「扩展能力」有，原型写作「后置」）。 */
  stage?: string;
  /** 平台段的条目来自 Registry，不是静态数据——它的 items 为空，由应用注入。 */
  items: readonly NavItemSpec[];
}

function sub(...pairs: readonly (readonly [string, string])[]): readonly NavSubTab[] {
  return pairs.map(([id, label]) => ({ id, label }));
}

// --- 分组 1：全局（ADMIN-IA §一 分组 1）---

export const GLOBAL_NAV_ITEMS: readonly NavItemSpec[] = [
  {
    id: "overview",
    label: "运营工作台",
    path: "/dashboard",
    stage: "F-A",
    // 路径保持 /dashboard:XM-0006 起就是这个地址，改了会打断已有书签。
    // 原型的 `#/g/overview` 只规定页面语义，不要求逐字相同的 URL(交接文档 §8)
    built: true,
    subTabs: [],
  },
  {
    id: "alerts",
    label: "告警与故障",
    path: "/alerts",
    stage: "F-A",
    built: true,
    subTabs: sub(
      ["alerts", "告警"],
      ["incidents", "故障事件"],
      ["rules", "规则"],
      ["notifications", "通知"],
      ["silences", "暂停告警"],
    ),
  },
  {
    id: "actions",
    label: "操作与审批",
    path: "/actions",
    stage: "F-B",
    // 操作目录/执行记录接真实数据（XM-ACTIONS0）；待审批子页签接审批队列
    // （XM-0030b-ui）——审批中心后端已实装，但尚未在环境里启用，队列会自己
    // 显示「未启用」而不是伪造一个空队列。页面级门禁仍在
    // （ADMIN-IA §七：未启用前必须显示门禁，不可伪造执行）。
    built: true,
    subTabs: sub(
      ["catalog", "操作目录"],
      ["pending", "待审批"],
      ["runs", "执行记录"],
      ["risk", "风险与启用条件"],
    ),
  },
  {
    id: "jobs",
    label: "后台任务",
    path: "/jobs",
    stage: "F-A",
    built: true,
    subTabs: sub(
      ["running", "运行中"],
      ["scheduled", "定时任务"],
      ["batches", "同步批次"],
      ["failures", "失败与重试"],
      ["repeated", "多次失败任务"],
    ),
  },
  {
    id: "audit",
    label: "审计记录",
    path: "/audit",
    stage: "F-A",
    built: true,
    subTabs: sub(["events", "审计记录"], ["evidence", "操作证据"], ["chain", "审计链验证"]),
  },
];

// --- 分组 3：平台治理（ADMIN-IA §一 分组 3）---

export const GOVERNANCE_NAV_ITEMS: readonly NavItemSpec[] = [
  {
    // XM-CARD3：ADMIN-IA 里**没有**这一条——原型画的四个平台里没有卡片这一块。
    // 放在「平台治理」下是实现期的判断：它管的是平台自己持有的支付工具，
    // 不属于任何一个上游平台。归属待产品负责人确认。
    id: "cards",
    label: "卡片管理",
    path: "/cards",
    stage: "M3",
    built: true,
    subTabs: [],
  },
  {
    // XM-SMS0：与卡片同一情况——ADMIN-IA 里没有这一条。放在「平台治理」下
    // 并紧挨卡片，是因为它们是同一类东西：平台自己持有的、用来注册与维持
    // 外部服务账号的资源（一个是支付工具，一个是手机号）。归属同样待确认。
    id: "sms",
    label: "接码中心",
    path: "/sms",
    stage: "M3",
    built: true,
    subTabs: [],
  },
  {
    id: "registry",
    label: "资源目录",
    path: "/registry",
    stage: "F-A",
    built: true,
    subTabs: sub(
      ["services", "服务"],
      ["connectors", "连接器"],
      ["connections", "连接"],
      ["capabilities", "支持能力"],
      ["apps", "应用与模块"],
      ["environments", "环境"],
    ),
  },
  {
    id: "identity",
    label: "人员与权限",
    path: "/identity",
    stage: "F-A",
    // XM-LOGIN：账号与身份子页已接真实数据（员工账号的建立/改角色/禁用/重置密码）。
    // 其余四个子页（权限规则、权限范围、密钥引用、会话）仍是诚实占位。
    built: true,
    subTabs: sub(
      ["accounts", "账号与身份"],
      ["rules", "权限规则"],
      ["scopes", "权限范围"],
      ["credentials", "密钥引用"],
      ["sessions", "会话"],
    ),
  },
  {
    id: "finance",
    label: "跨平台财务",
    path: "/finance",
    stage: "M3+",
    // XM-FINANCE-GLOBAL0（2026-09-07）：六格里「财务总览」接了两平台的
    // payments.daily 与 finance 的渠道/上游摘要，「财务配置」是已冻结决定的
    // 如实说明，「开票集成」早已是嵌入开票管理端的 iframe。
    // **支付通道 / 财务对账 / 异常与冻结三格仍是诚实占位**——后端根本不存在
    // （connectors/payment/ 是空目录、finance schema 里没有对账表），页面按
    // 蓝图逐字列头呈现并写清在等 M3 支付接入。stage 保持 M3+ 正是这个意思。
    built: true,
    subTabs: sub(
      ["overview", "财务总览"],
      ["channels", "支付通道"],
      ["reconciliation", "财务对账"],
      ["exceptions", "异常与冻结"],
      ["invoicing", "开票集成"],
      ["settings", "财务配置"],
    ),
  },
  {
    id: "ops",
    label: "运行保障",
    path: "/ops",
    stage: "M1+",
    // XM-OPS0：控制平面健康子页已接真实数据（GET /api/v1/ops/overview）。
    // 其余五个子页（稳定性、备份恢复、故障手册、迁移对比、模型质量）仍是诚实占位。
    built: true,
    subTabs: sub(
      ["health", "控制平面健康"],
      ["stability", "稳定性与外部监控"],
      ["backup", "备份与恢复"],
      ["runbook", "故障处理手册（Runbook）"],
      ["migration", "迁移与数据对比"],
      ["model-quality", "模型质量保障"],
    ),
  },
  {
    id: "changes",
    label: "版本与发布",
    path: "/changes",
    stage: "F-B",
    // XM-CHANGES0（2026-09-07）：「发布与回滚」的当前部署信息接了 ops 概览，
    // 其余格子按各自的真实源接或诚实占位。stage 保持 F-B——审批中心虽已启用
    // （XM-0030-ENABLE），但「变更单」这个对象本身仍未建，页面上说清了这一点。
    built: true,
    subTabs: sub(
      ["requests", "变更单"],
      ["releases", "发布与回滚"],
      ["tests", "自动测试与质量"],
      ["packages", "发布包与安全检查"],
      ["database", "数据库变更"],
    ),
  },
  {
    id: "design",
    label: "界面规范",
    path: "/design",
    stage: "UI",
    // XM-DESIGN0（2026-09-07）：**零后端依赖**——这一页展示的是设计系统本身，
    // 令牌从 @xingmang/design-tokens 推导、控件是真组件实例，一条端点都不读。
    // 「复杂组件」那一格仍是诚实占位：那四个组件仓库里一个都还没有。
    built: true,
    subTabs: sub(
      ["color", "颜色与排版"],
      ["controls", "按钮与表单"],
      ["cards", "卡片与状态"],
      ["tables", "表格与详情"],
      ["states", "页面状态"],
      ["components", "复杂组件"],
    ),
  },
  {
    id: "settings",
    label: "设置",
    path: "/settings",
    stage: "F-A 尾",
    built: true,
    subTabs: [],
  },
];

// --- 分组 4：扩展能力（ADMIN-IA §一 分组 4）---
//
// 原型里这一组是可折叠 <details>、默认收起、分组本身带阶段标签「后置」。
// 四页全部是**只读蓝图**：明确标注「仅预览、不保存、不发布、不执行」，
// 不得因此提前建后端（实施计划 §2.5）。

export const EXT_NAV_ITEMS: readonly NavItemSpec[] = [
  {
    id: "app",
    label: "应用与配置",
    path: "/ext/app",
    stage: "后置",
    // XM-EXT-APP（2026-09-08）：产品负责人推翻了 ADMIN-IA §5.4「四页只读蓝图、
    // 不得因此提前建后端」对这一页的适用（裁定变更逐字记在 §5.4）。这一页现在
    // 有真实后端（core.ext_app / core.ext_app_release）与真实路由，
    // **stage 仍留「后置」**：它说的是这一段在信息架构里的优先级，不是实装进度，
    // 而实装进度由 built 表达——两者混成一个字段的话，「这一段是后置的」这条
    // 设计事实会随着某一页建成而消失。
    built: true,
    subTabs: sub(
      ["catalog", "应用目录"],
      ["pages", "页面配置"],
      ["components", "页面组件"],
      ["releases", "版本与发布"],
    ),
  },
  {
    id: "integration",
    label: "接口与自动化",
    path: "/ext/integration",
    // stage 保持「后置」：这一页建成的只是四格里的两格半（调用方对账、规则
    // 登记、运行记录），Webhook 那一格与规则的**执行**都还没有——分组本身
    // 仍是后置能力。built 翻成 true 只表示「点进去是真页面不是占位」。
    stage: "后置",
    // XM-EXT-INTEGRATION（2026-09-08）：产品负责人推翻 ADMIN-IA §5.4
    // 「只读蓝图、不得提前建后端」这条，对**这一页**改为真建（§5.4.1）。
    // 扩展能力段其余三页不变，仍是刻意的只读蓝图。
    built: true,
    subTabs: sub(
      ["clients", "API调用方"],
      ["webhooks", "Webhook"],
      ["flows", "自动化流程"],
      ["runs", "运行记录"],
    ),
  },
  {
    id: "publishing",
    label: "内容发布",
    path: "/ext/publishing",
    stage: "后置",
    // ADMIN-IA §5.4.1（2026-09-08）：产品负责人推翻了「扩展能力四页只读蓝图」
    // 在这一页上的适用，要求真建。扩展能力段里**只有这一页** built=true，
    // 另外三页的原裁定原样有效。
    built: true,
    subTabs: sub(
      ["calendar", "内容日历"],
      ["drafts", "草稿与素材"],
      ["approvals", "审批队列"],
      ["channels", "渠道与账号"],
      ["records", "发布记录"],
    ),
  },
  {
    id: "ai",
    label: "AI能力管理",
    path: "/ext/ai",
    stage: "后置",
    built: false,
    subTabs: sub(
      ["routes", "模型线路"],
      ["roles", "AI角色"],
      ["tools", "AI工具"],
      ["budget", "运行与预算"],
    ),
  },
];

/** 侧栏四分组（ADMIN-IA §一）。段序与条目顺序都照文档，不按「常用程度」重排：
 *  这份导航是运营心智的外化——先看全平台横切，再看某个被管系统，最后才是管平台自己。 */
export const NAV_GROUPS: readonly NavGroupSpec[] = [
  { id: "global", title: "全局", collapsible: false, items: GLOBAL_NAV_ITEMS },
  // 平台段的条目由 `GET /api/v1/services` 叠平台目录得到（见 admin-web/lib/platforms），
  // 不是静态数据，所以 items 为空
  { id: "platforms", title: "平台", collapsible: false, items: [] },
  { id: "governance", title: "平台治理", collapsible: false, items: GOVERNANCE_NAV_ITEMS },
  { id: "ext", title: "扩展能力", collapsible: true, stage: "后置", items: EXT_NAV_ITEMS },
];

/** 平台段的分组标题。平台条目动态生成，但标题与其他三组同源。 */
export const PLATFORM_GROUP_TITLE = "平台";

// --- 平台页签（ADMIN-IA §2.1）---

/** 平台页签。各平台**不同**，不是 v2 那种全平台统一模板——原型给 4 个平台画的是
 *  4 套不同的页签条（8→9 / 8→9 / 5 / 7）。做成统一模板会在第二个平台上就对不上。 */
export interface PlatformTabSpec {
  /** `?tab=` 的取值，与原型 `items` 的 id 逐字一致。 */
  value: string;
  /** 页签名，逐字照抄 ADMIN-IA §2.1。 */
  label: string;
  /** 该页签的阶段徽标（原型只给 `model` 标了 M1.5）。 */
  stage?: string;
  /** 页内子页签（ADMIN-IA §2.2）；空数组表示这一格用筛选条与周期控件组织。 */
  subTabs: readonly NavSubTab[];
}

export interface PlatformNavSpec {
  /** Registry 的 service_type，同时是 `/platforms/:serviceType` 的路由参数。 */
  serviceType: string;
  /** 原型里的平台 id(`#/s2/`、`#/newapi/`…)，留着与原型对账。 */
  prototypeId: string;
  /** 平台名，逐字照抄 ADMIN-IA §一 分组 2。 */
  label: string;
  stage: string;
  tabs: readonly PlatformTabSpec[];
}

/** 渠道保障的 3 个子页签（ADMIN-IA §2.2 `s2/model`）。Sub2API / NewAPI / CPA 共用。 */
const ASSURANCE_SUB_TABS = sub(
  ["overview", "保障概览"],
  ["probes", "检测任务"],
  ["history", "历史记录"],
);

/** 「渠道保障」页签（ADMIN-IA §8.1 裁定 #1 选项 A）。
 *
 *  原型最终态把它从 Sub2API/NewAPI 的页签数组里挤掉了（被「上游管理」顶替），
 *  但页面 `V["s2/model"]` 与路由分支都还在——是原型自己的孤儿路由。
 *  产品负责人裁定按选项 A 恢复，位置取选项字面的「末位」= 页签条末位
 *  （2026-08-28 裁定时原文写的是「第 9 个页签」，当时页签条一共 9 格；
 *  2026-09-02 「上游管理」并入「渠道管理」页内区块后条数减到 8 格，
 *  但「末位」这个位置本身没有变，仍是 ASSURANCE_TAB 排最后一个）。
 *  CPA 不动，仍按原型字面排在第 4 格。 */
const ASSURANCE_TAB: PlatformTabSpec = {
  value: "model",
  label: "渠道保障",
  stage: "M1.5",
  subTabs: ASSURANCE_SUB_TABS,
};

/** Sub2API 与 NewAPI 的公共前 7 格（ADMIN-IA §2.1：两者同构）。
 *  `finance` 的子页签两边不同，所以由各自传入。
 *
 *  2026-09-02 产品负责人裁定（ACCEPTANCE-LOG）：「上游管理」不再是独立页签，
 *  并入「渠道管理」页内区块——上游管理相关的账号/供应商登记簿、余额与整体
 *  毛利汇总，现在渲染在 `渠道管理` 页面渠道表下方，不再单独占一格页签。
 *  旧的 `suppliers` 页签值仍在 `LEGACY_TAB_ALIASES` 里保留 redirect
 *  （见 admin-web/lib/platforms.ts），不会变成 404。 */
function apiPlatformTabs(financeSubTabs: readonly NavSubTab[]): readonly PlatformTabSpec[] {
  return [
    { value: "overview", label: "概览", subTabs: [] },
    { value: "users", label: "用户管理", subTabs: [] },
    { value: "upstream", label: "渠道管理", subTabs: [] },
    { value: "finance", label: "支付与财务", subTabs: financeSubTabs },
    { value: "usage", label: "请求详情", subTabs: [] },
    { value: "creds", label: "连接与凭据", subTabs: [] },
    { value: "alerts", label: "告警", subTabs: [] },
    ASSURANCE_TAB,
  ];
}

export const PLATFORM_NAV_ITEMS: readonly PlatformNavSpec[] = [
  {
    serviceType: "sub2api",
    prototypeId: "s2",
    label: "Sub2API",
    stage: "F-A",
    tabs: apiPlatformTabs(
      sub(
        ["overview", "资金概览"],
        ["orders", "充值订单"],
        ["refunds", "退款与冲正"],
        ["profit", "利润核算"],
        ["invoices", "开票"],
      ),
    ),
  },
  {
    serviceType: "newapi",
    prototypeId: "newapi",
    label: "NewAPI",
    stage: "M1",
    // NewAPI 的「支付与财务」原型只画了 2 个子页签且没有开票，ADMIN-IA §8.2 #2
    // 曾裁定维持原型、不替它「修好」。CR-0005（2026-09-02，产品负责人指令）
    // 推翻了这条裁定：NewAPI 与 Sub2API 一样加「开票」子页签，但内容是嵌入开票
    // 系统管理端（EmbeddedConsoleFrame，/embed/admin/newapi），不是原生列表——
    // 原生列表仍属于第二阶段，等只读连接器与 CR-0002 冻结（ADMIN-IA §8.2 #2）。
    tabs: apiPlatformTabs(
      sub(["orders", "资金与订单"], ["profit", "利润核算"], ["invoices", "开票"]),
    ),
  },
  {
    serviceType: "cpa",
    prototypeId: "cpa",
    label: "CPA",
    stage: "M4",
    // 原型自留提问「CPA 是渠道代理，用户那栏是不是应该叫『代理商』？」
    // 产品负责人裁定按原型字面保留「用户管理」（ADMIN-IA §8.2 #5）
    tabs: [
      { value: "overview", label: "概览", subTabs: [] },
      { value: "users", label: "用户管理", subTabs: [] },
      { value: "upstream", label: "渠道管理", subTabs: [] },
      // CPA 的保障格排在第 4，是原型字面；Sub2API/NewAPI 的排在末位，是裁定 #1 的落点
      { value: "model", label: "渠道保障", stage: "M1.5", subTabs: ASSURANCE_SUB_TABS },
      { value: "finance", label: "支付与财务", subTabs: [] },
    ],
  },
  {
    serviceType: "server",
    prototypeId: "server",
    label: "服务器",
    stage: "M2",
    tabs: [
      { value: "overview", label: "概览", subTabs: [] },
      { value: "assets", label: "服务器资产", subTabs: [] },
      { value: "suppliers", label: "供应商与采购", subTabs: [] },
      { value: "services", label: "服务与容器", subTabs: [] },
      { value: "domains", label: "域名与证书", subTabs: [] },
      { value: "monitoring", label: "监控与告警", subTabs: [] },
      { value: "creds", label: "连接与凭据", subTabs: [] },
    ],
  },
];

// --- 查表 ---

const ITEMS_BY_PATH = new Map<string, { group: NavGroupSpec; item: NavItemSpec }>();
for (const group of NAV_GROUPS) {
  for (const item of group.items) ITEMS_BY_PATH.set(item.path, { group, item });
}

/** 路径 → 它在导航树里的位置。认不出来返回 undefined（面包屑据此显示空）。 */
export function navItemByPath(
  path: string,
): { group: NavGroupSpec; item: NavItemSpec } | undefined {
  // 结尾斜杠不影响判定：/dashboard 与 /dashboard/ 是同一页
  const normalized = path.length > 1 && path.endsWith("/") ? path.slice(0, -1) : path;
  return ITEMS_BY_PATH.get(normalized);
}

/** 页名。已实装的页面用它取自己的标题，于是侧栏、面包屑、页头三处永远是同一个名字
 *  ——XM-0042 之前它们各写各的，「注册表 / 资源目录」在同一屏上同时出现过。
 *
 *  找不到就抛：调用点传的是写死的常量，找不到只可能是导航数据被改坏了。
 *  这时候兜底显示一个别的名字，等于把问题藏到线上让运营去发现。 */
export function navLabel(path: string): string {
  const hit = navItemByPath(path);
  if (!hit) throw new Error(`导航数据里没有 ${path}——ADMIN-IA 与路由已经漂开`);
  return hit.item.label;
}

/** 全部非平台条目，按分组顺序摊平。路由表用它生成占位页，保证与侧栏同源。 */
export function allNavItems(): readonly NavItemSpec[] {
  return NAV_GROUPS.flatMap((group) => group.items);
}

/** 尚未实装、只建了路由位的条目。 */
export function placeholderNavItems(): readonly NavItemSpec[] {
  return allNavItems().filter((item) => !item.built);
}

export function platformNavSpec(serviceType: string): PlatformNavSpec | undefined {
  return PLATFORM_NAV_ITEMS.find((p) => p.serviceType === serviceType);
}

/** 侧栏右侧的阶段提示。已实装的条目不显示——一个写着「F-A」的标签贴在一张
 *  正常工作的页面旁边什么也没说明，而「未建」是运营真正需要知道的那件事。 */
export function navStageHint(item: NavItemSpec): string | undefined {
  return item.built ? undefined : `未建·${item.stage}`;
}
