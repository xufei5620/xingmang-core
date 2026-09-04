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
  listCardOperationsNeedingAttention,
  listCards,
  revealCard,
  type CardItem,
  type CardOperationItem,
} from "../api/cards";

const activeCard: CardItem = {
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
    vi.mocked(listCards).mockResolvedValue([activeCard]);
    vi.mocked(listCardOperationsNeedingAttention).mockResolvedValue([]);

    renderPanel();

    expect(await screen.findByText("533228******1234")).toBeTruthy();
    // 完整卡号不该出现在列表里——它根本不在读端点的响应里
    expect(screen.queryByText(/5332281234561234/)).toBeNull();
  });

  it("数据陈旧时显示提示，不把旧数字冒充实时", async () => {
    vi.mocked(listCards).mockResolvedValue([
      {
        ...activeCard,
        freshness: { synced_at: "2026-09-04T10:00:00Z", age_seconds: 7200, stale: true, never_synced: false },
      },
    ]);
    vi.mocked(listCardOperationsNeedingAttention).mockResolvedValue([]);

    renderPanel();

    expect(await screen.findByText("数据可能过期")).toBeTruthy();
  });

  it("从未同步与「很久没同步」显示成两回事", async () => {
    vi.mocked(listCards).mockResolvedValue([
      { ...activeCard, freshness: { age_seconds: 0, stale: true, never_synced: true } },
    ]);
    vi.mocked(listCardOperationsNeedingAttention).mockResolvedValue([]);

    renderPanel();

    expect(await screen.findByText("从未同步")).toBeTruthy();
    expect(screen.queryByText("数据可能过期")).toBeNull();
  });

  it("有待人工确认的操作时亮出横幅并劝阻重试", async () => {
    const stuck: CardOperationItem = {
      idempotency_key: "issue-1",
      kind: "issue",
      state: "unknown",
      card_alias: "xm-abc123",
      amount: "10",
      token_type: "USDT",
      reason: "已超过宽限期仍未查到卡",
      started_at: "2026-09-04T11:00:00Z",
      retry_allowed: false,
    };
    vi.mocked(listCards).mockResolvedValue([]);
    vi.mocked(listCardOperationsNeedingAttention).mockResolvedValue([stuck]);

    renderPanel();

    const banner = await screen.findByRole("alert");
    expect(banner.textContent).toContain("1 笔操作需要人工确认");
    // 最要紧的一句：确认之前别重试
    expect(banner.textContent).toContain("不要重试");
    expect(banner.textContent).toContain("issue-1");
  });

  it("没有待处置操作时不显示横幅", async () => {
    vi.mocked(listCards).mockResolvedValue([activeCard]);
    vi.mocked(listCardOperationsNeedingAttention).mockResolvedValue([]);

    renderPanel();

    await screen.findByText("533228******1234");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("卡面明文只在取回后显示，关闭对话框即清空", async () => {
    vi.mocked(listCards).mockResolvedValue([activeCard]);
    vi.mocked(listCardOperationsNeedingAttention).mockResolvedValue([]);
    vi.mocked(revealCard).mockResolvedValue({
      Number: "5332281234561234",
      CVV: "123",
      ExpiryMMYY: "1229",
      Currency: "USD",
    });

    renderPanel();

    fireEvent.click(await screen.findByRole("button", { name: "查看卡面" }));
    fireEvent.click(await screen.findByRole("button", { name: "获取卡面信息" }));

    expect(await screen.findByText("5332281234561234")).toBeTruthy();

    // 关闭后明文必须从 DOM 里消失：它不该留在任何地方
    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
    await waitFor(() => {
      expect(screen.queryByText("5332281234561234")).toBeNull();
    });
  });

  it("冻结走 Action 且每次带一个幂等键", async () => {
    vi.mocked(listCards).mockResolvedValue([activeCard]);
    vi.mocked(listCardOperationsNeedingAttention).mockResolvedValue([]);
    vi.mocked(freezeCard).mockResolvedValue({ runId: "run-1", result: {} });

    renderPanel();

    fireEvent.click(await screen.findByRole("button", { name: "冻结" }));

    await waitFor(() => expect(freezeCard).toHaveBeenCalledTimes(1));
    const params = vi.mocked(freezeCard).mock.calls[0]?.[0];
    expect(params?.card_id).toBe("card_1");
    expect(params?.idempotency_key).toBeTruthy();
  });

  it("已冻结的卡显示解冻而不是冻结", async () => {
    vi.mocked(listCards).mockResolvedValue([{ ...activeCard, status: "frozen" }]);
    vi.mocked(listCardOperationsNeedingAttention).mockResolvedValue([]);

    renderPanel();

    expect(await screen.findByRole("button", { name: "解冻" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "冻结" })).toBeNull();
  });
});
