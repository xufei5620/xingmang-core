import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import type { ReactElement } from "react";
import { RouterProvider, createMemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { devLogin, devLogout } from "./auth";
import { routes } from "./router";

const metricsBody = {
  items: [
    {
      metric_key: "sub2api.revenue.daily",
      source: "sub2api-prod",
      environment: "development",
      watermark: "wm-1",
      value: { day: "2026-08-25", amount_minor_units: 123456, currency: "CNY", order_count: 42 },
      freshness: {
        state: "stale",
        staleness_seconds: 7200,
        threshold_seconds: 1800,
        is_partial: false,
        observed_at: "2026-08-26T10:00:00Z",
        last_success: "2026-08-26T10:00:00Z",
        last_error_code: "",
      },
    },
    {
      metric_key: "sub2api.users.balance",
      source: "sub2api-prod",
      environment: "development",
      watermark: "",
      value: { balance_minor_units: 0, overdraft_minor_units: 0, currency: "CNY" },
      freshness: {
        state: "uninitialized",
        staleness_seconds: null,
        threshold_seconds: 1800,
        is_partial: false,
        observed_at: null,
        last_success: null,
        last_error_code: "",
      },
    },
  ],
};

const servicesBody = {
  items: [
    {
      id: "11111111-1111-1111-1111-111111111111",
      service_type: "sub2api",
      instance_id: "sub2api-dev",
      environment: "development",
      endpoint: "https://sub2api.example.com",
      owner: "平台组",
      status: "degraded",
      source_watermark: "wm-1",
      observed_at: "2026-08-26T10:00:00Z",
      stale_seconds: 120,
    },
  ],
};

/** 只实现客户端用到的 ok/status/json 三样，不依赖 jsdom 是否提供 Response。 */
function fakeResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

function stubFetch(handler: (url: string) => Response) {
  const fn = vi.fn((input: string) => Promise.resolve(handler(input)));
  vi.stubGlobal("fetch", fn);
  return fn;
}

function okHandler(url: string): Response {
  if (url.startsWith("/api/v1/metrics")) return fakeResponse(200, metricsBody);
  if (url.startsWith("/api/v1/services")) return fakeResponse(200, servicesBody);
  return fakeResponse(404, { error: { code: "NOT_REGISTERED", message: "未知路径" } });
}

function renderRoute(path: string): ReactElement {
  // 每个用例一个全新的 QueryClient：否则上一个用例的缓存会让断言看起来通过
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const router = createMemoryRouter(routes, { initialEntries: [path] });
  const tree = (
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  );
  render(tree);
  return tree;
}

describe("admin-web 路由（登录前/后壳）", () => {
  beforeEach(() => {
    devLogout();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("未登录访问 /dashboard 重定向到登录壳", async () => {
    renderRoute("/dashboard");
    expect(await screen.findByText("开发模式进入")).not.toBeNull();
  });

  it("已登录访问 /dashboard 显示 AdminShell 与两个导航入口", async () => {
    devLogin();
    renderRoute("/dashboard");
    expect(await screen.findByRole("heading", { name: "运营总览" })).not.toBeNull();
    expect(screen.getByRole("link", { name: "运营总览" })).not.toBeNull();
    expect(screen.getByRole("link", { name: "服务清单" })).not.toBeNull();
    expect(screen.getByRole("navigation")).not.toBeNull();
  });
});

describe("运营总览页", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("指标卡片显示友好名、金额与新鲜度徽章", async () => {
    renderRoute("/dashboard");
    expect(await screen.findByText("Sub2API 日收入")).not.toBeNull();
    expect(screen.getByText("¥1,234.56")).not.toBeNull();
    expect(screen.getByText("数据延迟")).not.toBeNull();
    expect(screen.getByText(/数据时间 2026-08-26 10:00:00 UTC · 落后 2 小时/)).not.toBeNull();
  });

  it("未初始化的指标显示「未初始化」而不是 ¥0.00（宪法 12 条）", async () => {
    renderRoute("/dashboard");
    await screen.findByText("Sub2API 用户余额");
    expect(screen.getAllByText("未初始化").length).toBeGreaterThan(0);
    expect(screen.queryByText("¥0.00")).toBeNull();
  });

  it("请求带上开发期身份头", async () => {
    const fetchMock = stubFetch(okHandler);
    renderRoute("/dashboard");
    await screen.findByText("Sub2API 日收入");
    const init = (fetchMock.mock.calls[0] as unknown as [string, RequestInit])[1];
    expect(init.headers).toMatchObject({
      "X-Dev-Principal-ID": "dev-operator",
      "X-Dev-Principal-Type": "HUMAN",
      "X-Dev-Scopes": "registry.read,ops.read",
    });
  });

  it("403 时提示缺少的权限名", async () => {
    stubFetch(() =>
      fakeResponse(403, {
        error: { code: "PERMISSION_DENIED", message: "缺少权限 ops.read", request_id: "req-7" },
      }),
    );
    renderRoute("/dashboard");
    expect(await screen.findByText("无权访问")).not.toBeNull();
    expect(screen.getByText(/ops\.read/)).not.toBeNull();
  });

  it("网络失败时显示可重试的错误态", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.reject(new TypeError("Failed to fetch"))));
    renderRoute("/dashboard");
    expect(await screen.findByText("加载失败")).not.toBeNull();
    expect(screen.getByRole("button", { name: "重试" })).not.toBeNull();
  });
});

describe("服务清单页", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("表格列出实例、状态徽章与采集新鲜度", async () => {
    renderRoute("/services");
    expect(await screen.findByText("sub2api-dev")).not.toBeNull();
    expect(screen.getByText("降级")).not.toBeNull();
    expect(screen.getByText("数据新鲜")).not.toBeNull();
    expect(screen.getByText(/落后 2 分钟/)).not.toBeNull();
    expect(screen.getByText(/数据时间 2026-08-26 10:00:00 UTC/)).not.toBeNull();
  });
});
