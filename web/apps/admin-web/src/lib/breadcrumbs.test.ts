import { describe, expect, it } from "vitest";
import { breadcrumbsFor, environmentLabel } from "./breadcrumbs";

const labels = (pathname: string): string[] => breadcrumbsFor(pathname).map((c) => c.label);

describe("breadcrumbsFor", () => {
  it("全局段的三页都落在「全局」下", () => {
    expect(labels("/dashboard")).toEqual(["全局", "运营总览"]);
    expect(labels("/alerts")).toEqual(["全局", "告警中心"]);
    expect(labels("/audit")).toEqual(["全局", "审计事件"]);
  });

  it("治理段的两页都落在「平台治理」下", () => {
    expect(labels("/registry")).toEqual(["平台治理", "注册表"]);
    expect(labels("/settings")).toEqual(["平台治理", "设置"]);
  });

  it("平台详情用目录里的展示名，不是 URL 里的 service_type", () => {
    expect(labels("/platforms/sub2api")).toEqual(["被管平台", "Sub2API"]);
  });

  it("目录里没有的平台原样显示——编个好听的名字会盖掉「我们不认识它」", () => {
    expect(labels("/platforms/unknown-thing")).toEqual(["被管平台", "unknown-thing"]);
  });

  it("查询串与结尾斜杠不影响判定", () => {
    expect(labels("/platforms/sub2api/")).toEqual(["被管平台", "Sub2API"]);
    expect(labels("/dashboard/")).toEqual(["全局", "运营总览"]);
  });

  it("认不出的路径返回空：面包屑说错一次就再也不能当坐标用", () => {
    expect(breadcrumbsFor("/")).toEqual([]);
    expect(breadcrumbsFor("")).toEqual([]);
    expect(breadcrumbsFor("/nope")).toEqual([]);
    // /platforms 没有第二段时同样不猜
    expect(breadcrumbsFor("/platforms")).toEqual([]);
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
