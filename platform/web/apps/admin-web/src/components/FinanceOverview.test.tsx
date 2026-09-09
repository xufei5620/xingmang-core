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

/** 一页 `GET /platforms/sub2api/orders` 响应。「最近事件」四行只用
 *  `stats_by_status` + `freshness`，`items` 是同一个响应里的逐笔明细，
 *  这一页不展示。 */
function rawOrdersPage({
  stats = { PAID: { count: 42, amount: { minor_units: "418000", currency: "USD" } } } as Record<string, unknown>,
  isPartial = false,
  state = "fresh",
} = {}) {
  return {
    items: [],
    next_cursor: "",
    stats_by_status: stats,
    from: "2024-02-14",
    to: "2024-02-14",
    data_source: "sub2api-staging",
    freshness: {
      state,
      threshold_seconds: 900,
      staleness_seconds: 12,
      is_partial: isPartial,
      observed_at: "2026-08-28T02:00:00Z",
      last_success: "2026-08-28T02:00:00Z",
      last_error_code: "",
    },
  };
}

/** 这一页同时打三条互不相干的 Query（渠道汇总 / 指标 / 逐笔订单）。
 *  一个通配响应会让三条里至少两条读到形状不对的 body——那种"绿"不说明
 *  任何事，所以每条都各自应答。 */
function stubFinanceFetch({
  channels = [rawChannel(channel())] as unknown[],
  metrics = [] as unknown[],
  orders = rawOrdersPage() as unknown,
} = {}) {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/orders")) return Promise.resolve(fakeResponse(orders));
    if (url.includes("/metrics")) return Promise.resolve(fakeResponse({ items: metrics }));
    return Promise.resolve(fakeResponse({ items: channels }));
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

/** 「最近事件」表里某一行的 `<tr>`。断言一律锚在行内，不在整页搜文本——
 *  这一页有六张卡、两张对照表，同一个词在别处出现是常态。 */
function eventRow(label: string): HTMLElement {
  const table = screen.getByRole("table", { name: "最近事件" });
  const cell = within(table).getByRole("cell", { name: label });
  const row = cell.closest("tr");
  if (!row) throw new Error(`未找到「${label}」所在的行`);
  return row;
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
    stubFinanceFetch();
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
    stubFinanceFetch();
    renderOverview();

    await screen.findAllByText("$12.35");
    for (const label of ["区间成功到账", "区间待处理", "区间失败", "退款与冲正", "支付手续费", "净现金流入"]) {
      const heading = await screen.findByRole("heading", { name: label, level: 3 });
      const card = heading.closest("article");
      expect(card).not.toBeNull();
      expect(within(card!).getByText("未接入")).toBeTruthy();
      expect(within(card!).getByText("—")).toBeTruthy();
    }
    // 六张卡读 sub2api.payments.daily 指标，「最近事件」读逐笔订单端点——
    // 两条独立的 Query。指标没有并不代表订单也没有，所以卡片「未接入」的
    // 同一次渲染里，下面那张表照样给得出真实数字。
    expect(within(eventRow("成功充值")).getByText("$4,180.00")).toBeTruthy();
  });

  it("sends the chosen Monday week range to the channel-summary Query boundary", async () => {
    const fetchMock = stubFinanceFetch();
    renderOverview();

    await screen.findAllByText("$12.35");
    fireEvent.click(screen.getByRole("button", { name: "周" }));
    await waitFor(() => {
      expect(fetchMock.mock.calls.some(([input]) => String(input).includes("from=2024-02-12") && String(input).includes("to=2024-02-18"))).toBe(true);
    });
    expect(screen.getByText("2024-02-12 ~ 2024-02-18 · 按周查看")).toBeTruthy();
  });

  it("recovers from clearing the native date input instead of crashing the rendered page", async () => {
    stubFinanceFetch();
    renderOverview();

    await screen.findAllByText("$12.35");
    fireEvent.change(screen.getByLabelText("统计日期"), { target: { value: "" } });
    expect(screen.getByRole("heading", { name: "资金对账", level: 2 })).toBeTruthy();
  });

  it("renders the reconciliation, profit bridge, and recent-event structure without false payment data", async () => {
    stubFinanceFetch();
    renderOverview();

    await screen.findAllByText("$12.35");
    expect(screen.getByRole("heading", { name: "资金对账", level: 2 })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "经营利润桥", level: 2 })).toBeTruthy();
    expect(screen.getByText("上游现金成本")).toBeTruthy();
    expect(screen.getByText("贡献利润")).toBeTruthy();
    // 「最近事件」是第三条 Query（逐笔订单），与渠道汇总不是同一个 Promise
    // 链——等它自己的行出现，否则只等到 $12.35 时这张表还停在加载态。
    await screen.findByRole("cell", { name: "成功充值" });
    expect(screen.getByText("待处理")).toBeTruthy();
    expect(screen.getByText("失败")).toBeTruthy();
    expect(screen.getByText("退款")).toBeTruthy();
    expect(screen.getAllByText("$12.35").length).toBeGreaterThan(1);
    expect(screen.getByText("$2.35")).toBeTruthy();
    expect(screen.getAllByText("$10.00").length).toBeGreaterThan(1);
    expect(screen.getByText(/支付费用与可归属基础设施/)).toBeTruthy();
  });

  it("keeps failed bridge aggregates explainable instead of rendering a bare dash", async () => {
    stubFinanceFetch({ channels: [rawChannel(channel({ usageRevenue: null }))] });
    renderOverview();

    expect(await screen.findByText("金额缺失")).toBeTruthy();
    expect(screen.getAllByText(/来源 finance-summary-a/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/覆盖完整 1\/1 条渠道/).length).toBeGreaterThan(0);
  });
});

/** 「最近事件」四行（XM-SUB2API-RECENT-EVENTS）。
 *
 *  这一组每一条正向断言在旧实现（四行写死「—」+「未接入」）下都是红的：
 *  断言的是具体金额、具体笔数、具体说明文案，没有一条能被「—」满足。 */
describe("Sub2ApiFinanceOverview 的「最近事件」", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  const FULL_STATS = {
    PAID: { count: 30, amount: { minor_units: "300000", currency: "USD" } },
    SUCCESS: { count: 12, amount: { minor_units: "118000", currency: "USD" } },
    PENDING: { count: 3, amount: { minor_units: "9900", currency: "USD" } },
    FAILED: { count: 5, amount: { minor_units: "15000", currency: "USD" } },
    REFUNDED: { count: 2, amount: { minor_units: "4000", currency: "USD" } },
  };

  it("四行显示 stats_by_status 算出的真实金额与笔数", async () => {
    stubFinanceFetch({ orders: rawOrdersPage({ stats: FULL_STATS }) });
    renderOverview();

    // 正向锚点：PAID+SUCCESS 两个原始状态归进同一个桶，30000+11800 分 =
    // $4,180.00、42 笔。期望值是手算的，不复用被测的分桶函数来拼答案。
    await screen.findByText("$4,180.00");
    expect(within(eventRow("成功充值")).getByText("42 笔")).toBeTruthy();
    expect(within(eventRow("待处理")).getByText("$99.00")).toBeTruthy();
    expect(within(eventRow("待处理")).getByText("3 笔")).toBeTruthy();
    expect(within(eventRow("失败")).getByText("$150.00")).toBeTruthy();
    expect(within(eventRow("失败")).getByText("5 笔")).toBeTruthy();
    expect(within(eventRow("退款")).getByText("$40.00")).toBeTruthy();
    expect(within(eventRow("退款")).getByText("2 笔")).toBeTruthy();
    // 数据新鲜度与来源必须可见（宪法：数据新鲜度必须可见）。
    expect(screen.getByText("来源 sub2api-staging · 窗口 2024-02-14 至 2024-02-14 · 本区间共 52 笔")).toBeTruthy();
  });

  // 与「资金概览」的渠道汇总同一条纪律：币种不一致就不给合计，也不换算。
  it("同一个桶里两种币种时不给合计，逐字说明为什么，并且不影响其它行", async () => {
    stubFinanceFetch({
      orders: rawOrdersPage({
        stats: {
          PAID: { count: 3, amount: { minor_units: "30000", currency: "CNY" } },
          SUCCESS: { count: 2, amount: { minor_units: "20000", currency: "USD" } },
          PENDING: { count: 4, amount: { minor_units: "9900", currency: "USD" } },
        },
      }),
    });
    renderOverview();

    // 对照组兼锚点：币种一致的「待处理」行照常给出数字。它在 fail closed
    // 判据被改松时**不该**变红——这样上面那条断言的红才有意义。
    await screen.findByText("$99.00");
    const success = within(eventRow("成功充值"));
    expect(success.getByText("—")).toBeTruthy();
    expect(success.getByText("币种不一致，合计给不出；不做隐式换算")).toBeTruthy();
    // 笔数不受币种影响：契约里 Count 本来就不区分币种。
    expect(success.getByText("5 笔")).toBeTruthy();
    expect(success.getByText("覆盖不全")).toBeTruthy();
  });

  // 缺席型断言单开一条：与上一条放在一起时，上一条的 getByText 会先抛出，
  // 这两行根本跑不到，变异也就证明不了它们不是恒真。
  it("跨币种时页面上不出现任何「加起来了」的金额", async () => {
    stubFinanceFetch({
      orders: rawOrdersPage({
        stats: {
          PAID: { count: 3, amount: { minor_units: "30000", currency: "CNY" } },
          SUCCESS: { count: 2, amount: { minor_units: "20000", currency: "USD" } },
          PENDING: { count: 4, amount: { minor_units: "9900", currency: "USD" } },
        },
      }),
    });
    renderOverview();

    // 先 await 正向锚点，确认这张表真的渲染完了，再做同步的缺席断言——
    // 把 waitFor 套在缺席断言外面几乎必然恒真。
    await screen.findByText("$99.00");
    // 30000 + 20000 = 50000 分。无论落在哪种币种符号上都是隐式换算的结果。
    expect(screen.queryByText("$500.00")).toBeNull();
    expect(screen.queryByText("¥500.00")).toBeNull();
  });

  it("桶缺席且汇总完整时是确认过的零，不是「未接入」", async () => {
    stubFinanceFetch({
      orders: rawOrdersPage({ stats: { PAID: { count: 42, amount: { minor_units: "418000", currency: "USD" } } } }),
    });
    renderOverview();

    await screen.findByText("$4,180.00");
    const refund = within(eventRow("退款"));
    expect(refund.getByText("$0.00")).toBeTruthy();
    expect(refund.getByText("0 笔")).toBeTruthy();
    expect(refund.getByText("完整")).toBeTruthy();
  });

  it("汇总被标记为不完整时，缺席的桶说「说不清」而不是 0", async () => {
    stubFinanceFetch({
      orders: rawOrdersPage({
        stats: { PAID: { count: 42, amount: { minor_units: "418000", currency: "USD" } } },
        isPartial: true,
        state: "partial",
      }),
    });
    renderOverview();

    await screen.findByText("$4,180.00");
    const refund = within(eventRow("退款"));
    expect(refund.getByText("说不清")).toBeTruthy();
    expect(refund.getByText("覆盖不全：这个分桶在本次汇总里没有出现，无法确认它真的是零")).toBeTruthy();
    // 出现过的桶：金额照给，但必须说清它只会偏低。
    expect(
      within(eventRow("成功充值")).getByText(
        "覆盖不全：上游有未计入金额的订单（非合约币种或未识别渠道），笔数仍完整，合计只会偏低",
      ),
    ).toBeTruthy();
  });

  // 同样单开一条：与上一条合并时上面的 getByText 会先抛出，这两行跑不到。
  it("汇总被标记为不完整时，缺席的桶一个数字都不给", async () => {
    stubFinanceFetch({
      orders: rawOrdersPage({
        stats: { PAID: { count: 42, amount: { minor_units: "418000", currency: "USD" } } },
        isPartial: true,
        state: "partial",
      }),
    });
    renderOverview();

    await screen.findByText("$4,180.00");
    const refund = within(eventRow("退款"));
    expect(refund.queryByText("$0.00")).toBeNull();
    expect(refund.queryByText("0 笔")).toBeNull();
  });

  it("上游出现未归类状态时报出来，并说明四行之和为什么对不上总数", async () => {
    stubFinanceFetch({
      orders: rawOrdersPage({
        stats: {
          PAID: { count: 42, amount: { minor_units: "418000", currency: "USD" } },
          CHARGEBACK: { count: 2, amount: { minor_units: "700", currency: "USD" } },
        },
      }),
    });
    renderOverview();

    await screen.findByText("$4,180.00");
    // 页顶那条「充值不是收入」的横幅也是 role=status，所以锚在区块里面找。
    const section = screen.getByRole("heading", { name: "最近事件", level: 2 }).closest("section");
    const note = within(section!).getByRole("status");
    expect(note.textContent).toContain("CHARGEBACK");
    expect(note.textContent).toContain("四行之和小于「本区间共 44 笔」");
  });

  // 这一页最容易被读错的地方：充值不是收入。页顶那条横幅在这里已经滚出
  // 屏幕，所以区分必须写在这个区块自己的标题下方。
  it("在区块内部就把「充值不是收入」讲清楚，并指出收入在哪一行", async () => {
    stubFinanceFetch({ orders: rawOrdersPage({ stats: FULL_STATS }) });
    renderOverview();

    await screen.findByText("$4,180.00");
    const section = screen.getByRole("heading", { name: "最近事件", level: 2 }).closest("section");
    expect(section).not.toBeNull();
    const text = section!.textContent ?? "";
    expect(text).toContain("用户充值与支付事件");
    expect(text).toContain("不是当期收入");
    expect(text).toContain("「经营利润桥」的「使用收入」");
    expect(text).toContain("两个数永远分开列，不相加");
  });

  // 两组「四个桶」口径标注（产品负责人裁定「两组都留，按全面来」）。
  it("两组分桶各自标明口径，并说明它们不是同一个数的两个答案", async () => {
    stubFinanceFetch({ orders: rawOrdersPage({ stats: FULL_STATS }) });
    renderOverview();

    await screen.findByText("$4,180.00");
    const snapshot = screen.getByRole("heading", { name: "支付日快照", level: 2 }).closest("section");
    expect(snapshot).not.toBeNull();
    const snapshotText = snapshot!.textContent ?? "";
    expect(snapshotText).toContain("sub2api.payments.daily");
    expect(snapshotText).toContain("单个业务日");
    expect(snapshotText).toContain("不是同一个数的两个答案");

    const events = screen.getByRole("heading", { name: "最近事件", level: 2 }).closest("section");
    const eventsText = events!.textContent ?? "";
    expect(eventsText).toContain("区间合计");
    expect(eventsText).toContain("跟随上方所选的统计区间");
    expect(eventsText).toContain("两组算的是同一批分桶，但覆盖的时间范围不同");
  });

  // 周/月区间上面六张卡全是「—」。今天它和「链路坏了」在界面上长得一样，
  // 而这两件事的下一步完全相反（前者不用管，后者要查）。
  it("非单日区间说清「—」是口径不覆盖，不是数据缺失", async () => {
    stubFinanceFetch({ orders: rawOrdersPage({ stats: FULL_STATS }) });
    renderOverview();

    await screen.findByText("$4,180.00");
    fireEvent.click(screen.getByRole("button", { name: "周" }));

    // findByText 命中的是内层 <strong>（它的 textContent 只有那半句）；
    // 要断言整段话得取包着它的那个段落。
    const note = (await screen.findByText(/超出这个口径能回答的范围/)).closest("p");
    expect(note).not.toBeNull();
    expect(note!.textContent).toContain("所选区间是 2024-02-12 至 2024-02-18");
    expect(note!.textContent).toContain("口径不覆盖，不是数据缺失");
    expect(note!.textContent).toContain("链路没有故障，不需要排查");
    // 指路：这个区间的合计要去哪看。
    expect(note!.textContent).toContain("见下方「最近事件」");
  });

  // 缺席型断言单开一条：单日区间**不该**出现那段解释，否则它就是永远显示的
  // 噪声，读者会连真正该看的那次也一并忽略。
  it("单日区间不出现那段「口径不覆盖」的解释", async () => {
    stubFinanceFetch({ orders: rawOrdersPage({ stats: FULL_STATS }) });
    renderOverview();

    // 先 await 正向锚点，确认整页真的渲染完了，再做同步的缺席断言。
    await screen.findByText("$4,180.00");
    expect(screen.getByRole("heading", { name: "支付日快照", level: 2 })).toBeTruthy();
    expect(screen.queryByText(/超出这个口径能回答的范围/)).toBeNull();
  });

  // 订单端点在 XM_PLATFORM_PAYMENTS_MODE=off 的部署上根本不挂载。那时这一块
  // 该说「未接入」，而整页其余部分（都来自另外两条 Query）必须照常可用。
  it("订单端点未挂载时只有这一块未接入，页面其余部分照常", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes("/orders")) {
          return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}) } as Response);
        }
        if (url.includes("/metrics")) return Promise.resolve(fakeResponse({ items: [] }));
        return Promise.resolve(fakeResponse({ items: [rawChannel(channel())] }));
      }),
    );
    renderOverview();

    expect(await screen.findByText(/支付与财务的逐笔订单在当前环境未启用/)).toBeTruthy();
    expect(screen.queryByRole("table", { name: "最近事件" })).toBeNull();
    // 另外两条 Query 的结论没有被一起抹掉。
    expect(screen.getAllByText("$12.35").length).toBeGreaterThan(0);
    expect(screen.getByRole("heading", { name: "经营利润桥", level: 2 })).toBeTruthy();
  });
});
