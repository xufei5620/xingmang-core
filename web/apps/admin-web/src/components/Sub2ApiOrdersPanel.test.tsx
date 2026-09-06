import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import { Sub2ApiOrdersPanel } from "./Sub2ApiOrdersPanel";

function response(status: number, body: unknown): Response {
  return { ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function renderPanel(initialEntry = "/platforms/sub2api?tab=finance&sub=orders&day=2026-08-27") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <QueryClientProvider client={queryClient}>
        <Sub2ApiOrdersPanel />
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

function page(items: unknown[], overrides: Record<string, unknown> = {}) {
  return {
    items,
    next_cursor: "",
    stats_by_status: {},
    from: "2026-08-27",
    to: "2026-08-27",
    data_source: "sub2api-fake",
    freshness: fresh,
    ...overrides,
  };
}

describe("Sub2ApiOrdersPanel（XM-PAY1）", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("空窗口显示诚实空态，不是一张空表格", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response(200, page([])))));
    renderPanel();
    expect(await screen.findByText("这个窗口没有充值订单")).toBeTruthy();
  });

  it("渲染逐笔订单：金额按各自真实币种，手续费/净额分别显示已知值与恒定未接入", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          response(
            200,
            page([
              order(),
              order({
                order_id: "9002",
                status: "PENDING",
                amount: { minor_units: "5000", currency: "CNY" },
                fee: { minor_units: null, currency: "" },
                method: "wxpay",
              }),
            ]),
          ),
        ),
      ),
    );
    renderPanel();

    const table = await screen.findByRole("table", { name: "Sub2API 充值订单：金额、手续费、状态与创建时间" });
    expect(within(table).getByText("$100.00")).toBeTruthy(); // 9001 订单金额 USD
    expect(within(table).getByText("¥50.00")).toBeTruthy(); // 9002 订单金额 CNY（逐行真实币种）
    expect(within(table).getByText("$1.00")).toBeTruthy(); // 9001 手续费
    // 净额、到账两列恒为「未接入」——两行都是，不因为状态不同而有区别；
    // 2 行 × 2 列 = 4 处。9002 缺席的手续费走的是另一套措辞（"—"，见
    // amountBodyText），不计入这个数。
    expect(within(table).getAllByText("未接入")).toHaveLength(4);
  });

  it("加载更多沿用 next_cursor 原样回传，累积追加而不是替换", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("cursor=MTAw")) {
        return Promise.resolve(response(200, page([order({ order_id: "9002" })])));
      }
      return Promise.resolve(response(200, page([order({ order_id: "9001" })], { next_cursor: "MTAw" })));
    });
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();

    await screen.findByText("9001");
    const loadMore = await screen.findByRole("button", { name: "加载更多" });
    fireEvent.click(loadMore);

    await screen.findByText("9002");
    expect(screen.getByText("9001")).toBeTruthy(); // 第一页仍在，累积而非替换
    expect(
      fetchMock.mock.calls.some(([input]) => String(input).includes("cursor=MTAw")),
    ).toBe(true);
    expect(screen.queryByRole("button", { name: "加载更多" })).toBeNull(); // 第二页 next_cursor 为空，翻到底
  });

  it("状态筛选按归一化四桶分组，而不是原始状态字面量", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          response(
            200,
            page([
              order({ order_id: "9001", status: "PAID" }),
              order({ order_id: "9003", status: "REFUND_FAILED" }),
            ]),
          ),
        ),
      ),
    );
    renderPanel();

    await screen.findByText("9001");
    fireEvent.change(screen.getByRole("combobox", { name: "状态" }), { target: { value: "失败" } });
    // REFUND_FAILED 的展示状态是"退款失败"（含"失败"两个字），但它属于
    // "退款与冲正"桶——按"失败"筛选不该把它筛进来（子串碰撞回归测试）。
    expect(screen.queryByText("9003")).toBeNull();
  });

  it("详情链接带上订单所在的 UTC 业务日，供裸链接场景复原正确窗口", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response(200, page([order()])))));
    renderPanel();

    const link = await screen.findByRole("link", { name: "查看订单 9001 的详情" });
    expect(link.getAttribute("href")).toBe("/platforms/sub2api/finance/orders/9001?day=2026-08-27");
  });
});

describe("本区间汇总（XM-PAY-STATUS-ROLLUP）", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("按归一化分桶显示整个区间的笔数与金额，并说明它不随翻页变化", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          response(
            200,
            page([order()], {
              stats_by_status: {
                PAID: { count: 3, amount: { minor_units: "30000", currency: "CNY" } },
                SUCCESS: { count: 2, amount: { minor_units: "20000", currency: "CNY" } },
                PENDING: { count: 1, amount: { minor_units: "5000", currency: "CNY" } },
              },
            }),
          ),
        ),
      ),
    );

    renderPanel();

    const summary = (await screen.findByRole("heading", { name: "本区间汇总", level: 3 })).closest(
      "section",
    ) as HTMLElement;
    // 成功到账把 PAID 与 SUCCESS 合起来：5 笔、¥500.00。
    expect(within(summary).getByText("5")).toBeTruthy();
    expect(within(summary).getByText("¥500.00")).toBeTruthy();
    // 原始状态一并列出，归一化不掩盖上游说了什么。
    expect(within(summary).getByText("PAID、SUCCESS")).toBeTruthy();
    // 覆盖范围要说清楚：整个区间，不是已加载的那几页。
    expect(within(summary).getByText(/全部.*6.*笔订单/)).toBeTruthy();
  });

  it("上游出现没见过的状态时单独报出来，不悄悄丢掉", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          response(
            200,
            page([order()], {
              stats_by_status: {
                CHARGEBACK: { count: 2, amount: { minor_units: "700", currency: "CNY" } },
              },
            }),
          ),
        ),
      ),
    );

    renderPanel();

    const summary = (await screen.findByRole("heading", { name: "本区间汇总", level: 3 })).closest(
      "section",
    ) as HTMLElement;
    // 两处都要出现：顶部的警示条点名它，表格里也有一行「未知」承载它。
    expect(within(summary).getAllByText("CHARGEBACK").length).toBe(2);
    expect(within(summary).getByText(/尚未归类的状态/)).toBeTruthy();
    expect(within(summary).getByText("未知")).toBeTruthy();
  });

  it("整桶没有金额时显示「—」而不是 0", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          response(
            200,
            page([order()], {
              stats_by_status: { PENDING: { count: 4, amount: { minor_units: null, currency: "" } } },
            }),
          ),
        ),
      ),
    );

    renderPanel();

    const summary = (await screen.findByRole("heading", { name: "本区间汇总", level: 3 })).closest(
      "section",
    ) as HTMLElement;
    expect(within(summary).getByText("待处理")).toBeTruthy();
    expect(within(summary).getByText("4")).toBeTruthy();
    expect(within(summary).getByText("—")).toBeTruthy();
    expect(within(summary).queryByText("¥0.00")).toBeNull();
  });
});
