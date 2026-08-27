import type { ServiceItem } from "../api/platform";

/** 平台「还没接进来」时，导航与详情页要说清楚的那件事。
 *
 *  分成三种而不是压成一个里程碑字符串：ADMIN-IA 平台表里这几行的状态本来就
 *  不是一回事（✅ / 🟡 契约草案 / ⬜ Mx）。把它们写成同一句话，就会把
 *  「平台已经建好了，只是当前环境没登记实例」和「压根还没排期」说成一样，
 *  而这两者对看板前面的人意味着完全不同的下一步动作。 */
export type PlatformPlan =
  /** 页面已建（Sub2API）。Registry 里没有实例时是「没登记」，不是「没做」。 */
  | { kind: "shipped" }
  /** 规划于某个里程碑，对应 ADMIN-IA 的 ⬜ Mx。 */
  | { kind: "milestone"; milestone: string }
  /** 契约已有草案、页面待冻结后再建，对应 ADMIN-IA 的 🟡。 */
  | { kind: "contractDraft"; task: string };

export interface PlatformSpec {
  /** Registry 的 service_type，同时是 /platforms/:serviceType 的路由参数。
   *
   *  取值对齐后端注释里给出的那一组（internal/platform/registry/service.go:35
   *  `sub2api / newapi / cpa / invoice / payment`），并满足 registry 的标识符
   *  正则 `^[a-z0-9][a-z0-9-]{0,63}$`——它既是 URL 又是将来登记时要填的键，
   *  两边对不上的话，平台接入那天导航会凭空多出一个重复条目。 */
  serviceType: string;
  label: string;
  plan: PlatformPlan;
  /** 一句话范围。未接入平台的详情页上，这是唯一能诚实给出的内容。 */
  scope: string;
}

/** 被管平台目录 —— 导航「被管平台」段与平台详情页共用的唯一清单。
 *
 *  逐行抄自 docs/architecture/ADMIN-IA.md 的「平台清单与状态」表：那张表是导航
 *  结构的单一事实源，这里只是它的可执行副本。要加平台先改文档再改这里。
 *
 *  为什么需要一份静态清单，而不是纯粹跟着 Registry 走：规格 §12 惯例要求未接入
 *  的东西**显示为未接入**而不是隐藏。一个还没接的平台从导航里消失，与它不存在
 *  是两回事——运营看不出「这块我们打算做、但还没做」，就会以为平台不管这摊事。 */
export const PLATFORM_CATALOG: readonly PlatformSpec[] = [
  {
    serviceType: "sub2api",
    label: "Sub2API",
    plan: { kind: "shipped" },
    scope: "订阅转 API 的售卖与计费：用户数与余额、日收入/成本、渠道令牌与余额。",
  },
  {
    serviceType: "newapi",
    label: "NewAPI",
    plan: { kind: "milestone", milestone: "M1" },
    scope: "上游模型网关：渠道、令牌与用量，接入后走标准模板。",
  },
  {
    serviceType: "cpa",
    label: "CPA",
    plan: { kind: "milestone", milestone: "M4" },
    scope: "推广投放与结算，接入后走标准模板。",
  },
  {
    serviceType: "invoice",
    label: "开票系统",
    plan: { kind: "contractDraft", task: "XM-0028" },
    scope: "开票申请、资格与文件；文件所有权在开票系统一侧（ADR-018 数据通道隔离）。",
  },
  {
    serviceType: "payment",
    label: "支付",
    plan: { kind: "milestone", milestone: "M3" },
    scope: "订单、支付通道与对账；退款等写操作需 Foundation-B 审批。",
  },
  {
    serviceType: "server",
    label: "服务器",
    plan: { kind: "milestone", milestone: "M2" },
    scope: "容器、证书与日志，经 Server Agent 接入（ADR-015）。",
  },
  {
    // ADMIN-IA 对这一行写的是「挂在平台段末或全局，待定」。这里按前一个选项
    // 放在平台段末，因为它的对象仍然是「某个上游平台的模型是不是真的」。
    // 待产品负责人定夺（见 PR 描述里的文档歧义清单）。
    serviceType: "model-assurance",
    label: "模型保障",
    plan: { kind: "milestone", milestone: "M1.5" },
    scope: "跨上游的模型一致性：声明、探针与结论（ADR-010）。",
  },
];

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
 *  目录里没有、Registry 里有的 service_type 照样列出来：ADMIN-IA 说被管平台段
 *  「登记即出现」。这类平台没有中文名可用，就直接拿键名当标题——给一个未知
 *  service_type 编一个好听的名字会骗人，和 lib/metrics 对未登记指标的处理是
 *  同一条规矩。 */
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

/** 取单个平台条目。找不到表示目录与 Registry 都不认识它。 */
export function findPlatform(
  serviceType: string,
  services: readonly ServiceItem[],
): PlatformEntry | undefined {
  return groupPlatforms(services).find((entry) => entry.spec.serviceType === serviceType);
}

/** 导航条目右侧的状态标签（只在不可点时显示）。 */
export function pendingBadge(plan: PlatformPlan): string {
  switch (plan.kind) {
    case "shipped":
      return "未登记";
    case "milestone":
      return `未接入·${plan.milestone}`;
    case "contractDraft":
      return `契约草案·${plan.task}`;
  }
}

/** 详情页大占位的标题。 */
export function pendingHeadline(plan: PlatformPlan): string {
  switch (plan.kind) {
    case "shipped":
      return "本环境未登记该平台的实例";
    case "milestone":
      return `未接入，规划于 ${plan.milestone}`;
    case "contractDraft":
      return `未接入，契约草案 ${plan.task} 待冻结`;
  }
}

/** 平台详情页的统一页签。
 *
 *  顺序与命名逐格抄自 ADMIN-IA 的「统一页签模板」表，也是这次重构的主要产出：
 *  以后每接入一个平台就是把这六格填上，不再各自发明一套导航。
 *
 *  做成常量数组而不是让各平台自己拼：顺序一旦允许被覆盖，「全平台命名一致」
 *  这句话在第二个平台上就不成立了。 */
export const PLATFORM_TABS = [
  { value: "overview", label: "概览" },
  { value: "trends", label: "指标趋势" },
  { value: "resources", label: "渠道/资源" },
  { value: "connection", label: "连接与凭据" },
  { value: "alerts", label: "告警" },
  { value: "operations", label: "操作" },
] as const;

export type PlatformTabValue = (typeof PLATFORM_TABS)[number]["value"];

/** 默认页签：ADMIN-IA 规定概览「永远第 1 格」。 */
export const DEFAULT_PLATFORM_TAB: PlatformTabValue = "overview";

/** 「渠道/资源」页签的值。旧 /channels 的重定向指向它，所以单独具名：
 *  重定向目标写成字面量的话，改页签命名时没人会想起来还有一条旧路径要跟着改。 */
export const RESOURCES_TAB: PlatformTabValue = "resources";

/** `?tab=` 参数 → 页签。认不出来的一律回到概览。
 *
 *  不对未知值报错：URL 是人手改的、书签是旧的，把一个拼错的查询参数升级成
 *  一屏错误页，除了拦住人什么也没做成。 */
export function normalizeTab(raw: string | null | undefined): PlatformTabValue {
  const hit = PLATFORM_TABS.find((tab) => tab.value === raw);
  return hit ? hit.value : DEFAULT_PLATFORM_TAB;
}

/** 指标键的平台前缀（`sub2api.revenue.daily` → `sub2api`）。
 *
 *  总览卡片靠它决定「查看平台 →」指向谁。用前缀而不是写一张
 *  metric_key → 平台的映射表：新平台的指标一上线就自动带上入口，
 *  不需要有人记得回来补表。 */
export function platformOfMetricKey(metricKey: string): string {
  const dot = metricKey.indexOf(".");
  return dot > 0 ? metricKey.slice(0, dot) : "";
}
