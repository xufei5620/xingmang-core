import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import { PlatformOrderDetailPage, PlatformRefundDetailPage } from "./PlatformOrderDetailPage";

function fakeResponse(body: unknown, status = 200): Response {
  return { ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function renderPage(path: string) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/platforms/:serviceType/finance/orders/:orderId" element={<PlatformOrderDetailPage />} />
          <Route path="/platforms/:serviceType/finance/refunds/:orderId" element={<PlatformRefundDetailPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
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

function orderBody(overrides: Record<string, unknown> = {}) {
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

function detailBody(order: ReturnType<typeof orderBody>, overrides: Record<string, unknown> = {}) {
  return {
    order,
    from: "2026-08-27",
    to: "2026-08-27",
    data_source: "sub2api-fake",
    freshness: fresh,
    ...overrides,
  };
}

describe("PlatformOrderDetailPage / PlatformRefundDetailPage（XM-PAY1）", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("找到订单时显示订单金额、手续费与订单信息", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(fakeResponse(detailBody(orderBody())))));
    renderPage("/platforms/sub2api/finance/orders/9001?day=2026-08-27");

    expect(await screen.findByRole("heading", { name: "9001", level: 2 })).toBeTruthy();
    expect(screen.getByText("$100.00")).toBeTruthy(); // 订单金额
    expect(screen.getByText("$1.00")).toBeTruthy(); // 支付手续费
    expect(screen.getByText("alipay")).toBeTruthy();
    expect(screen.getByText("OUT-9001")).toBeTruthy();
    // 净入账恒为未接入
    expect(screen.getByText(/净现金流公式尚未确定/)).toBeTruthy();
  });

  it("未找到时展示原型同款的诚实「未找到」状态，不回退到别的订单", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          fakeResponse(
            { error: { code: "ACTION_NOT_REGISTERED", message: "订单 9999 在 2026-08-27 至 2026-08-27 窗口内未找到" } },
            404,
          ),
        ),
      ),
    );
    renderPage("/platforms/sub2api/finance/orders/9999?day=2026-08-27");

    expect(await screen.findByRole("heading", { name: "未找到订单", level: 2 })).toBeTruthy();
    expect(screen.getByText(/窗口内未找到/)).toBeTruthy();
    expect(screen.getByText(/没有回退到其他订单/)).toBeTruthy();
    // 不能把另一笔订单的字段渲染出来
    expect(screen.queryByText("$100.00")).toBeNull();
  });

  it("退款详情变体：4 格顺序把原金额/退款金额放前面，且带上退款说明", async () => {
    const refundOrder = orderBody({
      order_id: "9003",
      status: "PARTIALLY_REFUNDED",
      amount: { minor_units: "20000", currency: "USD" },
      refund_amount: { minor_units: "5000", currency: "USD" },
    });
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(fakeResponse(detailBody(refundOrder)))));
    renderPage("/platforms/sub2api/finance/refunds/9003?day=2026-08-27");

    expect(await screen.findByRole("heading", { name: "9003", level: 2 })).toBeTruthy();
    expect(screen.getByText("$200.00")).toBeTruthy(); // 原订单金额
    expect(screen.getByText("$50.00")).toBeTruthy(); // 退款金额
    // "上游没有独立的退款记录" 这句话在页头描述与底部说明里各出现一次，
    // 用底部说明特有的收尾句去唯一定位后者。
    expect(screen.getByText(/不在这里猜一个"看起来合理"的值/)).toBeTruthy();
  });

  it("退款变体未找到时返回目标是退款与冲正列表，不是充值订单列表", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          fakeResponse({ error: { code: "ACTION_NOT_REGISTERED", message: "未找到" } }, 404),
        ),
      ),
    );
    renderPage("/platforms/sub2api/finance/refunds/9999?day=2026-08-27");

    const back = await screen.findByRole("link", { name: "返回退款与冲正" });
    expect(back.getAttribute("href")).toBe("/platforms/sub2api?tab=finance&sub=refunds");
  });

  it("订单变体的返回链接指向充值订单，不是退款与冲正", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(fakeResponse(detailBody(orderBody())))));
    renderPage("/platforms/sub2api/finance/orders/9001?day=2026-08-27");

    const back = await screen.findByRole("link", { name: "返回充值订单" });
    expect(back.getAttribute("href")).toBe("/platforms/sub2api?tab=finance&sub=orders");
  });

  it("整组端点未挂载（裸 404）时走通用错误态，不是「未找到订单」", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse("404 page not found", 404))),
    );
    renderPage("/platforms/sub2api/finance/orders/9001?day=2026-08-27");

    // looksLikeUnmountedRoute 命中时 getPlatformOrder 会抛 FeatureNotMountedError，
    // 页面走 ApiStateView 的通用「未接入」错误态（PageState，不是 PageHeader，
    // 没有 heading 语义），不是订单详情自己的"未找到订单"文案。
    expect(await screen.findByText("未接入")).toBeTruthy();
    expect(screen.queryByText("未找到订单")).toBeNull();
    expect(screen.queryByText("$100.00")).toBeNull();
  });
});
