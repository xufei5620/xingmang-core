import { describe, expect, it } from "vitest";
import {
  describePeriod,
  granularityLabel,
  GRANULARITY_OPTIONS,
} from "@xingmang/ui-admin";
import {
  describeCoverage,
  parseBusinessDay,
  parseGranularity,
} from "./period";

describe("granularityLabel", () => {
  it("三个粒度各有自己的说法", () => {
    expect(granularityLabel("day")).toBe("按日查看");
    expect(granularityLabel("week")).toBe("按周查看");
    expect(granularityLabel("month")).toBe("按月查看");
  });

  it("三个按钮的顺序与原型一致", () => {
    expect(GRANULARITY_OPTIONS.map((o) => o.label)).toEqual(["日", "周", "月"]);
  });
});

describe("describePeriod", () => {
  it("日粒度与原型逐字一致", () => {
    expect(
      describePeriod({ day: "2026-08-27", granularity: "day", from: "2026-08-27", to: "2026-08-27" }),
    ).toBe("2026-08-27 · 按日查看");
  });

  it("周与月说出到底是哪几天", () => {
    // 只写「按周查看」而不说是哪七天，在跨月那一周会让人读错
    expect(
      describePeriod({ day: "2026-08-27", granularity: "week", from: "2026-08-24", to: "2026-08-30" }),
    ).toBe("2026-08-24 ~ 2026-08-30 · 按周查看");
    expect(
      describePeriod({ day: "2026-08-27", granularity: "month", from: "2026-08-01", to: "2026-08-31" }),
    ).toBe("2026-08-01 ~ 2026-08-31 · 按月查看");
  });

  it("还没拿到区间时显示「—」而不是编一个", () => {
    expect(describePeriod(undefined)).toBe("—");
    expect(describePeriod({ day: "", granularity: "day", from: "", to: "" })).toBe("—");
  });
});

describe("parseGranularity", () => {
  it("认得三个粒度", () => {
    expect(parseGranularity("day")).toBe("day");
    expect(parseGranularity("week")).toBe("week");
    expect(parseGranularity("month")).toBe("month");
  });

  it("认不出的回落到日（URL 是人能手改的）", () => {
    expect(parseGranularity("weekly")).toBe("day");
    expect(parseGranularity("")).toBe("day");
    expect(parseGranularity(null)).toBe("day");
  });
});

describe("parseBusinessDay", () => {
  it("接受严格的 YYYY-MM-DD", () => {
    expect(parseBusinessDay("2026-08-27")).toBe("2026-08-27");
    expect(parseBusinessDay(" 2026-08-27 ")).toBe("2026-08-27");
  });

  it("少位写法不接受", () => {
    // 一个宽容的解析会让「2026-8-1」与「2026-08-01」查出两份不同的区间数
    expect(parseBusinessDay("2026-8-1")).toBe("");
  });

  it("不存在的日期不接受", () => {
    // new Date("2026-02-30") 会悄悄变成 3 月 2 日，于是人选的日期
    // 和查出来的数对不上，而两者都是合理的数字
    expect(parseBusinessDay("2026-02-30")).toBe("");
    expect(parseBusinessDay("2026-13-01")).toBe("");
    expect(parseBusinessDay("2026-00-10")).toBe("");
  });

  it("闰年当天是合法的", () => {
    expect(parseBusinessDay("2028-02-29")).toBe("2028-02-29");
    expect(parseBusinessDay("2026-02-29")).toBe("");
  });

  it("非日期一律回落到空串（= 用服务端的今天）", () => {
    expect(parseBusinessDay(null)).toBe("");
    expect(parseBusinessDay("今天")).toBe("");
    expect(parseBusinessDay("2026-08-27T10:00:00Z")).toBe("");
  });
});

describe("describeCoverage", () => {
  it("覆盖全时不啰嗦", () => {
    expect(describeCoverage(8, 8, true)).toBe("覆盖全部 8 位用户");
  });

  it("覆盖不全时说明这个数是下界", () => {
    // 这是本页最要紧的一句话：一个只覆盖了 5/8 的合计，
    // 和一个真的合计长得一模一样（宪法 12 条）
    expect(describeCoverage(5, 8, false)).toBe("只覆盖 5/8 位用户，实际金额不低于这个数");
  });

  it("一个用户都没有时不说覆盖率", () => {
    expect(describeCoverage(0, 0, false)).toBe("本区间没有符合条件的用户");
  });
});
