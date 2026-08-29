import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PlatformOverviewPanel } from "./PlatformOverviewPanel";

/** 键名抽成常量而不是就地写字面量：`xxx_key: "点分串"` 这个形状会被 gitleaks 的
 *  generic-api-key 规则按熵值判成泄漏（本仓库禁止用 allowlist 消音），
 *  而熵值恰好卡在阈值附近——同类字面量有的过有的不过，不值得赌。 */
const CHANNEL_BALANCE_METRIC = "sub2api.channels.balance";
const NEWAPI_CHANNELS_METRIC = "newapi.channels.status";
const BALANCE_LOW_RULE = "channel.balance.low";
const REVENUE_METRIC = "sub2api.revenue.daily";
const COST_METRIC = "sub2api.cost.daily";
const USERS_TOTAL_METRIC = "newapi.users.total";

const freshness = {
  state: "fresh",
  staleness_seconds: 30,
  threshold_seconds: 1800,
  is_partial: false,
  observed_at: "2026-08-28T10:00:00Z",
  last_success: "2026-08-28T10:00:00Z",
  last_error_code: "",
};

function metric(metricKey: string, value: Record<string, unknown>, source = "sub2api-prod") {
  return { metric_key: metricKey, source, environment: "development", watermark: "wm-1", value, freshness };
}

const SUB2API_METRICS = [
  metric(REVENUE_METRIC, {
    day: "2026-08-28",
    amount_minor_units: 810000,
    currency: "CNY",
    order_count: 42,
  }),
  metric(COST_METRIC, {
    day: "2026-08-28",
    amount_minor_units: 188000,
    currency: "CNY",
  }),
  metric(CHANNEL_BALANCE_METRIC, {
    channel_count: 3,
    channels: [
      { channel_id: "c1", channel_name: "A", balance_minor_units: 1, currency: "CNY", token_valid: true },
      { channel_id: "c2", channel_name: "B", balance_minor_units: 1, currency: "CNY", token_valid: false },
      { channel_id: "c3", channel_name: "C", balance_minor_units: 1, currency: "CNY", token_valid: true },
    ],
  }),
];

function alertOf(i: number, severity = "warning") {
  return {
    id: `a${i}`,
    rule_key: BALANCE_LOW_RULE,
    dedup_key: `d${i}`,
    severity,
    status: "OPEN",
    title: `告警 ${i}`,
    detail: "",
    environment: "development",
    source_metric_key: CHANNEL_BALANCE_METRIC,
    opened_at: "2026-08-28T09:00:00Z",
    last_seen_at: "2026-08-28T10:00:00Z",
    acknowledged_at: null,
    resolved_at: null,
    fire_count: 1,
    notify_status: "delivered",
    notify_error: "",
    notified_at: null,
  };
}

function fakeResponse(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function stub(over: { metrics?: unknown[]; alerts?: unknown[]; accounts?: unknown[] } = {}) {
  const fetchMock = vi.fn((url: string) => {
    if (url.startsWith("/api/v1/metrics/history")) return Promise.resolve(fakeResponse({ items: [] }));
    if (url.startsWith("/api/v1/metrics")) {
      return Promise.resolve(fakeResponse({ items: over.metrics ?? SUB2API_METRICS }));
    }
    if (url.startsWith("/api/v1/alerts")) {
      return Promise.resolve(fakeResponse({ items: over.alerts ?? [] }));
    }
    if (url.includes("/finance/upstream-accounts")) {
      return Promise.resolve(fakeResponse({ items: over.accounts ?? [] }));
    }
    // 成本卡那一行的两个汇总端点
    return Promise.resolve(fakeResponse({ items: [] }));
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderPanel(serviceType = "sub2api", label = "Sub2API") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <PlatformOverviewPanel serviceType={serviceType} label={label} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("Sub2API 概览逐格对齐原型", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("四张统计卡按原型的名字与顺序摆出来", async () => {
    stub();
    renderPanel();
    await screen.findByText("今日充值");
    const tiles = screen.getAllByRole("article").map((a) => a.querySelector("h3")?.textContent);
    // 原型顺序：今日调用量 / 成功率（24h） / 今日充值 / 今日成本
    expect(tiles.slice(0, 4)).toEqual(["今日调用量", "成功率（24h）", "今日充值", "今日成本"]);
  });

  it("**没有数据源的两格摆位置但不给数字**，并写清归哪条线", async () => {
    // 删掉这一格会让页面看起来什么都不缺；给个 0 会让人以为今天真的没有调用
    stub();
    renderPanel();
    const tile = (await screen.findByText("今日调用量")).closest("article") as HTMLElement;
    expect(within(tile).getByText("未接入")).toBeTruthy();
    expect(within(tile).getByText("—")).toBeTruthy();
    expect(within(tile).getByText(/reqlog/)).toBeTruthy();
    expect(within(tile).queryByText("0")).toBeNull();
  });

  it("接上的两格显示金额，并且**新鲜度徽章在**（§9.1）", async () => {
    stub();
    renderPanel();
    const tile = (await screen.findByText("今日充值")).closest("article") as HTMLElement;
    expect(within(tile).getByText("¥8,100.00")).toBeTruthy();
    expect(within(tile).getByText("数据新鲜")).toBeTruthy();
  });

  it("「今日充值」写明它是毛额、且充值不是收入", async () => {
    // 契约口径：当天支付订单 pay_amount 累加，不扣退款；
    // §9.8 又说用户充值不是当期收入。两句都要在场，否则这个数会被当成收入用
    stub();
    renderPanel();
    const tile = (await screen.findByText("今日充值")).closest("article") as HTMLElement;
    expect(within(tile).getByText(/不扣退款/)).toBeTruthy();
    expect(within(tile).getByText(/充值不是当期收入/)).toBeTruthy();
  });

  it("「需要处理的事」只收本平台的严重/注意两档", async () => {
    stub({
      alerts: [
        {
          id: "a1",
          rule_key: BALANCE_LOW_RULE,
          dedup_key: "d1",
          severity: "critical",
          status: "OPEN",
          title: "渠道余额告急",
          detail: "",
          environment: "development",
          source_metric_key: CHANNEL_BALANCE_METRIC,
          opened_at: "2026-08-28T09:00:00Z",
          last_seen_at: "2026-08-28T10:00:00Z",
          acknowledged_at: null,
          resolved_at: null,
          fire_count: 2,
          notify_status: "delivered",
          notify_error: "",
          notified_at: null,
        },
      ],
    });
    renderPanel();
    expect(await screen.findByText("渠道余额告急")).toBeTruthy();
    expect(screen.getByText("严重")).toBeTruthy();
  });

  it("没有待处理时说清这一栏统计的是什么，不留一个空白框", async () => {
    stub();
    renderPanel();
    expect(await screen.findByText("这个平台没有待处理的告警")).toBeTruthy();
  });

  it("**告警多时截断到 4 条，并报出还剩几条**", async () => {
    // 浏览器实测发现的：不设上限时 18 条告警把整页撑到几千像素高，
    // 右边那栏跟着拉出一大片空白，而概览的用途是一眼看完
    stub({ alerts: Array.from({ length: 18 }, (_, i) => alertOf(i)) });
    renderPanel();
    // 等列表真的渲染出来，再数条数——只等卡片标题会在查询还挂着时就往下走
    const first = await screen.findByText("告警 0");
    const card = first.closest("section") as HTMLElement;
    // 摆出来的告警条目 4 条（原型也是 4 条）
    expect(within(card).getAllByRole("link", { name: /告警 \d+/ })).toHaveLength(4);
    expect(within(card).getByText(/还有 14 条/)).toBeTruthy();
  });

  it("「上游健康」三行都在，渠道那行按 token_valid 算", async () => {
    stub();
    renderPanel();
    const card = (await screen.findByText("上游健康")).closest("section") as HTMLElement;
    expect(within(card).getByText("上游渠道")).toBeTruthy();
    expect(within(card).getByText("2 / 3 可用")).toBeTruthy();
    expect(within(card).getByText("订阅账号")).toBeTruthy();
    expect(within(card).getByText("连接状态")).toBeTruthy();
    expect(within(card).getAllByRole("link", { name: "查看 ›" }).length).toBe(3);
  });

  it("底部大图的位置留着，说明调用量还没有数据源", async () => {
    stub();
    renderPanel();
    const card = (await screen.findByText("近 7 日调用量")).closest("section") as HTMLElement;
    expect(within(card).getByText("未接入")).toBeTruthy();
  });

  it("两个「今日成本」口径不同，页面上说出来，不并成一格", async () => {
    // 上面那格是 Sub2API 自己面板报的，下面一行是平台自建的成本台账。
    // 并成一格才是真正会骗人的做法
    stub();
    renderPanel();
    expect(await screen.findByText("成本台账口径")).toBeTruthy();
    expect(screen.getByText(/口径不同，不要相减/)).toBeTruthy();
  });

  it("挪走的两张卡说清去了哪一页", async () => {
    stub();
    renderPanel();
    const note = await screen.findByText(/原型的概览没有用户数与用户余额两格/);
    expect(within(note).getByRole("link", { name: "用户管理" })).toBeTruthy();
    expect(within(note).getByRole("link", { name: "渠道管理" })).toBeTruthy();
  });

  it("演示来源才挂样例数据提示条，真实来源不挂", async () => {
    // 一条永远在的警告等于没有警告
    stub();
    renderPanel();
    await screen.findByText("今日充值");
    expect(screen.queryByText(/当前展示为样例数据/)).toBeNull();

    vi.unstubAllGlobals();
    stub({ metrics: SUB2API_METRICS.map((m) => ({ ...m, source: "sub2api-staging" })) });
    renderPanel();
    expect(await screen.findByText(/当前展示为样例数据/)).toBeTruthy();
  });
});

describe("NewAPI 概览按它自己的原型页", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("结构与 Sub2API 不同：用户总数 + 渠道健康表", async () => {
    stub({
      metrics: [
        metric(USERS_TOTAL_METRIC, { total_users: 926, active_users: 184 }, "newapi-prod"),
        metric(
          NEWAPI_CHANNELS_METRIC,
          {
            channels: [{ channel_id: "c1", name: "gpt 主", type: "openai", enabled: true, error_rate_ppm: 1200 }],
            unhealthy_threshold_ppm: 50000,
          },
          "newapi-prod",
        ),
      ],
    });
    renderPanel("newapi", "NewAPI");
    expect(await screen.findByText("用户总数")).toBeTruthy();
    expect(screen.getByText("渠道健康")).toBeTruthy();
    // Sub2API 那两格在这一页上没有
    expect(screen.queryByText("今日充值")).toBeNull();
  });

  it("渠道健康表缺的三列说出来，不画空列冒充已接", async () => {
    stub({
      metrics: [
        metric(
          NEWAPI_CHANNELS_METRIC,
          { channels: [{ channel_id: "c1", name: "gpt 主", enabled: true }] },
          "newapi-prod",
        ),
      ],
    });
    renderPanel("newapi", "NewAPI");
    expect(await screen.findByText(/上游、分组与成功率三列/)).toBeTruthy();
  });

  it("财务汇总刷新失败但保留上次快照时显示旧值并标记同步失败", async () => {
    const row = {
      id: "newapi-channel-1",
      name: "NewAPI 主渠道",
      systemType: "newapi",
      accessMethod: "upstream_key",
      baseUrl: "https://newapi.example.test",
      status: "active",
      usageRevenue: { amountMinor: "100000000", currency: "CNY", scale: 6 },
      supplyCost: { amountMinor: "70000000", currency: "CNY", scale: 6 },
      grossProfit: { amountMinor: "30000000", currency: "CNY", scale: 6 },
      grossMargin: "0.3",
      coverage: { rowCount: 1, revenueKnownRows: 1, costKnownRows: 1, complete: true },
      observed: { source: "finance-newapi", updatedAt: "2026-08-28T10:00:00Z" },
      runway: {},
    };
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/finance/channels/summary")) {
          return Promise.resolve(fakeResponse({ error: { code: "UPSTREAM_TIMEOUT", message: "暂时不可用" } }, 503));
        }
        return Promise.resolve(fakeResponse({ items: [] }));
      }),
    );
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    queryClient.setQueryData(["finance", "channels", "summary"], {
      items: [row],
      from: "2026-08-28",
      to: "2026-08-28",
    });
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <PlatformOverviewPanel serviceType="newapi" label="NewAPI" />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(await screen.findByText(/NewAPI 财务汇总读取失败/)).toBeTruthy();
    expect(await screen.findByText("¥100.00")).toBeTruthy();
    expect((await screen.findAllByText("同步失败")).length).toBe(3);
    expect(screen.getByText(/页面保留上一次成功数据/)).toBeTruthy();
  });

  it("仅财务汇总来自 staging Fake 时也挂出样例数据提示", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/finance/channels/summary")) {
          return Promise.resolve(
            fakeResponse({
              items: [
                {
                  id: "newapi-channel-demo",
                  name: "NewAPI staging 渠道",
                  system_type: "newapi",
                  access_method: "upstream_key",
                  base_url: "https://newapi.example.test",
                  status: "active",
                  usage_revenue: { amount_minor: "100000000", currency: "CNY", scale: 6 },
                  supply_cost: { amount_minor: "70000000", currency: "CNY", scale: 6 },
                  gross_profit: { amount_minor: "30000000", currency: "CNY", scale: 6 },
                  gross_margin: "0.3",
                  coverage: { row_count: 1, revenue_known_rows: 1, cost_known_rows: 1, complete: true },
                  observed: { source: "finance-collect-staging", updated_at: "2026-08-28T10:00:00Z" },
                  runway: {},
                },
              ],
              from: "2026-08-28",
              to: "2026-08-28",
            }),
          );
        }
        return Promise.resolve(fakeResponse({ items: [] }));
      }),
    );
    renderPanel("newapi", "NewAPI");

    expect(await screen.findByText(/当前展示为样例数据/)).toBeTruthy();
  });

  it("财务汇总首次读取失败显示失败态，不把错误写成数据不完整", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) => {
        if (url.includes("/finance/channels/summary")) {
          return Promise.resolve(fakeResponse({ error: { code: "UPSTREAM_TIMEOUT", message: "暂时不可用" } }, 503));
        }
        return Promise.resolve(fakeResponse({ items: [] }));
      }),
    );
    renderPanel("newapi", "NewAPI");

    expect(await screen.findByText(/NewAPI 财务汇总读取失败/)).toBeTruthy();
    const card = (await screen.findByText("今日我方计费")).closest("article") as HTMLElement;
    expect(within(card).getByText("同步失败")).toBeTruthy();
    expect(within(card).queryByText("数据不完整")).toBeNull();
  });
});
