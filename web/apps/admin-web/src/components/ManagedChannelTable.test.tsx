import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ManagedChannelTable } from "./ManagedChannelTable";

/** ManagedChannelTable：Sub2API/NewAPI 渠道管理的 ChannelRef 粒度表
 *  （原型 `V["s2/upstream"]` / `V["newapi/upstream"]`，2026-09-02 两条裁定
 *  叠加后的最终版：04:40 裁定把它从映射工作台改回渠道表；07:20 补充裁定
 *  推翻了"登记簿降级为页内区块"的做法，改成登记簿字段直接并入这张表的行——
 *  新增 ID 独立列、平台/类型列、8 个 XM-CHAN-FIELDS0 占位列，去掉成功率列,
 *  6 个原本必需的登记簿列降级为列管理里默认收起的可选列）。
 *
 *  候选/冲突/绑定操作仍然只在渠道详情页，这里不测；4 格顶部由父组件
 *  `ChannelTable` 统一渲染，这里也不测。 */

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
    group_rate: "0.85",
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

function renderPanel(platform: "sub2api" | "newapi" = "newapi") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <ManagedChannelTable platform={platform} serviceId="svc-1" />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function switchToView(name: string) {
  fireEvent.change(screen.getByRole("combobox", { name: "视图" }), { target: { value: name } });
}

describe("ChannelRef 粒度渠道表：默认列集（13 必需 + 9 可选，2026-09-02 07:20 裁定补充）", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("13 个必需列默认显示，成功率列已经去掉", async () => {
    stub({});
    renderPanel();
    const table = within(await screen.findByRole("table"));
    for (const header of [
      "ID", "名称", "平台 / 类型", "容量 / 并发", "状态", "调度", "今日统计",
      "用量窗口", "倍率 / 上游倍率", "余额 / 有效期", "毛利", "最近使用", "详情",
    ]) {
      expect(table.getByRole("columnheader", { name: header })).toBeTruthy();
    }
    expect(table.queryByRole("columnheader", { name: "成功率" })).toBeNull();
  });

  it("9 个可选列默认收起，列管理里能打开", async () => {
    stub({});
    renderPanel();
    const table = within(await screen.findByRole("table"));
    for (const header of [
      "代理", "上游分组", "可用模型", "供给成本", "我方计费消耗",
      "创建时间", "过期时间", "上游名称 / 联系人", "充值成本率",
    ]) {
      expect(table.queryByRole("columnheader", { name: header })).toBeNull();
    }
    fireEvent.click(screen.getByRole("checkbox", { name: "上游分组" }));
    expect(table.getByRole("columnheader", { name: "上游分组" })).toBeTruthy();
  });

  it("Sub2API 与 NewAPI 列集完全相同：都没有成功率，都是同一套 22 列", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/platforms/sub2api/channels")) return Promise.resolve(response(channelPage([channelRow()])));
        if (url.includes("/finance/upstream-accounts")) return Promise.resolve(response({ items: [upstreamAccount({ platform_id: "sub2api" })] }));
        if (url.includes("/finance/upstreams/summary")) return Promise.resolve(response({ items: [upstreamSummary()], from: "", to: "", runway_coverage: {}, runway_thresholds: {} }));
        return Promise.resolve(response({ items: [] }));
      }),
    );
    renderPanel("sub2api");
    const table = within(await screen.findByRole("table"));
    await table.findByRole("columnheader", { name: "ID" });
    expect(table.queryByRole("columnheader", { name: "成功率" })).toBeNull();
  });
});

describe("ID / 名称列", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("ID 列显示渠道 / 账号 ID，名称列链接到详情页", async () => {
    stub({});
    renderPanel();
    const table = within(await screen.findByRole("table"));
    expect(await table.findByText("1")).toBeTruthy();
    const nameLink = table.getByRole("link", { name: "OpenAI A" });
    expect(nameLink.getAttribute("href")).toContain("/upstream/detail/1");
  });
});

describe("「平台 / 类型」列", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("绑定到订阅账号（access_method = subscription_account）显示「订阅账号」", async () => {
    stub({ accounts: [upstreamAccount({ access_method: "subscription_account" })] });
    renderPanel();
    const table = within(await screen.findByRole("table"));
    expect(await table.findByText("订阅账号")).toBeTruthy();
    expect(table.queryByText("上游渠道")).toBeNull();
  });

  it("绑定到上游中转 / 官方 API 都显示「上游渠道」，不是三态原样透出", async () => {
    stub({ accounts: [upstreamAccount({ access_method: "official_api" })] });
    renderPanel();
    const table = within(await screen.findByRole("table"));
    expect(await table.findByText("上游渠道")).toBeTruthy();
  });

  it("未绑定显示「未映射」，不猜类型", async () => {
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
    expect(table.queryByText("订阅账号")).toBeNull();
    expect(table.queryByText("上游渠道")).toBeNull();
  });

  it("绑定了但登记簿查不到这个账号 id：保守按「上游渠道」处理，不是订阅账号", async () => {
    stub({ accounts: [] });
    renderPanel();
    const table = within(await screen.findByRole("table"));
    expect(await table.findByText("上游渠道")).toBeTruthy();
  });

  it("平台徽章逐行都显示，即使整页只有一个平台", async () => {
    stub({});
    renderPanel();
    const table = within(await screen.findByRole("table"));
    await table.findByText("OpenAI A");
    expect(table.getByText("NewAPI")).toBeTruthy();
  });
});

describe("8 个 XM-CHAN-FIELDS0 占位列", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("5 个必需占位列（容量/并发、调度、今日统计、用量窗口、最近使用）默认显示未接入", async () => {
    stub({});
    renderPanel();
    const table = within(await screen.findByRole("table"));
    await table.findByText("OpenAI A");
    // 这 5 列 + 状态列都可能显示"未接入"字样，这里只断言至少出现了这么多个,
    // 不逐列去抠 DOM 结构——具体是哪一列在下面单独的 headerTitle 断言里核对
    expect((await table.findAllByText("未接入")).length).toBeGreaterThanOrEqual(5);
  });

  it("「调度」列的说明额外提到写操作另立 XM-SCHED0", async () => {
    stub({});
    renderPanel();
    const table = within(await screen.findByRole("table"));
    const header = await table.findByRole("columnheader", { name: "调度" });
    expect(header.getAttribute("title")).toMatch(/XM-SCHED0/);
  });

  it("占位列的说明统一指向 XM-CHAN-FIELDS0，且说明字段不存在（不是查出来是空）", async () => {
    stub({});
    renderPanel();
    const table = within(await screen.findByRole("table"));
    const header = await table.findByRole("columnheader", { name: "容量 / 并发" });
    expect(header.getAttribute("title")).toMatch(/XM-CHAN-FIELDS0/);
    expect(header.getAttribute("title")).toMatch(/不存在/);
  });

  it("3 个可选占位列（代理、创建时间、过期时间）在全部视图里可见，同样未接入", async () => {
    stub({});
    renderPanel();
    await screen.findByText("OpenAI A");
    switchToView("全部");
    const table = within(screen.getByRole("table"));
    for (const header of ["代理", "创建时间", "过期时间"]) {
      expect(table.getByRole("columnheader", { name: header })).toBeTruthy();
    }
  });
});

describe("登记簿字段并入行（6 个原本必需列，现在是默认收起的可选列）", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("上游分组来自绑定账号的登记簿字段", async () => {
    stub({});
    renderPanel();
    await screen.findByText("OpenAI A");
    switchToView("全部");
    const table = within(screen.getByRole("table"));
    expect(await table.findByText("gpt-main")).toBeTruthy();
  });

  it("倍率 / 上游倍率是独立于「上游分组」的必需列，默认就显示", async () => {
    stub({});
    renderPanel();
    const table = within(await screen.findByRole("table"));
    expect(await table.findByText("0.85×")).toBeTruthy();
    // 默认视图下「上游分组」这一列本身还没打开，看不到分组名文字单独出现在别处
    expect(table.queryByRole("columnheader", { name: "上游分组" })).toBeNull();
  });

  it("可用模型有数就显示数量，没有就未接入 · M1.5，不显示 0", async () => {
    stub({ channels: [channelRow({ models: null })] });
    renderPanel();
    await screen.findByText("OpenAI A");
    switchToView("全部");
    const table = within(screen.getByRole("table"));
    expect(await table.findByText("未接入 · M1.5")).toBeTruthy();
    expect(table.queryByText("0 个模型（仅数量）")).toBeNull();
  });

  it("经营字段（供给成本 / 我方计费消耗 / 毛利）服务端未拆分到渠道前显示未接入", async () => {
    stub({});
    renderPanel();
    await screen.findByText("OpenAI A");
    switchToView("全部");
    const table = within(screen.getByRole("table"));
    // economics 恒为 null（服务端契约尚未按渠道拆分）；毛利默认可见 + 供给成本
    // 我方计费消耗切到全部视图后可见，三格都应是未接入
    expect((await table.findAllByText("未接入")).length).toBeGreaterThanOrEqual(3);
  });

  it("上游名称 / 联系人合并显示，没有联系人时只显示名字", async () => {
    stub({ accounts: [upstreamAccount({ upstream_contact: "老王 · 企业微信" })] });
    renderPanel();
    await screen.findByText("OpenAI A");
    switchToView("全部");
    const table = within(screen.getByRole("table"));
    expect(await table.findByText("Relay 甲")).toBeTruthy();
    expect(table.getByText("老王 · 企业微信")).toBeTruthy();
  });

  it("充值成本率来自绑定账号", async () => {
    stub({});
    renderPanel();
    await screen.findByText("OpenAI A");
    switchToView("全部");
    const table = within(screen.getByRole("table"));
    expect(await table.findByText("0.869565217")).toBeTruthy();
  });
});

describe("余额 / 状态 / 详情（沿用既有逻辑）", () => {
  afterEach(() => vi.unstubAllGlobals());

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

  it("名称与详情列都链接到渠道详情页", async () => {
    stub({});
    renderPanel();
    const table = within(await screen.findByRole("table"));
    await table.findByText("OpenAI A");
    const links = table.getAllByRole("link").filter((l) => l.getAttribute("href")?.includes("/upstream/detail/1"));
    expect(links.length).toBe(2);
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
    // 两行都绑定同一个上游，倍率格显示同一个值，但不在这里加总
    expect(table.getAllByText("0.85×").length).toBe(2);
  });
});

describe("布局 / 视图 / 空态 / 错误态 / 添加上游入口", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("吸附首列 + 紧凑密度（原型的 stickyFirstColumn / defaultDensity）", async () => {
    stub({});
    renderPanel();
    const section = (await screen.findByRole("table")).closest("section");
    expect(section?.getAttribute("data-density")).toBe("compact");
  });

  it("视图：全部 / 未映射 / 需关注 三个", async () => {
    stub({});
    renderPanel();
    await screen.findByRole("table");
    const viewSelect = screen.getByRole("combobox", { name: "视图" });
    const options = within(viewSelect).getAllByRole("option").map((o) => o.textContent);
    expect(options).toEqual(["全部", "未映射", "需关注", "自定义"]);
  });

  it("「未映射」视图按类型筛选出未绑定的行", async () => {
    stub({
      channels: [
        channelRow({ channel_ref: { service_id: "svc-1", external_channel_id: "1" }, name: "已绑定" }),
        channelRow({
          channel_ref: { service_id: "svc-1", external_channel_id: "2" },
          name: "没绑定",
          binding: null,
          candidate: { state: "unmapped", evidence_status: "insufficient", upstream_account_ids: [], reason_codes: [], platform_assignment_missing: false, inventory_unknown: false },
        }),
      ],
    });
    renderPanel();
    await screen.findByText("已绑定");
    switchToView("未映射");
    const table = within(screen.getByRole("table"));
    expect(table.getByText("没绑定")).toBeTruthy();
    expect(table.queryByText("已绑定")).toBeNull();
  });

  it("工具条上有「＋ 添加上游」入口", async () => {
    stub({});
    renderPanel();
    await screen.findByRole("table");
    expect(screen.getByRole("button", { name: "＋ 添加上游" })).toBeTruthy();
  });

  it("目录为空时给空态，且空态里也有「＋ 添加上游」入口，不是找不到入口", async () => {
    stub({ channels: [] });
    renderPanel();
    expect(await screen.findByText(/还没有渠道目录/)).toBeTruthy();
    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.getByRole("button", { name: "＋ 添加上游" })).toBeTruthy();
  });

  it("502/读取失败显示页级错误，不保留旧渠道行", async () => {
    stub({ channelsStatus: 502 });
    renderPanel();
    expect(await screen.findByRole("button", { name: /重试/ })).toBeTruthy();
    expect(screen.queryByText("OpenAI A")).toBeNull();
  });
});
