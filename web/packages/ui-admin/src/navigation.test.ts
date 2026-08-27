import { describe, expect, it } from "vitest";
import {
  allNavItems,
  navItemByPath,
  navLabel,
  navStageHint,
  placeholderNavItems,
  platformNavSpec,
  NAV_GROUPS,
  PLATFORM_GROUP_TITLE,
  PLATFORM_NAV_ITEMS,
} from "./navigation";

const group = (id: string) => NAV_GROUPS.find((g) => g.id === id);
const groupLabels = (id: string) => group(id)?.items.map((item) => item.label) ?? [];
const subLabels = (path: string) =>
  navItemByPath(path)?.item.subTabs.map((sub) => sub.label) ?? [];

describe("侧栏四分组（ADMIN-IA v3 §一，逐字）", () => {
  it("分组标题与顺序：全局 / 平台 / 平台治理 / 扩展能力", () => {
    // 分组 2 的标题在原型里写死的就是「平台」，不是 v2 的「被管平台」，
    // 也不是交接文档 §7.2 的「被管平台」
    expect(NAV_GROUPS.map((g) => g.title)).toEqual(["全局", "平台", "平台治理", "扩展能力"]);
    expect(PLATFORM_GROUP_TITLE).toBe("平台");
  });

  it("只有「扩展能力」是可折叠的，且带阶段标签「后置」", () => {
    expect(NAV_GROUPS.filter((g) => g.collapsible).map((g) => g.id)).toEqual(["ext"]);
    expect(group("ext")?.stage).toBe("后置");
  });

  it("全局段 5 条（分组 1）", () => {
    expect(groupLabels("global")).toEqual([
      "运营工作台",
      "告警与故障",
      "操作与审批",
      "后台任务",
      "审计记录",
    ]);
  });

  it("平台治理段 7 条（分组 3）", () => {
    expect(groupLabels("governance")).toEqual([
      "资源目录",
      "人员与权限",
      "跨平台财务",
      "运行保障",
      "版本与发布",
      "界面规范",
      "设置",
    ]);
  });

  it("扩展能力段 4 条（分组 4）——「AI能力管理」没有空格", () => {
    // 交接文档 §7.4 把这一组写作「后置能力」、条目写作「集成与自动化」「AI 控制平面」，
    // 以原型为准（ADMIN-IA §一 分组 4 下的注）
    expect(groupLabels("ext")).toEqual(["应用与配置", "接口与自动化", "内容发布", "AI能力管理"]);
  });

  it("平台段的条目由 Registry 驱动，静态数据里是空的", () => {
    expect(group("platforms")?.items).toEqual([]);
  });

  it("路径互不重复：路由表由这份数据生成，重了会有一条永远进不去", () => {
    const paths = allNavItems().map((item) => item.path);
    expect(new Set(paths).size).toBe(paths.length);
  });

  it("每条路径都以 / 开头，且不以 / 结尾", () => {
    for (const item of allNavItems()) {
      expect(item.path.startsWith("/")).toBe(true);
      expect(item.path.endsWith("/")).toBe(false);
    }
  });
});

describe("页内子页签（ADMIN-IA v3 §2.2，逐字）", () => {
  it("全局段四页的子页签", () => {
    expect(subLabels("/alerts")).toEqual(["告警", "故障事件", "规则", "通知", "暂停告警"]);
    expect(subLabels("/actions")).toEqual([
      "操作目录",
      "待审批",
      "执行记录",
      "风险与启用条件",
    ]);
    expect(subLabels("/jobs")).toEqual([
      "运行中",
      "定时任务",
      "同步批次",
      "失败与重试",
      "多次失败任务",
    ]);
    expect(subLabels("/audit")).toEqual(["审计记录", "操作证据", "审计链验证"]);
  });

  it("治理段的子页签，含「开票集成」与「模型质量保障」两格", () => {
    expect(subLabels("/registry")).toEqual([
      "服务",
      "连接器",
      "连接",
      "支持能力",
      "应用与模块",
      "环境",
    ]);
    expect(subLabels("/identity")).toEqual([
      "账号与身份",
      "权限规则",
      "权限范围",
      "密钥引用",
      "会话",
    ]);
    expect(subLabels("/finance")).toEqual([
      "财务总览",
      "支付通道",
      "财务对账",
      "异常与冻结",
      "开票集成",
      "财务配置",
    ]);
    expect(subLabels("/ops")).toEqual([
      "控制平面健康",
      "稳定性与外部监控",
      "备份与恢复",
      "故障处理手册（Runbook）",
      "迁移与数据对比",
      "模型质量保障",
    ]);
    expect(subLabels("/changes")).toEqual([
      "变更单",
      "发布与回滚",
      "自动测试与质量",
      "发布包与安全检查",
      "数据库变更",
    ]);
    expect(subLabels("/design")).toEqual([
      "颜色与排版",
      "按钮与表单",
      "卡片与状态",
      "表格与详情",
      "页面状态",
      "复杂组件",
    ]);
  });

  it("扩展能力四页的子页签", () => {
    expect(subLabels("/ext/app")).toEqual(["应用目录", "页面配置", "页面组件", "版本与发布"]);
    expect(subLabels("/ext/integration")).toEqual([
      "API调用方",
      "Webhook",
      "自动化流程",
      "运行记录",
    ]);
    expect(subLabels("/ext/publishing")).toEqual([
      "内容日历",
      "草稿与素材",
      "审批队列",
      "渠道与账号",
      "发布记录",
    ]);
    expect(subLabels("/ext/ai")).toEqual(["模型线路", "AI角色", "AI工具", "运行与预算"]);
  });

  it("运营工作台与设置没有子页签——原型用筛选条与周期控件组织它们", () => {
    expect(subLabels("/dashboard")).toEqual([]);
    expect(subLabels("/settings")).toEqual([]);
  });

  it("子页签 id 在同一页内不重复：`?sub=` 认不出唯一一格就没法分享", () => {
    for (const item of allNavItems()) {
      const ids = item.subTabs.map((sub) => sub.id);
      expect(new Set(ids).size).toBe(ids.length);
    }
  });
});

describe("平台页签（ADMIN-IA v3 §2.1，逐字）", () => {
  it("4 个平台，名称与顺序照文档", () => {
    expect(PLATFORM_NAV_ITEMS.map((p) => p.label)).toEqual([
      "Sub2API",
      "NewAPI",
      "CPA",
      "服务器",
    ]);
    expect(PLATFORM_NAV_ITEMS.map((p) => p.prototypeId)).toEqual([
      "s2",
      "newapi",
      "cpa",
      "server",
    ]);
  });

  it("Sub2API / NewAPI 各 9 格，「渠道保障」在末位（裁定 #1 的落点）", () => {
    for (const serviceType of ["sub2api", "newapi"]) {
      const tabs = platformNavSpec(serviceType)?.tabs ?? [];
      expect(tabs).toHaveLength(9);
      expect(tabs.at(-1)?.value).toBe("model");
      expect(tabs.at(-1)?.label).toBe("渠道保障");
      expect(tabs.at(-1)?.stage).toBe("M1.5");
    }
  });

  it("CPA 的「渠道保障」按原型字面排在第 4 格，不跟着挪到末位", () => {
    const tabs = platformNavSpec("cpa")?.tabs ?? [];
    expect(tabs[3]?.value).toBe("model");
    expect(tabs[3]?.stage).toBe("M1.5");
  });

  it("每个平台的页签 value 不重复", () => {
    for (const platform of PLATFORM_NAV_ITEMS) {
      const values = platform.tabs.map((tab) => tab.value);
      expect(new Set(values).size).toBe(values.length);
    }
  });

  it("认不出的 serviceType 返回 undefined，不编一套页签出来", () => {
    expect(platformNavSpec("someday-crm")).toBeUndefined();
  });
});

describe("查表与状态标签", () => {
  it("navItemByPath 认路径，结尾斜杠不影响", () => {
    expect(navItemByPath("/registry")?.item.label).toBe("资源目录");
    expect(navItemByPath("/registry/")?.item.label).toBe("资源目录");
    expect(navItemByPath("/registry")?.group.title).toBe("平台治理");
    expect(navItemByPath("/nope")).toBeUndefined();
  });

  it("navLabel 找不到就抛，不兜底成别的名字", () => {
    // 兜底显示一个别的名字，等于把「导航数据被改坏了」藏到线上让运营去发现
    expect(navLabel("/dashboard")).toBe("运营工作台");
    expect(() => navLabel("/nope")).toThrow(/ADMIN-IA/);
  });

  it("已实装的页不挂阶段标签，未实装的挂「未建·<阶段>」", () => {
    const dashboard = navItemByPath("/dashboard")?.item;
    const actions = navItemByPath("/actions")?.item;
    expect(dashboard && navStageHint(dashboard)).toBeUndefined();
    expect(actions && navStageHint(actions)).toBe("未建·F-B");
  });

  it("placeholderNavItems 就是全部 built=false 的条目", () => {
    const paths = placeholderNavItems().map((item) => item.path);
    // 全局段 2 + 治理段 5（资源目录与设置已实装）+ 扩展能力 4
    expect(paths).toEqual([
      "/actions",
      "/jobs",
      "/identity",
      "/finance",
      "/ops",
      "/changes",
      "/design",
      "/ext/app",
      "/ext/integration",
      "/ext/publishing",
      "/ext/ai",
    ]);
    expect(placeholderNavItems().every((item) => navStageHint(item) !== undefined)).toBe(true);
  });

  it("已实装的四页正是现在真有内容的那四页", () => {
    expect(allNavItems().filter((item) => item.built).map((item) => item.path)).toEqual([
      "/dashboard",
      "/alerts",
      "/audit",
      "/registry",
      "/settings",
    ]);
  });
});
