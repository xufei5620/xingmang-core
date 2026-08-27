import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { OVERVIEW_POLL_INTERVAL_MS } from "../lib/autoRefresh";
import { OverviewPage } from "./OverviewPage";

// 指标键抽成常量而不是就地写字面量：`metric_key: "……"` 这个形状会被 gitleaks 的
// generic-api-key 规则当成泄露的密钥（同一条误报见 api/platform.test.ts）。
// 与其让 secret-scan 常红到没人再看它，不如换个写法绕开这个形状
const REVENUE_METRIC = "sub2api.revenue.daily";

const metricsBody = {
  items: [
    {
      metric_key: REVENUE_METRIC,
      source: "sub2api-prod",
      environment: "development",
      watermark: "wm-1",
      value: { day: "2026-08-25", amount_minor_units: 123456, currency: "CNY", order_count: 42 },
      freshness: {
        state: "fresh",
        staleness_seconds: 30,
        threshold_seconds: 1800,
        is_partial: false,
        observed_at: "2026-08-26T10:00:00Z",
        last_success: "2026-08-26T10:00:00Z",
        last_error_code: "",
      },
    },
  ],
};

/** 告警卡的响应。空清单：这个文件只关心自动刷新，不关心告警内容。
 *  但它必须被 stub——总览页会真的去拉 /api/v1/alerts（XM-0033）。 */
const alertsBody = { items: [] };

const historyBody = {
  items: [0, 1, 2].map((i) => ({
    observed_at: `2026-08-26T0${i}:00:00Z`,
    synced_at: `2026-08-26T0${i}:05:00Z`,
    status: "ok",
    is_partial: false,
    watermark: `wm-${i}`,
    last_error_code: "",
    value: { amount_minor_units: 1000 + i * 100, currency: "CNY" },
  })),
};

function fakeResponse(body: unknown): Response {
  return { ok: true, status: 200, json: () => Promise.resolve(body) } as unknown as Response;
}

function renderPage() {
  const queryClient = new QueryClient({
    // staleTime 0：这个用例要观察「定时器到点后又发了一次请求」，
    // 缓存窗口会把第二次请求吃掉，那就测不到刷新本身了
    defaultOptions: { queries: { retry: false, staleTime: 0 } },
  });
  // MemoryRouter 是必需的：告警卡里有一个通往 /alerts 的 <Link>（XM-0033），
  // 指标卡上还有「查看平台 →」（XM-0034）。没有路由上下文 react-router 直接抛异常。
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <OverviewPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function historyCalls(fetchMock: ReturnType<typeof vi.fn>): number {
  return fetchMock.mock.calls.filter((c) => String(c[0]).startsWith("/api/v1/metrics/history"))
    .length;
}

describe("总览页的 60 秒自动刷新（Codex #4）", () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    // shouldAdvanceTime：让 @testing-library 的 waitFor 仍能在假时钟下推进，
    // 否则 findBy* 会永远等下去
    vi.useFakeTimers({ shouldAdvanceTime: true });
    fetchMock = vi.fn((input: string) => {
      // history 必须排在 metrics 前面：两者的前缀是包含关系
      if (input.startsWith("/api/v1/metrics/history")) return Promise.resolve(fakeResponse(historyBody));
      if (input.startsWith("/api/v1/alerts")) return Promise.resolve(fakeResponse(alertsBody));
      return Promise.resolve(fakeResponse(metricsBody));
    });
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("计时器到点后趋势历史被重新拉取，而不是永远停在首次加载", async () => {
    renderPage();
    // 折线是挂载后才拉的，先等它出现
    expect(await screen.findByRole("img", { name: /近 24 小时趋势/ })).not.toBeNull();
    const before = historyCalls(fetchMock);
    expect(before).toBeGreaterThan(0);

    await act(async () => {
      vi.advanceTimersByTime(OVERVIEW_POLL_INTERVAL_MS);
    });

    // 页头写着「每 60 秒自动刷新」，那折线就必须真的跟着走：
    // 只 refetch ['metrics'] 的话主数字会更新、折线永远停在首屏那一刻
    await waitFor(() => expect(historyCalls(fetchMock)).toBeGreaterThan(before));
  });

  it("页面不可见时不拉——包括折线（刷新入口只有一个，规则也只有一份）", async () => {
    renderPage();
    await screen.findByRole("img", { name: /近 24 小时趋势/ });
    const before = historyCalls(fetchMock);

    const spy = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    await act(async () => {
      vi.advanceTimersByTime(OVERVIEW_POLL_INTERVAL_MS);
    });
    expect(historyCalls(fetchMock)).toBe(before);
    spy.mockRestore();
  });
});
