import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DailyUsagePanel } from "./DailyUsagePanel";

function seriesBody(over: Record<string, unknown> = {}) {
  return {
    from: "2026-08-22",
    to: "2026-08-28",
    points: [
      { day: "2026-08-28", consumed: { minor_units: "12000", currency: "CNY" }, requests: { value: 42 } },
    ],
    coverage: { expected_days: 7, covered_days: 1, complete: false },
    snapshot: {
      observed_at: "2026-08-28T09:00:00Z",
      source: "sub2api-fake",
      watermark: "wm",
      is_partial: false,
    },
    ...over,
  };
}

function stubFetch(body: unknown = seriesBody()) {
  const fetchMock = vi.fn(() =>
    Promise.resolve({
      ok: true,
      status: 200,
      json: () => Promise.resolve(body),
    } as unknown as Response),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

/** chi 对没挂载的路由回纯文本 404：json() 会像浏览器解析 HTML/纯文本一样抛出，
 *  不是带 error.code 的 JSON 错误包（与 PlatformUsersPanel.test.tsx 的
 *  stubNotMounted 同一形状）。 */
function stubNotMounted() {
  const fetchMock = vi.fn(() =>
    Promise.resolve({
      ok: false,
      status: 404,
      json: () => Promise.reject(new SyntaxError("Unexpected token <")),
    } as unknown as Response),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderPanel() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <DailyUsagePanel platform="sub2api" userId="u_10241" />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("DailyUsagePanel", () => {
  it("正常加载时渲染逐日序列", async () => {
    stubFetch();
    renderPanel();

    expect(await screen.findByText("2026-08-28")).toBeTruthy();
    expect(screen.getByText("覆盖 1 / 7 天")).toBeTruthy();
  });

  it("XM_PLATFORM_USERS_MODE=off 时显示未接入而不是失败态", async () => {
    stubNotMounted();
    renderPanel();

    expect(await screen.findByText("未接入")).toBeTruthy();
    expect(screen.getByText(/XM_PLATFORM_USERS_MODE=off/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "重试" })).toBeNull();
    // 修复前：404 解析不出 code 会显示「请求失败（HTTP 404）（错误码 UNKNOWN）」
    expect(screen.queryByText(/UNKNOWN/)).toBeNull();
    expect(screen.queryByRole("table")).toBeNull();
  });
});
