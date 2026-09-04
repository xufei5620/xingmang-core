import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CardsPanel } from "./CardsPanel";

vi.mock("../api/cards", async () => {
  const actual = await vi.importActual<typeof import("../api/cards")>("../api/cards");
  return {
    ...actual,
    listCards: vi.fn(),
    listCardOperationsNeedingAttention: vi.fn(),
    revealCard: vi.fn(),
    freezeCard: vi.fn(),
    unfreezeCard: vi.fn(),
    issueCard: vi.fn(),
  };
});

import {
  freezeCard,
  issueCard,
  listCardOperationsNeedingAttention,
  listCards,
  type CardItem,
  type CardOperationItem,
} from "../api/cards";

const activeCard: CardItem = {
  account: "MAIN",
  card_id: "card_1",
  mask: "533228******1234",
  holder_name: "ZHANG WEI",
  card_alias: "xm-abc123",
  status: "active",
  currency: "USD",
  balance_minor: 1234,
  owner_ref: "ops-team",
  freshness: { synced_at: "2026-09-04T11:59:00Z", age_seconds: 60, stale: false, never_synced: false },
};

function renderPanel() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <CardsPanel />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.clearAllMocks();
});

describe("CardsPanel", () => {
  it("列出卡片时只显示掩码卡号", async () => {
    vi.mocked(listCards).mockResolvedValue({ cards: [activeCard], accounts: ["MAIN", "BACKUP"] });
    vi.mocked(listCardOperationsNeedingAttention).mockResolvedValue([]);

    renderPanel();

    expect(await screen.findByText("533228******1234")).toBeTruthy();
    // 完整卡号不该出现在列表里——它根本不在读端点的响应里
    expect(screen.queryByText(/5332281234561234/)).toBeNull();
  });

  it("数据陈旧时显示提示，不把旧数字冒充实时", async () => {
    vi.mocked(listCards).mockResolvedValue({
      cards: [
        {
          ...activeCard,
          freshness: {
            synced_at: "2026-09-04T10:00:00Z",
            age_seconds: 7200,
            stale: true,
            never_synced: false,
          },
        },
      ],
      accounts: ["MAIN"],
    });
    vi.mocked(listCardOperationsNeedingAttention).mockResolvedValue([]);

    renderPanel();

    expect(await screen.findByText("数据可能过期")).toBeTruthy();
  });

  it("从未同步与「很久没同步」显示成两回事", async () => {
    vi.mocked(listCards).mockResolvedValue({
      cards: [{ ...activeCard, freshness: { age_seconds: 0, stale: true, never_synced: true } }],
      accounts: ["MAIN"],
    });
    vi.mocked(listCardOperationsNeedingAttention).mockResolvedValue([]);

    renderPanel();

    expect(await screen.findByText("从未同步")).toBeTruthy();
    expect(screen.queryByText("数据可能过期")).toBeNull();
  });

  it("有待人工确认的操作时亮出横幅并劝阻重试", async () => {
    const stuck: CardOperationItem = {
      idempotency_key: "issue-1",
      account: "MAIN",
      kind: "issue",
      state: "unknown",
      card_alias: "xm-abc123",
      amount: "10",
      token_type: "USDT",
      reason: "已超过宽限期仍未查到卡",
      started_at: "2026-09-04T11:00:00Z",
      retry_allowed: false,
    };
    vi.mocked(listCards).mockResolvedValue({ cards: [], accounts: ["MAIN"] });
    vi.mocked(listCardOperationsNeedingAttention).mockResolvedValue([stuck]);

    renderPanel();

    const banner = await screen.findByRole("alert");
    expect(banner.textContent).toContain("1 笔操作需要人工确认");
    // 最要紧的一句：确认之前别重试
    expect(banner.textContent).toContain("不要重试");
    expect(banner.textContent).toContain("issue-1");
  });

  it("没有待处置操作时不显示横幅", async () => {
    vi.mocked(listCards).mockResolvedValue({ cards: [activeCard], accounts: ["MAIN", "BACKUP"] });
    vi.mocked(listCardOperationsNeedingAttention).mockResolvedValue([]);

    renderPanel();

    await screen.findByText("533228******1234");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  // 明文落库之后（产品负责人 2026-09-04 决定），卡号直接来自列表响应，
  // 不再走一次性 reveal。后端按 card.reveal 权限决定回不回明文——
  // 前端只显示它拿到的，不做「本地隐藏」那种假控制。
  it("拿到明文时列表直接显示完整卡号", async () => {
    vi.mocked(listCards).mockResolvedValue({
      cards: [{ ...activeCard, pan: "4413571234567843", cvv: "123", expiry_mmyy: "1229" }],
      accounts: ["MAIN"],
    });
    vi.mocked(listCardOperationsNeedingAttention).mockResolvedValue([]);

    renderPanel();

    expect(await screen.findByText("4413571234567843")).toBeTruthy();
    expect(screen.getByRole("button", { name: "复制" })).toBeTruthy();
  });

  // 没有明文（缺 card.reveal 权限，或卡还没 active 拉不到）时回落到掩码，
  // 且不该出现复制按钮——复制一个掩码没有意义。
  it("没有明文时回落到掩码", async () => {
    vi.mocked(listCards).mockResolvedValue({ cards: [activeCard], accounts: ["MAIN"] });
    vi.mocked(listCardOperationsNeedingAttention).mockResolvedValue([]);

    renderPanel();

    expect(await screen.findByText("533228******1234")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "复制" })).toBeNull();
  });

  it("冻结走 Action 且每次带一个幂等键", async () => {
    vi.mocked(listCards).mockResolvedValue({ cards: [activeCard], accounts: ["MAIN", "BACKUP"] });
    vi.mocked(listCardOperationsNeedingAttention).mockResolvedValue([]);
    vi.mocked(freezeCard).mockResolvedValue({ runId: "run-1", result: {} });

    renderPanel();

    fireEvent.click(await screen.findByRole("button", { name: "冻结" }));

    await waitFor(() => expect(freezeCard).toHaveBeenCalledTimes(1));
    const params = vi.mocked(freezeCard).mock.calls[0]?.[0];
    expect(params?.card_id).toBe("card_1");
    // 账号必须跟着这张卡走，不能落到「第一个账号」
    expect(params?.account).toBe("MAIN");
    expect(params?.idempotency_key).toBeTruthy();
  });

  it("已冻结的卡显示解冻而不是冻结", async () => {
    vi.mocked(listCards).mockResolvedValue({
      cards: [{ ...activeCard, status: "frozen" }],
      accounts: ["MAIN"],
    });
    vi.mocked(listCardOperationsNeedingAttention).mockResolvedValue([]);

    renderPanel();

    expect(await screen.findByRole("button", { name: "解冻" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "冻结" })).toBeNull();
  });

  // 两个账号可以持有**同一个上游卡 id**（投影表的唯一键就是
  // (environment, account, upstream_card_id)）。表格的 rowKey 只用 card_id
  // 会让 React key 撞车，把一行渲染两遍——本地跑起来才发现的，
  // 因为 fake 模式下两个账号的替身各自从 1 开始编号。
  it("两个账号的同名卡 id 各渲染一行", async () => {
    vi.mocked(listCards).mockResolvedValue({
      cards: [
        { ...activeCard, account: "CHRIS", card_id: "same-id", holder_name: "ZHANG WEI" },
        { ...activeCard, account: "LINFENG", card_id: "same-id", holder_name: "LI FANG" },
      ],
      accounts: ["CHRIS", "LINFENG"],
    });
    vi.mocked(listCardOperationsNeedingAttention).mockResolvedValue([]);

    renderPanel();

    // 两个持卡人都在 = 两行都渲染了。不断言账号文本：筛选下拉里也有
    // 同名 option，会匹配到多个元素。
    expect(await screen.findByText("ZHANG WEI")).toBeTruthy();
    expect(screen.getByText("LI FANG")).toBeTruthy();
  });

  // 账号列表是异步到达的（跟卡片列表同一个查询）。表单的账号初值若只在
  // 首次渲染时取一次，就会永远停在空——不改选择直接提交会带一个空账号，
  // 后端虽然会拒（fail closed 生效），但表单本身是坏的。
  // 这个也是本地跑起来才看见的：单测里查询是同步 resolve 的，看不出来。
  it("开卡表单默认选中第一个账号", async () => {
    vi.mocked(listCards).mockResolvedValue({
      cards: [],
      accounts: ["CHRIS", "LINFENG"],
    });
    vi.mocked(listCardOperationsNeedingAttention).mockResolvedValue([]);
    vi.mocked(issueCard).mockResolvedValue({ runId: "run-1", result: {} });

    renderPanel();

    // 先等账号清单到达——真实路径是页面加载完再开表单。
    // 不等的话测的是「加载中就提交」那个不现实的竞态。
    await screen.findByText(/还没有卡片/);

    fireEvent.click(screen.getByRole("button", { name: "开卡" }));

    fireEvent.change(screen.getByLabelText("充值金额"), { target: { value: "10" } });
    fireEvent.change(screen.getByLabelText("企业成员邮箱"), { target: { value: "a@b.com" } });
    fireEvent.change(screen.getByLabelText("持卡人姓名"), { target: { value: "X Y" } });
    fireEvent.click(screen.getByRole("button", { name: "确认开卡" }));

    await waitFor(() => expect(issueCard).toHaveBeenCalledTimes(1));
    const params = vi.mocked(issueCard).mock.calls[0]?.[0];
    expect(params?.account).toBe("CHRIS");
  });
});
