import { describe, expect, it } from "vitest";
import type { ServiceItem } from "../api/platform";
import {
  DEFAULT_PLATFORM_TAB,
  findPlatform,
  groupPlatforms,
  normalizeTab,
  platformHasRequests,
  tabsForPlatform,
  pendingBadge,
  pendingHeadline,
  platformOfMetricKey,
  platformOpens,
  PLATFORM_CATALOG,
  PLATFORM_TABS,
  RESOURCES_TAB,
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

describe("平台归组（导航「被管平台」段的数据来源）", () => {
  it("Registry 为空时，目录里的平台全部列出但都是未接入", () => {
    const entries = groupPlatforms([]);
    expect(entries).toHaveLength(PLATFORM_CATALOG.length);
    expect(entries.every((e) => !e.registered)).toBe(true);
    // 未接入不等于不显示：§12 惯例要求显示为未接入
    expect(entries.map((e) => e.spec.label)).toContain("NewAPI");
  });

  it("按 service_type 归组，Registry 里有的标为已接入", () => {
    const entries = groupPlatforms([service("sub2api", "sub2api-dev")]);
    const sub2api = entries.find((e) => e.spec.serviceType === "sub2api");
    expect(sub2api?.registered).toBe(true);
    expect(sub2api?.services).toHaveLength(1);
    const newapi = entries.find((e) => e.spec.serviceType === "newapi");
    expect(newapi?.registered).toBe(false);
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

  it("目录顺序照 ADMIN-IA 平台表，不按登记情况重排", () => {
    const entries = groupPlatforms([service("payment", "pay-1")]);
    expect(entries.slice(0, PLATFORM_CATALOG.length).map((e) => e.spec.serviceType)).toEqual(
      PLATFORM_CATALOG.map((spec) => spec.serviceType),
    );
  });

  it("findPlatform 认识目录内的平台，也认得出彻底不存在的", () => {
    expect(findPlatform("sub2api", [])?.spec.label).toBe("Sub2API");
    expect(findPlatform("不存在的平台", [])).toBeUndefined();
  });
});

describe("未接入状态的文案", () => {
  it("里程碑平台说清楚规划在哪个里程碑", () => {
    expect(pendingBadge({ kind: "milestone", milestone: "M1" })).toBe("未接入·M1");
    expect(pendingHeadline({ kind: "milestone", milestone: "M1" })).toBe("未接入，规划于 M1");
  });

  it("契约草案平台不写成「未接入·Mx」——它的状态不是排期，是等冻结", () => {
    expect(pendingBadge({ kind: "contractDraft", task: "XM-0028" })).toBe("契约草案·XM-0028");
    expect(pendingHeadline({ kind: "contractDraft", task: "XM-0028" })).toContain("XM-0028");
  });

  it("已建平台在本环境没登记时说「未登记」，不说「未接入」", () => {
    // 页面已经有了，只是这个环境的 Registry 里没有实例——和「还没做」不是一回事
    expect(pendingBadge({ kind: "shipped" })).toBe("未登记");
  });

  it("开票系统按 ADMIN-IA 标为契约草案，不是某个里程碑", () => {
    const invoice = PLATFORM_CATALOG.find((p) => p.serviceType === "invoice");
    expect(invoice?.plan.kind).toBe("contractDraft");
  });
});

describe("统一页签模板", () => {
  it("顺序与命名逐格对齐 ADMIN-IA 的页签表（「请求」为 XM-0039 新增）", () => {
    expect(PLATFORM_TABS.map((tab) => tab.label)).toEqual([
      "概览",
      "指标趋势",
      "渠道/资源",
      "请求",
      "连接与凭据",
      "告警",
      "操作",
    ]);
  });

  it("概览永远是第 1 格，也是默认页签", () => {
    expect(PLATFORM_TABS[0]?.value).toBe("overview");
    expect(DEFAULT_PLATFORM_TAB).toBe("overview");
  });

  it("认不出来的 tab 参数回到概览，而不是报错", () => {
    expect(normalizeTab("resources")).toBe("resources");
    expect(normalizeTab("拼错了")).toBe(DEFAULT_PLATFORM_TAB);
    expect(normalizeTab(null)).toBe(DEFAULT_PLATFORM_TAB);
  });

  it("旧 /channels 重定向指向的页签确实在模板里", () => {
    expect(PLATFORM_TABS.some((tab) => tab.value === RESOURCES_TAB)).toBe(true);
  });
});

describe("「请求」页签只对有请求数据的平台显示（XM-0039）", () => {
  it("reqlog 只抄 NewAPI 与 Sub2API", () => {
    // 与后端 requestlog.SupportsPlatform 逐字对应。给别的平台挂一个永远空的
    // 页签，等于告诉运营「这个平台没有请求」，而事实是我们压根没抄它
    expect(platformHasRequests("sub2api")).toBe(true);
    expect(platformHasRequests("newapi")).toBe(true);
    for (const p of ["cpa", "invoice", "payment", "server", "model-assurance", ""]) {
      expect(platformHasRequests(p)).toBe(false);
    }
  });

  it("没有请求数据的平台，页签清单里不出现「请求」", () => {
    expect(tabsForPlatform("sub2api").some((t) => t.value === "requests")).toBe(true);
    expect(tabsForPlatform("cpa").some((t) => t.value === "requests")).toBe(false);
    // 摘掉一格之后其余六格顺序不变——统一模板这句话不能因为一个例外就松掉
    expect(tabsForPlatform("cpa").map((t) => t.value)).toEqual(
      PLATFORM_TABS.filter((t) => t.value !== "requests").map((t) => t.value),
    );
  });

  it("`?tab=requests` 贴到没有请求数据的平台上时回到概览", () => {
    // 选中一个根本没渲染的页签会得到一屏空白；回到概览至少是个说得通的页面
    expect(normalizeTab("requests", "sub2api")).toBe("requests");
    expect(normalizeTab("requests", "cpa")).toBe(DEFAULT_PLATFORM_TAB);
    // 不传 serviceType 时只校验页签名本身（调用方还不知道是哪个平台）
    expect(normalizeTab("requests")).toBe("requests");
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

describe("页面开不开：登记 vs 有内容（XM-0035）", () => {
  it("NewAPI 没登记也照样开：它的内容来自 newapi.* 指标，不依赖注册表", () => {
    const entry = findPlatform("newapi", []);
    expect(entry?.registered).toBe(false);
    // 挂着「未接入」而页面明明有指标可显示，那是看板在说谎。
    // §12 惯例要的是别把没接的说成接了，不是把接了的说成没接。
    expect(entry && platformOpens(entry)).toBe(true);
  });

  it("还没建页的平台不因此一起放行——开关是逐平台声明的，不是全局放宽", () => {
    const cpa = findPlatform("cpa", []);
    expect(cpa && platformOpens(cpa)).toBe(false);
    const invoice = findPlatform("invoice", []);
    expect(invoice && platformOpens(invoice)).toBe(false);
  });

  it("登记了就开，与声明无关", () => {
    const cpa = findPlatform("cpa", [service("cpa", "cpa-1")]);
    expect(cpa && platformOpens(cpa)).toBe(true);
  });

  it("NewAPI 的目录状态是「已建页」，不再是某个里程碑", () => {
    const spec = PLATFORM_CATALOG.find((p) => p.serviceType === "newapi");
    expect(spec?.plan.kind).toBe("shipped");
  });
});
