import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { KeyMetadataPanel } from "./KeyMetadataPanel";

function pageBody(over: Record<string, unknown> = {}) {
  return {
    items: [
      {
        id: "key-1",
        prefix: "sk-a1b2",
        status: "active",
        created_at: "2026-08-01T00:00:00Z",
        last_used_at: "2026-08-28T09:00:00Z",
        today_peak_rpm: { value: 12 },
      },
    ],
    next_cursor: "",
    snapshot: {
      observed_at: "2026-08-28T09:00:00Z",
      source: "sub2api-fake",
      watermark: "wm",
      is_partial: false,
    },
    ...over,
  };
}

function stubFetch(body: unknown = pageBody()) {
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

/** chi 对没挂载的路由回纯文本 404（与 PlatformUsersPanel.test.tsx 的
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
      <KeyMetadataPanel platform="sub2api" userId="u_10241" />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("KeyMetadataPanel", () => {
  it("正常加载时渲染 Key 元数据行，不出现完整 Key 或 secret 字样", async () => {
    stubFetch();
    renderPanel();

    expect(await screen.findByText("sk-a1b2")).toBeTruthy();
    expect(screen.getByText("启用")).toBeTruthy();
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
