import { describe, expect, it } from "vitest";
import type {
  ChannelSummary,
  Money,
  RunwayCoverage,
  UpstreamSummary,
} from "../api/finance";
import {
  costCollectionNote,
  costCollectionState,
  crossPlatformBucketTotal,
  crossPlatformTotalNote,
  runwayAttentionEmptyReason,
  runwayAttentionItems,
  type PlatformBucketFact,
} from "./financeGlobal";

const DAY = "2026-09-07";

function fact(overrides: Partial<PlatformBucketFact> = {}): PlatformBucketFact {
  return {
    label: "Sub2API",
    available: true,
    day: DAY,
    currency: "CNY",
    amountMinor: 100n,
    isPartial: false,
    ...overrides,
  };
}

function money(amountMinor: string, currency = "CNY", scale = 6): Money {
  return { amountMinor, currency, scale };
}

function coverage(overrides: Partial<ChannelSummary["coverage"]> = {}): ChannelSummary["coverage"] {
  return {
    rowCount: 2,
    revenueKnownRows: 2,
    costKnownRows: 2,
    accountGrainRows: 0,
    mixedCurrency: false,
    complete: true,
    ...overrides,
  };
}

function channel(overrides: Partial<ChannelSummary> = {}): ChannelSummary {
  return {
    id: "ch-1",
    name: "渠道一",
    systemType: "sub2api",
    accessMethod: "api_key",
    metered: true,
    baseUrl: "",
    platformId: "",
    credentialRef: "",
    rechargeRatio: "",
    rechargeCostRate: "",
    businessDayTz: "Asia/Shanghai",
    status: "enabled",
    tokenCount: 1,
    usageRevenue: money("1000000"),
    supplyCost: money("400000"),
    grossProfit: money("600000"),
    grossMargin: "0.600000",
    coverage: coverage(),
    observed: {
      costObservedAt: null,
      revenueObservedAt: null,
      updatedAt: "2026-09-07T02:00:00Z",
      source: "finance.profit_daily",
    },
    runway: {
      days: null,
      level: "",
      reason: "",
      windowDays: 7,
      coveredDays: 0,
      dailyAverage: null,
      balance: null,
      balanceObservedAt: null,
    },
    ...overrides,
  };
}

function upstream(overrides: Partial<UpstreamSummary> = {}): UpstreamSummary {
  const base = channel();
  return {
    id: base.id,
    name: base.name,
    supplierKey: "supplier-1",
    systemType: base.systemType,
    accessMethod: base.accessMethod,
    baseUrl: base.baseUrl,
    rechargeCostRate: base.rechargeCostRate,
    credentialRef: base.credentialRef,
    status: base.status,
    tokenCount: base.tokenCount,
    usageRevenue: base.usageRevenue,
    supplyCost: base.supplyCost,
    grossProfit: base.grossProfit,
    coverage: base.coverage,
    observed: base.observed,
    runway: base.runway,
    ...overrides,
  };
}

function runwayOf(days: number | null, level: UpstreamSummary["runway"]["level"]) {
  return {
    days,
    level,
    reason: days === null ? ("no_balance" as const) : ("" as const),
    windowDays: 7,
    coveredDays: 7,
    dailyAverage: money("100000"),
    balance: money("500000"),
    balanceObservedAt: "2026-09-07T01:00:00Z",
  };
}

describe("crossPlatformBucketTotal 跨平台分桶合计", () => {
  it("两个平台同币种时相加，并说清这个数由谁组成", () => {
    const total = crossPlatformBucketTotal(
      [fact({ label: "Sub2API", amountMinor: 12_345n }), fact({ label: "NewAPI", amountMinor: 500n })],
      DAY,
    );
    expect(total).toEqual({
      kind: "total",
      amountMinor: 12_845n,
      currency: "CNY",
      contributors: ["Sub2API", "NewAPI"],
      notApplicable: [],
      partial: false,
    });
    expect(crossPlatformTotalNote(total, DAY)).toBe(`Sub2API + NewAPI 合计 · 业务日 ${DAY}`);
  });

  it("币种不一致时 fail closed：不相加，说「币种不一致，合计给不出」并点名两边的币种", () => {
    const total = crossPlatformBucketTotal(
      [
        fact({ label: "Sub2API", currency: "CNY", amountMinor: 100n }),
        fact({ label: "NewAPI", currency: "USD", amountMinor: 100n }),
      ],
      DAY,
    );
    expect(total.kind).toBe("mixed-currency");
    const note = crossPlatformTotalNote(total, DAY);
    expect(note).toContain("币种不一致，合计给不出");
    expect(note).toContain("Sub2API CNY");
    expect(note).toContain("NewAPI USD");
  });

  it("任一平台今天没有可用汇总时整个合计给不出，而不是只加另一半", () => {
    const total = crossPlatformBucketTotal(
      [fact({ label: "Sub2API", amountMinor: 999n }), fact({ label: "NewAPI", available: false })],
      DAY,
    );
    expect(total.kind).toBe("unavailable");
    expect(crossPlatformTotalNote(total, DAY)).toContain("NewAPI");
    // 关键：不能出现一个「只算了 Sub2API」的合计
    expect(total).not.toHaveProperty("amountMinor");
  });

  it("指标业务日与所选业务日不一致的平台同样让合计给不出（指标是单日粒度）", () => {
    const total = crossPlatformBucketTotal(
      [fact({ label: "Sub2API" }), fact({ label: "NewAPI", day: "2026-09-06" })],
      DAY,
    );
    expect(total.kind).toBe("unavailable");
    expect(crossPlatformTotalNote(total, DAY)).toContain("NewAPI");
  });

  it("桶键缺席且覆盖完整＝契约里确认过的零，按 0 计入而不是让合计消失", () => {
    const total = crossPlatformBucketTotal(
      [
        fact({ label: "Sub2API", amountMinor: 700n }),
        fact({ label: "NewAPI", amountMinor: null, isPartial: false }),
      ],
      DAY,
    );
    expect(total).toMatchObject({ kind: "total", amountMinor: 700n, partial: false });
  });

  it("桶键缺席但覆盖不全时说不清是不是零，合计给不出", () => {
    const total = crossPlatformBucketTotal(
      [
        fact({ label: "Sub2API", amountMinor: 700n }),
        fact({ label: "NewAPI", amountMinor: null, isPartial: true }),
      ],
      DAY,
    );
    expect(total.kind).toBe("unavailable");
    expect(crossPlatformTotalNote(total, DAY)).toContain("覆盖不全");
  });

  it("有平台覆盖不全但金额在场时给出合计，并在说明里标注这个合计偏低", () => {
    const total = crossPlatformBucketTotal(
      [fact({ label: "Sub2API", amountMinor: 700n, isPartial: true }), fact({ label: "NewAPI", amountMinor: 300n })],
      DAY,
    );
    expect(total).toMatchObject({ kind: "total", amountMinor: 1000n, partial: true });
    expect(crossPlatformTotalNote(total, DAY)).toContain("这个合计偏低");
  });

  it("「不适用」的平台不阻塞合计，也不被当成缺口，而是单独点一句", () => {
    const total = crossPlatformBucketTotal(
      [
        fact({ label: "Sub2API", amountMinor: 250n }),
        // NewAPI 没有退款概念：不是「还没做」，所以不能让退款合计因它给不出
        fact({ label: "NewAPI", notApplicable: true, available: false, day: null, currency: "" }),
      ],
      DAY,
    );
    expect(total).toMatchObject({
      kind: "total",
      amountMinor: 250n,
      contributors: ["Sub2API"],
      notApplicable: ["NewAPI"],
    });
    expect(crossPlatformTotalNote(total, DAY)).toContain("NewAPI 不适用");
  });

  it("桶键在场但金额形状不对时不按 0 兜底，合计给不出", () => {
    const total = crossPlatformBucketTotal(
      [
        fact({ label: "Sub2API", amountMinor: 700n }),
        fact({ label: "NewAPI", amountMinor: null, invalidAmount: true }),
      ],
      DAY,
    );
    expect(total.kind).toBe("unavailable");
    expect(crossPlatformTotalNote(total, DAY)).toContain("不是合法的整数最小单位");
  });

  it("平台没声明合约币种时不做没有单位的相加", () => {
    const total = crossPlatformBucketTotal(
      [fact({ label: "Sub2API", currency: "" }), fact({ label: "NewAPI" })],
      DAY,
    );
    expect(total.kind).toBe("unavailable");
    expect(crossPlatformTotalNote(total, DAY)).toContain("没有声明合约币种");
  });
});

describe("costCollectionState 区分「今天毛利 0」与「成本还没采到」", () => {
  it("一条渠道都没有：说链路还没接", () => {
    const state = costCollectionState([]);
    expect(state).toEqual({ kind: "no-channels" });
    expect(costCollectionNote(state)).toContain("不是「今天成本为 0」");
  });

  it("有渠道有台账行但成本侧 0 行有数：说成本还没采到，并点名那个开关", () => {
    const state = costCollectionState([
      channel({ id: "a", coverage: coverage({ costKnownRows: 0, complete: false }) }),
      channel({ id: "b", coverage: coverage({ costKnownRows: 0, complete: false }) }),
    ]);
    expect(state).toEqual({ kind: "not-collected", channels: 2, rows: 4 });
    const note = costCollectionNote(state);
    expect(note).toContain("成本还没采到，不是「今天成本为 0」");
    expect(note).toContain("XM_FINANCE_COLLECT_ENABLED");
  });

  it("成本侧全部采到：明说显示 0 就是真的 0", () => {
    const state = costCollectionState([channel({ id: "a" }), channel({ id: "b" })]);
    expect(state).toEqual({ kind: "covered", channels: 2, rows: 4 });
    expect(costCollectionNote(state)).toContain("显示 0 就是真的 0");
  });

  it("部分渠道数据不完整：说合计偏高、覆盖不全", () => {
    const state = costCollectionState([
      channel({ id: "a" }),
      channel({ id: "b", coverage: coverage({ costKnownRows: 1, complete: false }) }),
    ]);
    expect(state).toEqual({
      kind: "partial",
      channels: 2,
      completeChannels: 1,
      costKnownRows: 3,
      rows: 4,
    });
    // 后端在任一侧覆盖不全时把金额置空，所以卡上是「—」而不是一个偏高的数
    expect(costCollectionNote(state)).toContain("显示「—」而不是一个偏高的数");
  });

  it("有渠道命中多币种：金额已被后端置空，毛利合计给不出", () => {
    const state = costCollectionState([
      channel({ id: "a" }),
      channel({ id: "b", coverage: coverage({ mixedCurrency: true, complete: false }) }),
    ]);
    expect(state).toEqual({ kind: "mixed-currency", channels: 2 });
    expect(costCollectionNote(state)).toContain("多币种");
  });
});

describe("runwayAttentionItems 只收触发档位的上游", () => {
  it("healthy 与判不出天数的行都不进「需要处理」，其余按最紧优先排序", () => {
    const items = runwayAttentionItems([
      upstream({ id: "healthy", runway: runwayOf(90, "healthy") }),
      upstream({ id: "unknown", runway: runwayOf(null, "") }),
      upstream({ id: "warning", runway: runwayOf(9, "warning") }),
      upstream({ id: "critical", runway: runwayOf(2, "critical") }),
      upstream({ id: "serious", runway: runwayOf(20, "serious") }),
    ]);
    expect(items.map((item) => item.id)).toEqual(["critical", "warning", "serious"]);
    expect(items.map((item) => item.days)).toEqual([2, 9, 20]);
  });

  it("天数相同的两行按 id 稳定排序，同一份数据每次给出同一个顺序", () => {
    const items = runwayAttentionItems([
      upstream({ id: "b", runway: runwayOf(5, "critical") }),
      upstream({ id: "a", runway: runwayOf(5, "critical") }),
    ]);
    expect(items.map((item) => item.id)).toEqual(["a", "b"]);
  });
});

describe("runwayAttentionEmptyReason 三种空态各说各的话", () => {
  const cov = (known: number, total: number): RunwayCoverage => ({ known, total, reasons: {} });

  it("一个上游都没登记：链路还没接", () => {
    expect(runwayAttentionEmptyReason([], cov(0, 0))).toContain("一个上游账号都没有登记");
  });

  it("登记了但余额一个都没读到：余额还没采到，不是今天没事", () => {
    const reason = runwayAttentionEmptyReason([upstream({ runway: runwayOf(null, "") })], cov(0, 3));
    expect(reason).toContain("余额还没采到");
    expect(reason).toContain("不是「今天没有要处理的事」");
  });

  it("读到了余额且都没触发档位：说清覆盖率，不断言全部安全", () => {
    const reason = runwayAttentionEmptyReason(
      [upstream({ runway: runwayOf(90, "healthy") })],
      cov(2, 5),
    );
    expect(reason).toContain("余额覆盖 2/5 个上游");
    expect(reason).toContain("不等于「全部安全」");
  });
});
