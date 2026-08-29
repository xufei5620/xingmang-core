import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PlatformDetailPage } from "./PlatformDetailPage";
import { tabsForPlatform } from "../lib/platforms";

function fakeResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

function renderPage(path: string) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/platforms/:serviceType" element={<PlatformDetailPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  cleanup();
});

describe("平台详情页无障碍名称", () => {
  it("主页签与子页签都有可访问名称", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          fakeResponse(200, {
            items: [
              {
                id: "11111111-1111-1111-1111-111111111111",
                service_type: "sub2api",
                instance_id: "sub2api-dev",
                environment: "development",
                endpoint: "https://sub2api.example.com",
                owner: "平台组",
                status: "active",
                source_watermark: "wm-1",
                observed_at: "2026-08-26T10:00:00Z",
                stale_seconds: 0,
              },
            ],
          }),
        ),
      ),
    );

    renderPage("/platforms/sub2api?tab=model");

    expect(await screen.findByRole("tablist", { name: "平台主页签" })).not.toBeNull();
    const modelTab = tabsForPlatform("sub2api").find((tab) => tab.value === "model");
    expect(modelTab).toBeDefined();
    expect(screen.getByRole("tablist", { name: `${modelTab!.label}子页签` })).not.toBeNull();
  });
});
