import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChannelsPanel } from "./ChannelsPanel";
import { NewApiChannelsPanel } from "./NewApiChannelsPanel";

const freshness = {
  state: "fresh",
  staleness_seconds: 10,
  threshold_seconds: 1800,
  is_partial: false,
  observed_at: "2026-08-28T09:00:00Z",
  last_success: "2026-08-28T09:00:00Z",
  last_error_code: "",
};

function metric(metricKey: string, value: Record<string, unknown>) {
  return {
    metric_key: metricKey,
    value,
    freshness,
    source: "collector",
    watermark: "wm-20260828T090000Z",
  };
}

function fakeResponse(body: unknown): Response {
  return { ok: true, status: 200, json: () => Promise.resolve(body) } as unknown as Response;
}

function stubMetrics(items: unknown[]) {
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(fakeResponse({ items }))));
}

function renderPanel(node: React.ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>{node}</MemoryRouter>
    </QueryClientProvider>,
  );
}

async function findTable() {
  return within(await screen.findByRole("table"));
}

describe("渠道管理按 §9.5 收窄", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("Sub2API：说明这张表只做单账号 / 单 Key 核算，并给出去上游管理的入口", async () => {
    // 收窄的关键不在列怎么排，而在于把这句话说出来：同一个上游下的三条渠道
    // 各显示一份余额，不写清楚「共享」的人会把它加三遍
    stubMetrics([
      metric("sub2api.channels.balance", {
        channels: [
          { channel_id: "ch-1", channel_name: "OpenAI 主", balance_minor_units: 12345, currency: "CNY" },
        ],
      }),
    ]);
    renderPanel(<ChannelsPanel />);
    expect(await screen.findByText(/只做单账号 \/ 单 Key 核算/)).toBeTruthy();
    const link = screen.getByRole("link", { name: "上游管理" });
    expect(link.getAttribute("href")).toBe("/platforms/sub2api?tab=suppliers");
  });

  it("NewAPI：说明与 Sub2API 共用上游目录但各自核算", async () => {
    stubMetrics([
      metric("newapi.channels.status", {
        channels: [{ channel_id: "c1", name: "gpt 主", enabled: true, model_count: 3 }],
        unhealthy_threshold_ppm: 50_000,
      }),
    ]);
    renderPanel(<NewApiChannelsPanel />);
    expect(await screen.findByText(/共用上游目录和充值成本率/)).toBeTruthy();
    expect(screen.getByRole("link", { name: "上游管理" }).getAttribute("href")).toBe(
      "/platforms/newapi?tab=suppliers",
    );
  });

  it("供给成本 / 毛利 / 毛利率三列都在，且一起显示「未接入」", async () => {
    // 三列里有两列显示数字、一列显示「未接入」，人会以为是那一列坏了；
    // 显示 ¥0.00 更糟——会被读成「这条渠道这期没花钱」
    stubMetrics([
      metric("sub2api.channels.balance", {
        channels: [
          { channel_id: "ch-1", channel_name: "OpenAI 主", balance_minor_units: 12345, currency: "CNY" },
        ],
      }),
    ]);
    renderPanel(<ChannelsPanel />);
    const table = await findTable();
    for (const header of ["供给成本", "毛利", "毛利率"]) {
      expect(table.getByText(header)).toBeTruthy();
    }
    expect(table.getAllByText("未接入")).toHaveLength(3);
    expect(table.queryByText("¥0.00")).toBeNull();
  });

  it("没有数据源的三列**不可排序**——按一列全是「未接入」的东西排序只会让人以为排序坏了", async () => {
    stubMetrics([
      metric("sub2api.channels.balance", {
        channels: [
          { channel_id: "ch-1", channel_name: "OpenAI 主", balance_minor_units: 12345, currency: "CNY" },
        ],
      }),
    ]);
    renderPanel(<ChannelsPanel />);
    const table = await findTable();
    // 有数据源的列（余额）给排序按钮，没有的（毛利）不给
    expect(table.getByRole("button", { name: /余额/ })).toBeTruthy();
    expect(table.queryByRole("button", { name: /^毛利/ })).toBeNull();
  });

  it("主列改叫平台自己的名字，一眼看出一行代表什么", async () => {
    stubMetrics([
      metric("sub2api.channels.balance", {
        channels: [
          { channel_id: "ch-1", channel_name: "OpenAI 主", balance_minor_units: 1, currency: "CNY" },
        ],
      }),
    ]);
    renderPanel(<ChannelsPanel />);
    expect((await findTable()).getByRole("button", { name: /Sub2API 账号/ })).toBeTruthy();
  });
});
