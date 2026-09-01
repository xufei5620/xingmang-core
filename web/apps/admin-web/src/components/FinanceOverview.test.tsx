import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { ChannelSummary } from "../api/finance";
import {
  aggregateChannelMoney,
  aggregateFreshness,
  periodRangeFor,
} from "../lib/financeOverview";
import { Sub2ApiFinanceOverview } from "./Sub2ApiFinanceOverview";

/**
 * 这些断言分别会抓住：金额被 Number/float 转换、跨平台行误入、跨币种或标度
 * 被悄悄相加，以及周区间没有从周一开始。它们使用手算的字面量期望，不能复用
 * 被测辅助函数来拼答案。
 */
function channel(overrides: Partial<ChannelSummary> = {}): ChannelSummary {
  return {
    id: "channel-sub2api-a",
    name: "Sub2API A",
    systemType: "sub2api",
    accessMethod: "upstream_key",
    metered: true,
    baseUrl: "https://a.example.test",
    platformId: "sub2api-prod",
    credentialRef: "secret://finance/a",
    rechargeRatio: "1.0",
    rechargeCostRate: "1.0",
    businessDayTz: "Asia/Shanghai",
    status: "active",
    tokenCount: 2,
    usageRevenue: { amountMinor: "12345678", currency: "USD", scale: 6 },
    supplyCost: { amountMinor: "2345678", currency: "USD", scale: 6 },
    grossProfit: { amountMinor: "10000000", currency: "USD", scale: 6 },
    grossMargin: "0.810000",
    coverage: {
      rowCount: 2,
      revenueKnownRows: 2,
      costKnownRows: 2,
      accountGrainRows: 0,
      mixedCurrency: false,
      complete: true,
    },
    observed: {
      costObservedAt: "2026-08-28T02:00:00Z",
      revenueObservedAt: "2026-08-28T02:00:00Z",
      updatedAt: "2026-08-28T02:00:00Z",
      source: "finance-summary-a",
    },
    runway: {
      days: null,
      level: "",
      reason: "no_balance",
      windowDays: 0,
      coveredDays: 0,
      dailyAverage: null,
      balance: null,
      balanceObservedAt: null,
    },
    ...overrides,
  };
}

function rawChannel(item: ChannelSummary) {
  return {
    id: item.id,
    name: item.name,
    system_type: item.systemType,
    access_method: item.accessMethod,
    metered: item.metered,
    base_url: item.baseUrl,
    platform_id: item.platformId,
    credential_ref: item.credentialRef,
    recharge_ratio: item.rechargeRatio,
    recharge_cost_rate: item.rechargeCostRate,
    business_day_tz: item.businessDayTz,
    status: item.status,
    token_count: item.tokenCount,
    usage_revenue: item.usageRevenue && {
      amount_minor: item.usageRevenue.amountMinor,
      currency: item.usageRevenue.currency,
      scale: item.usageRevenue.scale,
    },
    supply_cost: item.supplyCost && {
      amount_minor: item.supplyCost.amountMinor,
      currency: item.supplyCost.currency,
      scale: item.supplyCost.scale,
    },
    gross_profit: item.grossProfit && {
      amount_minor: item.grossProfit.amountMinor,
      currency: item.grossProfit.currency,
      scale: item.grossProfit.scale,
    },
    gross_margin: item.grossMargin,
    coverage: {
      row_count: item.coverage.rowCount,
      revenue_known_rows: item.coverage.revenueKnownRows,
      cost_known_rows: item.coverage.costKnownRows,
      account_grain_rows: item.coverage.accountGrainRows,
      mixed_currency: item.coverage.mixedCurrency,
      complete: item.coverage.complete,
    },
    observed: {
      cost_observed_at: item.observed.costObservedAt,
      revenue_observed_at: item.observed.revenueObservedAt,
      updated_at: item.observed.updatedAt,
      source: item.observed.source,
    },
  };
}

function fakeResponse(body: unknown): Response {
  return { ok: true, status: 200, json: () => Promise.resolve(body) } as Response;
}

function renderOverview(initialDate = "2024-02-14") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <Sub2ApiFinanceOverview initialDate={initialDate} />
    </QueryClientProvider>,
  );
}

describe("periodRangeFor", () => {
  it("uses hand-derived date-only day, Monday-week, and month boundaries", () => {
    expect(periodRangeFor("2026-08-27", "day")).toEqual({ from: "2026-08-27", to: "2026-08-27" });
    expect(periodRangeFor("2026-08-27", "week")).toEqual({ from: "2026-08-24", to: "2026-08-30" });
    expect(periodRangeFor("2026-02-14", "month")).toEqual({ from: "2026-02-01", to: "2026-02-28" });
  });
});

describe("aggregateChannelMoney", () => {
  it("sums only Sub2API scale-6 integer strings and uses the oldest observation", () => {
    const result = aggregateChannelMoney(
      [
        channel(),
        channel({
          id: "channel-sub2api-b",
          usageRevenue: { amountMinor: "2000000", currency: "USD", scale: 6 },
          observed: {
            costObservedAt: "2026-08-28T01:00:00Z",
            revenueObservedAt: "2026-08-28T01:00:00Z",
            updatedAt: "2026-08-28T01:00:00Z",
            source: "finance-summary-b",
          },
        }),
        channel({ id: "channel-newapi", systemType: "newapi", usageRevenue: { amountMinor: "9999999", currency: "USD", scale: 6 } }),
      ],
      "usageRevenue",
    );

    expect(result.money).toEqual({ amountMinor: "14345678", currency: "USD", scale: 6 });
    expect(result.oldestObservedAt).toBe("2026-08-28T01:00:00Z");
    expect(result.contributingRows).toBe(2);
    expect(result.coverage).toEqual({ completeRows: 2, totalRows: 2 });
  });

  it.each([
    ["missing money", channel({ usageRevenue: null })],
    ["invalid integer", channel({ usageRevenue: { amountMinor: "12.34", currency: "USD", scale: 6 } })],
    ["mixed currency", channel({ usageRevenue: { amountMinor: "1", currency: "CNY", scale: 6 } })],
    ["mixed scale", channel({ usageRevenue: { amountMinor: "1", currency: "USD", scale: 2 } })],
    ["formatter-unsupported scale", channel({ usageRevenue: { amountMinor: "1", currency: "USD", scale: 19 } })],
  ])("refuses a %s aggregate instead of publishing a plausible number", (_name, second) => {
    expect(aggregateChannelMoney([channel(), second], "usageRevenue").money).toBeNull();
  });

  it("treats an empty Sub2API selection as unavailable instead of zero", () => {
    expect(aggregateChannelMoney([channel({ systemType: "newapi" })], "grossProfit").money).toBeNull();
  });

  it.each([
    ["missing money", channel({ usageRevenue: null }), "missing-money"],
    ["missing observation", channel({ observed: { costObservedAt: null, revenueObservedAt: null, updatedAt: null, source: "finance-summary-a" } }), "missing-observation"],
    ["invalid observation", channel({ observed: { costObservedAt: "invalid", revenueObservedAt: "invalid", updatedAt: "not-a-timestamp", source: "finance-summary-a" } }), "invalid-observation"],
  ])("never presents %s as fresh trustworthy evidence", (_name, item, expectedReason) => {
    const aggregate = aggregateChannelMoney([item], "usageRevenue");
    expect(aggregateFreshness(aggregate, Date.parse("2026-08-28T04:00:00Z"))).toMatchObject({
      state: "uninitialized",
      is_partial: true,
      last_error_code: expectedReason,
    });
  });

  it("does not drop a contributor without an observation when choosing the freshness boundary", () => {
    const aggregate = aggregateChannelMoney([
      channel(),
      channel({ id: "missing-observed", observed: { costObservedAt: null, revenueObservedAt: null, updatedAt: null, source: "finance-summary-b" } }),
    ], "usageRevenue");
    expect(aggregate.oldestObservedAt).toBeNull();
    expect(aggregateFreshness(aggregate, Date.parse("2026-08-28T04:00:00Z"))).toMatchObject({ state: "uninitialized" });
  });

  it("chooses the true oldest UTC instant when valid RFC3339 offsets sort opposite lexically", () => {
    const aggregate = aggregateChannelMoney([
      channel({ observed: { costObservedAt: "2026-08-28T10:00:00+08:00", revenueObservedAt: "2026-08-28T10:00:00+08:00", updatedAt: "2026-08-28T10:00:00+08:00", source: "east-eight" } }),
      channel({ id: "utc-three", observed: { costObservedAt: "2026-08-28T03:00:00Z", revenueObservedAt: "2026-08-28T03:00:00Z", updatedAt: "2026-08-28T03:00:00Z", source: "utc" } }),
    ], "usageRevenue");
    // +08:00 entry is 02:00Z, one hour older than the lexically smaller 03:00Z string.
    expect(aggregate.oldestObservedAt).toBe("2026-08-28T10:00:00+08:00");
  });
});

describe("Sub2ApiFinanceOverview", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("renders the period control and eight finance cards in the approved order", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) =>
        Promise.resolve(
          fakeResponse(String(input).includes("/metrics") ? { items: [] } : { items: [rawChannel(channel())] }),
        ),
      ),
    );
    renderOverview();

    await screen.findAllByText("$12.35");
    // 资金概览卡是一条独立的 /api/v1/metrics Query，与渠道数据不是同一个
    // Promise 链；等最后一张卡（净现金流入）出现，保证两条 Query 都已落定
    // 再读取整页的标题顺序，否则偶发只等到渠道数据、payment 卡还没挂载。
    await screen.findByRole("heading", { name: "净现金流入", level: 3 });
    expect(screen.getByLabelText("统计日期")).toBeTruthy();
    expect(screen.getByRole("button", { name: "日", pressed: true })).toBeTruthy();
    expect(
      screen
        .getAllByRole("heading", { level: 3 })
        .map((heading) => heading.textContent)
        .filter((label) => ["区间成功到账", "区间待处理", "区间失败", "退款与冲正", "支付手续费", "净现金流入", "使用收入", "渠道毛利"].includes(label ?? "")),
    ).toEqual(["区间成功到账", "区间待处理", "区间失败", "退款与冲正", "支付手续费", "净现金流入", "使用收入", "渠道毛利"]);
  });

  it("keeps all six payment cards explicitly unavailable/not-applicable and never fabricates event amounts", async () => {
    // 没有 sub2api.payments.daily 指标（/api/v1/metrics 返回空），资金概览卡
    // 因此走"未接入"分支——与渠道数据是两条独立的 Query，各自的假 fetch
    // 都要能正确响应，不能靠同一个通配响应蒙混过去。
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) =>
        Promise.resolve(
          fakeResponse(String(input).includes("/metrics") ? { items: [] } : { items: [rawChannel(channel())] }),
        ),
      ),
    );
    renderOverview();

    await screen.findAllByText("$12.35");
    for (const label of ["区间成功到账", "区间待处理", "区间失败", "退款与冲正", "支付手续费", "净现金流入"]) {
      const heading = await screen.findByRole("heading", { name: label, level: 3 });
      const card = heading.closest("article");
      expect(card).not.toBeNull();
      expect(within(card!).getByText("未接入")).toBeTruthy();
      expect(within(card!).getByText("—")).toBeTruthy();
    }
    expect(screen.getByRole("table", { name: "最近事件" })).toBeTruthy();
    expect(screen.getByText(/支付事件端点尚未接入/)).toBeTruthy();
  });

  it("sends the chosen Monday week range to the channel-summary Query boundary", async () => {
    const fetchMock = vi.fn((_input: RequestInfo | URL) => Promise.resolve(fakeResponse({ items: [rawChannel(channel())] })));
    vi.stubGlobal("fetch", fetchMock);
    renderOverview();

    await screen.findAllByText("$12.35");
    fireEvent.click(screen.getByRole("button", { name: "周" }));
    await waitFor(() => {
      expect(fetchMock.mock.calls.some(([input]) => String(input).includes("from=2024-02-12") && String(input).includes("to=2024-02-18"))).toBe(true);
    });
    expect(screen.getByText("2024-02-12 ~ 2024-02-18 · 按周查看")).toBeTruthy();
  });

  it("recovers from clearing the native date input instead of crashing the rendered page", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(fakeResponse({ items: [rawChannel(channel())] }))));
    renderOverview();

    await screen.findAllByText("$12.35");
    fireEvent.change(screen.getByLabelText("统计日期"), { target: { value: "" } });
    expect(screen.getByRole("heading", { name: "资金对账", level: 2 })).toBeTruthy();
  });

  it("renders the reconciliation, profit bridge, and recent-event structure without false payment data", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(fakeResponse({ items: [rawChannel(channel())] }))));
    renderOverview();

    await screen.findAllByText("$12.35");
    expect(screen.getByRole("heading", { name: "资金对账", level: 2 })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "经营利润桥", level: 2 })).toBeTruthy();
    expect(screen.getByText("上游现金成本")).toBeTruthy();
    expect(screen.getByText("贡献利润")).toBeTruthy();
    expect(screen.getByText("成功充值")).toBeTruthy();
    expect(screen.getByText("待处理")).toBeTruthy();
    expect(screen.getByText("失败")).toBeTruthy();
    expect(screen.getByText("退款")).toBeTruthy();
    expect(screen.getAllByText("$12.35").length).toBeGreaterThan(1);
    expect(screen.getByText("$2.35")).toBeTruthy();
    expect(screen.getAllByText("$10.00").length).toBeGreaterThan(1);
    expect(screen.getByText(/支付费用与可归属基础设施/)).toBeTruthy();
  });

  it("keeps failed bridge aggregates explainable instead of rendering a bare dash", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(fakeResponse({ items: [rawChannel(channel({ usageRevenue: null }))] }))));
    renderOverview();

    expect(await screen.findByText("金额缺失")).toBeTruthy();
    expect(screen.getAllByText(/来源 finance-summary-a/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/覆盖完整 1\/1 条渠道/).length).toBeGreaterThan(0);
  });
});
