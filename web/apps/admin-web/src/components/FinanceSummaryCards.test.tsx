import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FinanceSummaryCards, formatPercent, runwayReasonText } from "./FinanceSummaryCards";

/** 成本三卡的渲染纪律（XM-0037d）。
 *
 *  这一层要守的只有一条，但它有好几个面：**给不出就说给不出，并说清为什么**。
 *  「今天毛利 0」会让人去查定价，而真相可能只是「今天的成本还没采到」——
 *  两者在页面上必须长得不一样（宪法 12 条、后端 §5.1）。 */

const observed = {
  cost_observed_at: "2026-08-28T05:59:00Z",
  revenue_observed_at: "2026-08-28T05:59:00Z",
  updated_at: new Date().toISOString(),
  source: "finance-collect-staging",
};

function channel(over: Record<string, unknown> = {}) {
  return {
    id: "c1",
    name: "sub2api · https://a.test",
    system_type: "sub2api",
    access_method: "upstream_key",
    metered: true,
    base_url: "https://a.test",
    platform_id: "sub2api-prod",
    recharge_ratio: "1.5",
    recharge_cost_rate: "0.666667",
    token_count: 2,
    usage_revenue: { amount_minor: "12345600", currency: "USD", scale: 6 },
    supply_cost: { amount_minor: "3875819", currency: "USD", scale: 6 },
    gross_profit: { amount_minor: "8469781", currency: "USD", scale: 6 },
    gross_margin: "0.686042",
    coverage: {
      row_count: 2,
      revenue_known_rows: 2,
      cost_known_rows: 2,
      account_grain_rows: 1,
      mixed_currency: false,
      complete: true,
    },
    observed,
    runway: { days: null, reason: "no_balance", window_days: 7, covered_days: 0 },
    ...over,
  };
}

function upstream(over: Record<string, unknown> = {}) {
  return {
    ...channel(),
    supplier_key: "sub2api|https://a.test",
    ...over,
  };
}

function fakeResponse(body: unknown): Response {
  return { ok: true, status: 200, json: () => Promise.resolve(body) } as unknown as Response;
}

function stubFetch(channels: unknown[], upstreams: unknown[], extra: Record<string, unknown> = {}) {
  return vi.fn((input: unknown) => {
    const url = String(input);
    if (url.includes("/finance/upstreams/summary")) {
      return Promise.resolve(
        fakeResponse({
          items: upstreams,
          from: "2026-08-28",
          to: "2026-08-28",
          runway_coverage: { total: upstreams.length, known: 0, reasons: {} },
          runway_thresholds: { critical_days: 5, warning_days: 10, serious_days: 20 },
          ...extra,
        }),
      );
    }
    return Promise.resolve(
      fakeResponse({ items: channels, from: "2026-08-28", to: "2026-08-28" }),
    );
  });
}

function renderCards() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <FinanceSummaryCards systemType="sub2api" label="Sub2API" />
    </QueryClientProvider>,
  );
}

describe("成本三卡", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("金额按 scale-6 换算——直接按币种最小单位读会差一万倍", async () => {
    vi.stubGlobal("fetch", stubFetch([channel()], [upstream()]));
    renderCards();
    // 8469781 微美元 = $8.47（半进）
    expect(await screen.findByText("$8.47")).toBeTruthy();
    // 3875819 微美元 = $3.88（§2.4 的 worked example）
    expect(screen.getByText("$3.88")).toBeTruthy();
  });

  it("覆盖不全时金额显示「—」而不是一个偏低的和", async () => {
    const partial = channel({
      supply_cost: null,
      gross_profit: null,
      coverage: {
        row_count: 3,
        revenue_known_rows: 3,
        cost_known_rows: 2,
        account_grain_rows: 0,
        mixed_currency: false,
        complete: false,
      },
    });
    vi.stubGlobal("fetch", stubFetch([partial], [upstream()]));
    renderCards();
    // 「1/1 条渠道数据完整」会变成 0/1——覆盖率必须和金额一起呈现
    expect(await screen.findByText(/0\/1 条渠道数据完整/)).toBeTruthy();
    expect(screen.getAllByText("—").length).toBeGreaterThan(0);
  });

  it("账号级聚合行单独说一句——那几把令牌没有独立的下钻", async () => {
    vi.stubGlobal("fetch", stubFetch([channel()], [upstream()]));
    renderCards();
    expect(await screen.findByText(/1 行为账号级聚合/)).toBeTruthy();
  });

  it("可用天数给不出时必须说清为什么，而不是只留一个「—」", async () => {
    vi.stubGlobal("fetch", stubFetch([channel()], [upstream()]));
    renderCards();
    expect(
      await screen.findByText(/尚未读到上游余额（余额采集未接通）/),
    ).toBeTruthy();
    // 覆盖率边界要显式说出来（§12 拍板）
    expect(screen.getByText(/余额覆盖 0\/1 个上游/)).toBeTruthy();
  });

  it("算得出天数时带出档位与余额观测时刻（§10.4 硬要求）", async () => {
    const withRunway = upstream({
      runway: {
        days: 4,
        level: "critical",
        reason: "",
        window_days: 7,
        covered_days: 7,
        daily_average: { amount_minor: "4000000", currency: "USD", scale: 6 },
        balance: { amount_minor: "16000000", currency: "USD", scale: 6 },
        balance_observed_at: "2026-08-28T05:59:00Z",
      },
    });
    vi.stubGlobal("fetch", stubFetch([channel()], [withRunway]));
    renderCards();
    expect(await screen.findByText("4 天")).toBeTruthy();
    expect(screen.getByText("critical")).toBeTruthy();
    expect(screen.getByText(/余额观测/)).toBeTruthy();
    // 窗口与阈值都要说明白，否则「4 天」是个裸数字
    expect(screen.getByText(/近 7 个完整业务日（实到 7 天）/)).toBeTruthy();
    expect(screen.getByText(/告警档 ≤5\/10\/20 天/)).toBeTruthy();
  });

  it("多条上游时取**最紧**的那条，不做平均", async () => {
    const tight = upstream({
      id: "u-tight",
      name: "sub2api · https://tight.test",
      runway: { days: 3, level: "critical", reason: "", window_days: 7, covered_days: 7 },
    });
    const loose = upstream({
      id: "u-loose",
      name: "sub2api · https://loose.test",
      runway: { days: 300, level: "healthy", reason: "", window_days: 7, covered_days: 7 },
    });
    vi.stubGlobal("fetch", stubFetch([channel()], [loose, tight]));
    renderCards();
    // 平均会给出 151 天，把最该被看见的那条藏起来
    expect(await screen.findByText("3 天")).toBeTruthy();
    expect(screen.getByText(/tight\.test/)).toBeTruthy();
  });

  it("贡献利润是诚实占位，不显示 0", async () => {
    vi.stubGlobal("fetch", stubFetch([channel()], [upstream()]));
    renderCards();
    expect(await screen.findByText("贡献利润")).toBeTruthy();
    expect(screen.getByText(/待 M3 支付接入/)).toBeTruthy();
  });

  it("没有本平台渠道时说「还没有登记任何渠道」，而不是显示 0", async () => {
    vi.stubGlobal("fetch", stubFetch([channel({ system_type: "newapi" })], []));
    renderCards();
    expect(await screen.findByText(/本平台还没有登记任何渠道/)).toBeTruthy();
  });
});

describe("formatPercent：定点字符串 → 百分比，不经浮点", () => {
  it("常见取值", () => {
    expect(formatPercent("0.686042")).toBe("68.60%");
    expect(formatPercent("0.687500")).toBe("68.75%");
    expect(formatPercent("1.000000")).toBe("100.00%");
    expect(formatPercent("0")).toBe("0.00%");
  });

  it("负毛利率照样显示——藏起来会让亏损渠道看起来只是没赚", () => {
    expect(formatPercent("-0.500000")).toBe("-50.00%");
  });

  it("形态不对时给「—」而不是 NaN%", () => {
    expect(formatPercent("abc")).toBe("—");
    expect(formatPercent("")).toBe("—");
  });
});

describe("runwayReasonText：每种原因有自己的话", () => {
  it("「不适用」与「没接通」不能说成同一句", () => {
    expect(runwayReasonText("not_applicable")).not.toBe(runwayReasonText("no_balance"));
    expect(runwayReasonText("not_applicable")).toContain("订阅型");
    expect(runwayReasonText("no_balance")).toContain("未接通");
  });

  it("未知原因也给得出一句话，不返回空串", () => {
    expect(runwayReasonText("something-new")).not.toBe("");
  });
});
