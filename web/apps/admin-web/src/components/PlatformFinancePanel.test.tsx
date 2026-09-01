import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { financeSubTab } from "./PlatformFinancePanel";

function response(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

const ORIGIN = "https://invoice.example.test";

/** CR-0005 平台线 g：Sub2API 的「开票」从「等 CR-0002」占位换成嵌入开票系统
 *  管理端；NewAPI 补第 3 个子页签「开票」（推翻 ADMIN-IA §8.2 #2「不补开票」
 *  的裁定）。两边内容都是 InvoiceConsolePanel，这里只断言接线正确——
 *  组件自身的门禁行为（scope/配置缺失）由 InvoiceConsolePanel.test.tsx 覆盖。 */
describe("financeSubTab · invoices（CR-0005）", () => {
  afterEach(() => {
    delete window.__XM_CONFIG__;
  });

  it("Sub2API 的 invoices 子页签渲染嵌入框，不再是 CR-0002 阻塞占位", () => {
    window.__XM_CONFIG__ = { invoiceConsoleOrigin: ORIGIN };
    render(<>{financeSubTab("sub2api", "invoices")}</>);
    const frame = screen.getByTitle("开票") as HTMLIFrameElement;
    expect(frame.tagName).toBe("IFRAME");
    expect(frame.src).toBe(`${ORIGIN}/embed/admin/sub2api`);
    // 旧占位的两句招牌文案不该再出现
    expect(screen.queryByText(/CR-0002/)).toBeNull();
    expect(screen.queryByText(/不得实现开票资格算法/)).toBeNull();
  });

  it("NewAPI 现在也认识 invoices 子页签（CR-0005 推翻「不补开票」）", () => {
    window.__XM_CONFIG__ = { invoiceConsoleOrigin: ORIGIN };
    render(<>{financeSubTab("newapi", "invoices")}</>);
    const frame = screen.getByTitle("开票") as HTMLIFrameElement;
    expect(frame.src).toBe(`${ORIGIN}/embed/admin/newapi`);
  });

  it("未配置来源时两个平台都显示未接入，不渲染 iframe", () => {
    expect(financeSubTab("sub2api", "invoices")).toBeDefined();
    render(<>{financeSubTab("sub2api", "invoices")}</>);
    expect(screen.getByText(/未配置开票控制台来源（XM_INVOICE_CONSOLE_ORIGIN）/)).not.toBeNull();
    expect(screen.queryByTitle("开票")).toBeNull();
  });

  it("其余子页签不受影响：Sub2API 的 orders 正常渲染充值订单台账（XM-PAY1）", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          response(200, {
            items: [],
            next_cursor: "",
            stats_by_status: {},
            from: "2026-08-27",
            to: "2026-08-27",
            data_source: "sub2api-fake",
            freshness: {
              state: "fresh",
              staleness_seconds: 1,
              threshold_seconds: 60,
              is_partial: false,
              observed_at: "2026-08-27T10:00:00Z",
              last_success: "2026-08-27T10:00:00Z",
              last_error_code: "",
            },
          }),
        ),
      ),
    );
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <MemoryRouter initialEntries={["/platforms/sub2api?tab=finance&sub=orders"]}>
        <QueryClientProvider client={queryClient}>{financeSubTab("sub2api", "orders")}</QueryClientProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByText("这个窗口没有充值订单")).not.toBeNull();
    vi.unstubAllGlobals();
  });

  it("认不出的子页签 id 仍回落 undefined，交给调用方处理", () => {
    expect(financeSubTab("sub2api", "不存在的子页签")).toBeUndefined();
    expect(financeSubTab("newapi", "不存在的子页签")).toBeUndefined();
  });
});
