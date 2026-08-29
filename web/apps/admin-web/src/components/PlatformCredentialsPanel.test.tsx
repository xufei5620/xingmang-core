import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PlatformCredentialsPanel } from "./PlatformCredentialsPanel";

function response(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function service(overrides: Record<string, unknown> = {}) {
  return {
    id: "service-newapi-1",
    service_type: "newapi",
    instance_id: "newapi-prod-a",
    environment: "production",
    endpoint: "https://newapi.example.invalid",
    owner: "平台运营组",
    status: "active",
    source_watermark: "wm-20260829-01",
    observed_at: "2026-08-29T08:30:00Z",
    stale_seconds: 90,
    ...overrides,
  };
}

function renderPanel(platform = "newapi") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <PlatformCredentialsPanel platform={platform} />
    </QueryClientProvider>,
  );
}

describe("平台连接与凭据", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("只展示 service_type 精确匹配的平台实例及最近一次真实观测", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          response({
            items: [
              service(),
              service({
                id: "service-sub2api-1",
                service_type: "sub2api",
                instance_id: "sub2api-prod-a",
                endpoint: "https://sub2api.example.invalid",
              }),
            ],
          }),
        ),
      ),
    );

    renderPanel();

    expect(await screen.findByText("newapi-prod-a")).toBeTruthy();
    expect(screen.queryByText("sub2api-prod-a")).toBeNull();
    const table = within(screen.getByRole("table"));
    expect(table.getByText("https://newapi.example.invalid")).toBeTruthy();
    expect(table.getByText("平台运营组")).toBeTruthy();
    expect(table.getByText("数据新鲜")).toBeTruthy();
    expect(table.getByText(/2026-08-29 08:30:00 UTC/)).toBeTruthy();
  });

  it("CredentialRef、连接登记簿和探测历史没有 Query 时明确显示未接入", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response({ items: [service()] }))));

    renderPanel();

    await screen.findByText("newapi-prod-a");
    const credential = screen.getByRole("heading", { name: "凭据引用", level: 3 }).closest(
      "article",
    ) as HTMLElement;
    expect(within(credential).getByText("未接入")).toBeTruthy();
    expect(within(credential).getByText(/连接登记簿 Query 尚未提供/)).toBeTruthy();

    const probes = screen.getByRole("heading", { name: "健康探测历史", level: 3 }).closest(
      "article",
    ) as HTMLElement;
    expect(within(probes).getByText("未接入")).toBeTruthy();
    expect(within(probes).getByText(/只能显示服务最近一次观测/)).toBeTruthy();
    expect(screen.queryByText(/下次轮换/)).toBeNull();
  });

  it("平台没有登记实例时显示有行动指向的空态，不渲染空表", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response({ items: [service({ service_type: "sub2api" })] }))));

    renderPanel();

    expect(await screen.findByText("NewAPI 还没有登记实例")).toBeTruthy();
    expect(screen.getByText(/平台治理.*服务注册表/)).toBeTruthy();
    expect(screen.queryByRole("table")).toBeNull();
  });
});
