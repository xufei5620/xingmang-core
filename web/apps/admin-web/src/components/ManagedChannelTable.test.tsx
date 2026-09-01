import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ManagedChannelTable } from "./ManagedChannelTable";

/** ManagedChannelTable：Sub2API/NewAPI 渠道管理的 ChannelRef 粒度表
 *  （原型 `V["s2/upstream"]` / `V["newapi/upstream"]`，2026-09-02 裁定纠正）。
 *
 *  这一片把它从 XM-C-MAP0 做的映射工作台（KPI 是目录渠道/已确认映射/待处理
 *  冲突/目录完整性，筛选是映射状态）改回原型的渠道表：候选/冲突/绑定操作
 *  搬到渠道详情页，这里只画列、筛选、视图与 4 格顶部——4 格顶部现在由父组件
 *  `ChannelTable` 统一渲染，不属于这个组件，所以这里不测它。 */

function response(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function channelPage(items: unknown[]) {
  return {
    service: { id: "svc-1", service_type: "newapi", instance_id: "newapi-a", environment: "development" },
    inventory: { state: "ok", source: "newapi-a", observed_at: "2026-08-29T01:00:00Z", complete: true, truncated: false, reported_count: items.length, fetched_count: items.length, coverage_partial: false, evidence: "reported_count" },
    from: "2026-08-29", to: "2026-08-29",
    items,
    runway_coverage: { total: 1, known: 1, reasons: {} }, next_cursor: null,
  };
}

function channelRow(over: Record<string, unknown> = {}) {
  return {
    channel_ref: { service_id: "svc-1", external_channel_id: "1" },
    name: "OpenAI A",
    binding: { id: "b1", upstream_account_id: "up-1", valid_from: "2026-08-28T00:00:00Z", reason: "人工确认" },
    candidate: { state: "candidate", evidence_status: "sufficient", upstream_account_ids: ["up-1"], reason_codes: [], platform_assignment_missing: false, inventory_unknown: false },
    economics: null, economics_state: "binding_pending_economics", conflicts: [],
    health: { state: "observed" }, models: { count: 4 }, assurance: null, runway: { days: 12 },
    observed: { source: "newapi-a", observed_at: "2026-08-29T01:00:00Z", is_stale: false },
    ...over,
  };
}

function upstreamAccount(over: Record<string, unknown> = {}) {
  return {
    id: "up-1",
    system_type: "newapi",
    access_method: "upstream_key",
    upstream_name: "Relay 甲",
    upstream_contact: "",
    upstream_group: "gpt-main",
    base_url: "https://relay-a.example.com",
    credential_ref: "secret://xm/upstream/a",
    recharge_ratio: "1.15",
    group_rate: "",
    recharge_cost_rate: "0.869565217",
    currency: "CNY",
    business_day_tz: "+08:00",
    platform_id: "newapi",
    status: "active",
    environment: "development",
    metered: true,
    token_mappings: [],
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
    ...over,
  };
}

function upstreamSummary(over: Record<string, unknown> = {}) {
  return {
    id: "up-1", name: "relay-a", supplier_key: "", system_type: "newapi", access_method: "upstream_key",
    base_url: "https://relay-a.example.com", recharge_cost_rate: "0.869565217", credential_ref: "",
    status: "active", token_count: 2,
    usage_revenue: null, supply_cost: null, gross_profit: null,
    coverage: { row_count: 1, revenue_known_rows: 0, cost_known_rows: 0, account_grain_rows: 0, mixed_currency: false, complete: false },
    observed: { cost_observed_at: null, revenue_observed_at: null, updated_at: null, source: "" },
    runway: { days: 12, level: "warning", reason: "", window_days: 7, covered_days: 7, daily_average: null, balance: { amount_minor: "123450000", currency: "CNY", scale: 6 }, balance_observed_at: "2026-08-28T08:30:00Z" },
    ...over,
  };
}

function stub(handlers: { channels?: unknown[]; channelsStatus?: number; accounts?: unknown[]; summaries?: unknown[] }) {
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string) => {
      if (url.includes("/platforms/newapi/channels")) {
        if (handlers.channelsStatus && handlers.channelsStatus >= 400) {
          return Promise.resolve(response({ error: { code: "EXECUTION_FAILED", message: "目录读取失败" } }, handlers.channelsStatus));
        }
        return Promise.resolve(response(channelPage(handlers.channels ?? [channelRow()])));
      }
      if (url.includes("/finance/upstream-accounts")) {
        return Promise.resolve(response({ items: handlers.accounts ?? [upstreamAccount()] }));
      }
      if (url.includes("/finance/upstreams/summary")) {
        return Promise.resolve(response({
          items: handlers.summaries ?? [upstreamSummary()],
          from: "2026-08-28", to: "2026-08-28",
          runway_coverage: { total: 1, known: 1, reasons: {} },
          runway_thresholds: { critical_days: 5, warning_days: 10, serious_days: 20 },
        }));
      }
      return Promise.resolve(response({ items: [] }));
    }),
  );
}

function renderPanel() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <ManagedChannelTable platform="newapi" serviceId="svc-1" />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("ChannelRef 粒度渠道表（原型逐格对齐）", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("列头逐字对齐原型（NewAPI 没有成功率列）", async () => {
    stub({});
    renderPanel();
    const table = within(await screen.findByRole("table"));
    for (const header of ["NewAPI 渠道", "平台 / 来源", "上游分组", "可用模型", "供给成本", "我方计费消耗", "余额状态", "毛利", "状态", "详情"]) {
      expect(table.getByRole("columnheader", { name: header })).toBeTruthy();
    }
    expect(table.queryByRole("columnheader", { name: "成功率" })).toBeNull();
  });

  it("Sub2API 多一列成功率，且恒为未接入 · M1.5", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/platforms/sub2api/channels")) return Promise.resolve(response(channelPage([channelRow()])));
        if (url.includes("/finance/upstream-accounts")) return Promise.resolve(response({ items: [upstreamAccount({ platform_id: "sub2api" })] }));
        if (url.includes("/finance/upstreams/summary")) return Promise.resolve(response({ items: [upstreamSummary()], from: "", to: "", runway_coverage: {}, runway_thresholds: {} }));
        return Promise.resolve(response({ items: [] }));
      }),
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <ManagedChannelTable platform="sub2api" serviceId="svc-1" />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    const table = within(await screen.findByRole("table"));
    expect(table.getByRole("columnheader", { name: /成功率/ })).toBeTruthy();
    expect(table.getByText("未接入 · M1.5")).toBeTruthy();
  });

  it("已绑定的渠道在「平台 / 来源」列显示上游账号的名字，不是它自己的 id", async () => {
    stub({});
    renderPanel();
    const table = within(await screen.findByRole("table"));
    expect(await table.findByText("Relay 甲")).toBeTruthy();
    expect(table.queryByText("up-1")).toBeNull();
  });

  it("未绑定的渠道显示「未映射」徽章，不编一个上游名字", async () => {
    stub({
      channels: [
        channelRow({
          channel_ref: { service_id: "svc-1", external_channel_id: "2" },
          name: "Claude B",
          binding: null,
          candidate: { state: "unmapped", evidence_status: "insufficient", upstream_account_ids: [], reason_codes: [], platform_assignment_missing: false, inventory_unknown: false },
        }),
      ],
    });
    renderPanel();
    const table = within(await screen.findByRole("table"));
    expect(await table.findByText("未映射")).toBeTruthy();
  });

  it("上游分组来自绑定账号的登记簿字段", async () => {
    stub({});
    renderPanel();
    const table = within(await screen.findByRole("table"));
    expect(await table.findByText("gpt-main")).toBeTruthy();
  });

  it("可用模型有数就显示数量，没有就未接入 · M1.5，不显示 0", async () => {
    stub({ channels: [channelRow({ models: null })] });
    renderPanel();
    const table = within(await screen.findByRole("table"));
    expect(await table.findByText("未接入 · M1.5")).toBeTruthy();
    expect(table.queryByText("0 个模型（仅数量）")).toBeNull();
  });

  it("经营字段（供给成本 / 我方计费消耗 / 毛利）服务端未拆分到渠道前显示未接入", async () => {
    stub({});
    renderPanel();
    const table = within(await screen.findByRole("table"));
    // economics 恒为 null（服务端契约尚未按渠道拆分），三格都应是未接入
    expect((await table.findAllByText("未接入")).length).toBeGreaterThanOrEqual(3);
  });

  it("余额来自绑定上游的汇总，显示共享余额与可用天数", async () => {
    stub({});
    renderPanel();
    const table = within(await screen.findByRole("table"));
    expect(await table.findByText(/约 12 天/)).toBeTruthy();
    expect(table.getByText(/上游账号共享余额/)).toBeTruthy();
  });

  it("状态：未绑定或数据过期都算需关注，绑定且新鲜算健康", async () => {
    stub({
      channels: [
        channelRow({ channel_ref: { service_id: "svc-1", external_channel_id: "fresh" }, name: "新鲜", observed: { source: "x", observed_at: "t", is_stale: false } }),
        channelRow({ channel_ref: { service_id: "svc-1", external_channel_id: "stale" }, name: "过期", observed: { source: "x", observed_at: "t", is_stale: true } }),
        channelRow({ channel_ref: { service_id: "svc-1", external_channel_id: "unmapped" }, name: "未映射的", binding: null, candidate: { state: "unmapped", evidence_status: "insufficient", upstream_account_ids: [], reason_codes: [], platform_assignment_missing: false, inventory_unknown: false } }),
      ],
    });
    renderPanel();
    const table = within(await screen.findByRole("table"));
    await table.findByText("新鲜");
    expect(table.getAllByText("健康").length).toBe(1);
    expect(table.getAllByText("需关注").length).toBe(2);
  });

  it("渠道名与详情列都链接到渠道详情页", async () => {
    stub({});
    renderPanel();
    const table = within(await screen.findByRole("table"));
    await table.findByText("OpenAI A");
    const links = table.getAllByRole("link").filter((l) => l.getAttribute("href")?.includes("/upstream/detail/1"));
    expect(links.length).toBe(2);
  });

  it("吸附首列 + 紧凑密度（原型的 stickyFirstColumn / defaultDensity）", async () => {
    stub({});
    renderPanel();
    const section = (await screen.findByRole("table")).closest("section");
    expect(section?.getAttribute("data-density")).toBe("compact");
  });

  it("视图：全部 / 未映射 / 需关注 三个（原型「全部 / OpenAI / 需关注」，第二个换成我们真能筛的维度）", async () => {
    stub({});
    renderPanel();
    await screen.findByRole("table");
    const viewSelect = screen.getByRole("combobox", { name: "视图" });
    const options = within(viewSelect).getAllByRole("option").map((o) => o.textContent);
    expect(options).toEqual(["全部", "未映射", "需关注", "自定义"]);
  });

  it("目录为空时给空态，不是空表", async () => {
    stub({ channels: [] });
    renderPanel();
    expect(await screen.findByText(/还没有渠道目录/)).toBeTruthy();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("502/读取失败显示页级错误，不保留旧渠道行", async () => {
    stub({ channelsStatus: 502 });
    renderPanel();
    expect(await screen.findByRole("button", { name: /重试/ })).toBeTruthy();
    expect(screen.queryByText("OpenAI A")).toBeNull();
  });

  it("同一上游绑定多个渠道时，各渠道仍是独立一行，且不重复合计共享事实", async () => {
    stub({
      channels: [
        channelRow({ channel_ref: { service_id: "svc-1", external_channel_id: "1" }, name: "OpenAI A" }),
        channelRow({ channel_ref: { service_id: "svc-1", external_channel_id: "2" }, name: "Claude B" }),
      ],
    });
    renderPanel();
    const table = within(await screen.findByRole("table"));
    expect(table.getByText("OpenAI A")).toBeTruthy();
    expect(table.getByText("Claude B")).toBeTruthy();
    // 两行都指向同一个上游名字，但余额只是引用同一份共享事实，不在这里加总
    expect(table.getAllByText("Relay 甲").length).toBe(2);
  });
});
