import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { CardWorkbench } from "./CardWorkbench";

vi.mock("../api/cards", async () => {
  const actual = await vi.importActual<typeof import("../api/cards")>("../api/cards");
  return {
    ...actual,
    listCards: vi.fn(),
    listCardOperationsNeedingAttention: vi.fn(),
    listCardBalances: vi.fn(),
    listCardChallenges: vi.fn(),
    listCardTransactions: vi.fn(),
  };
});

import {
  listCardBalances,
  listCardChallenges,
  listCardOperationsNeedingAttention,
  listCardTransactions,
  listCards,
  type CardItem,
} from "../api/cards";

function card(over: Partial<CardItem> & { card_id: string }): CardItem {
  return {
    account: "LINFENG",
    mask: "441357******9228",
    holder_name: "Claude",
    card_alias: "xm-e91ba7f121a23c63",
    status: "active",
    currency: "USD",
    balance_minor: 100,
    renewal_risk: "none",
    freshness: { synced_at: "2026-09-05T00:00:00Z", age_seconds: 10, stale: false, never_synced: false },
    ...over,
  } as CardItem;
}

function renderAt(path: string) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/cards" element={<CardWorkbench />} />
          <Route path="/cards/:account/:cardId" element={<CardWorkbench />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function seed(cards: CardItem[]) {
  vi.mocked(listCards).mockResolvedValue({ cards, accounts: ["LINFENG"], memberEmails: [] });
  vi.mocked(listCardOperationsNeedingAttention).mockResolvedValue([]);
  vi.mocked(listCardBalances).mockResolvedValue([]);
  vi.mocked(listCardChallenges).mockResolvedValue([]);
  vi.mocked(listCardTransactions).mockResolvedValue([]);
}

afterEach(() => vi.clearAllMocks());

// 左右分栏：左侧清单、右侧选中卡的详情，同屏可见。
//
// 产品负责人 2026-09-05 要求照 Infini 后台的形态做（他每天在用那个）。
it("左侧列出全部卡，右侧显示选中卡的详情", async () => {
  seed([
    card({ card_id: "c1", card_alias: "Two.V", mask: "441357******2650" }),
    card({ card_id: "c2", card_alias: "flower", mask: "441357******8817" }),
  ]);
  renderAt("/cards");

  // 两张都在左侧清单里。
  expect(await screen.findByRole("button", { name: /Two\.V/ })).toBeTruthy();
  expect(screen.getByRole("button", { name: /flower/ })).toBeTruthy();

  // 没指定时默认选中第一张，右侧直接有内容——空右栏会让人以为页面坏了。
  expect(screen.getByRole("heading", { name: /Two\.V/ })).toBeTruthy();
});

// **选中哪张卡写进路由。**
//
// 这是我们比 Infini 多做的一点：他们的右栏选中态只活在内存里，刷新就回到
// 第一张，也没法把「这张卡」发给同事。ADMIN-IA §3 要求主对象可链接，
// 产品负责人推翻的是它的视觉形态（左右分栏），不是这条实质。
it("点左侧一张卡会把它写进路由", async () => {
  seed([
    card({ card_id: "c1", card_alias: "Two.V" }),
    card({ card_id: "c2", card_alias: "flower" }),
  ]);
  renderAt("/cards");

  fireEvent.click(await screen.findByRole("button", { name: /flower/ }));
  expect(screen.getByRole("heading", { name: /flower/ })).toBeTruthy();
});

// 直接粘链接进来就选中那张卡，不是第一张。
it("路由里带卡片时直接选中它", async () => {
  seed([
    card({ card_id: "c1", card_alias: "Two.V" }),
    card({ card_id: "c2", card_alias: "flower" }),
  ]);
  renderAt("/cards/LINFENG/c2");

  expect(await screen.findByRole("heading", { name: /flower/ })).toBeTruthy();
});

// 四个动作贴着余额，和 Infini 一样一眼可达。
it("充值、赎回、锁定、关停就在余额旁边", async () => {
  seed([card({ card_id: "c1", card_alias: "Two.V" })]);
  renderAt("/cards");

  await screen.findByRole("heading", { name: /Two\.V/ });
  for (const name of ["充值", "赎回", "锁定", "关停"]) {
    expect(screen.getByRole("button", { name })).toBeTruthy();
  }
});

// 一张卡都没有时不画空的分栏骨架。
it("没有卡片时给出可操作的空态", async () => {
  seed([]);
  renderAt("/cards");

  expect(await screen.findByText(/还没有卡片/)).toBeTruthy();
});

// 卡面明文与开卡时间在右栏直接可见，不藏在任何折叠或「列管理」后面。
//
// 这条从删掉的 CardsPanel.test.tsx 搬过来：产品负责人 2026-09-05 明确要求
// CVV 与有效期默认显示，理由是卡号本来就整串显示着，藏另外两样是半拉子
// 措施。表格没了，断言的对象换成右栏的卡片信息，要守的事情没变。
it("卡面明文与开卡时间在右栏直接可见", async () => {
  seed([
    card({
      card_id: "c1",
      card_alias: "Two.V",
      pan: "4413576524979228",
      cvv: "631",
      expiry_mmyy: "12/2031",
      issued_at: "2026-09-04T07:36:02Z",
      user_email: "xufei5620136@gmail.com",
    }),
  ]);
  renderAt("/cards");

  await screen.findByRole("heading", { name: /Two\.V/ });
  expect(screen.getAllByText("4413576524979228").length).toBeGreaterThan(0);
  expect(screen.getByText("631")).toBeTruthy();
  expect(screen.getAllByText("12/2031").length).toBeGreaterThan(0);
  // 开卡时间要到秒并带时区——截图发给另一个时区的人不能变成错的。
  expect(screen.getByText("2026-09-04 07:36:02 UTC")).toBeTruthy();
  // 字段命名与 Infini 后台一致：持卡人姓名 / 持卡人邮箱。
  expect(screen.getByText("持卡人姓名")).toBeTruthy();
  expect(screen.getByText("持卡人邮箱")).toBeTruthy();
});

// 两个账号可以持有同一个上游卡 id（投影表唯一键是
// environment+account+upstream_card_id）。清单的 key 只用 card_id 会撞车。
it("两个账号的同名卡 id 各占一行", async () => {
  seed([
    card({ account: "CHRIS", card_id: "same", card_alias: "A卡" }),
    card({ account: "LINFENG", card_id: "same", card_alias: "B卡" }),
  ]);
  renderAt("/cards");

  expect(await screen.findByRole("button", { name: /A卡/ })).toBeTruthy();
  expect(screen.getByRole("button", { name: /B卡/ })).toBeTruthy();
});

// 卡一多就得能找。分栏之后表格那套筛选没有了，搜索是替代品。
it("左栏可以按卡名搜索", async () => {
  seed([
    card({ card_id: "c1", card_alias: "Two.V" }),
    card({ card_id: "c2", card_alias: "flower" }),
  ]);
  renderAt("/cards");

  await screen.findByRole("button", { name: /Two\.V/ });
  fireEvent.change(screen.getByLabelText("搜索卡片"), { target: { value: "flow" } });

  expect(screen.queryByRole("button", { name: /Two\.V/ })).toBeNull();
  expect(screen.getByRole("button", { name: /flower/ })).toBeTruthy();
});

// 验证码紧跟在 CVV 后面。
//
// 产品负责人要的次序是「卡号 → 有效期 → CVV → 验证码」，也就是在线支付时
// 逐项填表的次序。表格删掉之后这条一度没落实：验证码只在信息栏**上方**的
// 高亮块里，人填到 CVV 还得往回跳。
it("卡片信息里验证码紧跟 CVV", async () => {
  seed([card({ card_id: "c1", card_alias: "Two.V", cvv: "631", expiry_mmyy: "12/2031" })]);
  vi.mocked(listCardChallenges).mockResolvedValue([
    { account: "LINFENG", card_id: "c1", id: "ch1", type: "3ds", code: "778899" },
  ] as never);
  renderAt("/cards");

  await screen.findByRole("heading", { name: /Two\.V/ });
  const labels = Array.from(document.querySelectorAll("dt")).map((d) => d.textContent ?? "");
  const at = (name: string) => labels.indexOf(name);

  expect(at("有效期")).toBeGreaterThanOrEqual(0);
  expect(at("CVV")).toBe(at("有效期") + 1);
  expect(at("验证码")).toBe(at("CVV") + 1);
  // 码本身也要在（不止是个标签）。
  expect(screen.getAllByText("778899").length).toBeGreaterThan(0);
});

// 账号卡片可以点：点谁就只看谁名下的卡。
//
// 产品负责人 2026-09-05：「卡片根据账户分开，点击不同的账户显示账户下的
// 卡片」。发现遍历上线后卡数从 2 张涨到十几张，两个账号的卡混在一列里，
// 而「这张卡的钱从哪个账号出」恰恰是操作前要先确定的事。
it("点账号只显示该账号名下的卡", async () => {
  seed([
    card({ account: "CHRIS", card_id: "c1", card_alias: "克里斯卡" }),
    card({ account: "LINFENG", card_id: "c2", card_alias: "林风卡" }),
  ]);
  renderAt("/cards?account=CHRIS");

  expect(await screen.findByRole("button", { name: /克里斯卡/ })).toBeTruthy();
  expect(screen.queryByRole("button", { name: /林风卡/ })).toBeNull();
});

// 不带 account 就是全部——一个默认只显示某个账号的页面会让人以为卡丢了。
it("不指定账号时两个账号的卡都在", async () => {
  seed([
    card({ account: "CHRIS", card_id: "c1", card_alias: "克里斯卡" }),
    card({ account: "LINFENG", card_id: "c2", card_alias: "林风卡" }),
  ]);
  renderAt("/cards");

  expect(await screen.findByRole("button", { name: /克里斯卡/ })).toBeTruthy();
  expect(screen.getByRole("button", { name: /林风卡/ })).toBeTruthy();
});

// 筛掉了当前选中的卡时，选中态跟着落到还看得见的第一张。
//
// 否则右栏会显示一张左边根本看不到的卡——那种不一致比空右栏更让人困惑。
it("按账号筛选后选中态落在可见的卡上", async () => {
  seed([
    card({ account: "CHRIS", card_id: "c1", card_alias: "克里斯卡" }),
    card({ account: "LINFENG", card_id: "c2", card_alias: "林风卡" }),
  ]);
  renderAt("/cards/LINFENG/c2?account=CHRIS");

  expect(await screen.findByRole("heading", { name: /克里斯卡/ })).toBeTruthy();
});
