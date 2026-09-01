import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChannelDetailPage } from "./ChannelDetailPage";
import { SupplierCreatePage } from "./SupplierCreatePage";
import { UpstreamDetailPage } from "./UpstreamDetailPage";

function renderPage(path: string, element: React.ReactElement, route: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path={route} element={element} />
      </Routes>
    </MemoryRouter>,
  );
}

function renderQueryPage(path: string, element: React.ReactElement, route: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path={route} element={element} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function fakeResponse(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

const ACTIVE_SERVICE = { id: "svc-1", service_type: "sub2api", instance_id: "sub2api-a", environment: "development", endpoint: "", owner: "", status: "active", source_watermark: "", observed_at: null, stale_seconds: null };

function channelPage(items: unknown[]) {
  return {
    service: { id: "svc-1", service_type: "sub2api", instance_id: "sub2api-a", environment: "development" },
    inventory: { state: "ok", source: "sub2api-a", observed_at: "2026-08-29T01:00:00Z", complete: true, truncated: false, reported_count: items.length, fetched_count: items.length, coverage_partial: false, evidence: "reported_count" },
    from: "2026-08-29", to: "2026-08-29",
    items,
    runway_coverage: { total: 1, known: 1, reasons: {} }, next_cursor: null,
  };
}

function boundChannelRow(over: Record<string, unknown> = {}) {
  return {
    channel_ref: { service_id: "svc-1", external_channel_id: "channel-a" },
    name: "OpenAI A",
    binding: { id: "bind-1", upstream_account_id: "up-1", valid_from: "2026-08-28T00:00:00Z", reason: "人工确认" },
    candidate: { state: "candidate", evidence_status: "sufficient", upstream_account_ids: ["up-1"], reason_codes: [], platform_assignment_missing: false, inventory_unknown: false },
    economics: null, economics_state: "binding_pending_economics", conflicts: [],
    health: { state: "observed" }, models: { count: 4 }, assurance: null, runway: { days: 12 },
    observed: { source: "sub2api-a", observed_at: "2026-08-29T01:00:00Z", is_stale: false },
    ...over,
  };
}

function upstreamAccount(over: Record<string, unknown> = {}) {
  return {
    id: "up-1", system_type: "sub2api", access_method: "upstream_key", upstream_name: "Relay 甲",
    upstream_contact: "", upstream_group: "gpt-main", base_url: "https://relay-a.example.com",
    credential_ref: "secret://xm/upstream/a", recharge_ratio: "1.15", group_rate: "1.25",
    recharge_cost_rate: "0.869565217", currency: "CNY", business_day_tz: "+08:00", platform_id: "sub2api",
    status: "active", environment: "development", metered: true, token_mappings: [],
    created_at: "2026-08-01T00:00:00Z", updated_at: "2026-08-01T00:00:00Z",
    ...over,
  };
}

function upstreamSummary(over: Record<string, unknown> = {}) {
  return {
    id: "up-1", name: "relay-a", supplier_key: "", system_type: "sub2api", access_method: "upstream_key",
    base_url: "https://relay-a.example.com", recharge_cost_rate: "0.869565217", credential_ref: "",
    status: "active", token_count: 2, usage_revenue: null, supply_cost: null, gross_profit: null,
    coverage: { row_count: 1, revenue_known_rows: 0, cost_known_rows: 0, account_grain_rows: 0, mixed_currency: false, complete: false },
    observed: { cost_observed_at: null, revenue_observed_at: null, updated_at: null, source: "" },
    runway: { days: 12, level: "warning", reason: "", window_days: 7, covered_days: 7, daily_average: null, balance: { amount_minor: "123450000", currency: "CNY", scale: 6 }, balance_observed_at: "2026-08-28T08:30:00Z" },
    ...over,
  };
}

function stubDetailApi(handlers: { services?: unknown[]; channels?: unknown[]; accounts?: unknown[]; summaries?: unknown[] }) {
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string) => {
      if (url.includes("/api/v1/services")) return Promise.resolve(fakeResponse({ items: handlers.services ?? [ACTIVE_SERVICE] }));
      if (url.includes("/platforms/sub2api/channels")) return Promise.resolve(fakeResponse(channelPage(handlers.channels ?? [boundChannelRow()])));
      if (url.includes("/finance/upstream-accounts")) return Promise.resolve(fakeResponse({ items: handlers.accounts ?? [upstreamAccount()] }));
      if (url.includes("/finance/upstreams/summary")) return Promise.resolve(fakeResponse({ items: handlers.summaries ?? [upstreamSummary()], from: "", to: "", runway_coverage: {}, runway_thresholds: {} }));
      return Promise.resolve(fakeResponse({ items: [] }));
    }),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("渠道详情：2026-09-02 起接真实渠道目录 + 上游映射", () => {
  it("找到渠道时展示真实名称、映射与「上游映射」卡片；经营核算仍诚实标未接入", async () => {
    stubDetailApi({});
    renderQueryPage(
      "/platforms/sub2api/upstream/detail/channel-a",
      <ChannelDetailPage />,
      "/platforms/:serviceType/upstream/detail/:channelId",
    );

    expect(screen.getByRole("heading", { name: "渠道详情", level: 2 })).not.toBeNull();
    expect(await screen.findByText("OpenAI A")).not.toBeNull();
    // 上游分组来自绑定账号的登记簿——顶部格与「渠道与映射」区块各显示一处
    expect((await screen.findAllByText("gpt-main")).length).toBe(2);
    // 上游映射卡片：已绑定 + 解绑/改绑入口
    expect(screen.getByRole("heading", { name: "上游映射" })).not.toBeNull();
    expect(screen.getByRole("button", { name: "解绑" })).not.toBeNull();
    expect(screen.getByRole("button", { name: "改绑" })).not.toBeNull();
    // 经营核算服务端还没按渠道拆分，继续显式未接入，不编数字
    expect(screen.getAllByText("未接入").length).toBeGreaterThan(0);
    expect(screen.queryByText("¥0.00")).toBeNull();
    expect(screen.getByRole("link", { name: "返回 Sub2API 渠道管理" }).getAttribute("href")).toBe(
      "/platforms/sub2api?tab=upstream",
    );
  });

  it("「类型」字段按绑定账号的接入方式派生：订阅账号 / 上游渠道 / 未映射", async () => {
    stubDetailApi({ accounts: [upstreamAccount({ access_method: "subscription_account" })] });
    renderQueryPage(
      "/platforms/sub2api/upstream/detail/channel-a",
      <ChannelDetailPage />,
      "/platforms/:serviceType/upstream/detail/:channelId",
    );
    await screen.findByText("OpenAI A");
    // 「类型」（本片新增，粗二分）与「接入方式」（既有，三态）在 subscription_account
    // 这个值上恰好显示同一个字样，因此按「类型」这个 <dt> 的同级 <dd> 精确定位,
    // 不用全局文字查找——否则会撞上「接入方式」那一条
    const typeTerm = screen.getByText("类型", { selector: "dt" });
    const typeDetail = typeTerm.nextElementSibling as HTMLElement;
    expect(within(typeDetail).getByText("订阅账号")).not.toBeNull();
  });

  it("「容量与调度」一节：8 个 XM-CHAN-FIELDS0 占位字段全部显式未接入", async () => {
    stubDetailApi({});
    renderQueryPage(
      "/platforms/sub2api/upstream/detail/channel-a",
      <ChannelDetailPage />,
      "/platforms/:serviceType/upstream/detail/:channelId",
    );
    await screen.findByText("OpenAI A");
    const section = screen.getByRole("heading", { name: "容量与调度" }).closest("section");
    expect(section).not.toBeNull();
    const withinSection = within(section as HTMLElement);
    for (const label of ["容量 / 并发", "调度", "今日统计", "用量窗口", "最近使用", "创建时间", "过期时间", "代理"]) {
      expect(withinSection.getByText(label)).not.toBeNull();
    }
    expect(withinSection.getAllByText("未接入").length).toBe(8);
  });

  it("未绑定的渠道：上游映射卡片显示候选/未映射状态，可以确认绑定", async () => {
    stubDetailApi({
      channels: [
        boundChannelRow({
          binding: null,
          candidate: { state: "candidate", evidence_status: "sufficient", upstream_account_ids: ["up-1"], reason_codes: ["base_url_match"], platform_assignment_missing: false, inventory_unknown: false },
        }),
      ],
    });
    renderQueryPage(
      "/platforms/sub2api/upstream/detail/channel-a",
      <ChannelDetailPage />,
      "/platforms/:serviceType/upstream/detail/:channelId",
    );

    expect(await screen.findByText("待确认")).not.toBeNull();
    expect(screen.getByText("base_url_match")).not.toBeNull();
    expect(screen.getByRole("button", { name: "确认绑定" })).not.toBeNull();
    expect(screen.queryByRole("button", { name: "解绑" })).toBeNull();
  });

  it("确认绑定需要填写理由才能提交", async () => {
    stubDetailApi({ channels: [boundChannelRow({ binding: null, candidate: { state: "unmapped", evidence_status: "insufficient", upstream_account_ids: [], reason_codes: [], platform_assignment_missing: false, inventory_unknown: false } })] });
    renderQueryPage(
      "/platforms/sub2api/upstream/detail/channel-a",
      <ChannelDetailPage />,
      "/platforms/:serviceType/upstream/detail/:channelId",
    );
    fireEvent.click(await screen.findByRole("button", { name: "确认绑定" }));
    const dialog = within(await screen.findByRole("dialog"));
    fireEvent.change(dialog.getByLabelText(/上游账号 id/), { target: { value: "up-9" } });
    fireEvent.click(dialog.getByRole("button", { name: "保存" }));
    expect(await dialog.findByText(/必须写明为什么确认这条映射/)).not.toBeNull();
  });

  it("确认绑定成功后显示回执并刷新渠道目录", async () => {
    const fetchMock = vi.fn((url: string, init?: RequestInit) => {
      if (init?.method === "POST" && url.includes("finance.platform_channel_binding.set")) {
        return Promise.resolve(fakeResponse({ action_run_id: "run-77" }));
      }
      if (url.includes("/api/v1/services")) return Promise.resolve(fakeResponse({ items: [ACTIVE_SERVICE] }));
      if (url.includes("/platforms/sub2api/channels")) {
        return Promise.resolve(
          fakeResponse(
            channelPage([
              boundChannelRow({
                binding: null,
                candidate: { state: "candidate", evidence_status: "sufficient", upstream_account_ids: ["up-1"], reason_codes: [], platform_assignment_missing: false, inventory_unknown: false },
              }),
            ]),
          ),
        );
      }
      if (url.includes("/finance/upstream-accounts")) return Promise.resolve(fakeResponse({ items: [upstreamAccount()] }));
      if (url.includes("/finance/upstreams/summary")) return Promise.resolve(fakeResponse({ items: [upstreamSummary()], from: "", to: "", runway_coverage: {}, runway_thresholds: {} }));
      return Promise.resolve(fakeResponse({ items: [] }));
    });
    vi.stubGlobal("fetch", fetchMock);
    renderQueryPage(
      "/platforms/sub2api/upstream/detail/channel-a",
      <ChannelDetailPage />,
      "/platforms/:serviceType/upstream/detail/:channelId",
    );

    fireEvent.click(await screen.findByRole("button", { name: "确认绑定" }));
    const dialog = within(await screen.findByRole("dialog"));
    fireEvent.change(dialog.getByLabelText(/上游账号 id/), { target: { value: "up-1" } });
    fireEvent.change(dialog.getByLabelText(/理由/), { target: { value: "核对过 base_url 一致" } });
    fireEvent.click(dialog.getByRole("button", { name: "保存" }));

    expect(await screen.findByText(/run_id run-77/)).not.toBeNull();
    const post = fetchMock.mock.calls.find((c) => (c[1] as RequestInit)?.method === "POST");
    expect(post?.[0]).toBe("/api/v1/actions/finance.platform_channel_binding.set/versions/1/execute");
    const body = JSON.parse((post?.[1] as RequestInit).body as string) as { params: Record<string, string> };
    expect(body.params).toMatchObject({
      service_id: "svc-1",
      external_channel_id: "channel-a",
      upstream_account_id: "up-1",
      reason: "核对过 base_url 一致",
    });
  });

  it("解绑必须填写理由，且带上 expected_binding_id 做乐观并发校验", async () => {
    const fetchMock = vi.fn((url: string, init?: RequestInit) => {
      if (init?.method === "POST") return Promise.resolve(fakeResponse({ action_run_id: "run-88" }));
      if (url.includes("/api/v1/services")) return Promise.resolve(fakeResponse({ items: [ACTIVE_SERVICE] }));
      if (url.includes("/platforms/sub2api/channels")) return Promise.resolve(fakeResponse(channelPage([boundChannelRow()])));
      if (url.includes("/finance/upstream-accounts")) return Promise.resolve(fakeResponse({ items: [upstreamAccount()] }));
      if (url.includes("/finance/upstreams/summary")) return Promise.resolve(fakeResponse({ items: [upstreamSummary()], from: "", to: "", runway_coverage: {}, runway_thresholds: {} }));
      return Promise.resolve(fakeResponse({ items: [] }));
    });
    vi.stubGlobal("fetch", fetchMock);
    renderQueryPage(
      "/platforms/sub2api/upstream/detail/channel-a",
      <ChannelDetailPage />,
      "/platforms/:serviceType/upstream/detail/:channelId",
    );

    fireEvent.click(await screen.findByRole("button", { name: "解绑" }));
    const dialog = within(await screen.findByRole("dialog"));
    fireEvent.click(dialog.getByRole("button", { name: "确认解绑" }));
    expect(await dialog.findByText(/必须写明为什么解绑/)).not.toBeNull();

    fireEvent.change(dialog.getByLabelText(/理由/), { target: { value: "该渠道已停用" } });
    fireEvent.click(dialog.getByRole("button", { name: "确认解绑" }));

    await waitFor(() => {
      expect(fetchMock.mock.calls.some((c) => String(c[0]).includes("finance.platform_channel_binding.remove"))).toBe(true);
    });
    const post = fetchMock.mock.calls.find((c) => String(c[0]).includes("finance.platform_channel_binding.remove"));
    const body = JSON.parse((post?.[1] as RequestInit).body as string) as { params: Record<string, string> };
    expect(body.params).toMatchObject({ expected_binding_id: "bind-1", reason: "该渠道已停用" });
  });

  it("目录里没有这个 channelId 时给出未找到的空态", async () => {
    stubDetailApi({ channels: [] });
    renderQueryPage(
      "/platforms/sub2api/upstream/detail/channel-a",
      <ChannelDetailPage />,
      "/platforms/:serviceType/upstream/detail/:channelId",
    );
    expect(await screen.findByText("渠道目录里没有这一条")).not.toBeNull();
  });

  it("没有唯一 active service 时说明原因，不猜一个 service 出来", async () => {
    stubDetailApi({ services: [{ ...ACTIVE_SERVICE, status: "degraded" }] });
    renderQueryPage(
      "/platforms/sub2api/upstream/detail/channel-a",
      <ChannelDetailPage />,
      "/platforms/:serviceType/upstream/detail/:channelId",
    );
    expect(await screen.findByText("无法定位这条渠道")).not.toBeNull();
  });

  it("渠道目录读取失败时给错误态，可重试", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/api/v1/services")) return Promise.resolve(fakeResponse({ items: [ACTIVE_SERVICE] }));
        if (url.includes("/platforms/sub2api/channels")) return Promise.resolve(fakeResponse({ error: { code: "boom", message: "读取失败" } }, 500));
        return Promise.resolve(fakeResponse({ items: [] }));
      }),
    );
    renderQueryPage(
      "/platforms/sub2api/upstream/detail/channel-a",
      <ChannelDetailPage />,
      "/platforms/:serviceType/upstream/detail/:channelId",
    );
    expect(await screen.findByRole("button", { name: /重试/ })).not.toBeNull();
  });

  it("未知平台与空语义不渲染为另一平台的详情，且不发请求", () => {
    const fetchMock = vi.fn(() => Promise.reject(new Error("must not fetch")));
    vi.stubGlobal("fetch", fetchMock);
    renderPage(
      "/platforms/cpa/upstream/detail/channel-a",
      <ChannelDetailPage />,
      "/platforms/:serviceType/upstream/detail/:channelId",
    );
    expect(screen.getByRole("heading", { name: "页面不存在", level: 2 })).not.toBeNull();
    expect(screen.getByText(/平台 cpa 不支持渠道详情/)).not.toBeNull();
    expect(screen.queryByRole("heading", { name: "渠道详情", level: 2 })).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("上游详情 / 添加上游：仍是 UI-only 壳，返回入口跟着上游管理并入渠道管理页改名", () => {
  it("上游详情明确展示比例、余额、成本和凭据边界，返回入口指向渠道管理页", () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    renderPage(
      "/platforms/newapi/suppliers/upstream-a",
      <UpstreamDetailPage />,
      "/platforms/:serviceType/suppliers/:upstreamId",
    );

    expect(screen.getByRole("heading", { name: "上游详情", level: 2 })).not.toBeNull();
    expect(screen.getAllByText("upstream-a").length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText("充值比例与成本口径")).not.toBeNull();
    expect(screen.getByText("上游全部分组")).not.toBeNull();
    expect(screen.getByText(/上游账号密码、API Key 与 Token 永不回显/)).not.toBeNull();
    const link = screen.getByRole("link", { name: "返回 NewAPI 上游管理" });
    expect(link.getAttribute("href")).toBe("/platforms/newapi?tab=upstream");
    expect(screen.queryAllByRole("button")).toHaveLength(0);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("`suppliers/new` 是只读字段蓝图，所有输入禁用且没有提交按钮；返回入口同样指向渠道管理页", () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    renderPage(
      "/platforms/sub2api/suppliers/new",
      <SupplierCreatePage />,
      "/platforms/:serviceType/suppliers/new",
    );

    expect(screen.getByRole("heading", { name: "添加上游", level: 2 })).not.toBeNull();
    expect(screen.getByText(/只读表单蓝图：当前不会保存、提交或调用任何 Action/)).not.toBeNull();
    expect(screen.getAllByRole("textbox").length).toBeGreaterThan(0);
    expect(screen.getAllByRole("textbox").every((input) => (input as HTMLInputElement).disabled)).toBe(true);
    const link = screen.getByRole("link", { name: "返回 Sub2API 上游管理" });
    expect(link.getAttribute("href")).toBe("/platforms/sub2api?tab=upstream");
    expect(screen.queryAllByRole("button")).toHaveLength(0);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
