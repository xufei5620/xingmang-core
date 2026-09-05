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

  it("凭据引用列出本平台声明需要的引用及其配置状态与指纹（XM-CREDS-TAB-REFS）", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: unknown) => {
        const url = String(input);
        if (url.includes("/credentials/expected")) {
          return Promise.resolve(
            response({
              items: [
                {
                  credential_ref: "secret://newapi/readonly-token",
                  platform: "newapi",
                  purpose: "NewAPI 管理员 access token",
                  configured: true,
                },
                {
                  credential_ref: "secret://newapi/revenue-db",
                  platform: "newapi",
                  purpose: "NewAPI 收入库只读口令",
                  configured: false,
                },
                {
                  credential_ref: "secret://sub2api-prod/read-token",
                  platform: "sub2api",
                  purpose: "Sub2API admin x-api-key",
                  configured: true,
                },
              ],
            }),
          );
        }
        if (url.includes("/api/v1/credentials")) {
          return Promise.resolve(
            response({
              items: [
                {
                  credential_ref: "secret://newapi/readonly-token",
                  scope: "newapi",
                  updated_at: "2026-09-01T02:03:04Z",
                  fingerprint: "sha256:abcdef0123456789",
                  version: 3,
                  available: true,
                  revoked: false,
                },
              ],
            }),
          );
        }
        return Promise.resolve(response({ items: [service()] }));
      }),
    );

    renderPanel();

    await screen.findByText("newapi-prod-a");
    const credential = (await screen.findByRole("heading", { name: "凭据引用", level: 3 })).closest(
      "section",
    ) as HTMLElement;
    // 只列本平台的引用：sub2api 那条不在这一页。
    expect(await within(credential).findByText("secret://newapi/readonly-token")).toBeTruthy();
    expect(within(credential).getByText("secret://newapi/revenue-db")).toBeTruthy();
    expect(within(credential).queryByText("secret://sub2api-prod/read-token")).toBeNull();
    expect(within(credential).getByText("已配置")).toBeTruthy();
    expect(within(credential).getByText("未配置")).toBeTruthy();
    // 指纹只显示前缀，永远不是值本身。
    expect(within(credential).getByText("sha256:abcdef01…")).toBeTruthy();
    expect(within(credential).getByText("3")).toBeTruthy();
    // 统计卡说的是「已配置 / 本平台声明需要的总数」。
    expect(screen.getByText("1 / 2")).toBeTruthy();
    expect(screen.getByText("有未配置")).toBeTruthy();
  });

  it("没有 credential.manage 权限时仍显示配置状态，只是没有指纹与版本", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: unknown) => {
        const url = String(input);
        if (url.includes("/credentials/expected")) {
          return Promise.resolve(
            response({
              items: [
                {
                  credential_ref: "secret://newapi/revenue-db",
                  platform: "newapi",
                  purpose: "NewAPI 收入库只读口令",
                  configured: false,
                },
              ],
            }),
          );
        }
        if (url.includes("/api/v1/credentials")) {
          return Promise.resolve(response({ message: "缺少权限 credential.manage" }, 403));
        }
        return Promise.resolve(response({ items: [service()] }));
      }),
    );

    renderPanel();

    const credential = (await screen.findByRole("heading", { name: "凭据引用", level: 3 })).closest(
      "section",
    ) as HTMLElement;
    expect(await within(credential).findByText("secret://newapi/revenue-db")).toBeTruthy();
    expect(within(credential).getByText("未配置")).toBeTruthy();
    expect(await within(credential).findByText(/credential\.manage/)).toBeTruthy();
  });

  it("健康探测历史仍明确显示未接入，不拿服务观测冒充探测", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response({ items: [service()] }))));

    renderPanel();

    await screen.findByText("newapi-prod-a");
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
