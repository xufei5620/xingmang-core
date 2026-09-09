import { PLATFORM_NAV_ITEMS, platformNavSpec, type PlatformTabSpec } from "@xingmang/ui-admin";
import type { ServiceItem } from "../api/platform";

/** 平台「还没接进来」时，导航与详情页要说清楚的那件事。
 *
 *  分成两种而不是压成一个里程碑字符串：ADMIN-IA 平台表里这几行的状态本来就
 *  不是一回事（✅ 已建 / ⬜ Mx）。把它们写成同一句话，就会把「平台已经建好了，
 *  只是当前环境没登记实例」和「压根还没排期」说成一样，而这两者对看板前面的人
 *  意味着完全不同的下一步动作。
 *
 *  v2 还有第三种 `contractDraft`（开票系统的「契约草案·XM-0028」）。开票不再是
 *  平台之后它没有了使用者，一并删掉——留着一个没人用的状态，下一个人会以为
 *  平台段还支持这种状态。 */
export type PlatformPlan =
  /** 页面已建（Sub2API）。Registry 里没有实例时是「没登记」，不是「没做」。 */
  | { kind: "shipped" }
  /** 规划于某个里程碑，对应 ADMIN-IA 的 ⬜ Mx。 */
  | { kind: "milestone"; milestone: string };

export interface PlatformSpec {
  /** Registry 的 service_type，同时是 /platforms/:serviceType 的路由参数。
   *
   *  取值对齐后端注释里给出的那一组(internal/platform/registry/service.go:35
   *  `sub2api / newapi / cpa / invoice / payment`)，并满足 registry 的标识符
   *  正则 `^[a-z0-9][a-z0-9-]{0,63}$`——它既是 URL 又是将来登记时要填的键，
   *  两边对不上的话，平台接入那天导航会凭空多出一个重复条目。 */
  serviceType: string;
  label: string;
  plan: PlatformPlan;
  /** 一句话范围。未接入平台的详情页上，这是唯一能诚实给出的内容。 */
  scope: string;
  /** 页面内容不依赖服务注册表时置 true——没登记也照样有真内容（XM-0035）。
   *
   *  为什么需要这个开关：`registered` 会被拿去回答两个问题——「注册表里有没有
   *  这个实例」和「这一页点进去有没有**真数据**」。对 NewAPI 这两个答案不一致：
   *  它的概览与渠道表读的是 `/metrics`（采集任务写的），与注册表毫无关系。
   *
   *  不置这个开关的后果不是「少一个链接」，而是**看板在说谎**：页面明明有
   *  五条指标可显示，导航却挂着「未登记」。§12 惯例要的是别把没接的说成接了，
   *  不是把接了的说成没接。 */
  opensWithoutRegistry?: boolean;
}

/** 平台的接入状态与范围说明。
 *
 *  与 PLATFORM_NAV_ITEMS（ui-admin/navigation）分开放：那边是**信息架构**
 *  （叫什么、有哪几格页签），这边是**接入事实**（建没建、点进去有没有真数据）。
 *  前者是文档的可执行副本，后者会随每次接入变化；混在一起的话，Storybook 就得
 *  连带知道 Registry 是什么，而它不该知道。
 *
 *  键必须与 PLATFORM_NAV_ITEMS 的 serviceType 一一对应，由 PLATFORM_CATALOG
 *  的构造保证（少一条就 undefined，类型上过不去）。 */
const PLATFORM_FACTS: Record<string, Omit<PlatformSpec, "serviceType" | "label">> = {
  sub2api: {
    plan: { kind: "shipped" },
    scope: "订阅转 API 的售卖与计费：用户数与余额、日收入/成本、渠道令牌与余额。",
  },
  newapi: {
    // XM-0035 起页面已建（概览 + 渠道两格实装，数据来自 newapi.* 指标）。
    // 真实上游只读客户端仍在等 XM-0038——但那是**数据是真是假**的问题，
    // 由指标卡的来源与演示横幅回答，不是「页面建没建」的问题。
    plan: { kind: "shipped" },
    opensWithoutRegistry: true,
    scope: "上游模型网关：渠道状态与错误率、用户与余额、充值/订阅、模型用量。",
  },
  cpa: {
    plan: { kind: "milestone", milestone: "M4" },
    scope: "推广投放与结算：代理商、渠道与渠道保障，接入后走原型的 5 格页签。",
  },
  server: {
    plan: { kind: "milestone", milestone: "M2" },
    scope: "服务器资产中心：资产、供应商与采购、服务与容器、域名与证书、监控与告警，经 Server Agent 接入（ADR-015）。",
  },
};

/** 平台目录 —— 导航「平台」段与平台详情页共用的唯一清单。
 *
 *  名称与顺序来自 ui-admin 的 PLATFORM_NAV_ITEMS(ADMIN-IA v3 §一 分组 2 的
 *  可执行副本)，这里只往上叠接入事实。要加平台先改文档，再改 navigation.ts,
 *  再回来补一条 PLATFORM_FACTS。
 *
 *  为什么需要一份静态清单，而不是纯粹跟着 Registry 走：规格 §12 惯例要求未接入
 *  的东西**显示为未接入**而不是隐藏。一个还没接的平台从导航里消失，与它不存在
 *  是两回事——运营看不出「这块我们打算做、但还没做」，就会以为平台不管这摊事。 */
export const PLATFORM_CATALOG: readonly PlatformSpec[] = PLATFORM_NAV_ITEMS.map((nav) => {
  const facts = PLATFORM_FACTS[nav.serviceType];
  if (!facts) throw new Error(`平台目录缺少 ${nav.serviceType} 的接入事实`);
  return { serviceType: nav.serviceType, label: nav.label, ...facts };
});

/** 明确**不是平台**的 service_type（ADMIN-IA v3 §5.2）。
 *
 *  这三个在 v2 里各占平台段一行，原型把它们全部降级归位了：
 *  支付与开票 → 各平台的「支付与财务」页签 + 治理段「跨平台财务」；
 *  模型保障 → 各平台的「渠道保障」页签 + 治理段「运行保障 / 模型质量保障」。
 *
 *  为什么光从目录里删掉还不够：groupPlatforms 有一条「登记即出现」的规则——
 *  Registry 里有、目录里没有的 service_type 会被自动补进平台段。后端的
 *  registry 认得 `payment` 与 `invoice`（service.go 的注释里就有），生产环境
 *  一旦登记过，它们会以**原始键名**重新出现在侧栏上，把刚做完的裁剪抵消掉。
 *  所以要一份显式的排除名单，而不是指望「目录里没有」。 */
export const NON_PLATFORM_SERVICE_TYPES: ReadonlySet<string> = new Set([
  "payment",
  "invoice",
  "model-assurance",
]);

/** Registry 读取状态。
 *
 *  「读不到 Registry」和「Registry 里没有」必须分开：前者我们不知道，后者我们
 *  知道且是空的。把读取失败画成「未接入」，就是拿一次 403 去断言一个平台没接——
 *  这正是新鲜度铁律（§9.1）在导航上的同一条要求。 */
export type RegistryState = "loading" | "error" | "ready";

export interface PlatformEntry {
  spec: PlatformSpec;
  /** Registry 中该 service_type 下的实例；空数组表示这个环境没有登记。 */
  services: ServiceItem[];
  /** 仅在 Registry 读到了的前提下才为 true。 */
  registered: boolean;
}

/** 把 Registry 的服务按 service_type 归组，套到平台目录上。
 *
 *  目录里没有、Registry 里有的 service_type 照样列出来：ADMIN-IA 说平台段
 *  「登记即出现」。这类平台没有中文名可用，就直接拿键名当标题——给一个未知
 *  service_type 编一个好听的名字会骗人，和 lib/metrics 对未登记指标的处理是
 *  同一条规矩。**但排除名单里的三个例外**：它们不是「我们还不认识的平台」，
 *  而是「我们认识、并且已经确定它不是平台」。 */
export function groupPlatforms(services: readonly ServiceItem[]): PlatformEntry[] {
  const byType = new Map<string, ServiceItem[]>();
  for (const service of services) {
    const bucket = byType.get(service.service_type);
    if (bucket) bucket.push(service);
    else byType.set(service.service_type, [service]);
  }

  const entries: PlatformEntry[] = PLATFORM_CATALOG.map((spec) => {
    const found = byType.get(spec.serviceType) ?? [];
    byType.delete(spec.serviceType);
    return { spec, services: found, registered: found.length > 0 };
  });

  for (const [serviceType, found] of byType) {
    if (NON_PLATFORM_SERVICE_TYPES.has(serviceType)) continue;
    entries.push({
      spec: {
        serviceType,
        label: serviceType,
        plan: { kind: "shipped" },
        // 目录外的平台没有规格里的一句话范围，留空由详情页略过这一段，
        // 而不是替它编一句
        scope: "",
      },
      services: found,
      registered: true,
    });
  }
  return entries;
}

/** 这个平台的详情页现在点进去有没有**真数据**。
 *
 *  与「页面开不开」已经不是同一个问题了：原型把 4 个平台全画成可进入的
 *  （CPA 是占位页、服务器是 wip），所以导航一律给链接，详情页一律展开页签条。
 *  这个判据现在只用来决定页头挂不挂「未接入·Mx / 未登记」的状态徽章，
 *  以及各页签里放的是真面板还是诚实占位。 */
export function platformOpens(entry: PlatformEntry): boolean {
  return entry.registered || entry.spec.opensWithoutRegistry === true;
}

/** 取单个平台条目。找不到表示目录与 Registry 都不认识它。 */
export function findPlatform(
  serviceType: string,
  services: readonly ServiceItem[],
): PlatformEntry | undefined {
  return groupPlatforms(services).find((entry) => entry.spec.serviceType === serviceType);
}

/** 导航条目右侧的状态标签（数据不真时显示）。 */
export function pendingBadge(plan: PlatformPlan): string {
  switch (plan.kind) {
    case "shipped":
      return "未登记";
    case "milestone":
      return `未接入·${plan.milestone}`;
  }
}

/** 详情页页头的状态说明。 */
export function pendingHeadline(plan: PlatformPlan): string {
  switch (plan.kind) {
    case "shipped":
      return "本环境未登记该平台的实例";
    case "milestone":
      return `未接入，规划于 ${plan.milestone}`;
  }
}

/** 侧栏平台条目右侧的状态标签；数据是真的就不挂标签。 */
export function platformNavHint(entry: PlatformEntry): string | undefined {
  return platformOpens(entry) ? undefined : pendingBadge(entry.spec.plan);
}

/** 哪些平台有请求数据。
 *
 *  与后端 requestlog.SupportsPlatform 逐字对应：reqlog 是 nginx 与
 *  NewAPI/Sub2API 之间的透明代理，只抄这两家的 `/v1/*`。别的被管平台
 *  （CPA、服务器）根本不在它的代理路径上。
 *
 *  ADMIN-IA v3 的页签集合已经只给这两个平台画了「请求详情」这一格，所以它不再
 *  用来过滤页签（那是 v2 统一模板时代的用途）。留着是因为请求详情页仍要用它
 *  早返回：不在抄录范围内的平台连问都不该问——那是一次注定 404 的**高敏读取**，
 *  发出去就会在审计链上留下一条谁也不需要的记录。测试拿它反过来钉页签集合。 */
const PLATFORMS_WITH_REQUESTS = new Set(["sub2api", "newapi"]);

export function platformHasRequests(serviceType: string): boolean {
  return PLATFORMS_WITH_REQUESTS.has(serviceType);
}

/** 「请求详情」页签的取值。测试用它把上面那份名单与页签集合钉在一起。 */
export const USAGE_TAB = "usage";

/** 某个平台的页签集合（ADMIN-IA v3 §2.1）。
 *
 *  各平台**不同**——原型给 4 个平台画的是 4 套页签条，不是 v2 那种统一模板。
 *  数据在 ui-admin/navigation 里，这里只是转发，好让页面不必知道 IA 放在哪。 */
export function tabsForPlatform(serviceType: string): readonly PlatformTabSpec[] {
  return platformNavSpec(serviceType)?.tabs ?? [];
}

/** 默认页签：ADMIN-IA 规定概览「永远第 1 格」，4 个平台都是。 */
export const DEFAULT_PLATFORM_TAB = "overview";

/** 旧 `?tab=` 值 → 新值（ADMIN-IA v3 §4.1 redirect 全表）。
 *
 *  前四条是 v2 统一模板的命名被原型替换掉的那几格；后三条是**原型自带的**
 *  历史别名（ADMIN-IA §三），只在服务器平台上成立。
 *
 *  别名解析后仍要过一遍「这个平台有没有这一格」：`?tab=logs` 贴到 Sub2API 上，
 *  别名指向的 `monitoring` 在 Sub2API 根本不存在，这时它是 Not Found 而不是
 *  「跳到监控页」——认得出名字不等于那一格在这里有。 */
const LEGACY_TAB_ALIASES: Readonly<Record<string, string>> = {
  resources: "upstream",
  requests: "usage",
  connection: "creds",
  // 裁定 #3：「指标趋势」砍掉，趋势并进各页卡片——概览那一格就是它的落点
  trends: "overview",
  containers: "services",
  certs: "domains",
  logs: "monitoring",
  // 2026-09-02 产品负责人裁定：Sub2API/NewAPI 的「上游管理」页签并入
  // 「渠道管理」页——旧书签跳到 upstream。04:40 裁定期间登记簿曾经是渠道
  // 管理页下方的独立区块，那时这里还配了一张 LEGACY_TAB_ALIAS_ANCHORS 锚点表
  // 让落地后自动滚过去；07:20 的补充裁定把登记簿字段直接并入了渠道表的行
  // 与详情页，已经没有独立区块可滚，锚点表随之删除——落地在渠道管理页顶部
  // 就是登记簿字段所在的地方，不需要额外滚动。服务器平台的 `suppliers`
  // （供应商与采购）不受影响：它本来就是 server 页签集合里的现役页签,
  // 不会走到这条 alias。
  suppliers: "upstream",
};

/** 裁定 #4：平台级「操作」页签砍掉，Action 入口收进全局「操作与审批」。 */
const ACTIONS_PAGE_PATH = "/actions";

export type PlatformTabResolution =
  /** 认识，而且这个平台有这一格。 */
  | { kind: "ok"; tab: string }
  /** 旧名字，改跳同平台的新页签（URL 要真的换掉，否则书签永远是旧的）。 */
  | { kind: "redirect"; tab: string }
  /** 这一格整个挪到了别的页面。 */
  | { kind: "moved"; path: string }
  /** 认不出来。**不回落概览**——交接文档 §8：未知对象显示 Not Found,
   *  不允许默默回退。静默回落的代价是人以为自己看的是刚贴进去的那一格。 */
  | { kind: "notFound"; tab: string };

/** `?tab=` 参数 → 该显示哪一格 / 该跳去哪 / 还是 404。 */
export function resolvePlatformTab(
  serviceType: string,
  raw: string | null | undefined,
): PlatformTabResolution {
  const tabs = tabsForPlatform(serviceType);
  if (raw === null || raw === undefined || raw === "") {
    return { kind: "ok", tab: DEFAULT_PLATFORM_TAB };
  }
  // 目录外的平台（Registry 里有、IA 里没有）没有页签集合可校验。这时候把任何
  // ?tab= 都判成 404 是在拿「我们还没给它定页签」去指责地址写错了——详情页
  // 会说清楚这个平台还没有页签定义，那才是实情
  if (tabs.length === 0) return { kind: "ok", tab: raw };
  if (tabs.some((tab) => tab.value === raw)) return { kind: "ok", tab: raw };
  if (raw === "operations") return { kind: "moved", path: ACTIONS_PAGE_PATH };

  const alias = LEGACY_TAB_ALIASES[raw];
  if (alias !== undefined && tabs.some((tab) => tab.value === alias)) {
    return { kind: "redirect", tab: alias };
  }
  return { kind: "notFound", tab: raw };
}

/** 被下线的三个平台路径的去处（ADMIN-IA v3 §4.1 redirect 全表）。
 *
 *  写成一张表而不是散在路由文件里：改页签命名的时候，没有人会想起来还有几条
 *  旧路径跟着它。测试逐条断言目标里的 `?tab=`/`?sub=` 在导航数据里真的存在，
 *  于是「改名把 redirect 指到一个不存在的页签」这件事会当场失败，而不是等到
 *  某个人点开旧书签看到 404。 */
export const LEGACY_PLATFORM_ROUTES: Readonly<Record<string, string>> = {
  // 开票并入 Sub2API「支付与财务」的「开票」子页签（原型有完整的开票详情页）
  invoice: "/platforms/sub2api?tab=finance&sub=invoices",
  // 支付不是平台，跨平台汇总在治理段
  payment: "/finance",
  // 裁定 #1（选项 A）：模型保障恢复为各平台的「渠道保障」页签
  "model-assurance": "/platforms/sub2api?tab=model",
};

/** v1 旧路径 `/channels` 的去处。原来指向「渠道/资源」那一格，原型把它拆成了
 *  「渠道管理」与「上游管理」两格，这条跟着改到上游管理。 */
export const CHANNELS_REDIRECT = "/platforms/sub2api?tab=upstream";

/** 指标键的平台前缀(`sub2api.revenue.daily` → `sub2api`)。
 *
 *  总览卡片靠它决定「查看平台 →」指向谁。用前缀而不是写一张
 *  metric_key → 平台的映射表：新平台的指标一上线就自动带上入口，
 *  不需要有人记得回来补表。 */
export function platformOfMetricKey(metricKey: string): string {
  const dot = metricKey.indexOf(".");
  return dot > 0 ? metricKey.slice(0, dot) : "";
}
