import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import { Sub2ApiRefundsPanel } from "./Sub2ApiRefundsPanel";

function response(status: number, body: unknown): Response {
  return { ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function renderPanel(initialEntry = "/platforms/sub2api?tab=finance&sub=refunds&day=2026-08-27") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <QueryClientProvider client={queryClient}>
        <Sub2ApiRefundsPanel />
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

const fresh = {
  state: "fresh",
  staleness_seconds: 5,
  threshold_seconds: 60,
  is_partial: false,
  observed_at: "2026-08-27T10:00:00Z",
  last_success: "2026-08-27T10:00:00Z",
  last_error_code: "",
};

function order(overrides: Record<string, unknown> = {}) {
  return {
    order_id: "9001",
    created_at: "2026-08-27T10:00:00Z",
    status: "PAID",
    amount: { minor_units: "10000", currency: "USD" },
    method: "alipay",
    user_ref: "a***@example.test",
    upstream_order_ref: "OUT-9001",
    fee: { minor_units: "100", currency: "USD" },
    refund_amount: { minor_units: "0", currency: "USD" },
    ...overrides,
  };
}

function page(items: unknown[]) {
  return {
    items,
    next_cursor: "",
    stats_by_status: {},
    from: "2026-08-27",
    to: "2026-08-27",
    data_source: "sub2api-fake",
    freshness: fresh,
  };
}

describe("Sub2ApiRefundsPanel（XM-PAY1）", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("从充值订单台账里只挑出退款生命周期状态的行，其余状态不出现", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          response(
            200,
            page([
              order({ order_id: "9001", status: "PAID" }), // 不该出现：不是退款状态
              order({
                order_id: "9003",
                status: "PARTIALLY_REFUNDED",
                amount: { minor_units: "20000", currency: "USD" },
                refund_amount: { minor_units: "5000", currency: "USD" },
              }),
              order({ order_id: "9004", status: "REFUNDED", refund_amount: { minor_units: "9900", currency: "USD" } }),
            ]),
          ),
        ),
      ),
    );
    renderPanel();

    await screen.findByText("9003");
    expect(screen.queryByText("9001")).toBeNull();
    expect(screen.getByText("9004")).toBeTruthy();
    expect(screen.getByText(/其中 2 笔处于退款生命周期/)).toBeTruthy();
  });

  it("原金额取订单面值，退款金额取实退——部分退款时两者不相等", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          response(
            200,
            page([
              order({
                order_id: "9003",
                status: "PARTIALLY_REFUNDED",
                amount: { minor_units: "20000", currency: "USD" },
                refund_amount: { minor_units: "5000", currency: "USD" },
              }),
            ]),
          ),
        ),
      ),
    );
    renderPanel();

    const table = await screen.findByRole("table", { name: "Sub2API 退款与冲正：原金额、退款金额与状态" });
    expect(within(table).getByText("$200.00")).toBeTruthy(); // 原金额（面值）
    expect(within(table).getByText("$50.00")).toBeTruthy(); // 退款金额（实退，小于面值）
  });

  it("这个窗口没有退款记录时显示诚实空态，不是空表格", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(response(200, page([order({ order_id: "9001", status: "PAID" })])))),
    );
    renderPanel();
    expect(await screen.findByText("这个窗口没有退款记录")).toBeTruthy();
  });

  it("详情链接指向 /finance/refunds/ 而不是 /finance/orders/，并带上业务日", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(response(200, page([order({ order_id: "9003", status: "REFUNDED" })]))),
      ),
    );
    renderPanel();

    const link = await screen.findByRole("link", { name: "查看订单 9003 的退款详情" });
    expect(link.getAttribute("href")).toBe("/platforms/sub2api/finance/refunds/9003?day=2026-08-27");
  });

  it("说明文案明说这一页不提供「直接退款」按钮", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response(200, page([])))));
    renderPanel();
    expect(await screen.findByText(/不会有「直接退款」的按钮/)).toBeTruthy();
  });
});
