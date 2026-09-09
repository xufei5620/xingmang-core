import { NAV_GROUPS, PLATFORM_NAV_ITEMS } from "@xingmang/ui-admin";
import { describe, expect, it } from "vitest";
import { breadcrumbsFor, environmentLabel } from "./breadcrumbs";

const labels = (pathname: string): string[] => breadcrumbsFor(pathname).map((c) => c.label);

describe("breadcrumbsFor", () => {
  it("全局段五页（ADMIN-IA v3 §一 分组 1，逐字）", () => {
    expect(labels("/dashboard")).toEqual(["全局", "运营工作台"]);
    expect(labels("/alerts")).toEqual(["全局", "告警与故障"]);
    expect(labels("/actions")).toEqual(["全局", "操作与审批"]);
    expect(labels("/jobs")).toEqual(["全局", "后台任务"]);
    expect(labels("/audit")).toEqual(["全局", "审计记录"]);
  });

  it("治理段七页（分组 3，逐字）", () => {
    expect(labels("/registry")).toEqual(["平台治理", "资源目录"]);
    expect(labels("/identity")).toEqual(["平台治理", "人员与权限"]);
    expect(labels("/finance")).toEqual(["平台治理", "跨平台财务"]);
    expect(labels("/ops")).toEqual(["平台治理", "运行保障"]);
    expect(labels("/changes")).toEqual(["平台治理", "版本与发布"]);
    expect(labels("/design")).toEqual(["平台治理", "界面规范"]);
    expect(labels("/settings")).toEqual(["平台治理", "设置"]);
  });

  it("扩展能力段四页（分组 4，逐字；注意「AI能力管理」无空格）", () => {
    expect(labels("/ext/app")).toEqual(["扩展能力", "应用与配置"]);
    expect(labels("/ext/integration")).toEqual(["扩展能力", "接口与自动化"]);
    expect(labels("/ext/publishing")).toEqual(["扩展能力", "内容发布"]);
    expect(labels("/ext/ai")).toEqual(["扩展能力", "AI能力管理"]);
  });

  it("平台段的段名是「平台」，不是 v2 的「被管平台」", () => {
    expect(labels("/platforms/sub2api")).toEqual(["平台", "Sub2API"]);
    expect(labels("/platforms/server")).toEqual(["平台", "服务器"]);
  });

  it("每一条导航项都有面包屑，且与侧栏是同一个名字", () => {
    // XM-0042 之前面包屑另抄了一份路由表，于是侧栏改名后面包屑还在说旧名字。
    // 同一个页面在屏幕上有两个名字，坐标就不是坐标了
    for (const group of NAV_GROUPS) {
      for (const item of group.items) {
        expect(labels(item.path)).toEqual([group.title, item.label]);
      }
    }
    for (const platform of PLATFORM_NAV_ITEMS) {
      expect(labels(`/platforms/${platform.serviceType}`)).toEqual(["平台", platform.label]);
    }
  });

  it("目录里没有的平台原样显示——编个好听的名字会盖掉「我们不认识它」", () => {
    expect(labels("/platforms/unknown-thing")).toEqual(["平台", "unknown-thing"]);
  });

  it("被下线的三个不再有平台面包屑，只回显原始键名", () => {
    // 它们的路径都会被 redirect 走，真的落到这里说明 redirect 漏了一条；
    // 这时候显示「平台 / 开票系统」等于承认它还是个平台
    expect(labels("/platforms/invoice")).toEqual(["平台", "invoice"]);
    expect(labels("/platforms/payment")).toEqual(["平台", "payment"]);
  });

  it("查询串与结尾斜杠不影响判定", () => {
    expect(labels("/platforms/sub2api/")).toEqual(["平台", "Sub2API"]);
    expect(labels("/dashboard/")).toEqual(["全局", "运营工作台"]);
  });

  it("认不出的路径返回空：面包屑说错一次就再也不能当坐标用", () => {
    expect(breadcrumbsFor("/")).toEqual([]);
    expect(breadcrumbsFor("")).toEqual([]);
    expect(breadcrumbsFor("/nope")).toEqual([]);
    // /platforms 没有第二段时同样不猜
    expect(breadcrumbsFor("/platforms")).toEqual([]);
    // /ext 也一样：它是分组前缀，不是页面
    expect(breadcrumbsFor("/ext")).toEqual([]);
  });
});

describe("environmentLabel", () => {
  it("显式配置了就照实显示", () => {
    const env = environmentLabel("staging");
    expect(env.label).toBe("环境 staging");
    expect(env.hint).toContain("staging");
  });

  it("没配置说明的是「服务端解析」，不是「不知道」", () => {
    // 前端默认不传 environment 是有意的（api/config），显示成「未知」
    // 会让人以为自己不知道在哪个环境，于是不敢操作
    const env = environmentLabel(undefined);
    expect(env.label).toContain("服务端解析");
    expect(env.hint).toContain("服务端");
  });
});
