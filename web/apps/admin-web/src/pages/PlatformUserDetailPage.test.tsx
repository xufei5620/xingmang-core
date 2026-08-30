import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DEMO_BANNER_TEXT } from "../lib/demoData";
import { PlatformUserDetailPage } from "./PlatformUserDetailPage";

function userItem(over: Record<string, unknown> = {}) {
  return {
    id: "u_10241",
    username: "张伟",
    email_masked: "zh***@example.com",
    status: "active",
    balance: { minor_units: "0", currency: "CNY" },
    period_recharge: { minor_units: null, currency: "" },
    period_consumed: { minor_units: "0", currency: "CNY" },
    last_30d_consumed: { minor_units: "812000", currency: "CNY" },
    last_active_at: "2026-08-28T09:00:00Z",
    token_prefix: "tok-a1b2",
    ...over,
  };
}

function pageBody(over: Record<string, unknown> = {}) {
  return {
    items: [userItem()],
    next_cursor: "",
    total_count: { value: 1 },
    total_balance: { minor_units: "0", currency: "CNY" },
    active_today: { value: 1 },
    period_totals: {
      recharge: { minor_units: null, currency: "" },
      consumed: { minor_units: "0", currency: "CNY" },
      covered_users: 1,
      total_users: 1,
      complete: true,
    },
    period: { day: "2026-08-28", granularity: "day", from: "2026-08-28", to: "2026-08-28" },
    data_source: "sub2api-fake",
    freshness: {
      state: "fresh",
      staleness_seconds: 5,
      threshold_seconds: 60,
      is_partial: false,
      observed_at: "2026-08-28T09:00:00Z",
      last_success: "2026-08-28T09:00:00Z",
      last_error_code: "",
    },
    ...over,
  };
}

function fakeResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

function detailFromPage(body: ReturnType<typeof pageBody>) {
  const user = body.items[0];
  return {
    ref: { platform: "sub2api", id: user?.id ?? "u_10241" },
    user,
    registered_at: null,
    period: body.period,
    snapshot: {
      observed_at: body.freshness.observed_at,
      source: body.data_source,
      watermark: "wm-detail",
      is_partial: body.freshness.is_partial,
    },
    capabilities: ["platformusers.user.detail_read"],
  };
}

function dailyBody() {
  return {
    from: "2026-08-22",
    to: "2026-08-28",
    points: Array.from({ length: 7 }, (_, index) => ({
      day: `2026-08-${String(22 + index).padStart(2, "0")}`,
      consumed: index === 1
        ? { minor_units: null, currency: "" }
        : { minor_units: index === 0 ? "0" : "1200", currency: "CNY" },
      requests: { value: index === 1 ? null : index * 3 },
    })),
    coverage: { expected_days: 7, covered_days: 6, complete: false },
    snapshot: { observed_at: "2026-08-28T09:00:00Z", source: "sub2api-fake", watermark: "wm-daily", is_partial: true },
  };
}

function keysBody() {
  return {
    items: [
      { id: "km-1", prefix: "sk-a1b2", status: "active", created_at: "2026-08-01T00:00:00Z", last_used_at: "2026-08-28T08:00:00Z", today_peak_rpm: { value: 42 } },
      { id: "km-2", prefix: "sk-old1", status: "revoked", created_at: null, last_used_at: null, today_peak_rpm: { value: null } },
      { id: "km-3", prefix: "sk-off1", status: "disabled", created_at: null, last_used_at: null, today_peak_rpm: { value: null } },
    ],
    next_cursor: "",
    snapshot: { observed_at: "2026-08-28T09:00:00Z", source: "sub2api-fake", watermark: "wm-keys", is_partial: false },
  };
}

function stubFetch(handler: (url: string, init?: RequestInit) => Response = () => fakeResponse(pageBody())) {
  const urls: string[] = [];
  const fetchMock = vi.fn((input: string, init?: RequestInit) => {
    const url = String(input);
    urls.push(url);
    const response = handler(url, init);
    // Keep fixture authoring compact while the page uses the v2 detail envelope:
    // successful old-shaped fixtures are projected into the explicit detail envelope.
    if (/\/users\/u-[^/]+$/.test(new URL(url, "http://local.test").pathname) && response.ok) {
      return Promise.resolve(response.json()).then((body) =>
        fakeResponse(body && "items" in body ? detailFromPage(body as ReturnType<typeof pageBody>) : body),
      );
    }
    const pathname = new URL(url, "http://local.test").pathname;
    if (pathname.endsWith("/daily-usage") && response.ok) {
      return Promise.resolve(response.json()).then((body) =>
        fakeResponse(body && "points" in body ? body : dailyBody()),
      );
    }
    if (pathname.endsWith("/keys") && response.ok) {
      return Promise.resolve(response.json()).then((body) =>
        fakeResponse(body && "snapshot" in body && "items" in body ? body : keysBody()),
      );
    }
    return Promise.resolve(response);
  });
  vi.stubGlobal("fetch", fetchMock);
  return { urls, fetchMock };
}

function renderPage(path = "/platforms/sub2api/users/u-755f3130323431") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <LocationProbe />
        <Routes>
          <Route
            path="/platforms/:serviceType/users/:userId"
            element={<PlatformUserDetailPage />}
          />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function LocationProbe() {
  const location = useLocation();
  return <output data-testid="location">{`${location.pathname}${location.search}`}</output>;
}

function tile(label: string): HTMLElement {
  return screen.getByRole("heading", { name: label, level: 3 }).closest("article") as HTMLElement;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("用户详情共享事实（platformusers v2）", () => {
  it("已知 0 与未知金额分开显示，并把来源、新鲜度与 Fake 横幅放在事实旁边", async () => {
    stubFetch();
    renderPage();

    expect(await screen.findByRole("heading", { name: "张伟", level: 2 })).toBeTruthy();
    expect(within(tile("可用余额")).getByText("¥0.00")).toBeTruthy();
    expect(within(tile("区间充值")).getByText("—")).toBeTruthy();
    expect(within(tile("区间消费")).getByText("¥0.00")).toBeTruthy();
    expect(within(tile("近 30 天消费")).getByText("¥8,120.00")).toBeTruthy();
    expect(screen.getByText(DEMO_BANNER_TEXT)).toBeTruthy();
    expect(screen.getByText(/来源 sub2api-fake/)).toBeTruthy();
    expect(screen.getByText(/数据时间 2026-08-28 09:00:00 UTC/)).toBeTruthy();
  });

  it("统计区间沿用 day/granularity 参数，并显示服务端回显而非浏览器重算", async () => {
    const { urls } = stubFetch(() =>
      fakeResponse(
        pageBody({
          period: { day: "2026-08-27", granularity: "week", from: "2026-08-24", to: "2026-08-30" },
        }),
      ),
    );
    renderPage("/platforms/sub2api/users/u-755f3130323431?day=2026-08-27&granularity=week");

    expect(await screen.findByText("2026-08-24 ~ 2026-08-30 · 按周查看")).toBeTruthy();
    const request = new URL(urls[0] ?? "", "http://local.test");
    expect(request.searchParams.get("day")).toBe("2026-08-27");
    expect(request.searchParams.get("granularity")).toBe("week");
    expect(request.pathname).toBe("/api/v1/platforms/sub2api/users/u-755f3130323431");
    expect(request.searchParams.get("q")).toBeNull();
    expect(request.searchParams.get("limit")).toBeNull();
    const dailyRequest = urls.find((raw) => new URL(raw, "http://local.test").pathname.endsWith("/daily-usage"));
    expect(dailyRequest).toBeTruthy();
    expect(new URL(dailyRequest ?? "", "http://local.test").searchParams.get("day")).toBe("2026-08-27");
    expect(new URL(dailyRequest ?? "", "http://local.test").searchParams.get("days")).toBe("7");
  });

  it("opaque route ID 解码后做精确查询，不产生路径穿越或默认用户回落", async () => {
    const opaqueId = "tenant/a?slot=#1% ready";
    const { urls } = stubFetch(() =>
      fakeResponse(pageBody({ items: [userItem({ id: opaqueId, username: "Opaque" })] })),
    );
    renderPage(
      "/platforms/newapi/users/u-74656e616e742f613f736c6f743d233125207265616479",
    );

    expect(await screen.findByRole("heading", { name: "Opaque", level: 2 })).toBeTruthy();
    const request = new URL(urls[0] ?? "", "http://local.test");
    expect(request.pathname).toBe("/api/v1/platforms/newapi/users/u-74656e616e742f613f736c6f743d233125207265616479");
    expect(request.searchParams.get("q")).toBeNull();
  });

  it.each([
    [".", "u-2e"],
    ["..", "u-2e2e"],
  ])("点段 ID %j 经 canonical segment 解码后精确查询", async (id, segment) => {
    const { urls } = stubFetch(() =>
      fakeResponse(pageBody({ items: [userItem({ id, username: "Dot User" })] })),
    );
    renderPage(`/platforms/sub2api/users/${segment}`);

    expect(await screen.findByRole("heading", { name: "Dot User", level: 2 })).toBeTruthy();
    const request = new URL(urls[0] ?? "", "http://local.test");
    expect(request.pathname).toContain("/users/u-2e");
  });

  it("缺失/非法邮箱、未知状态与非法时间都显式说明", async () => {
    stubFetch(() =>
      fakeResponse(
        pageBody({
          items: [
            userItem({
              email_masked: "invalid-contact",
              status: "mystery",
              last_active_at: "not-a-time",
            }),
          ],
        }),
      ),
    );
    renderPage();

    expect(await screen.findByText("格式不认识")).toBeTruthy();
    // 页头与基本信息都会重复状态；金额未知徽章也同词，至少有一处状态证据即可
    expect(screen.getAllByText("未知").length).toBeGreaterThan(0);
    expect(screen.getByText(/时间格式异常/)).toBeTruthy();
  });
});

describe("平台特有布局", () => {
  it("Sub2API 顶部展示按日趋势覆盖率，并用四个可点击子页签承载明细区", async () => {
    stubFetch();
    renderPage();

    expect(await screen.findByRole("heading", { name: "基本信息", level: 3 })).toBeTruthy();
    expect(screen.getByText("tok-a1b2")).toBeTruthy();
    for (const heading of ["客户类型", "注册时间", "近 7 天消费趋势"]) {
      expect(screen.getByRole("heading", { name: heading, level: 3 })).toBeTruthy();
    }
    expect(await screen.findByText(/覆盖 6 \/ 7 天/)).toBeTruthy();
    expect(screen.getByText(/缺失日保持未知/)).toBeTruthy();
    expect(screen.getAllByText("¥0.00").length).toBeGreaterThan(1);
    const tabs = screen.getAllByRole("tab");
    expect(tabs.map((tab) => tab.textContent)).toEqual([
      "消费明细",
      "充值记录",
      "开票记录",
      "API Key",
    ]);
    expect(screen.getByRole("tab", { name: "消费明细", selected: true })).toBeTruthy();
    expect(screen.getAllByRole("tabpanel")).toHaveLength(1);
    expect(
      within(screen.getByRole("tabpanel")).getByRole("heading", {
        name: "消费明细",
        level: 3,
      }),
    ).toBeTruthy();
    expect(within(screen.getByRole("tabpanel")).getByText("未接入")).toBeTruthy();
    expect(screen.getByText(/客户类型.*platformusers read contract v2/)).toBeTruthy();
    expect(screen.getByText(/注册时间.*platformusers read contract v2/)).toBeTruthy();
  });

  it("Sub2API 子页签写入 ?sub=，可分享恢复且同一时刻只显示一个 unavailable panel", async () => {
    stubFetch();
    renderPage();
    await screen.findByRole("tab", { name: "消费明细", selected: true });

    fireEvent.mouseDown(screen.getByRole("tab", { name: "开票记录" }), { button: 0 });

    expect(screen.getByRole("tab", { name: "开票记录", selected: true })).toBeTruthy();
    expect(screen.getByTestId("location").textContent).toBe(
      "/platforms/sub2api/users/u-755f3130323431?sub=invoices",
    );
    const panel = screen.getByRole("tabpanel");
    expect(within(panel).getByRole("heading", { name: "开票记录", level: 3 })).toBeTruthy();
    expect(within(panel).getByText(/开票集成契约/)).toBeTruthy();
    expect(within(panel).queryByRole("heading", { name: "消费明细", level: 3 })).toBeNull();
  });

  it("Sub2API 可从分享的 ?sub=keys 直接恢复 API Key 元数据面板", async () => {
    stubFetch();
    renderPage("/platforms/sub2api/users/u-755f3130323431?sub=keys");

    expect(await screen.findByRole("tab", { name: "API Key", selected: true })).toBeTruthy();
    const panel = within(screen.getByRole("tabpanel"));
    expect(panel.getByRole("heading", { name: "API Key 元数据", level: 3 })).toBeTruthy();
    expect(await panel.findByText("sk-a1b2")).toBeTruthy();
    expect(panel.getByText("已撤销")).toBeTruthy();
    expect(panel.getByText("已禁用")).toBeTruthy();
    expect(panel.queryByRole("button", { name: /复制|导出/ })).toBeNull();
  });

  it("Key 元数据缺少独立 scope 时显示拒绝，不回落到用户令牌前缀", async () => {
    stubFetch((url) => new URL(url, "http://local.test").pathname.endsWith("/keys")
      ? fakeResponse({ error: { code: "PERMISSION_DENIED", message: "缺少权限 platform.user_keys.read" } }, 403)
      : fakeResponse(pageBody()));
    renderPage("/platforms/sub2api/users/u-755f3130323431?sub=keys");
    const panel = within(await screen.findByRole("tabpanel"));
    expect(await panel.findByText("无权访问")).toBeTruthy();
    expect(panel.getByText(/platform\.user_keys\.read/)).toBeTruthy();
    expect(panel.queryByText("sk-a1b2")).toBeNull();
  });

  it("Key 元数据支持按不透明游标加载下一页", async () => {
    const { urls } = stubFetch((url) => {
      const parsed = new URL(url, "http://local.test");
      if (!parsed.pathname.endsWith("/keys")) return fakeResponse(pageBody());
      if (parsed.searchParams.get("cursor")) {
        return fakeResponse({
          items: [{ id: "km-2", prefix: "sk-c3d4", status: "active", created_at: null, last_used_at: null, today_peak_rpm: { value: 3 } }],
          next_cursor: "",
          snapshot: { observed_at: "2026-08-28T09:00:00Z", source: "sub2api-fake", watermark: "wm-keys-2", is_partial: false },
        });
      }
      return fakeResponse({ ...keysBody(), items: [keysBody().items[0]], next_cursor: "cursor-next" });
    });
    renderPage("/platforms/sub2api/users/u-755f3130323431?sub=keys");
    const panel = within(await screen.findByRole("tabpanel"));
    expect(await panel.findByText("sk-a1b2")).toBeTruthy();
    fireEvent.click(await panel.findByRole("button", { name: "加载更多" }));
    expect(await panel.findByText("sk-c3d4")).toBeTruthy();
    const keyRequests = urls.filter((url) => new URL(url, "http://local.test").pathname.endsWith("/keys"));
    expect(new URL(keyRequests[1] ?? "", "http://local.test").searchParams.get("cursor")).toBe("cursor-next");
  });

  it("NewAPI 只保留自己的区间请求 unavailable 面板，不复制 Sub2API 丰富区域", async () => {
    stubFetch(() =>
      fakeResponse(pageBody({ data_source: "newapi-fake" })),
    );
    renderPage("/platforms/newapi/users/u-755f3130323431");

    expect(await screen.findByRole("heading", { name: "区间请求", level: 3 })).toBeTruthy();
    expect(screen.getByText(/没有稳定的 platform-user ID 关联/)).toBeTruthy();
    expect(screen.queryByRole("tab")).toBeNull();
    for (const heading of ["消费明细", "充值记录", "开票记录", "API Key"]) {
      expect(screen.queryByRole("heading", { name: heading, level: 3 })).toBeNull();
    }
  });
});

describe("查找状态与负向边界", () => {
  it("v2 404 才显示 definite not-found，不回落到列表样本", async () => {
    stubFetch(() => fakeResponse({ error: { code: "ACTION_NOT_REGISTERED", message: "没有这条用户记录" } }, 404));
    renderPage();

    expect(await screen.findByText("没有这个用户")).toBeTruthy();
    expect(screen.queryByText("无法确认用户是否存在")).toBeNull();
  });

  it("端点未挂载（无 error.code 的 404）显示未接入，不是没有这个用户（XM-UX-OFFSTATE）", async () => {
    // chi 对没挂载的路由回纯文本 404，不是 JSON——与上一条「具体用户不存在」
    // 用例的带 code JSON 404 结构不同，两者不能显示成同一种状态
    stubFetch(
      () =>
        ({
          ok: false,
          status: 404,
          json: () => Promise.reject(new SyntaxError("Unexpected token <")),
        }) as unknown as Response,
    );
    renderPage();

    expect(await screen.findByText("未接入")).toBeTruthy();
    expect(screen.getByText(/XM_PLATFORM_USERS_MODE=off/)).toBeTruthy();
    expect(screen.queryByText("没有这个用户")).toBeNull();
    expect(screen.queryByText(/失败|错误/)).toBeNull();
    // 之前的 bug：显示「请求失败（HTTP 404）（错误码 UNKNOWN）」
    expect(screen.queryByText(/UNKNOWN/)).toBeNull();
    expect(screen.queryByRole("button", { name: "重试" })).toBeNull();
  });

  it("v2 精确查找未完成时显示可重试错误，不误报没有这个用户", async () => {
    stubFetch(() => fakeResponse({ error: { code: "EXECUTION_FAILED", message: "用户精确查找未完成，请重试" } }, 502));
    renderPage();

    expect(await screen.findByText("加载失败")).toBeTruthy();
    expect(screen.getByText(/用户精确查找未完成/)).toBeTruthy();
    expect(screen.queryByText("没有这个用户")).toBeNull();
    expect(screen.getByRole("button", { name: "重试" })).toBeTruthy();
  });

  it("查询失败与 incomplete 使用不同状态，并允许重试可恢复错误", async () => {
    stubFetch(() =>
      fakeResponse({ error: { code: "EXECUTION_FAILED", message: "上游暂不可用" } }, 502),
    );
    renderPage();

    expect(await screen.findByText("加载失败")).toBeTruthy();
    expect(screen.getByRole("button", { name: "重试" })).toBeTruthy();
    expect(screen.queryByText("无法确认用户是否存在")).toBeNull();
  });

  it("只调用 platformusers 精确 Query，不碰 reqlog/content/invoice/finance", async () => {
    const { urls } = stubFetch();
    renderPage("/platforms/newapi/users/u-755f3130323431");
    await screen.findByRole("heading", { name: "张伟", level: 2 });

    expect(urls.length).toBeGreaterThan(0);
    for (const raw of urls) {
      const url = new URL(raw, "http://local.test");
      expect(url.pathname).toBe("/api/v1/platforms/newapi/users/u-755f3130323431");
      expect(url.pathname).not.toMatch(/requests|content|invoice|finance|reqlog/);
    }
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("精确查找错误的重试只重新执行一次 detail Query", async () => {
    const { fetchMock } = stubFetch(() =>
      fakeResponse({ error: { code: "EXECUTION_FAILED", message: "用户精确查找未完成，请重试" } }, 502),
    );
    renderPage();
    await screen.findByText("加载失败");

    fireEvent.click(screen.getByRole("button", { name: "重试" }));

    expect(fetchMock.mock.calls.length).toBe(2);
  });
});
