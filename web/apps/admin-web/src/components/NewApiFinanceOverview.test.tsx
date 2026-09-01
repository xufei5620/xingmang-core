import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router";

import { NewApiFinanceOverview } from "./NewApiFinanceOverview";

function response(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

function renderFinance(
  subId: "orders" | "profit" = "orders",
  initialEntry = "/platforms/newapi?tab=finance&day=2026-08-28",
) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <QueryClientProvider client={queryClient}>
        <NewApiFinanceOverview subId={subId} initialDate="2026-08-28" />
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

const fresh = {
  state: "fresh",
  staleness_seconds: 12,
  threshold_seconds: 1800,
  is_partial: false,
  observed_at: "2026-08-28T10:00:00Z",
  last_success: "2026-08-28T10:00:00Z",
  last_error_code: "",
};

/** 空的（但形状合法的）订单页——`/api/v1/platforms/newapi/orders` 的响应
 *  永远带 freshness（后端字段无 omitempty），mock 必须一样，否则
 *  FreshnessBadge 会因为 freshness 是 undefined 而抛错，那是 mock 形状不对，
 *  不是被测组件的 bug。 */
function emptyOrdersPage(overrides: Record<string, unknown> = {}) {
  return {
    items: [],
    next_cursor: "",
    stats_by_status: {},
    from: "2026-08-28",
    to: "2026-08-28",
    data_source: "newapi-fake",
    freshness: fresh,
    ...overrides,
  };
}

/** 按 URL 路由到 /metrics、/metrics/history 或 /orders 三条通道各自的响应——
 *  这一页现在同时会发起三种请求（四格里三个分桶卡走 /metrics，"月累计"
 *  独立走 /metrics/history，订单台账走 /orders），共用一个不分辨 URL 的假
 *  fetch 会让其中一条拿到另一条的形状，这不是被测代码的问题。 */
function mockFetch({
  metrics = { items: [] },
  history = { items: [] },
  orders = emptyOrdersPage(),
}: { metrics?: unknown; history?: unknown; orders?: unknown } = {}) {
  return vi.fn((input: string) => {
    if (input.includes("/orders")) return Promise.resolve(response(200, orders));
    if (input.includes("/metrics/history")) return Promise.resolve(response(200, history));
    if (input.includes("/metrics")) return Promise.resolve(response(200, metrics));
    return Promise.resolve(response(404, { error: { code: "NOT_REGISTERED", message: "unknown" } }));
  });
}

function paymentsDailyMetric(overrides: Record<string, unknown> = {}, valueOverrides: Record<string, unknown> = {}) {
  return {
    metric_key: "newapi.payments.daily",
    source: "newapi-prod",
    environment: "development",
    watermark: "day:2026-08-28",
    value: {
      day: "2026-08-28",
      currency: "CNY",
      by_status: { succeeded: { count: 4, amount_minor_units: 812000 } },
      fee_minor_units: null,
      net_minor_units: null,
      ...valueOverrides,
    },
    freshness: fresh,
    ...overrides,
  };
}

/** 「月累计」读的是真实挂钟"今天"（`businessTodayDateOnly()` 内部用
 *  `new Date()`，不吃 `initialDate`），与页面其余部分对"今天"的既有处理
 *  一致——但这意味着测试夹具不能硬编码某个假设的"今天"日期，必须按运行
 *  时的真实当前业务日现算，否则换一天跑测试就会全部落空。 */
function businessTodayForTest(): string {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: "Asia/Shanghai",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(new Date());
  const values = Object.fromEntries(parts.map((part) => [part.type, part.value]));
  return `${values.year ?? "1970"}-${values.month ?? "01"}-${values.day ?? "01"}`;
}

function historyItem(day: string, overrides: Record<string, unknown> = {}, valueOverrides: Record<string, unknown> = {}) {
  return {
    observed_at: `${day}T10:00:00Z`,
    synced_at: `${day}T10:00:00Z`,
    source: "newapi-prod",
    status: "ok",
    is_partial: false,
    watermark: `day:${day}`,
    last_error_code: "",
    value: {
      day,
      currency: "CNY",
      by_status: { succeeded: { count: 2, amount_minor_units: 100000 } },
      fee_minor_units: null,
      net_minor_units: null,
      ...valueOverrides,
    },
    ...overrides,
  };
}

describe("NewAPI 资金与订单", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("无指标与无订单时原型四格（区间到账/区间退款/月累计/支付失败）都诚实显示未接入/不适用，不编金额", async () => {
    vi.stubGlobal("fetch", mockFetch());
    renderFinance();

    for (const label of ["区间到账", "月累计", "支付失败"]) {
      const heading = await screen.findByRole("heading", { name: label, level: 3 });
      expect(within(heading.closest("article") as HTMLElement).getByText("未接入")).toBeTruthy();
    }
    // NewAPI 的退款恒为「不适用」——不是因为这次没数据，是这个上游结构上就
    // 没有退款概念（XM-PAY0 交接文档），与 Sub2API 六卡同一份判断逻辑。
    const refundHeading = await screen.findByRole("heading", { name: "区间退款", level: 3 });
    expect(within(refundHeading.closest("article") as HTMLElement).getByText("不适用")).toBeTruthy();
    // Sub2API 六卡独有的标签不该出现在这一页
    for (const label of ["区间成功到账", "退款与冲正", "支付手续费", "净现金流入"]) {
      expect(screen.queryByRole("heading", { name: label, level: 3 })).toBeNull();
    }
    expect(screen.queryByText("¥0.00")).toBeNull();

    // 没有订单时 DataTableV2 展示空态而不是一张空表格（rows.length === 0 分支），
    // 所以这里断言空态文案，不去找一张不存在的 table。
    expect(await screen.findByText("这个窗口没有充值订单")).toBeTruthy();
    expect(screen.getByText(/已读取 payments.read.v1/)).toBeTruthy();
  });

  it("单日 newapi.payments.daily 指标按最小单位显示，并带新鲜度与来源（区间到账）", async () => {
    vi.stubGlobal(
      "fetch",
      mockFetch({
        metrics: {
          items: [
            paymentsDailyMetric(),
            {
              metric_key: "newapi.subscription.daily",
              source: "newapi-prod",
              environment: "development",
              watermark: "wm-subscription",
              value: { day: "2026-08-28", amount_minor_units: "120000", currency: "CNY" },
              freshness: fresh,
            },
          ],
        },
      }),
    );
    renderFinance();

    const tile = await screen.findByRole("heading", { name: "区间到账", level: 3 });
    const card = tile.closest("article") as HTMLElement;
    expect(within(card).getByText("¥8,120.00")).toBeTruthy();
    expect(within(card).getByText(/4 笔/)).toBeTruthy();
    expect(within(card).getByText("数据新鲜")).toBeTruthy();
    expect(within(card).getByText(/来源 newapi-prod/)).toBeTruthy();
    // 订阅证据不受本片影响，仍旧正常显示。
    expect(screen.getByText("¥1,200.00")).toBeTruthy();
  });

  it("支付失败读同一个 payments.daily 指标的 failed 桶", async () => {
    vi.stubGlobal(
      "fetch",
      mockFetch({
        metrics: {
          items: [
            paymentsDailyMetric({}, { by_status: { succeeded: { count: 4, amount_minor_units: 812000 }, failed: { count: 2, amount_minor_units: 5000 } } }),
          ],
        },
      }),
    );
    renderFinance();

    const tile = await screen.findByRole("heading", { name: "支付失败", level: 3 });
    const card = tile.closest("article") as HTMLElement;
    expect(within(card).getByText("¥50.00")).toBeTruthy();
    expect(within(card).getByText(/2 笔/)).toBeTruthy();
  });

  it("月累计：按天历史观测求和，只给「今天」一天时求和正确", async () => {
    // "月累计"读的是真实挂钟"今天"（见 businessTodayForTest 的说明），
    // 用相对当前真实日期算出来的历史夹具，不能写死某个假设的日历日。
    const today = businessTodayForTest();
    vi.stubGlobal("fetch", mockFetch({ history: { items: [historyItem(today)] } }));
    renderFinance();

    const tile = await screen.findByRole("heading", { name: "月累计", level: 3 });
    const card = tile.closest("article") as HTMLElement;
    // 月累计走独立的 /metrics/history Query，"月累计"这个标题在加载中态就
    // 已经存在（heading 本身不等数据）——用 findByText 等到金额真正落定。
    // 只给"今天"一天时是否 complete 取决于今天是不是当月 1 号（真实挂钟
    // 日期，测试跑的那天决定），这里不断言具体覆盖率文案——那部分已经在
    // metrics.test.ts 用完全固定的日期逐条覆盖过，这里只确认组件正确把
    // 查询结果接了进来。
    expect(await within(card).findByText("¥1,000.00")).toBeTruthy();
  });

  it("月累计：历史查询够不到月初时标记覆盖不全，而不是假装完整", async () => {
    const today = businessTodayForTest();
    const monthStart = `${today.slice(0, 7)}-01`;
    // 只给「今天」一天历史（模拟服务端 7 天硬顶够不到月初），月累计仍然给出
    // 这一天真实求和的下界，但必须诚实说"覆盖不全"——除非今天恰好就是
    // 月初第一天（totalDays===1），这种边界日跳过这条强断言，避免测试本身
    // 因为跑测试那天恰好是 1 号而变得不稳定。
    vi.stubGlobal("fetch", mockFetch({ history: { items: [historyItem(today)] } }));
    renderFinance();

    const tile = await screen.findByRole("heading", { name: "月累计", level: 3 });
    const card = tile.closest("article") as HTMLElement;
    expect(await within(card).findByText("¥1,000.00")).toBeTruthy();
    if (monthStart !== today) {
      expect(within(card).getByText("覆盖不全")).toBeTruthy();
    }
  });

  it("月累计完全没有可用历史样本时诚实显示未接入，不是 0", async () => {
    vi.stubGlobal("fetch", mockFetch({ history: { items: [] } }));
    renderFinance();

    const tile = await screen.findByRole("heading", { name: "月累计", level: 3 });
    expect(within(tile.closest("article") as HTMLElement).getByText("未接入")).toBeTruthy();
    expect(screen.queryByText("¥0.00")).toBeNull();
  });

  it("订阅指标部分可用且金额为零时保持未知，不显示 ¥0.00", async () => {
    vi.stubGlobal(
      "fetch",
      mockFetch({
        metrics: {
          items: [
            {
              metric_key: "newapi.subscription.daily",
              source: "newapi-prod",
              environment: "development",
              watermark: "day:2026-08-28 subscription:unavailable_over_http",
              value: { day: "2026-08-28", amount_minor_units: "0", currency: "CNY" },
              // 兼容旧生产者：is_partial 为真时即使 state 误留 fresh，界面也要降级为不完整。
              freshness: { ...fresh, is_partial: true },
            },
          ],
        },
      }),
    );
    renderFinance();

    expect(
      await screen.findByText("NewAPI 上游没有订阅订单端点；金额未知，不显示 0"),
    ).toBeTruthy();
    expect(screen.getByText("数据不完整")).toBeTruthy();
    expect(screen.getByText(/来源 newapi-prod/)).toBeTruthy();
    expect(screen.queryByText("¥0.00")).toBeNull();
  });

  it("部分订阅指标有非零金额时保留下界，并显示数据不完整", async () => {
    vi.stubGlobal(
      "fetch",
      mockFetch({
        metrics: {
          items: [
            {
              metric_key: "newapi.subscription.daily",
              source: "newapi-prod",
              environment: "development",
              watermark: "day:2026-08-28 subscription:partial",
              value: { day: "2026-08-28", amount_minor_units: "120000", currency: "CNY" },
              freshness: { ...fresh, is_partial: true, state: "partial" },
            },
          ],
        },
      }),
    );
    renderFinance();

    expect(await screen.findByText("¥1,200.00")).toBeTruthy();
    expect(screen.getByText("数据不完整")).toBeTruthy();
  });

  it("未初始化的 payments.daily 指标即使残留零值也保持未知", async () => {
    vi.stubGlobal(
      "fetch",
      mockFetch({
        metrics: {
          items: [
            paymentsDailyMetric(
              {
                watermark: "",
                freshness: {
                  ...fresh,
                  state: "uninitialized",
                  observed_at: null,
                  last_success: null,
                  staleness_seconds: null,
                },
              },
              { by_status: {} },
            ),
          ],
        },
      }),
    );
    renderFinance();

    const tile = await screen.findByRole("heading", { name: "区间到账", level: 3 });
    expect(within(tile.closest("article") as HTMLElement).getByText("未接入")).toBeTruthy();
    expect(screen.queryByText("¥0.00")).toBeNull();
  });

  it("同步失败但有上次成功值时保留金额，并让新鲜度表达失败", async () => {
    vi.stubGlobal(
      "fetch",
      mockFetch({
        metrics: {
          items: [
            paymentsDailyMetric({
              watermark: "day:2026-08-28",
              freshness: { ...fresh, state: "failed", staleness_seconds: 7200, last_error_code: "upstream_timeout" },
            }),
          ],
        },
      }),
    );
    renderFinance();

    const tile = await screen.findByRole("heading", { name: "区间到账", level: 3 });
    const card = tile.closest("article") as HTMLElement;
    expect(within(card).getByText("¥8,120.00")).toBeTruthy();
    expect(within(card).getByText("同步失败")).toBeTruthy();
  });

  it("周/月只显示不可用说明，并把日期与粒度保留在可分享 URL 控件（月累计不受影响，仍是当月）", async () => {
    vi.stubGlobal("fetch", mockFetch());
    renderFinance("orders", "/platforms/newapi?tab=finance&day=2026-08-28&granularity=week");

    expect(await screen.findByText("2026-08-24 ~ 2026-08-30 · 按周查看")).toBeTruthy();
    const heading = await screen.findByRole("heading", { name: "区间到账", level: 3 });
    expect(within(heading.closest("article") as HTMLElement).getByText(/周\/月需要按天聚合/)).toBeTruthy();
    // "月累计"是固定的自然月至今，不随 PeriodControls 的周/月切换而改变语义
    // （它本身没有"周/月不可用"这一说，一直都是当前自然月）。
    expect(await screen.findByRole("heading", { name: "月累计", level: 3 })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "月" }));
    expect(await screen.findByText("2026-08-01 ~ 2026-08-31 · 按月查看")).toBeTruthy();
  });

  it("利润明细只展示 NewAPI 行，金额走 scale-aware formatter，筛选在搜索前", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: string) => {
        if (input.includes("/finance/channels/summary")) {
          return Promise.resolve(
            response(200, {
              items: [
                {
                  id: "newapi-channel-1",
                  name: "Gemini 主渠道",
                  system_type: "newapi",
                  access_method: "upstream_key",
                  base_url: "https://google.example.test",
                  platform_id: "newapi-prod",
                  recharge_ratio: "1.0",
                  recharge_cost_rate: "1.0",
                  status: "active",
                  usage_revenue: { amount_minor: "100000000", currency: "CNY", scale: 6 },
                  supply_cost: { amount_minor: "70000000", currency: "CNY", scale: 6 },
                  gross_profit: { amount_minor: "30000000", currency: "CNY", scale: 6 },
                  gross_margin: "0.3",
                  coverage: { row_count: 1, revenue_known_rows: 1, cost_known_rows: 1, complete: true },
                  observed: { source: "finance-newapi", updated_at: "2026-08-28T10:00:00Z" },
                  runway: {},
                },
                {
                  id: "sub2api-channel-1",
                  name: "不应出现的 Sub2API 行",
                  system_type: "sub2api",
                  status: "active",
                },
              ],
              from: "2026-08-28",
              to: "2026-08-28",
            }),
          );
        }
        return Promise.resolve(response(404, { error: { code: "NOT_REGISTERED", message: "unknown" } }));
      }),
    );
    renderFinance("profit");

    expect(await screen.findByText("Gemini 主渠道")).toBeTruthy();
    expect(screen.queryByText("不应出现的 Sub2API 行")).toBeNull();
    expect(screen.getByText("¥100.00")).toBeTruthy();
    expect(screen.getByText("¥70.00")).toBeTruthy();
    expect(screen.getByText("¥30.00")).toBeTruthy();
    expect(screen.getByText("30.00%")).toBeTruthy();
    expect(screen.queryByText("毛利率 30.00%")).toBeNull();
    const toolbar = screen.getByRole("toolbar", { name: "利润核算明细筛选与搜索" });
    const controls = [...toolbar.querySelectorAll("select, input[type=search]")];
    expect(controls.at(-1)?.getAttribute("type")).toBe("search");
    expect(screen.getByRole("columnheader", { name: "渠道" })).toBeTruthy();
    expect(screen.getByRole("columnheader", { name: "上游成本" })).toBeTruthy();
    expect(screen.getByRole("columnheader", { name: "毛利率" })).toBeTruthy();
    fireEvent.change(screen.getByRole("combobox", { name: "接入方式筛选" }), {
      target: { value: "上游 Key" },
    });
    expect(await screen.findByText("Gemini 主渠道")).toBeTruthy();
  });

  it("首次读取失败显示可重试错误，重试成功后恢复利润空态", async () => {
    let attempts = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn((input: string) => {
        if (!input.includes("/finance/channels/summary")) return Promise.resolve(response(404, {}));
        attempts += 1;
        return attempts === 1
          ? Promise.resolve(response(503, { error: { code: "UPSTREAM_UNAVAILABLE", message: "暂时不可用" } }))
          : Promise.resolve(response(200, { items: [], from: "2026-08-28", to: "2026-08-28" }));
      }),
    );
    renderFinance("profit");
    expect(await screen.findByText("加载失败")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "重试" }));
    expect(await screen.findByText("暂无 NewAPI 利润明细")).toBeTruthy();
  });
});
