import { describe, expect, it } from "vitest";
import type { ChannelSummary, Money, Runway } from "../api/finance";
import { formatGrossMargin } from "./channelEconomics";
import {
  aggregateMargin,
  channelTotals,
  countByAccessMethod,
  countNeedTopup,
  summariesForPlatform,
} from "./channelTable";

function money(amountMinor: string, currency = "CNY", scale = 6): Money {
  return { amountMinor, currency, scale };
}

function runway(days: number | null, reason: Runway["reason"] = ""): Runway {
  return {
    days,
    level: days === null ? "" : days <= 5 ? "critical" : "healthy",
    reason: days === null ? reason || "no_balance" : "",
    windowDays: 7,
    coveredDays: 7,
    dailyAverage: null,
    balance: null,
    balanceObservedAt: null,
  };
}

function summary(over: Partial<ChannelSummary> = {}): ChannelSummary {
  return {
    id: "acc-1",
    name: "上游 A",
    systemType: "sub2api",
    accessMethod: "upstream_key",
    metered: true,
    baseUrl: "https://a.example.test",
    platformId: "sub2api",
    credentialRef: "secret://xm/a",
    rechargeRatio: "1.5",
    rechargeCostRate: "0.666667",
    businessDayTz: "+08:00",
    status: "active",
    tokenCount: 1,
    usageRevenue: money("100000000"),
    supplyCost: money("70000000"),
    grossProfit: money("30000000"),
    grossMargin: "0.3",
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
    runway: runway(30),
    ...over,
  };
}

describe("本页只看归属本平台的账号", () => {
  it("未配对的账号照样显示——藏起来它会从两个平台同时消失，而钱一直在花", () => {
    const rows = summariesForPlatform(
      [
        summary({ id: "a", platformId: "sub2api" }),
        summary({ id: "b", platformId: "newapi" }),
        summary({ id: "c", platformId: "" }),
      ],
      "sub2api",
    );
    expect(rows.map((r) => r.id)).toEqual(["a", "c"]);
  });
});

describe("接入方式三分计数", () => {
  it("official_api 单独一格，不并进 Key 也不并进订阅", () => {
    const counts = countByAccessMethod([
      summary({ accessMethod: "upstream_key" }),
      summary({ accessMethod: "upstream_key" }),
      summary({ accessMethod: "subscription_account" }),
      summary({ accessMethod: "official_api" }),
    ]);
    expect(counts).toEqual({ metered: 2, subscription: 1, official: 1, other: 0 });
  });

  it("认不出来的接入方式落到 other，而不是悄悄算进某一类", () => {
    expect(countByAccessMethod([summary({ accessMethod: "future_thing" })]).other).toBe(1);
  });
});

describe("「7 天内需补充」这一格", () => {
  it("天数已知且不超过阈值的才算数", () => {
    const got = countNeedTopup([
      summary({ id: "a", runway: runway(3) }),
      summary({ id: "b", runway: runway(7) }),
      summary({ id: "c", runway: runway(8) }),
    ]);
    expect(got.count).toBe(2);
    expect(got.unknown).toBe(0);
    expect(got.applicable).toBe(3);
  });

  // 这条是本文件最要紧的一条，与 XM-0049 告警规则 R5 同一条判据。
  it("算不出天数的既不算需补充，也不当作健康——它单独计入 unknown", () => {
    const got = countNeedTopup([
      summary({ id: "a", runway: runway(null) }),
      summary({ id: "b", runway: runway(null) }),
      summary({ id: "c", runway: runway(2) }),
    ]);
    expect(got.count).toBe(1);
    // 只显示 count=1 会被读成「另外两个很充裕」，而事实是我们不知道
    expect(got.unknown).toBe(2);
    expect(got.applicable).toBe(3);
  });

  it("订阅型不参与：它没有余额这个概念（§7 末段）", () => {
    const got = countNeedTopup([
      summary({ accessMethod: "subscription_account", metered: false, runway: runway(1) }),
    ]);
    expect(got).toEqual({ count: 0, unknown: 0, applicable: 0 });
  });

  it("判据取后端算好的 metered，不按 access_method 再判一次", () => {
    // access_method 看着像计量型，但后端说 metered=false —— 以后端为准
    const got = countNeedTopup([
      summary({ accessMethod: "upstream_key", metered: false, runway: runway(1) }),
    ]);
    expect(got.applicable).toBe(0);
  });
});

describe("顶部合计", () => {
  it("跳过给不出的金额，并报出覆盖了几行", () => {
    const totals = channelTotals([
      summary({ usageRevenue: money("100000000"), grossProfit: money("30000000") }),
      summary({ usageRevenue: null, grossProfit: null }),
    ]);
    expect(totals.revenue).toEqual({
      kind: "ok",
      total: 100000000n,
      currency: "CNY",
      scale: 6,
    });
    expect(totals.revenueCovered).toBe(1);
    expect(totals.rowCount).toBe(2);
  });

  it("币种不一致不给合计", () => {
    const totals = channelTotals([
      summary({ usageRevenue: money("1", "CNY") }),
      summary({ usageRevenue: money("1", "USD") }),
    ]);
    expect(totals.revenue.kind).toBe("mixed-currency");
  });
});

describe("合计毛利率（前端唯一自己算的比率，门槛比别处高）", () => {
  it("覆盖齐、币种一致时按 Σ毛利 ÷ Σ收入 给出，且与后端同形状", () => {
    const totals = channelTotals([
      summary({ usageRevenue: money("100000000"), grossProfit: money("30000000") }),
      summary({ usageRevenue: money("100000000"), grossProfit: money("10000000") }),
    ]);
    const raw = aggregateMargin(totals);
    expect(raw).toBe("0.200000");
    // 交给与逐行同一个格式化函数，保证显示这一半只有一套规则
    expect(formatGrossMargin(raw)).toBe("20%");
  });

  // 这条是这一组里最要紧的：覆盖不全的比率是编造出来的。
  it("覆盖不全时不给——用 1 行的毛利除以 1 行的收入不代表整页", () => {
    const totals = channelTotals([
      summary({ usageRevenue: money("100000000"), grossProfit: money("30000000") }),
      summary({ usageRevenue: null, grossProfit: null }),
    ]);
    expect(aggregateMargin(totals)).toBeNull();
  });

  it("收入为 0 时不给，而不是 0%（0% 会被读成「做了生意但没赚」）", () => {
    const totals = channelTotals([
      summary({ usageRevenue: money("0"), grossProfit: money("0") }),
    ]);
    expect(aggregateMargin(totals)).toBeNull();
  });

  it("亏损时保留负号", () => {
    const totals = channelTotals([
      summary({ usageRevenue: money("100000000"), grossProfit: money("-25000000") }),
    ]);
    expect(formatGrossMargin(aggregateMargin(totals))).toBe("-25%");
  });

  it("除不尽时向零截断，不四舍五入出一个更好看的数", () => {
    // 1 ÷ 3 = 0.333333…
    const totals = channelTotals([
      summary({ usageRevenue: money("3"), grossProfit: money("1") }),
    ]);
    expect(aggregateMargin(totals)).toBe("0.333333");
  });

  it("没有行时不给", () => {
    expect(aggregateMargin(channelTotals([]))).toBeNull();
  });
});
