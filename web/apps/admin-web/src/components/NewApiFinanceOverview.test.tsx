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

describe("NewAPI 资金与订单", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("无指标时保留原型四卡、订单表头与空态，不编金额", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response(200, { items: [] }))));
    renderFinance();

    expect(await screen.findByText("暂无本平台的资金类指标")).toBeTruthy();
    for (const label of ["区间到账", "区间退款", "月累计", "支付失败"]) {
      const heading = await screen.findByRole("heading", { name: label, level: 3 });
      expect(within(heading.closest("article") as HTMLElement).getByText("—")).toBeTruthy();
    }
    const table = screen.getByRole("table", { name: "NewAPI 支付订单" });
    expect(within(table).getByRole("columnheader", { name: "订单号" })).toBeTruthy();
    expect(within(table).getByRole("columnheader", { name: "支付方式" })).toBeTruthy();
    expect(screen.getByText("充值订单尚未接入")).toBeTruthy();
  });

  it("单日充值指标按币种最小单位显示，并带新鲜度与来源", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          response(200, {
            items: [
              {
                metric_key: "newapi.recharge.daily",
                source: "newapi-prod",
                environment: "development",
                watermark: "wm-recharge",
                value: { day: "2026-08-28", amount_minor_units: "812000", currency: "CNY", order_count: "4" },
                freshness: fresh,
              },
              {
                metric_key: "newapi.subscription.daily",
                source: "newapi-prod",
                environment: "development",
                watermark: "wm-subscription",
                value: { day: "2026-08-28", amount_minor_units: "120000", currency: "CNY" },
                freshness: fresh,
              },
            ],
          }),
        ),
      ),
    );
    renderFinance();

    const tile = await screen.findByRole("heading", { name: "区间到账", level: 3 });
    const card = tile.closest("article") as HTMLElement;
    expect(within(card).getByText("¥8,120.00")).toBeTruthy();
    expect(within(card).getByText("数据新鲜")).toBeTruthy();
    expect(within(card).getByText(/来源 newapi-prod/)).toBeTruthy();
    expect(screen.getByText("¥1,200.00")).toBeTruthy();
    expect(screen.getByText(/充值是资金流入/)).toBeTruthy();
  });

  it("周/月只显示不可用说明，并把日期与粒度保留在可分享 URL 控件", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response(200, { items: [] }))));
    renderFinance("orders", "/platforms/newapi?tab=finance&day=2026-08-28&granularity=week");

    expect(await screen.findByText("2026-08-24 ~ 2026-08-30 · 按周查看")).toBeTruthy();
    const heading = await screen.findByRole("heading", { name: "区间到账", level: 3 });
    expect(within(heading.closest("article") as HTMLElement).getByText(/周\/月需要订单聚合端点/)).toBeTruthy();
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
    const toolbar = screen.getByRole("toolbar", { name: "利润核算明细筛选与搜索" });
    const controls = [...toolbar.querySelectorAll("select, input[type=search]")];
    expect(controls.at(-1)?.getAttribute("type")).toBe("search");
    expect(screen.getByRole("columnheader", { name: "渠道" })).toBeTruthy();
    expect(screen.getByRole("columnheader", { name: "上游成本" })).toBeTruthy();
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
