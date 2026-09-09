import { describe, expect, it } from "vitest";
import type { ChannelSummary } from "../api/finance";
import { formatGrossMargin, isNegativeMargin } from "./channelEconomics";

function summary(over: Partial<ChannelSummary> = {}): ChannelSummary {
  return {
    id: "11111111-1111-4111-8111-111111111111",
    name: "OpenAI 中转·主",
    systemType: "sub2api",
    accessMethod: "upstream_key",
    metered: true,
    baseUrl: "https://relay-a.example.com",
    platformId: "sub2api",
    credentialRef: "",
    rechargeRatio: "1.15",
    rechargeCostRate: "0.869565217",
    businessDayTz: "+08:00",
    status: "active",
    tokenCount: 2,
    usageRevenue: { amountMinor: "100000000", currency: "CNY", scale: 6 },
    supplyCost: { amountMinor: "70000000", currency: "CNY", scale: 6 },
    grossProfit: { amountMinor: "30000000", currency: "CNY", scale: 6 },
    grossMargin: "0.300000",
    coverage: {
      rowCount: 1,
      revenueKnownRows: 1,
      costKnownRows: 1,
      accountGrainRows: 0,
      mixedCurrency: false,
      complete: true,
    },
    observed: {
      costObservedAt: null,
      revenueObservedAt: null,
      updatedAt: null,
      source: "",
    },
    runway: {
      days: null,
      level: "",
      reason: "",
      windowDays: 0,
      coveredDays: 0,
      dailyAverage: null,
      balance: null,
      balanceObservedAt: null,
    },
    ...over,
  };
}

describe("毛利率的展示", () => {
  it("定点字符串右移两位就是百分数，**不经过一次浮点**", () => {
    // 0.1 + 0.2 那类误差在比率上同样存在；这里全程字符串搬运
    expect(formatGrossMargin("0.300000")).toBe("30%");
    expect(formatGrossMargin("0.123456")).toBe("12.3456%");
    expect(formatGrossMargin("1")).toBe("100%");
    expect(formatGrossMargin("0.005")).toBe("0.5%");
  });

  it("负毛利保留符号，并且认得出来要标红", () => {
    expect(formatGrossMargin("-0.2")).toBe("-20%");
    expect(isNegativeMargin("-0.2")).toBe(true);
    expect(isNegativeMargin("0.2")).toBe(false);
    expect(isNegativeMargin(null)).toBe(false);
  });

  it("后端说 null 就返回 null——由调用方显示「—」，**不折成 0%**", () => {
    // 收入 ≤ 0 时后端给 null。0% 会被读成「做了生意但一分没赚」，
    // 而事实是分母是零
    expect(formatGrossMargin(null)).toBeNull();
    expect(formatGrossMargin("")).toBeNull();
    expect(formatGrossMargin("   ")).toBeNull();
  });

  it("认不出的形状返回 null，不硬加一个百分号糊弄过去", () => {
    expect(formatGrossMargin("30%")).toBeNull();
    expect(formatGrossMargin("1e-2")).toBeNull();
    expect(formatGrossMargin("abc")).toBeNull();
  });

  it("**前端不自己算毛利率**：只搬运后端给的那个数", () => {
    // 收入 100 / 毛利 30，后端却说 0.4 —— 前端照显示 40%。
    // 两处各算一遍才是真正的隐患：口径漂开之后页面与台账对不上，
    // 而两边看起来都完全正常
    const s = summary({ grossMargin: "0.400000" });
    expect(formatGrossMargin(s.grossMargin)).toBe("40%");
  });
});
