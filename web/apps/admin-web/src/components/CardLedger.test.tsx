import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { CardLedger } from "./CardLedger";
import { CardSubscriptions } from "./CardSubscriptions";

vi.mock("../api/cards", async () => {
  const actual = await vi.importActual<typeof import("../api/cards")>("../api/cards");
  return { ...actual, listAllCardTransactions: vi.fn(), listCards: vi.fn() };
});

import { listAllCardTransactions, listCards, type CardItem } from "../api/cards";

function wrap(node: React.ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>{node}</MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => vi.clearAllMocks());

// 交易记录的列名逐字对齐 Infini 后台。
//
// 产品负责人 2026-09-05：「跟 infini 那边的字段对齐，这样使用上不会不清楚」。
// 他每天在两个界面之间切换，同一个东西两个名字是纯粹的认知税。
it("交易记录的列名与 Infini 一致", async () => {
  vi.mocked(listAllCardTransactions).mockResolvedValue([
    {
      account: "LINFENG",
      card_id: "c1",
      card_alias: "Two.V",
      type: "Consume",
      amount_minor: -20000,
      fee_minor: 0,
      currency: "USD",
      status: "Completed",
      merchant: "OPENAI",
      occurred_at: "2026-09-05T05:29:45Z",
    },
  ]);
  wrap(<CardLedger />);

  await screen.findByText("OPENAI");
  const headers = Array.from(document.querySelectorAll("th")).map((h) => h.textContent ?? "");
  for (const name of ["时间", "商户", "交易类型", "结算金额", "卡片名称", "手续费", "状态"]) {
    expect(headers.some((h) => h.includes(name))).toBe(true);
  }
  // 卡片名称是这张表的定位列。
  //
  // 排除 <option>：筛选下拉的选项文本与单元格文本相同，getByText 会撞到
  // 两个元素——这是本仓库反复踩过的一处（见 CardsPanel 的状态筛选）。
  const cells = screen
    .getAllByText("Two.V")
    .filter((el) => el.tagName !== "OPTION");
  expect(cells.length).toBeGreaterThan(0);
});

// 卡已关停、投影行没了时，流水仍在，卡名退回后四位。
//
// 钱确实花了——那条流水消失比显示一个裸 id 更糟。
it("卡片名称缺失时退回卡号后四位", async () => {
  vi.mocked(listAllCardTransactions).mockResolvedValue([
    {
      account: "LINFENG",
      card_id: "441357******9228",
      type: "Consume",
      amount_minor: -100,
      fee_minor: 0,
      currency: "USD",
      status: "Completed",
      merchant: "OLD",
      occurred_at: "2026-09-05T05:29:45Z",
    },
  ]);
  wrap(<CardLedger />);

  await screen.findByText("OLD");
  const shown = screen.getAllByText(/9228/).filter((el) => el.tagName !== "OPTION");
  expect(shown.length).toBeGreaterThan(0);
});

function card(over: Partial<CardItem>): CardItem {
  return {
    account: "LINFENG",
    card_id: "c1",
    mask: "441357******9228",
    holder_name: "Claude",
    card_alias: "小拙",
    status: "active",
    currency: "USD",
    balance_minor: 0,
    renewal_risk: "none",
    freshness: { synced_at: "2026-09-05T00:00:00Z", age_seconds: 1, stale: false, never_synced: false },
    ...over,
  } as CardItem;
}

// 订阅页签的数据来自**登记**，不是推断。
//
// Infini 那个页签自己标着「基于交易记录自动识别，仅供参考，可能与实际订阅
// 不一致」。我们不猜：只列人填过的，因此每一条都可信。
it("订阅列出已登记的服务与下次扣款日期", async () => {
  vi.mocked(listCards).mockResolvedValue({
    cards: [
      card({ service_name: "OPENAI *CHATGPT SUBSCR", subscription_amount: "20.00", subscription_cycle: "monthly", next_renewal_on: "2026-10-01" }),
      card({ card_id: "c2", card_alias: "flower", service_name: "ANTHROPIC* CLAUDE SUB", subscription_amount: "100.00", subscription_cycle: "monthly", next_renewal_on: "2026-09-09" }),
      // 没登记订阅的卡不出现在这一页。
      card({ card_id: "c3", card_alias: "裸卡" }),
    ],
    accounts: ["LINFENG"],
    memberEmails: [],
  });
  wrap(<CardSubscriptions />);

  await screen.findByText("ANTHROPIC* CLAUDE SUB");
  expect(screen.getByText("OPENAI *CHATGPT SUBSCR")).toBeTruthy();
  expect(screen.queryByText("裸卡")).toBeNull();

  // 快到期的排在前面：这一页的用处就是「接下来要扣什么钱」。
  const rows = Array.from(document.querySelectorAll("tbody tr")).map((r) => r.textContent ?? "");
  expect(rows[0]).toContain("ANTHROPIC");

  // 每月订阅支出：只把 monthly 的加起来，20 + 100 = 120。
  expect(screen.getByText(/120(\.00)?/)).toBeTruthy();
});

// 一条订阅都没登记时说清怎么补，而不是空表。
it("没有登记订阅时给出可操作的空态", async () => {
  vi.mocked(listCards).mockResolvedValue({
    cards: [card({})],
    accounts: ["LINFENG"],
    memberEmails: [],
  });
  wrap(<CardSubscriptions />);

  expect(await screen.findByText(/登记用途/)).toBeTruthy();
});
