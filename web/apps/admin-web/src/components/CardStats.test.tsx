import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { CardStats } from "./CardStats";

vi.mock("../api/cards", async () => {
  const actual = await vi.importActual<typeof import("../api/cards")>("../api/cards");
  return { ...actual, getCardStats: vi.fn() };
});

import { getCardStats, type CardStats as CardStatsData } from "../api/cards";

function renderPanel(account = "") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <CardStats account={account} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function seed(over: Partial<CardStatsData> = {}) {
  vi.mocked(getCardStats).mockResolvedValue({
    buckets: [],
    merchants: [],
    cards: [],
    undated_count: 0,
    ...over,
  });
}

afterEach(() => vi.clearAllMocks());

// **绝不跨币种相加。** 把 USD 和 PHP 加到一起得出的数没有任何意义，
// 却会被当成钱——而且它长得和一个正确的金额一模一样。
it("两个币种分成两组，各自汇总", async () => {
  seed({
    buckets: [
      { currency: "USD", type: "consume", status: "completed", count: 2, amount_minor: -1000, fee_minor: 0 },
      { currency: "EUR", type: "consume", status: "completed", count: 1, amount_minor: -50000, fee_minor: 0 },
    ],
  });
  renderPanel();

  expect(await screen.findByText("USD")).toBeTruthy();
  expect(screen.getByText("EUR")).toBeTruthy();
  // 各自的已完成消费分别显示，没有出现一个合并后的数。
  expect(screen.getByText("$10.00")).toBeTruthy();
  expect(screen.getByText("€500.00")).toBeTruthy();
});

// 没登记过的币种**不猜小数位**，如实标注出来。
//
// 猜一个 2 位除下去，一笔 50000 最小单位会显示成 500.00——如果那个币种其实
// 没有小数位（日元那种），这个数就小了 100 倍，而它看起来完全正常。
// 这条在这里钉一遍，是因为统计页是最容易被人直接拿去当账用的地方。
it("未登记的币种如实标注金额单位未知，不按 2 位硬除", async () => {
  seed({
    buckets: [
      { currency: "PHP", type: "consume", status: "completed", count: 1, amount_minor: -50000, fee_minor: 0 },
    ],
  });
  renderPanel();

  // 六个磁贴都用同一个币种，所以标注会出现多次——这里要的是「它出现了」。
  expect((await screen.findAllByText(/金额单位未知/)).length).toBeGreaterThan(0);
  expect(screen.getByText(/PHP 50,000（最小单位/)).toBeTruthy();
  expect(screen.queryByText("₱500.00")).toBeNull();
});

// 授权中的金额**还会变**，不能算进「已完成消费」。
//
// 算进去的话这个数随后会自己缩水，而缩水的时刻没有任何提示——
// 有人拿它做过预算之后才发现对不上。
it("授权中的消费不计入已完成消费，单独一格", async () => {
  seed({
    buckets: [
      { currency: "USD", type: "consume", status: "completed", count: 1, amount_minor: -1000, fee_minor: 0 },
      { currency: "USD", type: "consume", status: "authorized", count: 1, amount_minor: -2500, fee_minor: 0 },
    ],
  });
  renderPanel();

  await screen.findByText("USD");
  const done = screen.getByText("已完成消费").closest("div");
  expect(done?.textContent).toContain("$10.00");
  const pending = screen.getByText("授权中（金额还会变）").closest("div");
  expect(pending?.textContent).toContain("$25.00");
});

// 上游没给发生时间的流水在按月统计里会凭空消失。**消失的钱是查不出来的**，
// 所以它必须被说出来，而不是悄悄丢掉。
it("有没能计入期间的流水时把笔数说出来", async () => {
  seed({
    buckets: [
      { currency: "USD", type: "consume", status: "completed", count: 1, amount_minor: -1000, fee_minor: 0 },
    ],
    undated_count: 3,
  });
  renderPanel();

  expect(await screen.findByText(/另有 3 笔流水上游没有给发生时间/)).toBeTruthy();
});

// 没有这种流水时不该出现那句话——一句恒常显示的提示会被当成背景噪音，
// 真出问题时也就没人看了。
it("没有漏计的流水时不显示那句提示", async () => {
  seed({
    buckets: [
      { currency: "USD", type: "consume", status: "completed", count: 1, amount_minor: -1000, fee_minor: 0 },
    ],
    undated_count: 0,
  });
  renderPanel();

  await screen.findByText("USD");
  expect(screen.queryByText(/没有给发生时间/)).toBeNull();
});

// 账号筛选要真的传到服务端去，而不是拉全量再在前端过滤：
// 前端过滤的那份是 limit 截断过的，过滤完只会更少。
it("把账号作为查询参数传给服务端", async () => {
  seed();
  renderPanel("CHRIS");

  await vi.waitFor(() =>
    expect(vi.mocked(getCardStats).mock.calls[0]?.[0]).toMatchObject({ account: "CHRIS" }),
  );
});
