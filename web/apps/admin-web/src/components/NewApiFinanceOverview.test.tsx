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

/** 按 URL 路由到 /metrics 或 /orders 两条通道各自的响应——两条通道现在
 *  会在同一次渲染里都被发起请求（资金概览卡走 /metrics，订单台账走
 *  /orders），共用一个不分辨 URL 的假 fetch 会让其中一条拿到另一条的形状,
 *  这不是被测代码的问题。 */
function mockFetch({
  metrics = { items: [] },
  orders = emptyOrdersPage(),
}: { metrics?: unknown; orders?: unknown } = {}) {
  return vi.fn((input: string) => {
    if (input.includes("/orders")) return Promise.resolve(response(200, orders));
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

describe("NewAPI 资金与订单", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("无指标与无订单时六卡与订单表都显示未接入/不适用，不编金额", async () => {
    vi.stubGlobal("fetch", mockFetch());
    renderFinance();

    for (const label of ["区间成功到账", "区间待处理", "区间失败", "支付手续费", "净现金流入"]) {
      const heading = await screen.findByRole("heading", { name: label, level: 3 });
      expect(within(heading.closest("article") as HTMLElement).getByText("未接入")).toBeTruthy();
    }
    // NewAPI 的退款与冲正恒为「不适用」——不是因为这次没数据，是这个上游
    // 结构上就没有退款概念（XM-PAY0 交接文档）。
    const refundHeading = await screen.findByRole("heading", { name: "退款与冲正", level: 3 });
    expect(within(refundHeading.closest("article") as HTMLElement).getByText("不适用")).toBeTruthy();
    expect(screen.queryByText("¥0.00")).toBeNull();

    // 没有订单时 DataTableV2 展示空态而不是一张空表格（rows.length === 0 分支），
    // 所以这里断言空态文案，不去找一张不存在的 table。
    expect(await screen.findByText("这个窗口没有充值订单")).toBeTruthy();
    expect(screen.getByText(/已读取 payments.read.v1/)).toBeTruthy();
  });

  it("单日 newapi.payments.daily 指标按最小单位显示，并带新鲜度与来源", async () => {
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

    const tile = await screen.findByRole("heading", { name: "区间成功到账", level: 3 });
    const card = tile.closest("article") as HTMLElement;
    expect(within(card).getByText("¥8,120.00")).toBeTruthy();
    expect(within(card).getByText(/4 笔/)).toBeTruthy();
    expect(within(card).getByText("数据新鲜")).toBeTruthy();
    expect(within(card).getByText(/来源 newapi-prod/)).toBeTruthy();
    // 订阅证据不受本片影响，仍旧正常显示。
    expect(screen.getByText("¥1,200.00")).toBeTruthy();
  });

  it("支付手续费恒为未知（NewAPI 没有第二个金额字段），净现金流入恒为未接入", async () => {
    vi.stubGlobal("fetch", mockFetch({ metrics: { items: [paymentsDailyMetric()] } }));
    renderFinance();

    const feeHeading = await screen.findByRole("heading", { name: "支付手续费", level: 3 });
    expect(within(feeHeading.closest("article") as HTMLElement).getByText("未接入")).toBeTruthy();
    const netHeading = screen.getByRole("heading", { name: "净现金流入", level: 3 });
    expect(within(netHeading.closest("article") as HTMLElement).getByText(/净现金流公式尚未确定/)).toBeTruthy();
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

    const tile = await screen.findByRole("heading", { name: "区间成功到账", level: 3 });
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

    const tile = await screen.findByRole("heading", { name: "区间成功到账", level: 3 });
    const card = tile.closest("article") as HTMLElement;
    expect(within(card).getByText("¥8,120.00")).toBeTruthy();
    expect(within(card).getByText("同步失败")).toBeTruthy();
  });

  it("周/月只显示不可用说明，并把日期与粒度保留在可分享 URL 控件", async () => {
    vi.stubGlobal("fetch", mockFetch());
    renderFinance("orders", "/platforms/newapi?tab=finance&day=2026-08-28&granularity=week");

    expect(await screen.findByText("2026-08-24 ~ 2026-08-30 · 按周查看")).toBeTruthy();
    const heading = await screen.findByRole("heading", { name: "区间成功到账", level: 3 });
    expect(within(heading.closest("article") as HTMLElement).getByText(/周\/月需要按天聚合/)).toBeTruthy();
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
