import { PLATFORM_NAV_ITEMS } from "@xingmang/ui-admin";
import { describe, expect, it } from "vitest";
import type { ServiceItem } from "../api/platform";
import {
  findPlatform,
  groupPlatforms,
  pendingBadge,
  pendingHeadline,
  platformHasRequests,
  platformNavHint,
  platformOfMetricKey,
  platformOpens,
  resolvePlatformTab,
  tabsForPlatform,
  CHANNELS_REDIRECT,
  DEFAULT_PLATFORM_TAB,
  LEGACY_PLATFORM_ROUTES,
  NON_PLATFORM_SERVICE_TYPES,
  PLATFORM_CATALOG,
  USAGE_TAB,
} from "./platforms";

function service(serviceType: string, instanceId: string): ServiceItem {
  return {
    id: `id-${instanceId}`,
    service_type: serviceType,
    instance_id: instanceId,
    environment: "development",
    endpoint: "https://example.com",
    owner: "平台组",
    status: "active",
    source_watermark: "",
    observed_at: null,
    stale_seconds: null,
  };
}

const labels = (serviceType: string): string[] =>
  tabsForPlatform(serviceType).map((tab) => tab.label);
const values = (serviceType: string): string[] =>
  tabsForPlatform(serviceType).map((tab) => tab.value);

describe("平台目录裁剪到 4 个（ADMIN-IA v3 §一 分组 2）", () => {
  it("只剩 Sub2API / NewAPI / CPA / 服务器，顺序与命名逐字照文档", () => {
    expect(PLATFORM_CATALOG.map((p) => p.serviceType)).toEqual([
      "sub2api",
      "newapi",
      "cpa",
      "server",
    ]);
    expect(PLATFORM_CATALOG.map((p) => p.label)).toEqual(["Sub2API", "NewAPI", "CPA", "服务器"]);
  });

  it("开票系统 / 支付 / 模型保障不再是平台条目", () => {
    // 原型把这三个整体降级归位了：开票与支付进各平台的「支付与财务」，
    // 模型保障进各平台的「渠道保障」（裁定 #1）+ 治理段「模型质量保障」
    for (const gone of ["invoice", "payment", "model-assurance"]) {
      expect(PLATFORM_CATALOG.some((p) => p.serviceType === gone)).toBe(false);
    }
  });

  it("目录与导航数据同源：名称与顺序不可能各改各的", () => {
    expect(PLATFORM_CATALOG.map((p) => p.serviceType)).toEqual(
      PLATFORM_NAV_ITEMS.map((p) => p.serviceType),
    );
    expect(PLATFORM_CATALOG.map((p) => p.label)).toEqual(PLATFORM_NAV_ITEMS.map((p) => p.label));
  });
});

describe("平台归组（导航「平台」段的数据来源）", () => {
  it("Registry 为空时，目录里的 4 个平台全部列出但都是未登记", () => {
    const entries = groupPlatforms([]);
    expect(entries).toHaveLength(PLATFORM_CATALOG.length);
    expect(entries.every((e) => !e.registered)).toBe(true);
    // 未接入不等于不显示：§12 惯例要求显示为未接入
    expect(entries.map((e) => e.spec.label)).toContain("服务器");
  });

  it("按 service_type 归组，Registry 里有的标为已登记", () => {
    const entries = groupPlatforms([service("sub2api", "sub2api-dev")]);
    expect(entries.find((e) => e.spec.serviceType === "sub2api")?.registered).toBe(true);
    expect(entries.find((e) => e.spec.serviceType === "newapi")?.registered).toBe(false);
  });

  it("同一平台的多个实例归到同一个条目下，不产生重复导航项", () => {
    const entries = groupPlatforms([
      service("sub2api", "sub2api-a"),
      service("sub2api", "sub2api-b"),
    ]);
    const hits = entries.filter((e) => e.spec.serviceType === "sub2api");
    expect(hits).toHaveLength(1);
    expect(hits[0]?.services.map((s) => s.instance_id)).toEqual(["sub2api-a", "sub2api-b"]);
  });

  it("目录里没有、但已登记的 service_type 照样出现（ADMIN-IA：登记即出现）", () => {
    const entries = groupPlatforms([service("someday-crm", "crm-1")]);
    const extra = entries.find((e) => e.spec.serviceType === "someday-crm");
    expect(extra?.registered).toBe(true);
    // 未知平台不编中文名，直接用键名——编一个好听的名字会骗人
    expect(extra?.spec.label).toBe("someday-crm");
    // 追加在目录之后，不打乱文档给定的顺序
    expect(entries.at(-1)?.spec.serviceType).toBe("someday-crm");
  });
});

describe("排除名单：被降级的三个 service_type 不许从 Registry 爬回平台段", () => {
  it("Registry 里登记了 payment / invoice / model-assurance 也不出现在平台段", () => {
    // 这是裁剪的真正防线。后端的 registry 认得 payment 与 invoice
    // （service.go 的注释里就有），生产环境一旦登记过，「登记即出现」那条规则
    // 会把它们以**原始键名**塞回侧栏，把裁剪整个抵消掉
    const entries = groupPlatforms([
      service("payment", "payment-dev"),
      service("invoice", "invoice-dev"),
      service("model-assurance", "assurance-dev"),
    ]);
    expect(entries).toHaveLength(PLATFORM_CATALOG.length);
    for (const gone of ["payment", "invoice", "model-assurance"]) {
      expect(entries.some((e) => e.spec.serviceType === gone)).toBe(false);
    }
  });

  it("排除名单逐条钉住，不是「碰巧目录里没有」", () => {
    expect([...NON_PLATFORM_SERVICE_TYPES].sort()).toEqual([
      "invoice",
      "model-assurance",
      "payment",
    ]);
  });

  it("排除名单不误伤别的平台：同一批里的未知平台照常出现", () => {
    const entries = groupPlatforms([service("payment", "pay-1"), service("someday-crm", "crm-1")]);
    expect(entries.some((e) => e.spec.serviceType === "someday-crm")).toBe(true);
    expect(entries.some((e) => e.spec.serviceType === "payment")).toBe(false);
  });

  it("findPlatform 也不认这三个——详情页跟着导航一起说「未知平台」", () => {
    expect(findPlatform("payment", [service("payment", "pay-1")])).toBeUndefined();
    expect(findPlatform("sub2api", [])?.spec.label).toBe("Sub2API");
    expect(findPlatform("不存在的平台", [])).toBeUndefined();
  });
});

describe("平台页签集合逐字对齐 ADMIN-IA v3 §2.1", () => {
  it("Sub2API 8 格（2026-09-02 裁定：上游管理并入渠道管理页内区块，不再单独占页签）", () => {
    expect(labels("sub2api")).toEqual([
      "概览",
      "用户管理",
      "渠道管理",
      "支付与财务",
      "请求详情",
      "连接与凭据",
      "告警",
      "渠道保障",
    ]);
    expect(values("sub2api")).not.toContain("suppliers");
  });

  it("NewAPI 与 Sub2API 同构", () => {
    expect(labels("newapi")).toEqual(labels("sub2api"));
    expect(values("newapi")).toEqual(values("sub2api"));
  });

  it("CPA 5 格，「渠道保障」按原型字面排在第 4 格而不是末位", () => {
    expect(labels("cpa")).toEqual(["概览", "用户管理", "渠道管理", "渠道保障", "支付与财务"]);
  });

  it("服务器 7 格：原型把它画成了完整的资产中心，不是六格模板", () => {
    expect(labels("server")).toEqual([
      "概览",
      "服务器资产",
      "供应商与采购",
      "服务与容器",
      "域名与证书",
      "监控与告警",
      "连接与凭据",
    ]);
  });

  it("各平台页签**不同**——v2 的全平台统一模板已经不在了", () => {
    expect(labels("sub2api")).not.toEqual(labels("server"));
    expect(labels("cpa")).not.toEqual(labels("sub2api"));
  });

  it("概览永远是第 1 格，也是默认页签", () => {
    for (const platform of PLATFORM_CATALOG) {
      expect(values(platform.serviceType)[0]).toBe(DEFAULT_PLATFORM_TAB);
    }
  });

  it("裁定 #3 / #4：「指标趋势」与「操作」两格已从所有平台上砍掉", () => {
    for (const platform of PLATFORM_CATALOG) {
      expect(labels(platform.serviceType)).not.toContain("指标趋势");
      expect(labels(platform.serviceType)).not.toContain("操作");
      expect(values(platform.serviceType)).not.toContain("trends");
      expect(values(platform.serviceType)).not.toContain("operations");
    }
  });

  it("「请求详情」只出现在 reqlog 抄录范围内的两个平台上", () => {
    // 挂一个永远空的请求页签，等于告诉运营「这个平台没有请求」，
    // 而事实是请求审计系统压根没抄它
    for (const platform of PLATFORM_CATALOG) {
      expect(values(platform.serviceType).includes(USAGE_TAB)).toBe(
        platformHasRequests(platform.serviceType),
      );
    }
    expect(platformHasRequests("sub2api")).toBe(true);
    expect(platformHasRequests("newapi")).toBe(true);
    for (const p of ["cpa", "server", ""]) expect(platformHasRequests(p)).toBe(false);
  });

  it("目录外的平台没有页签集合——不给它套一份编出来的模板", () => {
    expect(tabsForPlatform("someday-crm")).toEqual([]);
  });
});

describe("子页签逐字对齐 ADMIN-IA v3 §2.2", () => {
  const subs = (serviceType: string, tab: string): string[] =>
    tabsForPlatform(serviceType)
      .find((t) => t.value === tab)
      ?.subTabs.map((s) => s.label) ?? [];

  it("Sub2API 支付与财务 5 格，含「开票」", () => {
    expect(subs("sub2api", "finance")).toEqual([
      "资金概览",
      "充值订单",
      "退款与冲正",
      "利润核算",
      "开票",
    ]);
  });

  it("NewAPI 支付与财务现在 3 格，含「开票」（CR-0005 推翻裁定 #2）", () => {
    expect(subs("newapi", "finance")).toEqual(["资金与订单", "利润核算", "开票"]);
  });

  it("渠道保障 3 格，Sub2API / NewAPI / CPA 共用", () => {
    const expected = ["保障概览", "检测任务", "历史记录"];
    expect(subs("sub2api", "model")).toEqual(expected);
    expect(subs("newapi", "model")).toEqual(expected);
    expect(subs("cpa", "model")).toEqual(expected);
  });

  it("没有子页签的格子就是空的——原型用筛选条与周期控件组织它们", () => {
    expect(subs("sub2api", "overview")).toEqual([]);
    expect(subs("server", "assets")).toEqual([]);
  });
});

describe("?tab= 解析：认识 / 改名 / 挪走 / 认不出来", () => {
  it("认识而且这个平台有这一格 → 直接渲染", () => {
    expect(resolvePlatformTab("sub2api", "upstream")).toEqual({ kind: "ok", tab: "upstream" });
    expect(resolvePlatformTab("server", "domains")).toEqual({ kind: "ok", tab: "domains" });
  });

  it("没传 tab 时落在概览", () => {
    expect(resolvePlatformTab("sub2api", null)).toEqual({ kind: "ok", tab: "overview" });
    expect(resolvePlatformTab("sub2api", "")).toEqual({ kind: "ok", tab: "overview" });
  });

  it("v2 的旧页签名改跳到新名字（ADMIN-IA v3 §4.1）", () => {
    expect(resolvePlatformTab("sub2api", "resources")).toEqual({ kind: "redirect", tab: "upstream" });
    expect(resolvePlatformTab("sub2api", "requests")).toEqual({ kind: "redirect", tab: "usage" });
    expect(resolvePlatformTab("sub2api", "connection")).toEqual({ kind: "redirect", tab: "creds" });
    // 裁定 #3：趋势并进各页卡片，概览那一格就是它的落点
    expect(resolvePlatformTab("sub2api", "trends")).toEqual({ kind: "redirect", tab: "overview" });
  });

  it("2026-09-02 裁定：?tab=suppliers 改跳 ?tab=upstream", () => {
    expect(resolvePlatformTab("sub2api", "suppliers")).toEqual({ kind: "redirect", tab: "upstream" });
    expect(resolvePlatformTab("newapi", "suppliers")).toEqual({ kind: "redirect", tab: "upstream" });
    // 07:20 补充裁定把登记簿字段直接并入了渠道表的行与详情页，已经没有
    // 独立区块可滚，因此不再需要一张"改跳之后额外带锚点"的表
    // 服务器的 suppliers 是现役页签（供应商与采购），不是这条别名，
    // 不该被这条 alias 误伤
    expect(resolvePlatformTab("server", "suppliers")).toEqual({ kind: "ok", tab: "suppliers" });
  });

  it("原型自带的三条服务器别名（ADMIN-IA §三）", () => {
    expect(resolvePlatformTab("server", "containers")).toEqual({
      kind: "redirect",
      tab: "services",
    });
    expect(resolvePlatformTab("server", "certs")).toEqual({ kind: "redirect", tab: "domains" });
    expect(resolvePlatformTab("server", "logs")).toEqual({ kind: "redirect", tab: "monitoring" });
  });

  it("裁定 #4：平台级「操作」整格挪到全局「操作与审批」", () => {
    expect(resolvePlatformTab("sub2api", "operations")).toEqual({ kind: "moved", path: "/actions" });
    expect(resolvePlatformTab("server", "operations")).toEqual({ kind: "moved", path: "/actions" });
  });

  it("认得出名字、但这个平台没有那一格 → Not Found，不硬跳", () => {
    // `?tab=logs` 贴到 Sub2API 上：别名指向的 monitoring 在 Sub2API 根本不存在
    expect(resolvePlatformTab("sub2api", "logs")).toEqual({ kind: "notFound", tab: "logs" });
    // 服务器没有「渠道管理」，所以 resources 这条别名在它身上也不成立
    expect(resolvePlatformTab("server", "resources")).toEqual({
      kind: "notFound",
      tab: "resources",
    });
  });

  it("认不出来的 tab 给 Not Found，**不再静默回落概览**（交接文档 §8）", () => {
    // 旧行为是回落 overview：贴一个拼错的地址过去，屏幕上会显示概览，
    // 而人以为自己看的是刚贴进去的那一格
    expect(resolvePlatformTab("sub2api", "拼错了")).toEqual({ kind: "notFound", tab: "拼错了" });
    expect(resolvePlatformTab("sub2api", "model-assurance")).toEqual({
      kind: "notFound",
      tab: "model-assurance",
    });
  });

  it("目录外的平台不校验页签——那是「还没给它定页签」，不是地址写错了", () => {
    expect(resolvePlatformTab("someday-crm", "whatever")).toEqual({ kind: "ok", tab: "whatever" });
  });
});

describe("旧路径 redirect 的目标必须真的存在（ADMIN-IA v3 §4.1）", () => {
  const targets = [...Object.values(LEGACY_PLATFORM_ROUTES), CHANNELS_REDIRECT];

  it("每个平台内的 redirect 目标，其 ?tab= 与 ?sub= 都在导航数据里", () => {
    // 这条测试挡的是「改页签命名时忘了同步 redirect」：目标指到一个不存在的
    // 页签，旧书签就从「换了个地方」变成 404，而没有人会在改名时想起来查
    for (const target of targets) {
      const [path, search] = target.split("?");
      if (!path?.startsWith("/platforms/")) continue;
      const serviceType = path.slice("/platforms/".length);
      const params = new URLSearchParams(search);
      const tab = params.get("tab");
      expect(resolvePlatformTab(serviceType, tab)).toEqual({ kind: "ok", tab });

      const sub = params.get("sub");
      if (sub === null) continue;
      const spec = tabsForPlatform(serviceType).find((t) => t.value === tab);
      expect(spec?.subTabs.map((s) => s.id)).toContain(sub);
    }
  });

  it("三个被下线的平台各有去处，一条都不许漏", () => {
    expect(Object.keys(LEGACY_PLATFORM_ROUTES).sort()).toEqual([
      "invoice",
      "model-assurance",
      "payment",
    ]);
    // 排除名单与 redirect 表说的是同一批平台：删了一个却忘了给它 redirect，
    // 旧书签会直接掉进 404
    expect(Object.keys(LEGACY_PLATFORM_ROUTES).sort()).toEqual(
      [...NON_PLATFORM_SERVICE_TYPES].sort(),
    );
  });

  it("/channels 指向「上游管理」，不再是已经不存在的「渠道/资源」", () => {
    expect(CHANNELS_REDIRECT).toBe("/platforms/sub2api?tab=upstream");
  });
});

describe("未接入状态的文案", () => {
  it("里程碑平台说清楚规划在哪个里程碑", () => {
    expect(pendingBadge({ kind: "milestone", milestone: "M1" })).toBe("未接入·M1");
    expect(pendingHeadline({ kind: "milestone", milestone: "M1" })).toBe("未接入，规划于 M1");
  });

  it("已建平台在本环境没登记时说「未登记」，不说「未接入」", () => {
    // 页面已经有了，只是这个环境的 Registry 里没有实例——和「还没做」不是一回事
    expect(pendingBadge({ kind: "shipped" })).toBe("未登记");
  });
});

describe("点进去有没有真数据：登记 vs 指标", () => {
  it("NewAPI 没登记也有真数据：它的内容来自 newapi.* 指标，不依赖注册表", () => {
    const entry = findPlatform("newapi", []);
    expect(entry?.registered).toBe(false);
    // 挂着「未登记」而页面明明有指标可显示，那是看板在说谎
    expect(entry && platformOpens(entry)).toBe(true);
    expect(entry && platformNavHint(entry)).toBeUndefined();
  });

  it("还没建的平台不因此一起放行——开关是逐平台声明的，不是全局放宽", () => {
    const cpa = findPlatform("cpa", []);
    expect(cpa && platformOpens(cpa)).toBe(false);
    expect(cpa && platformNavHint(cpa)).toBe("未接入·M4");
    const server = findPlatform("server", []);
    expect(server && platformNavHint(server)).toBe("未接入·M2");
  });

  it("登记了就有真数据，与声明无关", () => {
    const cpa = findPlatform("cpa", [service("cpa", "cpa-1")]);
    expect(cpa && platformOpens(cpa)).toBe(true);
    expect(cpa && platformNavHint(cpa)).toBeUndefined();
  });

  it("Sub2API 在本环境没登记时挂「未登记」而不是某个里程碑", () => {
    const entry = findPlatform("sub2api", []);
    expect(entry && platformNavHint(entry)).toBe("未登记");
  });
});

describe("指标键 → 平台", () => {
  it("取第一个点之前的前缀", () => {
    expect(platformOfMetricKey("sub2api.revenue.daily")).toBe("sub2api");
  });

  it("没有点的键不硬凑一个平台出来", () => {
    expect(platformOfMetricKey("weird_metric")).toBe("");
    expect(platformOfMetricKey(".leading")).toBe("");
  });
});
