import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
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

  /** XM-SUB2API-PROFIT：这一格从 2026-09-07 起不再是占位。
   *
   *  这条用例在旧实现（`pending("利润核算", …)`）下必然红：占位不发请求、
   *  也不会渲染任何渠道行。断言表格里真的出现了 Sub2API 的渠道名，
   *  正是「接线了没有」这件事本身。 */
  it("Sub2API 的 profit 子页签渲染真实利润表，且只显示 Sub2API 的行", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: string) =>
        input.includes("/finance/channels/summary")
          ? Promise.resolve(
              response(200, {
                items: [
                  {
                    id: "s2-ch-1",
                    name: "Claude 转售渠道",
                    system_type: "sub2api",
                    access_method: "subscription_account",
                    base_url: "https://claude.sub2api.example.test",
                    status: "active",
                    usage_revenue: { amount_minor: "200000000", currency: "CNY", scale: 6 },
                    supply_cost: { amount_minor: "150000000", currency: "CNY", scale: 6 },
                    gross_profit: { amount_minor: "50000000", currency: "CNY", scale: 6 },
                    gross_margin: "0.25",
                    coverage: { row_count: 1, revenue_known_rows: 1, cost_known_rows: 1, complete: true },
                    observed: { source: "finance-collect", updated_at: "2026-08-28T10:00:00Z" },
                    runway: {},
                  },
                  {
                    id: "na-ch-1",
                    name: "不应出现的 NewAPI 行",
                    system_type: "newapi",
                    status: "active",
                    usage_revenue: { amount_minor: "100000000", currency: "CNY", scale: 6 },
                  },
                ],
                from: "2026-08-28",
                to: "2026-08-28",
              }),
            )
          : Promise.resolve(response(404, { error: { code: "NOT_REGISTERED", message: "unknown" } })),
      ),
    );
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <MemoryRouter initialEntries={["/platforms/sub2api?tab=finance&sub=profit&day=2026-08-28"]}>
        <QueryClientProvider client={queryClient}>{financeSubTab("sub2api", "profit")}</QueryClientProvider>
      </MemoryRouter>,
    );

    const table = await screen.findByRole("table", { name: "Sub2API 渠道利润核算" });
    expect(within(table).getByText("Claude 转售渠道")).not.toBeNull();
    expect(within(table).getByText("¥200.00")).not.toBeNull();
    expect(within(table).getByText("25.00%")).not.toBeNull();
    expect(within(table).queryByText("不应出现的 NewAPI 行")).toBeNull();
    expect(within(table).queryByText("¥100.00")).toBeNull();
    // 旧占位的两句招牌文案不该再出现——尤其是那句把供数指向
    // finance.profit-daily 的话：本片读的是 finance/channels/summary。
    expect(screen.queryByText(/接线随第 5 片/)).toBeNull();
    expect(screen.queryByText(/finance\.profit-daily 端点已有/)).toBeNull();
    vi.unstubAllGlobals();
  });

  it("认不出的子页签 id 仍回落 undefined，交给调用方处理", () => {
    expect(financeSubTab("sub2api", "不存在的子页签")).toBeUndefined();
    expect(financeSubTab("newapi", "不存在的子页签")).toBeUndefined();
  });
});
