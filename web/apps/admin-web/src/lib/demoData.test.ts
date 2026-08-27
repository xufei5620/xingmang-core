import { describe, expect, it } from "vitest";
import {
  DEFAULT_DEMO_SOURCES,
  demoDataConfigFromEnv,
  isDemoSource,
  shouldShowDemoBanner,
  type DemoDataConfig,
} from "./demoData";

function env(over: Record<string, string | undefined> = {}): ImportMetaEnv {
  return over as unknown as ImportMetaEnv;
}

const auto: DemoDataConfig = { mode: "auto", demoSources: DEFAULT_DEMO_SOURCES };

describe("demoDataConfigFromEnv", () => {
  it("不配任何东西时：auto + 默认演示实例列表", () => {
    expect(demoDataConfigFromEnv(env())).toEqual(auto);
  });

  it("VITE_XM_DATA_BADGE 认 demo / real 两个值", () => {
    expect(demoDataConfigFromEnv(env({ VITE_XM_DATA_BADGE: "demo" })).mode).toBe("demo");
    expect(demoDataConfigFromEnv(env({ VITE_XM_DATA_BADGE: " REAL " })).mode).toBe("real");
  });

  it("认不出的值按 auto 处理——拼错一个变量名不该悄悄关掉警告", () => {
    expect(demoDataConfigFromEnv(env({ VITE_XM_DATA_BADGE: "ture" })).mode).toBe("auto");
    expect(demoDataConfigFromEnv(env({ VITE_XM_DATA_BADGE: "" })).mode).toBe("auto");
  });

  it("演示实例列表可配，逗号分隔并归一到小写", () => {
    expect(
      demoDataConfigFromEnv(env({ VITE_XM_DEMO_SOURCES: " Fake-A , fake-b " })).demoSources,
    ).toEqual(["fake-a", "fake-b"]);
  });
});

describe("shouldShowDemoBanner", () => {
  it("source 命中已知演示实例就挂横幅", () => {
    expect(shouldShowDemoBanner(["sub2api-staging"], auto)).toBe(true);
    expect(shouldShowDemoBanner(["sub2api-prod", "sub2api-staging"], auto)).toBe(true);
  });

  it("没命中就不挂——误报会把横幅变成人人无视的噪音", () => {
    expect(shouldShowDemoBanner(["sub2api-prod"], auto)).toBe(false);
    expect(shouldShowDemoBanner([], auto)).toBe(false);
  });

  it("按整串相等比，不做前缀/包含匹配", () => {
    expect(isDemoSource("sub2api-staging-real", DEFAULT_DEMO_SOURCES)).toBe(false);
    expect(isDemoSource("SUB2API-STAGING", DEFAULT_DEMO_SOURCES)).toBe(true);
    expect(isDemoSource("", DEFAULT_DEMO_SOURCES)).toBe(false);
  });

  it("mode=demo 不等指标回来就挂；mode=real 是命中时的逃生口", () => {
    expect(shouldShowDemoBanner([], { ...auto, mode: "demo" })).toBe(true);
    expect(shouldShowDemoBanner(["sub2api-staging"], { ...auto, mode: "real" })).toBe(false);
  });
});
