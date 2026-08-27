import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactElement } from "react";
import { RouterProvider, createMemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { devLogin, devLogout } from "./auth";
import { routes } from "./router";

// 指标键抽成常量而不是就地写字面量：`xxx_key: "……"` 这个形状会被 gitleaks 的
// generic-api-key 规则当成泄露的密钥（同一条误报见 pages/OverviewPage.test.tsx
// 与 api/platform.test.ts）。本仓禁止加 gitleaks allowlist（会顺手掩盖真报，
// 见 scripts/check-governance.sh），所以换个写法比放宽扫描器划算。
const REVENUE_METRIC = "sub2api.revenue.daily";
const CHANNEL_BALANCE_METRIC = "sub2api.channels.balance";

const metricsBody = {
  items: [
    {
      metric_key: REVENUE_METRIC,
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

function stubFetch(handler: (url: string, init?: RequestInit) => Response) {
  const fn = vi.fn((input: string, init?: RequestInit) => Promise.resolve(handler(input, init)));
  vi.stubGlobal("fetch", fn);
  return fn;
}

const channelsBody = {
  items: [
    {
      metric_key: CHANNEL_BALANCE_METRIC,
      source: "sub2api-prod",
      environment: "development",
      watermark: "wm-9",
      value: {
        channel_count: 2,
        channels: [
          {
            channel_id: "ch-a",
            channel_name: "渠道甲",
            balance_minor_units: 10000,
            currency: "CNY",
            token_valid: true,
          },
          {
            channel_id: "ch-b",
            channel_name: "渠道乙",
            balance_minor_units: 2500,
            currency: "CNY",
            token_valid: false,
          },
        ],
      },
      freshness: {
        state: "fresh",
        staleness_seconds: 30,
        threshold_seconds: 1800,
        is_partial: false,
        observed_at: "2026-08-26T10:00:00Z",
        last_success: "2026-08-26T10:00:00Z",
        last_error_code: "",
      },
    },
  ],
};

const historyBody = {
  items: [0, 1, 2, 3].map((i) => ({
    observed_at: `2026-08-26T0${i}:00:00Z`,
    synced_at: `2026-08-26T0${i}:05:00Z`,
    status: i === 2 ? "failed" : "ok",
    is_partial: false,
    watermark: `wm-${i}`,
    last_error_code: i === 2 ? "upstream_timeout" : "",
    value: { amount_minor_units: 1000 + i * 100, currency: "CNY" },
  })),
};

const auditEvent = {
  sequence: 2,
  occurred_at: "2026-08-26T10:00:00Z",
  principal_id: "dev-operator",
  principal_type: "HUMAN",
  action_id: "registry.service.create",
  action_version: "1",
  action_run_id: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
  resource_type: "core.service",
  resource_id: "svc-1",
  environment: "development",
  request_id: "req-7",
  result: "succeeded",
  error_code: "",
  before_summary: null,
  after_summary: { instance_id: "sub2api-dev", status: "active" },
  event_hash: "a".repeat(64),
  prev_hash: "b".repeat(64),
};

/** XM-0033：一条 OPEN 未投递的严重告警 + 一条已静默的警告。
 *
 *  刻意配成这两条：前者是「已触发但没人被通知到」，后者是「静默不是解决」——
 *  两个最容易在界面上被显示错的状态。 */
const alertsBody = {
  items: [
    {
      id: "aaaa1111-2222-3333-4444-555555555555",
      rule_key: "metric.sync.failed",
      dedup_key: `metric.sync.failed:development:${REVENUE_METRIC}`,
      severity: "critical",
      status: "OPEN",
      title: "指标 sub2api.revenue.daily 同步失败",
      detail: "来源 sub2api-prod，错误码 upstream_timeout",
      environment: "development",
      source_metric_key: REVENUE_METRIC,
      opened_at: "2026-08-26T10:00:00Z",
      last_seen_at: "2026-08-26T10:05:00Z",
      acknowledged_at: null,
      resolved_at: null,
      fire_count: 6,
      notify_status: "failed",
      notify_error: "telegram: HTTP 502",
      notified_at: null,
    },
    {
      id: "bbbb1111-2222-3333-4444-555555555555",
      rule_key: "channel.balance.low",
      dedup_key: "channel.balance.low:development:ch-b",
      severity: "warning",
      status: "SILENCED",
      title: "渠道乙 余额不足",
      detail: "余额 2500 低于阈值 500000（均为最小货币单位，CNY）",
      environment: "development",
      source_metric_key: CHANNEL_BALANCE_METRIC,
      opened_at: "2026-08-26T09:00:00Z",
      last_seen_at: "2026-08-26T10:05:00Z",
      acknowledged_at: null,
      resolved_at: null,
      fire_count: 12,
      notify_status: "pending",
      notify_error: "",
      notified_at: null,
    },
  ],
};

function okHandler(url: string): Response {
  // history 必须排在 metrics 前面：两者的前缀是包含关系
  if (url.startsWith("/api/v1/metrics/history")) return fakeResponse(200, historyBody);
  if (url.startsWith("/api/v1/metrics")) return fakeResponse(200, metricsBody);
  if (url.startsWith("/api/v1/services")) return fakeResponse(200, servicesBody);
  if (url.startsWith("/api/v1/alerts")) return fakeResponse(200, alertsBody);
  if (url.startsWith("/api/v1/audit/events"))
    return fakeResponse(200, { items: [auditEvent], next_before: 0 });
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

  it("已登录访问 /dashboard 显示三段式导航（ADMIN-IA v2）", async () => {
    devLogin();
    renderRoute("/dashboard");
    expect(await screen.findByRole("heading", { name: "运营总览", level: 2 })).not.toBeNull();

    const nav = await screen.findByRole("navigation");
    for (const section of ["全局", "被管平台", "平台治理"]) {
      expect(within(nav).getByRole("heading", { name: section })).not.toBeNull();
    }
    // 告警中心来自 XM-0033，已合入 release，所以它是真链接而不是占位
    for (const name of ["运营总览", "告警中心", "审计事件", "注册表", "设置"]) {
      expect(within(nav).getByRole("link", { name })).not.toBeNull();
    }
    // 被管平台段由 /api/v1/services 驱动，要等这一次请求回来
    expect(await within(nav).findByRole("link", { name: "Sub2API" })).not.toBeNull();
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
      // 读看板要 registry.read + ops.read；审计页要 audit.read；
      // 服务登记/观测上报要 registry.service.manage（XM-0026 加的）；
      // 告警的确认与静默各要一个（XM-0033，刻意不合并成一个 scope）。
      // 告警的**读**路径复用 ops.read，所以这里没有第七个。
      "X-Dev-Scopes":
        "registry.read,ops.read,audit.read,registry.service.manage,alerts.alert.manage,alerts.silence.manage",
    });
  });

  it("403 时提示缺少的权限名", async () => {
    stubFetch(() =>
      fakeResponse(403, {
        error: { code: "PERMISSION_DENIED", message: "缺少权限 ops.read", request_id: "req-7" },
      }),
    );
    renderRoute("/dashboard");
    // 总览页有两条独立的 query（指标 + 告警），两个都会进错误态：
    // 用 findAll 而不是 find。它们**必须**分开，指标端点挂掉时告警卡
    // 仍要能显示（规格 §9.2 把告警列为总览的固定一项）。
    expect((await screen.findAllByText("无权访问")).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/ops\.read/).length).toBeGreaterThan(0);
  });

  it("网络失败时显示可重试的错误态", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.reject(new TypeError("Failed to fetch"))));
    renderRoute("/dashboard");
    expect((await screen.findAllByText("加载失败")).length).toBeGreaterThan(0);
    expect(screen.getAllByRole("button", { name: "重试" }).length).toBeGreaterThan(0);
  });
});

describe("演示数据横幅", () => {
  beforeEach(() => devLogin());
  afterEach(() => vi.unstubAllGlobals());

  it("指标 source 命中已知演示实例时挂出横幅，且没有关闭按钮（Codex #8）", async () => {
    const demo = { items: metricsBody.items.map((m) => ({ ...m, source: "sub2api-staging" })) };
    stubFetch((url) =>
      url.startsWith("/api/v1/metrics") && !url.startsWith("/api/v1/metrics/history")
        ? fakeResponse(200, demo)
        : okHandler(url),
    );
    renderRoute("/dashboard");
    expect(await screen.findByText(/当前展示的是演示数据（Fake 连接器）/)).not.toBeNull();
    // 可关闭的警告等于「点一次就永远看不见的警告」
    expect(screen.queryByRole("button", { name: /关闭|知道了|不再提示/ })).toBeNull();
  });

  it("source 没命中就不挂——误报会把横幅变成人人无视的噪音", async () => {
    stubFetch(okHandler); // metricsBody 里的 source 是 sub2api-prod
    renderRoute("/dashboard");
    await screen.findByText("Sub2API 日收入");
    expect(screen.queryByText(/当前展示的是演示数据/)).toBeNull();
  });
});

describe("注册表页（原服务清单）", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("表格列出实例、状态徽章与采集新鲜度", async () => {
    renderRoute("/registry");
    expect(await screen.findByText("sub2api-dev")).not.toBeNull();
    expect(screen.getByText("降级")).not.toBeNull();
    expect(screen.getByText("数据新鲜")).not.toBeNull();
    expect(screen.getByText(/落后 2 分钟/)).not.toBeNull();
    expect(screen.getByText(/数据时间 2026-08-26 10:00:00 UTC/)).not.toBeNull();
  });
});

describe("总览页的迷你趋势图", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("卡片渲染之后才去拉历史，拿到后画出折线", async () => {
    const fetchMock = stubFetch(okHandler);
    renderRoute("/dashboard");

    // 首屏那一批请求里没有 history：它是挂载之后才发的
    await screen.findByText("Sub2API 日收入");
    expect(String(fetchMock.mock.calls[0]?.[0])).not.toContain("/metrics/history");

    expect(
      await screen.findByRole("img", { name: /Sub2API 日收入 近 24 小时趋势/ }),
    ).not.toBeNull();
  });

  it("历史接口挂了只影响趋势那一小块，卡片主体照常显示", async () => {
    stubFetch((url) =>
      url.startsWith("/api/v1/metrics/history")
        ? fakeResponse(500, { error: { code: "INTERNAL", message: "服务内部错误" } })
        : okHandler(url),
    );
    renderRoute("/dashboard");

    expect(await screen.findByText("¥1,234.56")).not.toBeNull();
    expect((await screen.findAllByText("趋势不可用")).length).toBeGreaterThan(0);
    // 整页没有被换成错误态
    expect(screen.queryByText("加载失败")).toBeNull();
  });

  it("头部显示最后刷新时刻（HH:MM:SS）", async () => {
    renderRoute("/dashboard");
    await screen.findByText("Sub2API 日收入");
    expect(screen.getByText(/最后刷新 \d{2}:\d{2}:\d{2}/)).not.toBeNull();
  });
});

describe("Sub2API 平台详情·渠道/资源页签（原渠道明细页）", () => {
  beforeEach(() => {
    devLogin();
    stubFetch((url) =>
      url.startsWith("/api/v1/metrics/history") ? okHandler(url) : url.startsWith("/api/v1/metrics")
        ? fakeResponse(200, channelsBody)
        : okHandler(url),
    );
  });
  afterEach(() => vi.unstubAllGlobals());

  it("逐渠道列出余额、币种与令牌状态，并带上该指标的新鲜度", async () => {
    renderRoute("/platforms/sub2api?tab=resources");
    expect(await screen.findByText("渠道甲")).not.toBeNull();
    expect(screen.getByText("¥100.00")).not.toBeNull();
    expect(screen.getByText("¥25.00")).not.toBeNull();
    expect(screen.getByText("有效")).not.toBeNull();
    expect(screen.getByText("失效")).not.toBeNull();
    // 新鲜度徽章与数据时间跟着面板搬进页签，没有在迁移里丢掉（规格 §9.1）
    expect(screen.getByText("数据新鲜")).not.toBeNull();
    expect(screen.getByText(/数据时间 2026-08-26 10:00:00 UTC/)).not.toBeNull();
    expect(screen.getByText(/合计 ¥125\.00/)).not.toBeNull();
  });

  it("指标未初始化时不画表，明说没有可信明细（宪法 12 条）", async () => {
    const uninitialized = {
      items: [
        {
          ...channelsBody.items[0],
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
    stubFetch((url) =>
      url.startsWith("/api/v1/metrics") && !url.startsWith("/api/v1/metrics/history")
        ? fakeResponse(200, uninitialized)
        : okHandler(url),
    );
    renderRoute("/platforms/sub2api?tab=resources");
    // 页头徽章与空态各一处，都在说同一件事
    expect((await screen.findAllByText("未初始化")).length).toBe(2);
    expect(screen.getByText(/没有可信的渠道明细/)).not.toBeNull();
    expect(screen.queryByText("渠道甲")).toBeNull();
  });

  it("该环境没有渠道余额指标时给空态而不是空表", async () => {
    stubFetch((url) =>
      url.startsWith("/api/v1/metrics") && !url.startsWith("/api/v1/metrics/history")
        ? fakeResponse(200, { items: [] })
        : okHandler(url),
    );
    renderRoute("/platforms/sub2api?tab=resources");
    expect(await screen.findByText("暂无渠道余额指标")).not.toBeNull();
  });
});

describe("审计事件页", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("列出序号、动作、资源、结果与哈希前 8 位", async () => {
    renderRoute("/audit");
    expect(await screen.findByText("registry.service.create@1")).not.toBeNull();
    expect(screen.getByText("2")).not.toBeNull();
    expect(screen.getByText("core.service/svc-1")).not.toBeNull();
    expect(screen.getByText("成功")).not.toBeNull();
    expect(screen.getByText("aaaaaaaa")).not.toBeNull();
  });

  it("本页只有一条时不装作能验链，明说上一条不在本页（Codex #6）", async () => {
    renderRoute("/audit");
    expect(await screen.findByText("上一条不在本页")).not.toBeNull();
    // 页头不再断言「前序哈希必须等于下一行的事件哈希」
    expect(screen.queryByText(/必须.*相等/)).toBeNull();
    expect(screen.getByText(/完整性校验以 audit-verify 工具与链根签名为准/)).not.toBeNull();
  });

  it("环境过滤造成的序号缺口显示成中性说明，不报断链", async () => {
    // 后端的链是全局的，本页按环境过滤，6/4/2 这样的缺口完全正常：
    // 缺口两侧的 prev_hash 与 event_hash 本就不必相等
    stubFetch((url) =>
      url.startsWith("/api/v1/audit/events")
        ? fakeResponse(200, {
            items: [
              { ...auditEvent, sequence: 6, event_hash: "c".repeat(64), prev_hash: "x".repeat(64) },
              { ...auditEvent, sequence: 2, event_hash: "d".repeat(64) },
            ],
            next_before: 0,
          })
        : okHandler(url),
    );
    renderRoute("/audit");
    expect(await screen.findByText("中间有 3 条其他环境事件")).not.toBeNull();
    expect(screen.queryByText("与相邻行对不上")).toBeNull();
  });

  it("展开区给出完整哈希，不是只有 8 位前缀 + hover（Codex #9）", async () => {
    renderRoute("/audit");
    fireEvent.click(await screen.findByRole("button", { name: "详情" }));
    expect(await screen.findByText("a".repeat(64))).not.toBeNull();
    expect(screen.getByText("b".repeat(64))).not.toBeNull();
  });

  it("行可展开显示前后摘要，前态为 null 时明说「无」", async () => {
    renderRoute("/audit");
    fireEvent.click(await screen.findByRole("button", { name: "详情" }));
    expect(await screen.findByText("（无）")).not.toBeNull();
    expect(screen.getByText(/sub2api-dev/)).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "收起" }));
    await waitFor(() => expect(screen.queryByText("（无）")).toBeNull());
  });

  it("next_before 有值时给「加载更多」，点了追加下一页", async () => {
    stubFetch((url) => {
      if (!url.startsWith("/api/v1/audit/events")) return okHandler(url);
      return url.includes("before_seq=2")
        ? fakeResponse(200, { items: [{ ...auditEvent, sequence: 1 }], next_before: 0 })
        : fakeResponse(200, { items: [auditEvent], next_before: 2 });
    });
    renderRoute("/audit");

    fireEvent.click(await screen.findByRole("button", { name: "加载更多" }));
    await waitFor(() => expect(screen.getByText("1")).not.toBeNull());
    expect(await screen.findByText(/已到最早一条/)).not.toBeNull();
  });

  it("没有事件时给出「去执行一次动作」的引导", async () => {
    stubFetch((url) =>
      url.startsWith("/api/v1/audit/events")
        ? fakeResponse(200, { items: [], next_before: 0 })
        : okHandler(url),
    );
    renderRoute("/audit");
    expect(await screen.findByText("还没有审计事件")).not.toBeNull();
    expect(screen.getByText("在注册表页执行一次动作试试")).not.toBeNull();
  });

  it("缺 audit.read 时提示缺哪个权限", async () => {
    stubFetch((url) =>
      url.startsWith("/api/v1/audit/events")
        ? fakeResponse(403, {
            error: { code: "PERMISSION_DENIED", message: "缺少权限 audit.read" },
          })
        : okHandler(url),
    );
    renderRoute("/audit");
    expect(await screen.findByText("无权访问")).not.toBeNull();
    expect(screen.getByText(/audit\.read/)).not.toBeNull();
  });
});

describe("登记服务（写路径）", () => {
  beforeEach(() => {
    devLogin();
  });
  afterEach(() => vi.unstubAllGlobals());

  function fillRequired() {
    fireEvent.change(screen.getByLabelText(/服务类型/), { target: { value: "sub2api" } });
    fireEvent.change(screen.getByLabelText(/实例标识/), { target: { value: "sub2api-new" } });
    fireEvent.change(screen.getByLabelText(/对外地址/), {
      target: { value: "https://new.example.com" },
    });
    fireEvent.change(screen.getByLabelText(/负责人/), { target: { value: "平台组" } });
  }

  async function openDialog() {
    renderRoute("/registry");
    fireEvent.click(await screen.findByRole("button", { name: "登记服务" }));
    return within(await screen.findByRole("dialog"));
  }

  it("环境取自身份且只读，不做成可选下拉", async () => {
    stubFetch(okHandler);
    await openDialog();
    const env = screen.getByLabelText("环境") as HTMLInputElement;
    // servicesBody 里那条记录是 development
    expect(env.value).toBe("development");
    expect(env.readOnly).toBe(true);
  });

  it("必填项没填就不发请求，就地给出报错", async () => {
    const fetchMock = stubFetch(okHandler);
    const dialog = await openDialog();
    const before = fetchMock.mock.calls.length;

    fireEvent.click(dialog.getByRole("button", { name: "登记" }));
    expect(await screen.findByText("服务类型必填")).not.toBeNull();
    expect(fetchMock.mock.calls.length).toBe(before);
  });

  it("提交失败时焦点移到错误摘要（Codex #9：不然读屏用户只听到一片沉默）", async () => {
    stubFetch(okHandler);
    const dialog = await openDialog();
    fireEvent.click(dialog.getByRole("button", { name: "登记" }));

    const summary = await screen.findByText(/还有 4 处需要修正/);
    const focusTarget = summary.closest("[tabindex]");
    await waitFor(() => expect(document.activeElement).toBe(focusTarget));
  });

  it("地址里带凭据时就地拒绝，不把 token 送进审计摘要（Codex #7）", async () => {
    const fetchMock = stubFetch(okHandler);
    const dialog = await openDialog();
    fillRequired();
    fireEvent.change(screen.getByLabelText(/对外地址/), {
      target: { value: "https://u:p@new.example.com/?token=abc" },
    });
    const before = fetchMock.mock.calls.length;

    fireEvent.click(dialog.getByRole("button", { name: "登记" }));
    expect(await screen.findByText(/用户名\/密码/)).not.toBeNull();
    expect(fetchMock.mock.calls.length).toBe(before);
  });

  it("标识符非法时按后端同一条正则给出提示", async () => {
    stubFetch(okHandler);
    const dialog = await openDialog();
    fillRequired();
    fireEvent.change(screen.getByLabelText(/实例标识/), { target: { value: "Sub2API_New" } });
    fireEvent.click(dialog.getByRole("button", { name: "登记" }));
    expect(await screen.findByText(/只能用小写字母、数字与短横线/)).not.toBeNull();
  });

  it("提交走 Action 执行入口：只带 params，request_id 在头里", async () => {
    const fetchMock = stubFetch((url, init) => {
      if (init?.method === "POST") return fakeResponse(200, { action_run_id: "run-42" });
      return okHandler(url);
    });
    const dialog = await openDialog();
    fillRequired();
    fireEvent.click(dialog.getByRole("button", { name: "登记" }));

    await waitFor(() => expect(screen.getByText(/已登记，run_id=run-42/)).not.toBeNull());

    const post = fetchMock.mock.calls.find((c) => (c[1] as RequestInit)?.method === "POST");
    expect(post?.[0]).toBe("/api/v1/actions/registry.service.create/versions/1/execute");
    const init = post?.[1] as RequestInit;
    expect(JSON.parse(String(init.body))).toEqual({
      params: {
        service_type: "sub2api",
        instance_id: "sub2api-new",
        environment: "development",
        endpoint: "https://new.example.com",
        owner: "平台组",
      },
    });
    expect((init.headers as Record<string, string>)["X-Request-ID"]).toMatch(
      /^[A-Za-z0-9._:-]{1,128}$/,
    );
    // 回执把 run_id 指回审计页，但**不承诺**那里一定有这条事件：
    // 业务写/ActionRun/审计三段非原子且 fail-open（Codex #10）
    expect(screen.getByText(/审计事件通常几秒内出现在审计页/)).not.toBeNull();
    expect(screen.queryByText(/可在审计页查看这条事件/)).toBeNull();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("403 时弹窗留在原地，显示错误码与缺少的权限", async () => {
    stubFetch((url, init) => {
      if (init?.method === "POST") {
        return fakeResponse(403, {
          error: {
            code: "PERMISSION_DENIED",
            message: "缺少权限 registry.service.manage",
            request_id: "req-9",
          },
        });
      }
      return okHandler(url);
    });
    const dialog = await openDialog();
    fillRequired();
    fireEvent.click(dialog.getByRole("button", { name: "登记" }));

    expect(await screen.findByText(/错误码 PERMISSION_DENIED/)).not.toBeNull();
    expect(screen.getByText(/需要权限：registry\.service\.manage/)).not.toBeNull();
    expect(screen.getByText(/req-9/)).not.toBeNull();
    // 表单没被清掉，人不用重填
    expect((screen.getByLabelText(/实例标识/) as HTMLInputElement).value).toBe("sub2api-new");
    expect(screen.getByRole("dialog")).not.toBeNull();
  });
});

describe("上报观测（写路径）", () => {
  beforeEach(() => devLogin());
  afterEach(() => vi.unstubAllGlobals());

  it("提交后回执带 run_id，并重新拉一次服务列表", async () => {
    const fetchMock = stubFetch((url, init) => {
      if (init?.method === "POST") return fakeResponse(200, { action_run_id: "run-7" });
      return okHandler(url);
    });
    renderRoute("/registry");
    fireEvent.click(await screen.findByRole("button", { name: "上报观测" }));

    const dialog = within(await screen.findByRole("dialog"));
    const watermark = screen.getByLabelText(/数据水位/) as HTMLInputElement;
    expect(watermark.value).toMatch(/^wm-\d{8}T\d{6}Z$/);

    fireEvent.click(dialog.getByRole("button", { name: "上报" }));
    await waitFor(() => expect(screen.getByText(/已上报观测，run_id=run-7/)).not.toBeNull());

    const post = fetchMock.mock.calls.find((c) => (c[1] as RequestInit)?.method === "POST");
    expect(post?.[0]).toBe("/api/v1/actions/registry.service.observe/versions/1/execute");
    expect(JSON.parse(String((post?.[1] as RequestInit).body))).toMatchObject({
      params: { service_type: "sub2api", instance_id: "sub2api-dev", status: "degraded" },
    });
  });
});

// --- XM-0034 导航重构 ---

describe("三段式导航：被管平台段由 Registry 驱动", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("Registry 里有的平台可点，没有的在场但点不动（§12 惯例：显示未接入，不隐藏）", async () => {
    renderRoute("/dashboard");
    const nav = await screen.findByRole("navigation");
    // servicesBody 只登记了 sub2api
    expect(await within(nav).findByRole("link", { name: "Sub2API" })).not.toBeNull();

    // NewAPI 没登记：文字在、里程碑标签在，但不是链接
    expect(within(nav).getByText("NewAPI")).not.toBeNull();
    expect(within(nav).getByText("未接入·M1")).not.toBeNull();
    expect(within(nav).queryByRole("link", { name: /NewAPI/ })).toBeNull();
  });

  it("开票系统标成契约草案，不冒充成某个里程碑", async () => {
    renderRoute("/dashboard");
    const nav = await screen.findByRole("navigation");
    await within(nav).findByRole("link", { name: "Sub2API" });
    expect(within(nav).getByText("契约草案·XM-0028")).not.toBeNull();
  });

  it("读不到注册表时说「读取失败」，不说「未接入」", async () => {
    stubFetch((url) =>
      url.startsWith("/api/v1/services")
        ? fakeResponse(403, {
            error: { code: "FORBIDDEN", message: "缺少 scope: registry.read" },
          })
        : okHandler(url),
    );
    renderRoute("/dashboard");
    const nav = await screen.findByRole("navigation");
    // 拿一次 403 去断言「这个平台没接入」，与新鲜度铁律禁止的是同一类事
    expect((await within(nav).findAllByText("读取失败")).length).toBeGreaterThan(0);
    expect(within(nav).queryByText("未接入·M1")).toBeNull();
  });
});

describe("三段式导航：全局段顺序与禁用项", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("告警中心排在全局段的运营总览与审计事件之间（XM-0033 已合入，占位换成真链接）", async () => {
    renderRoute("/dashboard");
    const nav = await screen.findByRole("navigation");
    const alerts = within(nav).getByRole("link", { name: "告警中心" });
    expect(alerts.getAttribute("href")).toBe("/alerts");
    // 顺序照 ADMIN-IA 的全局段：运营总览 / 告警中心 / 审计事件
    const globalLinks = within(nav)
      .getAllByRole("link")
      .map((el) => el.textContent)
      .filter((t) => t && ["运营总览", "告警中心", "审计事件"].includes(t));
    expect(globalLinks).toEqual(["运营总览", "告警中心", "审计事件"]);
  });

  it("财务中心与变更与审批同样是禁用态，而不是从导航上消失", async () => {
    renderRoute("/dashboard");
    const nav = await screen.findByRole("navigation");
    for (const label of ["财务中心", "变更与审批"]) {
      expect(within(nav).getByText(label)).not.toBeNull();
      expect(within(nav).queryByRole("link", { name: new RegExp(label) })).toBeNull();
    }
  });
});

describe("v1 旧路径重定向（书签不失效）", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("/services 重定向到平台治理·注册表", async () => {
    renderRoute("/services");
    expect(await screen.findByRole("heading", { name: "注册表", level: 2 })).not.toBeNull();
    expect(await screen.findByText("sub2api-dev")).not.toBeNull();
  });

  it("/channels 重定向到 Sub2API 的渠道/资源页签，而不是概览", async () => {
    renderRoute("/channels");
    expect(await screen.findByRole("heading", { name: "Sub2API", level: 2 })).not.toBeNull();
    expect(await screen.findByRole("tab", { name: "渠道/资源", selected: true })).not.toBeNull();
  });
});

describe("平台详情：统一页签模板", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("六格页签的顺序与命名逐格对齐 ADMIN-IA", async () => {
    renderRoute("/platforms/sub2api");
    const tabs = await screen.findAllByRole("tab");
    expect(tabs.map((tab) => tab.textContent)).toEqual([
      "概览",
      "指标趋势",
      "渠道/资源",
      "连接与凭据",
      "告警",
      "操作",
    ]);
  });

  it("默认落在概览，且只显示本平台的指标卡", async () => {
    renderRoute("/platforms/sub2api");
    expect(await screen.findByRole("tab", { name: "概览", selected: true })).not.toBeNull();
    // 复用总览的卡片组件，新鲜度徽章跟着一起过来（规格 §9.1）
    expect(await screen.findByText("Sub2API 日收入")).not.toBeNull();
    expect(screen.getByText("数据延迟")).not.toBeNull();
  });

  it("点「渠道/资源」能切过去，渲染的是迁移进来的渠道面板", async () => {
    renderRoute("/platforms/sub2api");
    // 先等页签渲染出来，再按名字找：带 name 的查询在 jsdom 里每次重试都要算一遍
    // 可访问名，和「等接口回来」挤在同一个超时里容易假失败
    await screen.findAllByRole("tab");
    // Radix 的页签用 mousedown 激活（不是 click）——fireEvent.click 不带 mousedown，
    // 点了不会切。ui-primitives 的用例用 userEvent 走完整指针序列，
    // 但 admin-web 没有这个依赖，本任务也不许新增，于是直接发它真正监听的那个事件
    fireEvent.mouseDown(screen.getByRole("tab", { name: "渠道/资源" }), { button: 0 });
    expect(await screen.findByRole("tab", { name: "渠道/资源", selected: true })).not.toBeNull();
    // okHandler 的指标里没有渠道余额这一条，面板据此给空态而不是一张空表
    expect(await screen.findByText("暂无渠道余额指标")).not.toBeNull();
  });

  it("?tab= 可以直接深链到某一格", async () => {
    renderRoute("/platforms/sub2api?tab=operations");
    expect(await screen.findByRole("tab", { name: "操作", selected: true })).not.toBeNull();
  });

  it("拼错的 tab 参数不报错，回到概览", async () => {
    renderRoute("/platforms/sub2api?tab=拼错了");
    expect(await screen.findByRole("tab", { name: "概览", selected: true })).not.toBeNull();
  });

  it("未实现的页签给诚实占位：写明将来放什么、归哪个任务", async () => {
    renderRoute("/platforms/sub2api?tab=alerts");
    expect(await screen.findByText(/随 XM-0033 告警中心上线/)).not.toBeNull();
  });

  it("操作页签注明 L2+ 需审批，不假装现在就能执行", async () => {
    renderRoute("/platforms/sub2api?tab=operations");
    expect(await screen.findByText(/XM-0030/)).not.toBeNull();
  });
});

describe("平台详情：未接入与未知平台", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("未接入平台给一整屏占位 + 规格里的一句话范围，而不是六个空页签", async () => {
    renderRoute("/platforms/newapi");
    expect(await screen.findByText("未接入，规划于 M1")).not.toBeNull();
    expect(screen.getByText(/上游模型网关/)).not.toBeNull();
    // 摆出页签等于承诺点进去有东西，而这里一格都还没有
    expect(screen.queryAllByRole("tab")).toHaveLength(0);
  });

  it("契约草案平台的占位说的是「等冻结」，不是某个里程碑", async () => {
    renderRoute("/platforms/invoice");
    expect(await screen.findByText(/契约草案 XM-0028 待冻结/)).not.toBeNull();
  });

  it("目录与注册表都不认识的平台明说未知，不是白屏", async () => {
    renderRoute("/platforms/查无此平台");
    expect(await screen.findByText("未知平台")).not.toBeNull();
  });
});

describe("总览卡片的平台入口", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("sub2api.* 卡片带「查看平台 →」，链到该平台详情", async () => {
    renderRoute("/dashboard");
    const links = await screen.findAllByRole("link", { name: "查看平台 Sub2API" });
    expect(links.length).toBeGreaterThan(0);
    expect(links[0]?.getAttribute("href")).toBe("/platforms/sub2api");
  });
});

describe("横切行为在迁移后仍然在场", () => {
  beforeEach(() => devLogin());
  afterEach(() => vi.unstubAllGlobals());

  it("演示数据横幅在平台详情页照样挂着（横幅挂在壳上，不跟着页面走）", async () => {
    const demo = { items: metricsBody.items.map((m) => ({ ...m, source: "sub2api-staging" })) };
    stubFetch((url) =>
      url.startsWith("/api/v1/metrics") && !url.startsWith("/api/v1/metrics/history")
        ? fakeResponse(200, demo)
        : okHandler(url),
    );
    renderRoute("/platforms/sub2api");
    expect(await screen.findByText(/当前展示的是演示数据（Fake 连接器）/)).not.toBeNull();
  });

  it("平台详情页缺权限时提示缺哪个 scope，而不是空白", async () => {
    stubFetch((url) =>
      url.startsWith("/api/v1/services")
        ? fakeResponse(403, {
            error: { code: "FORBIDDEN", message: "缺少 scope: registry.read" },
          })
        : okHandler(url),
    );
    renderRoute("/platforms/sub2api");
    expect(await screen.findByText(/registry\.read/)).not.toBeNull();
  });
});

describe("设置页", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("只读展示当前身份与 scope", async () => {
    renderRoute("/settings");
    expect(await screen.findByRole("heading", { name: "设置", level: 2 })).not.toBeNull();
    expect(screen.getByText("dev-operator")).not.toBeNull();
    expect(screen.getByText("registry.read")).not.toBeNull();
    expect(screen.getByText("audit.read")).not.toBeNull();
  });

  it("明说这是「请求时携带的身份」而非服务端授予的权限", async () => {
    renderRoute("/settings");
    expect(await screen.findByText(/服务端为最终裁决者/)).not.toBeNull();
  });

  it("告警规则与静默是占位，注明随 XM-0033 上线", async () => {
    renderRoute("/settings");
    expect(await screen.findByText(/随 XM-0033 告警中心上线/)).not.toBeNull();
  });
});

// --- XM-0033 告警中心 --------------------------------------------------------

describe("告警中心页", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("列表显示严重度、状态、首见/最近、次数与投递状态", async () => {
    renderRoute("/alerts");
    expect(await screen.findByText("指标 sub2api.revenue.daily 同步失败")).not.toBeNull();

    // 严重度与状态各是一个徽章
    expect(screen.getByText("严重")).not.toBeNull();
    expect(screen.getByText("未处理")).not.toBeNull();
    // 首见与最近都要显示：只有一个就答不出「这个问题持续了多久」
    expect(screen.getByText(/首次 2026-08-26 10:00:00 UTC/)).not.toBeNull();
    expect(screen.getAllByText(/最近 2026-08-26 10:05:00 UTC/).length).toBeGreaterThan(0);
    // fire_count：抖了一下与持续了两小时的唯一区分依据
    expect(screen.getByText("6")).not.toBeNull();
    // 规则名翻成中文，原始键仍在 detail 之外可查
    expect(screen.getByText("指标同步失败")).not.toBeNull();
  });

  it("投递失败单独成列并显示原因——「已触发但没人被通知到」必须看得见", async () => {
    renderRoute("/alerts");
    await screen.findByText("指标 sub2api.revenue.daily 同步失败");
    expect(screen.getByText("投递失败")).not.toBeNull();
    expect(screen.getByText("telegram: HTTP 502")).not.toBeNull();
  });

  it("已静默的告警不显示成「已解决」，也不消失（静默不是解决）", async () => {
    renderRoute("/alerts");
    await screen.findByText("渠道乙 余额不足");
    // 它仍然在活跃列表里
    expect(screen.getByText("已静默")).not.toBeNull();
    expect(screen.queryByText("已解决")).toBeNull();
  });

  it("只有 OPEN / REOPENED 有「确认」按钮（与后端 WHERE 子句同一条规则）", async () => {
    renderRoute("/alerts");
    await screen.findByText("渠道乙 余额不足");
    // 两条告警：一条 OPEN、一条 SILENCED，所以只该有一个确认按钮
    expect(screen.getAllByRole("button", { name: "确认" })).toHaveLength(1);
  });

  it("零告警显示「无活动告警」，并提醒评估任务可能停了", async () => {
    stubFetch((url) =>
      url.startsWith("/api/v1/alerts") ? fakeResponse(200, { items: [] }) : okHandler(url),
    );
    renderRoute("/alerts");
    expect(await screen.findByText("无活动告警")).not.toBeNull();
    // 零告警既可能是好消息，也可能是评估器停了——不能只报喜
    expect(screen.getByText(/评估任务在跑/)).not.toBeNull();
  });

  it("确认走 Action 执行入口，回执带 run_id", async () => {
    const fetchMock = stubFetch((url, init) => {
      if (init?.method === "POST") return fakeResponse(200, { action_run_id: "run-ack-1" });
      return okHandler(url);
    });
    renderRoute("/alerts");
    fireEvent.click(await screen.findByRole("button", { name: "确认" }));

    await waitFor(() => expect(screen.getByText(/已确认，run_id=run-ack-1/)).not.toBeNull());
    const post = fetchMock.mock.calls.find(
      (c) => (c[1] as RequestInit | undefined)?.method === "POST",
    ) as unknown as [string, RequestInit];
    expect(post[0]).toBe("/api/v1/actions/alerts.alert.acknowledge/versions/1/execute");
    // 只带 params；request_id 在头里（后端 DisallowUnknownFields）
    expect(JSON.parse(String(post[1].body))).toEqual({
      params: { alert_id: "aaaa1111-2222-3333-4444-555555555555" },
    });
    expect((post[1].headers as Record<string, string>)["X-Request-ID"]).toBeTruthy();
  });

  it("确认被拒时错误就地显示，告警行不消失", async () => {
    stubFetch((url, init) => {
      if (init?.method === "POST")
        return fakeResponse(403, {
          error: {
            code: "PERMISSION_DENIED",
            message: "缺少权限 alerts.alert.manage",
            request_id: "req-9",
          },
        });
      return okHandler(url);
    });
    renderRoute("/alerts");
    fireEvent.click(await screen.findByRole("button", { name: "确认" }));

    expect(await screen.findByText(/缺少权限 alerts\.alert\.manage/)).not.toBeNull();
    // 一次 403 不该把这条告警从列表里抹掉，人还要看着它继续处理
    expect(screen.getByText("指标 sub2api.revenue.daily 同步失败")).not.toBeNull();
    expect(screen.getByText(/request_id: req-9/)).not.toBeNull();
  });
});

describe("创建静默窗口（写路径）", () => {
  beforeEach(() => devLogin());
  afterEach(() => vi.unstubAllGlobals());

  async function openSilenceDialog() {
    renderRoute("/alerts");
    fireEvent.click(await screen.findByRole("button", { name: "创建静默窗口" }));
    return within(await screen.findByRole("dialog"));
  }

  it("理由为空时不发请求，就地报错", async () => {
    const fetchMock = stubFetch(okHandler);
    const dialog = await openSilenceDialog();
    const before = fetchMock.mock.calls.filter(
      (c) => (c[1] as RequestInit | undefined)?.method === "POST",
    ).length;

    fireEvent.click(dialog.getByRole("button", { name: "创建" }));
    // 没有理由的静默在事后复盘时与「有人手滑」不可区分
    expect(await screen.findByText(/为什么静默/)).not.toBeNull();
    const after = fetchMock.mock.calls.filter(
      (c) => (c[1] as RequestInit | undefined)?.method === "POST",
    ).length;
    expect(after).toBe(before);
  });

  it("默认是全局静默，提交时 rule_key 传空串", async () => {
    const fetchMock = stubFetch((url, init) => {
      if (init?.method === "POST") return fakeResponse(200, { action_run_id: "run-s-1" });
      return okHandler(url);
    });
    const dialog = await openSilenceDialog();
    fireEvent.change(screen.getByLabelText(/理由/), {
      target: { value: "上游 Sub2API 维护窗口" },
    });
    fireEvent.click(dialog.getByRole("button", { name: "创建" }));

    await waitFor(() => expect(screen.getByText(/已创建静默窗口，run_id=run-s-1/)).not.toBeNull());
    const post = fetchMock.mock.calls.find(
      (c) => (c[1] as RequestInit | undefined)?.method === "POST",
    ) as unknown as [string, RequestInit];
    expect(post[0]).toBe("/api/v1/actions/alerts.silence.create/versions/1/execute");
    expect(JSON.parse(String(post[1].body))).toEqual({
      params: { rule_key: "", duration_minutes: 60, reason: "上游 Sub2API 维护窗口" },
    });
  });
});

describe("运营总览的告警卡", () => {
  beforeEach(() => devLogin());
  afterEach(() => vi.unstubAllGlobals());

  it("显示活跃告警计数与严重度分布", async () => {
    stubFetch(okHandler);
    renderRoute("/dashboard");
    // alertsBody 里两条：1 严重 + 1 警告（已静默的照样算）
    expect(await screen.findByText("严重 1")).not.toBeNull();
    expect(screen.getByText("警告 1")).not.toBeNull();
    expect(screen.getByRole("link", { name: "查看全部告警" })).not.toBeNull();
  });

  it("零告警显示「无活动告警」而不是一个大大的 0", async () => {
    // 0 和「还没接上」在一个数字上长得一模一样，而这两件事在运营上完全相反
    stubFetch((url) =>
      url.startsWith("/api/v1/alerts") ? fakeResponse(200, { items: [] }) : okHandler(url),
    );
    renderRoute("/dashboard");
    expect(await screen.findByText("无活动告警")).not.toBeNull();
    expect(screen.queryByText("严重 0")).toBeNull();
  });

  it("指标端点挂掉时告警卡照常显示（两条 query 是分开的）", async () => {
    stubFetch((url) => {
      if (url.startsWith("/api/v1/alerts")) return okHandler(url);
      if (url.startsWith("/api/v1/metrics"))
        return fakeResponse(500, { error: { code: "INTERNAL", message: "服务内部错误" } });
      return okHandler(url);
    });
    renderRoute("/dashboard");
    // 规格 §9.2 把告警列为总览的固定一项：指标挂了正是最需要看见告警的时候
    expect(await screen.findByText("严重 1")).not.toBeNull();
    expect(screen.getAllByText("加载失败").length).toBeGreaterThan(0);
  });
});
