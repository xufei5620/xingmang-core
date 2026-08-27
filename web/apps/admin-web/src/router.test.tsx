import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
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

function stubFetch(handler: (url: string, init?: RequestInit) => Response) {
  const fn = vi.fn((input: string, init?: RequestInit) => Promise.resolve(handler(input, init)));
  vi.stubGlobal("fetch", fn);
  return fn;
}

const channelsBody = {
  items: [
    {
      metric_key: "sub2api.channels.balance",
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

function okHandler(url: string): Response {
  // history 必须排在 metrics 前面：两者的前缀是包含关系
  if (url.startsWith("/api/v1/metrics/history")) return fakeResponse(200, historyBody);
  if (url.startsWith("/api/v1/metrics")) return fakeResponse(200, metricsBody);
  if (url.startsWith("/api/v1/services")) return fakeResponse(200, servicesBody);
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

  it("已登录访问 /dashboard 显示 AdminShell 与四个导航入口", async () => {
    devLogin();
    renderRoute("/dashboard");
    expect(await screen.findByRole("heading", { name: "运营总览" })).not.toBeNull();
    for (const name of ["运营总览", "渠道余额", "服务清单", "审计事件"]) {
      expect(screen.getByRole("link", { name })).not.toBeNull();
    }
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
      // 读看板要 registry.read + ops.read；审计页要 audit.read；
      // 服务登记/观测上报要 registry.service.manage（XM-0026 加的后两个）
      "X-Dev-Scopes": "registry.read,ops.read,audit.read,registry.service.manage",
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

describe("渠道明细页", () => {
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
    renderRoute("/channels");
    expect(await screen.findByText("渠道甲")).not.toBeNull();
    expect(screen.getByText("¥100.00")).not.toBeNull();
    expect(screen.getByText("¥25.00")).not.toBeNull();
    expect(screen.getByText("有效")).not.toBeNull();
    expect(screen.getByText("失效")).not.toBeNull();
    // 页头的新鲜度徽章与数据时间（规格 §9.1）
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
    renderRoute("/channels");
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
    renderRoute("/channels");
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
    expect(screen.getByText("在服务清单页执行一次动作试试")).not.toBeNull();
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
    renderRoute("/services");
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
    renderRoute("/services");
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
