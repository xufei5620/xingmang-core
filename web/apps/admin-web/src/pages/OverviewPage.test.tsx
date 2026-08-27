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

const alertsBody = { items: [] };
const servicesBody = { items: [] };
const auditBody = { items: [], next_before: 0 };

function fakeResponse(body: unknown): Response {
  return { ok: true, status: 200, json: () => Promise.resolve(body) } as unknown as Response;
}

function renderPage() {
  const queryClient = new QueryClient({
    // staleTime 0：这个用例要观察「定时器到点后又发了一次请求」，
    // 缓存窗口会把第二次请求吃掉，那就测不到刷新本身了
    defaultOptions: { queries: { retry: false, staleTime: 0 } },
  });
  // MemoryRouter 是必需的：工作台里到处都是 <Link>（告警、审计、平台矩阵），
  // 没有路由上下文 react-router 直接抛异常
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <OverviewPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function alertCalls(fetchMock: ReturnType<typeof vi.fn>): number {
  return fetchMock.mock.calls.filter((c) => String(c[0]).startsWith("/api/v1/alerts")).length;
}

describe("运营工作台的 60 秒自动刷新（Codex #4）", () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    // shouldAdvanceTime：让 @testing-library 的 waitFor 仍能在假时钟下推进，
    // 否则 findBy* 会永远等下去
    vi.useFakeTimers({ shouldAdvanceTime: true });
    fetchMock = vi.fn((input: string) => {
      if (input.startsWith("/api/v1/alerts")) return Promise.resolve(fakeResponse(alertsBody));
      if (input.startsWith("/api/v1/services")) return Promise.resolve(fakeResponse(servicesBody));
      if (input.startsWith("/api/v1/audit")) return Promise.resolve(fakeResponse(auditBody));
      return Promise.resolve(fakeResponse(metricsBody));
    });
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("计时器到点后重新拉取，而不是永远停在首屏", async () => {
    renderPage();
    await screen.findByText("我的待处理");
    const before = alertCalls(fetchMock);
    expect(before).toBeGreaterThan(0);

    await act(async () => {
      vi.advanceTimersByTime(OVERVIEW_POLL_INTERVAL_MS);
    });

    // 页头写着「每 60 秒自动刷新」，那这一屏就必须真的跟着走
    await waitFor(() => expect(alertCalls(fetchMock)).toBeGreaterThan(before));
  });

  it("页面不可见时不拉——刷新入口只有一个，规则也只有一份", async () => {
    renderPage();
    await screen.findByText("我的待处理");
    const before = alertCalls(fetchMock);

    const spy = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    await act(async () => {
      vi.advanceTimersByTime(OVERVIEW_POLL_INTERVAL_MS);
    });
    expect(alertCalls(fetchMock)).toBe(before);
    spy.mockRestore();
  });
});
