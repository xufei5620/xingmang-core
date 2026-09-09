import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { OVERVIEW_POLL_INTERVAL_MS } from "../lib/autoRefresh";
import { OverviewPage } from "./OverviewPage";

// 指标键抽成常量而不是就地写字面量：`metric_key: "……"` 这个形状会被 gitleaks 的
// generic-api-key 规则当成泄露的密钥（同一条误报见 api/platform.test.ts）。
// 与其让 secret-scan 常红到没人再看它，不如换个写法绕开这个形状
const REVENUE_METRIC = "sub2api.revenue.daily";

const metricsBody = {
  items: [
    {
      metric_key: REVENUE_METRIC,
      source: "sub2api-prod",
      environment: "development",
      watermark: "wm-1",
      value: { day: "2026-08-25", amount_minor_units: 123456, currency: "CNY", order_count: 42 },
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

const alertsBody = { items: [] };
const servicesBody = { items: [] };
const auditBody = { items: [], next_before: 0 };

function fakeResponse(body: unknown, status = 200): Response {
  return {
    ok: status < 400,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

function renderPage() {
  const queryClient = new QueryClient({
    // staleTime 0：这个用例要观察「定时器到点后又发了一次请求」，
    // 缓存窗口会把第二次请求吃掉，那就测不到刷新本身了
    defaultOptions: { queries: { retry: false, staleTime: 0 } },
  });
  // MemoryRouter 是必需的：工作台里到处都是 <Link>（告警、审计、平台矩阵），
  // 没有路由上下文 react-router 直接抛异常
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <OverviewPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function alertCalls(fetchMock: ReturnType<typeof vi.fn>): number {
  return fetchMock.mock.calls.filter((c) => String(c[0]).startsWith("/api/v1/alerts")).length;
}

describe("运营工作台的 60 秒自动刷新（Codex #4）", () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    // shouldAdvanceTime：让 @testing-library 的 waitFor 仍能在假时钟下推进，
    // 否则 findBy* 会永远等下去
    vi.useFakeTimers({ shouldAdvanceTime: true });
    fetchMock = vi.fn((input: string) => {
      if (input.startsWith("/api/v1/alerts")) return Promise.resolve(fakeResponse(alertsBody));
      if (input.startsWith("/api/v1/services")) return Promise.resolve(fakeResponse(servicesBody));
      if (input.startsWith("/api/v1/audit")) return Promise.resolve(fakeResponse(auditBody));
      return Promise.resolve(fakeResponse(metricsBody));
    });
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("计时器到点后重新拉取，而不是永远停在首屏", async () => {
    renderPage();
    await screen.findByText("我的待处理");
    const before = alertCalls(fetchMock);
    expect(before).toBeGreaterThan(0);

    await act(async () => {
      vi.advanceTimersByTime(OVERVIEW_POLL_INTERVAL_MS);
    });

    // 页头写着「每 60 秒自动刷新」，那这一屏就必须真的跟着走
    await waitFor(() => expect(alertCalls(fetchMock)).toBeGreaterThan(before));
  });

  it("页面不可见时不拉——刷新入口只有一个，规则也只有一份", async () => {
    renderPage();
    await screen.findByText("我的待处理");
    const before = alertCalls(fetchMock);

    const spy = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    await act(async () => {
      vi.advanceTimersByTime(OVERVIEW_POLL_INTERVAL_MS);
    });
    expect(alertCalls(fetchMock)).toBe(before);
    spy.mockRestore();
  });
});

// ---------------------------------------------------------------------------
// XM-WORKBENCH-APPROVALS：「待审批」这一格接上审批中心
// ---------------------------------------------------------------------------

/** 固定「现在」：这一格右侧那一列显示的是「还有多久到期」，不定住时钟就没法
 *  逐字断言，而只断言「有个数」等于什么都没测。 */
const APPROVALS_NOW = new Date("2026-09-07T10:00:00Z");

function approval(overrides: Record<string, unknown> = {}) {
  return {
    id: "ap-1",
    action_id: "registry.connection.set_status",
    action_version: "1",
    risk_level: "L3",
    params: {},
    params_hash: "sha256:abc",
    requester_id: "staff_bob",
    requester_type: "HUMAN",
    reason: "上游换域名，需要重新登记",
    status: "PENDING",
    created_at: "2026-09-07T08:00:00Z",
    expires_at: "2026-09-07T13:00:00Z",
    decisions: [],
    votes_required: 2,
    votes_cast: 0,
    privileged_vote_required: false,
    privileged_vote_cast: false,
    ...overrides,
  };
}

function activeAlert() {
  return {
    id: "al-1",
    rule_key: "metric.sync.failed",
    dedup_key: "d-1",
    severity: "critical",
    status: "OPEN",
    title: "指标同步失败",
    detail: "",
    environment: "development",
    source_metric_key: REVENUE_METRIC,
    opened_at: "2026-09-07T09:00:00Z",
    last_seen_at: "2026-09-07T09:30:00Z",
    acknowledged_at: null,
    resolved_at: null,
    fire_count: 3,
    notify_status: "pending",
    notify_error: "",
    notified_at: null,
  };
}

/** chi 对没有挂载的路由回**纯文本** 404，解析不出 `error.code`——api/client.ts 的
 *  looksLikeUnmountedRoute 正是靠这层结构差异判「整组端点不存在」。这里逐字模拟
 *  它，否则测的就不是真实的未启用形态（模拟成带 code 的 404，走的是普通错误
 *  分支，这条用例就变成在测别的东西了）。 */
function bareNotFound(): Response {
  return {
    ok: false,
    status: 404,
    json: () => Promise.reject(new Error("not json")),
  } as unknown as Response;
}

/** 除审批外都给一份能渲染的空数据；审批那条由各用例自己覆盖。 */
function baseHandler(url: string): Response {
  if (url.startsWith("/api/v1/alerts")) return fakeResponse({ items: [] });
  if (url.startsWith("/api/v1/services")) return fakeResponse(servicesBody);
  if (url.startsWith("/api/v1/audit")) return fakeResponse(auditBody);
  if (url.startsWith("/api/v1/jobs/runs")) return fakeResponse({ items: [], next_before: 0 });
  if (url.startsWith("/api/v1/approvals")) return fakeResponse({ items: [], limit: 20 });
  // 失败作业聚合默认答「没接」，于是「前端自己分组」那一路是被显式选中的，
  // 不是因为这个 URL 掉进了下面那个 metricsBody 兜底、字段恰好读成 undefined。
  // 一条恰好成立的前提与一条正确的前提长得一模一样，区别只在下次改动时。
  if (url.startsWith("/api/v1/ops/overview")) return fakeResponse(opsOverviewBody("not_wired"));
  return fakeResponse(metricsBody);
}

/** 只保留本组关心的那三个字段：这一屏不读总览的别的部分。 */
function opsOverviewBody(
  status: "ok" | "not_wired" | "query_failed",
  byKind: unknown[] | null = null,
) {
  return {
    failed_jobs_by_kind: byKind,
    failed_jobs_status: status,
    failed_jobs_window_hours: 24,
  };
}

function renderWorkbench(path: string) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <OverviewPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

/** 「我的待处理」那一张卡。**必须收窄到这张卡再断言**：顶部四格和运营焦点里
 *  各有几个「未接入」徽章，在整屏上找它既会撞见别人的，也测不出这一格到底
 *  显示了什么。 */
function workCard(): HTMLElement {
  return screen
    .getByRole("heading", { name: "我的待处理", level: 3 })
    .closest("section") as HTMLElement;
}

/** 「平台状态矩阵」那一张卡。理由同 workCard：「数据不完整」「未接入」在这一
 *  屏上至少各出现两三处（顶部四格、运营焦点、矩阵五行），全屏找要么撞见别人
 *  的，要么——更糟——让缺席型断言恒真。 */
function matrixCard(): HTMLElement {
  return screen
    .getByRole("heading", { name: "平台状态矩阵", level: 3 })
    .closest("section") as HTMLElement;
}

/** 一条已放弃的后台任务（`GET /api/v1/jobs/runs` 的一项）。 */
function discardedRun(overrides: Record<string, unknown> = {}) {
  return {
    id: 1,
    kind: "card_sync",
    queue: "default",
    state: "discarded",
    attempt: 3,
    max_attempts: 3,
    created_at: "2026-09-07T08:00:00Z",
    scheduled_at: "2026-09-07T08:00:00Z",
    attempted_at: "2026-09-07T09:00:00Z",
    finalized_at: "2026-09-07T09:59:00Z",
    duration_ms: 120,
    error_count: 3,
    last_error: null,
    args: {},
    ...overrides,
  };
}

// XM-WORKBENCH-TRUTH：生产上 288 条 card_sync 把这一格全占满，真正需要人处理
// 的东西被挤出首屏。合并之后最要紧的不是「少了几行」，而是那个计数会不会被读
// 成「一共就这么多」——取数上限是 20，真实是 288。
describe("我的待处理·失败任务合并（XM-WORKBENCH-TRUTH）", () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(APPROVALS_NOW);
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  const twenty = Array.from({ length: 20 }, (_, i) => discardedRun({ id: 100 + i }));

  function stubJobs(body: unknown) {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: string) =>
        Promise.resolve(
          input.startsWith("/api/v1/jobs/runs") ? fakeResponse(body) : baseHandler(input),
        ),
      ),
    );
  }

  it("同一类型的 20 条合并成一行，不再占满整格", async () => {
    stubJobs({ items: twenty, next_before: 12345 });
    renderWorkbench("/?work=jobs");
    const card = workCard();
    await within(card).findByText(/卡片数据同步 已放弃/);
    expect(within(card).getAllByRole("listitem")).toHaveLength(1);
  });

  // 取数上限 20 而真实 288：只显示「×20」会把界面从「288 行刷屏」退化成
  // 「一个看起来权威的错数字」。粒度归 ×N 的「+」，整句归截断提示，两处分工
  // 不重复，但缺一不可。
  it("取数被截断时既带「+」也说清只取了 20 条", async () => {
    stubJobs({ items: twenty, next_before: 12345 });
    renderWorkbench("/?work=jobs");
    const card = workCard();
    expect(await within(card).findByText(/卡片数据同步 已放弃 ×20\+/)).not.toBeNull();
    expect(within(card).getByText(/已放弃的后台任务只取了 20 条/)).not.toBeNull();
  });

  it("没有被截断时不带「+」——不给一个假的「还有更多」", async () => {
    stubJobs({ items: twenty, next_before: 0 });
    renderWorkbench("/?work=jobs");
    const card = workCard();
    expect(await within(card).findByText(/卡片数据同步 已放弃 ×20$/)).not.toBeNull();
  });

  // 折叠时明细必须**不在 DOM 里**。用 <details> 或 CSS 隐藏的话，jsdom 里
  // 照样查得到，这条断言会恒真——本仓在 Portal / 懒渲染上踩过同一个坑。
  it("明细默认收起，展开后才出现在文档里", async () => {
    stubJobs({ items: twenty, next_before: 0 });
    renderWorkbench("/?work=jobs");
    const card = workCard();
    const toggle = await within(card).findByRole("button", { name: /卡片数据同步 已放弃/ });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(within(card).queryByText(/卡片数据同步 重试 3 次后放弃/)).toBeNull();

    await act(async () => {
      toggle.click();
    });
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(within(card).getAllByText(/卡片数据同步 重试 3 次后放弃/)).toHaveLength(20);
  });

  it("只有一条时不合并、也没有展开按钮，逐字保持原样", async () => {
    stubJobs({ items: [discardedRun({ id: 7 })], next_before: 0 });
    renderWorkbench("/?work=jobs");
    const card = workCard();
    expect(await within(card).findByText("卡片数据同步 重试 3 次后放弃")).not.toBeNull();
    expect(within(card).queryByRole("button", { name: /已放弃 ×/ })).toBeNull();
  });
});

// XM-WORKBENCH-WIRE-OPS：上面那一组走的是「前端自己数」。纯函数那一层的三态
// 已经在 lib/workbench.test.ts 里逐条钉过了，这一组只回答另一个问题——
// **那条路真的被这一屏走到了吗**。判据写对了但没人调用，单测一样全绿。
describe("我的待处理·失败任务合计取自后端（XM-WORKBENCH-WIRE-OPS）", () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(APPROVALS_NOW);
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  const twenty = Array.from({ length: 20 }, (_, i) => discardedRun({ id: 100 + i }));
  const cardSyncAggregate = {
    kind: "card_sync",
    count: 288,
    first_at: "2026-09-06T10:00:00Z",
    last_at: "2026-09-07T09:59:00Z",
    last_run_id: 90210,
    error_count: 3,
    last_error: null,
  };

  function stub(jobs: unknown, overview: unknown) {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: string) => {
        if (input.startsWith("/api/v1/jobs/runs")) return Promise.resolve(fakeResponse(jobs));
        if (input.startsWith("/api/v1/ops/overview")) {
          return Promise.resolve(fakeResponse(overview));
        }
        return Promise.resolve(baseHandler(input));
      }),
    );
  }

  it("这一屏真的去问了那个端点", async () => {
    // 少了这一条，下面每一条都可能只是因为别的原因恰好成立。
    stub({ items: twenty, next_before: 12345 }, opsOverviewBody("ok", [cardSyncAggregate]));
    renderWorkbench("/?work=jobs");
    await within(workCard()).findByText(/卡片数据同步 已放弃/);
    const fetchMock = globalThis.fetch as unknown as ReturnType<typeof vi.fn>;
    expect(
      fetchMock.mock.calls.filter((c) => String(c[0]).startsWith("/api/v1/ops/overview")).length,
    ).toBeGreaterThan(0);
  });

  it("界面上那个数是后端的 288，不是取到的 20", async () => {
    stub({ items: twenty, next_before: 12345 }, opsOverviewBody("ok", [cardSyncAggregate]));
    renderWorkbench("/?work=jobs");
    const card = workCard();
    expect(await within(card).findByText(/卡片数据同步 已放弃 ×288/)).not.toBeNull();
    // 后端给的是确数，「+」那个尾巴不该再挂着——它说的是「还不止这些」
    expect(within(card).queryByText(/×20\+/)).toBeNull();
    expect(within(card).queryByText(/已放弃 ×20$/)).toBeNull();
  });

  it("查库失败时退回前端分组，并在这一屏上说出来", async () => {
    stub({ items: twenty, next_before: 12345 }, opsOverviewBody("query_failed", null));
    renderWorkbench("/?work=jobs");
    const card = workCard();
    expect(await within(card).findByText(/卡片数据同步 已放弃 ×20\+/)).not.toBeNull();
    expect(within(card).getByText(/后端查库失败/)).not.toBeNull();
  });

  it("未接入时同样退回前端分组，但**不**说那句话——那是良性的部署事实", async () => {
    stub({ items: twenty, next_before: 12345 }, opsOverviewBody("not_wired", null));
    renderWorkbench("/?work=jobs");
    const card = workCard();
    expect(await within(card).findByText(/卡片数据同步 已放弃 ×20\+/)).not.toBeNull();
    expect(within(card).queryByText(/后端查库失败/)).toBeNull();
  });

  it("总览端点自己 500 时不影响这一格：退回前端分组，也不说后端坏了", async () => {
    // 我们没有依据说后端哪里不对——可能只是这一次网络抖了。
    vi.stubGlobal(
      "fetch",
      vi.fn((input: string) => {
        if (input.startsWith("/api/v1/jobs/runs")) {
          return Promise.resolve(fakeResponse({ items: twenty, next_before: 12345 }));
        }
        if (input.startsWith("/api/v1/ops/overview")) {
          return Promise.resolve(
            fakeResponse({ error: { code: "INTERNAL", message: "boom" } }, 500),
          );
        }
        return Promise.resolve(baseHandler(input));
      }),
    );
    renderWorkbench("/?work=jobs");
    const card = workCard();
    expect(await within(card).findByText(/卡片数据同步 已放弃 ×20\+/)).not.toBeNull();
    expect(within(card).queryByText(/后端查库失败/)).toBeNull();
  });
});

// 这一格的黄灯亮了两周，界面上一个字的说明都没有。纯函数测试证明算得对，
// DOM 测试证明它真的被渲染出来——两者缺一，另一半就会假绿。
describe("平台状态矩阵：把「为什么是这个状态」写进格子里", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  const partialMetric = {
    metric_key: "newapi.subscription.daily",
    source: "newapi-prod",
    environment: "development",
    watermark: "day:2026-09-06;subscription:unavailable_over_http",
    value: {},
    freshness: {
      state: "partial",
      staleness_seconds: 30,
      threshold_seconds: 1800,
      is_partial: true,
      observed_at: "2026-09-07T09:59:00Z",
      last_success: "2026-09-07T09:59:00Z",
      last_error_code: "",
    },
  };

  it("数据不完整的那一行说清是哪条指标、为什么，并挂上水位线原文", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: string) =>
        Promise.resolve(
          input.startsWith("/api/v1/metrics")
            ? fakeResponse({ items: [partialMetric] })
            : baseHandler(input),
        ),
      ),
    );
    renderWorkbench("/");
    const card = matrixCard();
    const note = await within(card).findByText(/NewAPI 日订阅这一轮采集成功了/);
    expect(note.textContent).toContain("这不是故障，也不会自己好转");
    expect(note.getAttribute("title")).toContain("unavailable_over_http");
    // 解释挂在对的那一行上，不是整表一句
    const newapiRow = within(card).getByRole("link", { name: "NewAPI" }).closest("tr");
    expect(newapiRow?.contains(note)).toBe(true);
  });

  it("开票那一行在格子里说清「只读对接」与「点进去的嵌入管理端」不是一回事", async () => {
    vi.stubGlobal("fetch", vi.fn((input: string) => Promise.resolve(baseHandler(input))));
    renderWorkbench("/");
    const card = matrixCard();
    const scope = await within(card).findByText(/这一行说的是只读数据对接/);
    expect(scope.textContent).toContain("两者不是一回事");
    const invoiceRow = within(card).getByRole("link", { name: "开票系统" }).closest("tr");
    expect(invoiceRow?.contains(scope)).toBe(true);
    // 这一行今天算出来仍然是「未接入」——诚实不等于换结论
    expect(invoiceRow?.textContent).toContain("未接入");
  });
});

describe("我的待处理·待审批（XM-WORKBENCH-APPROVALS）", () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(APPROVALS_NOW);
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  function stub(handler: (url: string) => Response) {
    vi.stubGlobal("fetch", vi.fn((input: string) => Promise.resolve(handler(input))));
  }

  it("列出仍等着投票的单，并直达「操作与审批」页的待审批子页签", async () => {
    stub((url) =>
      url.startsWith("/api/v1/approvals")
        ? fakeResponse({ items: [approval({ votes_cast: 1 })], limit: 20 })
        : baseHandler(url),
    );
    renderWorkbench("/?work=approvals");
    const row = await screen.findByText("registry.connection.set_status@1");
    // 这一格原来显示的是「『待审批』还没有数据源」，一行数据也没有
    expect(screen.queryByText("「待审批」还没有数据源")).toBeNull();
    expect(screen.getByText("staff_bob 提交 · 还差 1 票（已 1/2）")).not.toBeNull();
    // 右侧那一列问的是「什么时候要处理完」：固定时钟 10:00，夹具到期 13:00
    expect(screen.getByText("3 小时后到期")).not.toBeNull();
    expect((row.closest("a") as HTMLAnchorElement).getAttribute("href")).toBe(
      "/actions?sub=pending",
    );
  });

  // 端点整组没挂载 = 这套前端在对一个启用之前的旧 platform-api 说话，不是
  // 「这一次请求失败了」。走 api/approvals 的 FeatureNotMountedError 那条路，
  // 与「操作与审批」页同一套说法，工作台不另立一份。
  it("审批中心整组 404 时说「未接入」并给出原因，而不是「加载失败」", async () => {
    stub((url) => (url.startsWith("/api/v1/approvals") ? bareNotFound() : baseHandler(url)));
    renderWorkbench("/?work=approvals");
    const card = workCard();
    expect(await within(card).findByText("未接入")).not.toBeNull();
    // 逐字钉住指向后端版本的那两句：只断言「有说明」不够，说错方向的说明会把
    // 人支去等一个不会再来的排期
    expect(
      within(card).getByText(/这套后台连上的 platform-api 还没有审批中心/),
    ).not.toBeNull();
    expect(
      within(card).getByText(/请确认 platform-api 已滚到含该变更的版本，而不是等待排期/),
    ).not.toBeNull();
    // 不是「加载失败」，也不给重试按钮——再点一次不会让旧后端长出这组路由
    expect(within(card).queryByText("加载失败")).toBeNull();
    expect(within(card).queryByRole("button", { name: "重试" })).toBeNull();
  });

  // 这一条是上一条的对照：同一格换成真的请求失败时必须说「加载失败」并给重试
  // 按钮。少了它，「未接入」那条断言就可能是恒真的——两条路径根本没分开。
  it("审批端点真的报错时照常显示「加载失败」，与「未接入」分得开", async () => {
    stub((url) =>
      url.startsWith("/api/v1/approvals")
        ? fakeResponse({ error: { code: "UNAVAILABLE", message: "上游不可用" } }, 503)
        : baseHandler(url),
    );
    renderWorkbench("/?work=approvals");
    const card = workCard();
    expect(await within(card).findByText("加载失败")).not.toBeNull();
    expect(within(card).getByText(/上游不可用（错误码 UNAVAILABLE）/)).not.toBeNull();
    expect(within(card).queryByText("未接入")).toBeNull();
    expect(within(card).getByRole("button", { name: "重试" })).not.toBeNull();
  });

  it("没有待投票的单时说清这一格空的是什么，不含糊成「暂无数据」", async () => {
    stub(baseHandler);
    renderWorkbench("/?work=approvals");
    expect(await screen.findByText("没有待处理事项")).not.toBeNull();
    expect(screen.getByText(/L2 及以上的调用才会在这里排队/)).not.toBeNull();
    expect(screen.getByText(/已过期的单也不算/)).not.toBeNull();
  });

  // 「全部」的空态整句相等而不是 `not.toContain("审批")`：在这个位置断言"某个词
  // 不在"太容易恒真（换个说法它照样"不在"）。整句既钉住了审批已经从「还没接」
  // 的名单里划掉，也钉住了剩下三类仍然被逐个点名——零不等于「都处理完了」。
  it("「全部」的空态不再把审批算作没接，但仍逐个点名其余三类", async () => {
    stub(baseHandler);
    renderWorkbench("/");
    expect(await screen.findByText("没有待处理事项")).not.toBeNull();
    expect(screen.getByText(/并不代表全部待办/).textContent).toBe(
      "当前没有未处理的告警、没有等着投票的审批单，也没有失败的后台任务。" +
        "注意：财务异常、到期项与待评审变更还没有接入，这一屏并不代表全部待办。",
    );
  });

  it("取满上限时说这一屏可能不是全部", async () => {
    stub((url) =>
      url.startsWith("/api/v1/approvals")
        ? fakeResponse({ items: [approval()], limit: 20, truncated: true })
        : baseHandler(url),
    );
    renderWorkbench("/?work=approvals");
    expect(await screen.findByText(/待审批的单只取了 20 条/)).not.toBeNull();
  });

  it("「全部」里审批单跟告警排在一起，顺序是告警在前", async () => {
    stub((url) => {
      if (url.startsWith("/api/v1/alerts")) return fakeResponse({ items: [activeAlert()] });
      if (url.startsWith("/api/v1/approvals")) {
        return fakeResponse({ items: [approval()], limit: 20 });
      }
      return baseHandler(url);
    });
    renderWorkbench("/");
    await screen.findByText("registry.connection.set_status@1");
    const titles = within(workCard())
      .getAllByRole("listitem")
      .map((li) => li.textContent ?? "");
    expect(titles).toHaveLength(2);
    // 严重告警比一张还没到期的审批单更急，顺序本身就是一种排序建议
    expect(titles[0]).toContain("指标同步失败");
    expect(titles[1]).toContain("registry.connection.set_status@1");
  });

  // 刻意的取舍，与「失败任务」同一条：「全部」以告警为主，审批那条挂了只是
  // 少几行，不该把整块换成错误态——那正是最需要看见告警的时候。代价是未挂载
  // 这件事在「全部」里看不见，要点进「待审批」才说，见交接文档 risks。
  it("审批端点挂了不会把「全部」整块变红：告警照常显示", async () => {
    stub((url) => {
      if (url.startsWith("/api/v1/alerts")) return fakeResponse({ items: [activeAlert()] });
      if (url.startsWith("/api/v1/approvals")) return bareNotFound();
      return baseHandler(url);
    });
    renderWorkbench("/");
    expect(await screen.findByText("指标同步失败")).not.toBeNull();
    const card = workCard();
    expect(within(card).queryByText("加载失败")).toBeNull();
    expect(within(card).queryByText("未接入")).toBeNull();
  });
});

// 生产事实（2026-09-09）：alerts.alert 里那条 upstream.version.changed 是
// ACKNOWLEDGED（acknowledged_at 15:45Z），fire_count 每分钟 +1，last_seen_at
// 每轮更新；工作台把它与未处理的一样列出、标「警告」、只写「已持续 13 小时 ·
// 触发 821 次」，没有任何绝对时间，也看不出已确认。负责人原话：「这个没有带
// 时间，我感觉好像我确认之后还在告警」。
describe("我的待处理·已确认的告警（XM-WORKBENCH-TRUTH 第四轮）", () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(APPROVALS_NOW);
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  function ackedAlert() {
    return {
      ...activeAlert(),
      id: "al-acked",
      rule_key: "upstream.version.changed",
      severity: "warning",
      status: "ACKNOWLEDGED",
      title: "上游版本变化",
      opened_at: "2026-09-06T21:00:00Z",
      acknowledged_at: "2026-09-06T23:45:00Z",
      last_seen_at: "2026-09-07T09:59:00Z",
      fire_count: 821,
    };
  }

  function stubAlerts(items: unknown[]) {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: string) =>
        Promise.resolve(
          input.startsWith("/api/v1/alerts") ? fakeResponse({ items }) : baseHandler(input),
        ),
      ),
    );
  }

  it("已确认的不与未处理混排：收进底部折叠组，组头写条数，展开才见明细", async () => {
    stubAlerts([activeAlert(), ackedAlert()]);
    renderWorkbench("/");
    const card = workCard();
    await within(card).findByText("指标同步失败");

    // 折叠时：主列表里只有那条未处理的，已确认那条的标题与「警告」徽章都不在 DOM 里
    expect(within(card).getAllByRole("listitem")).toHaveLength(1);
    expect(within(card).queryByText("上游版本变化")).toBeNull();
    expect(within(card).queryByText("警告")).toBeNull();
    const toggle = within(card).getByRole("button", { name: /已确认，等待自愈（1 条）/ });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");

    await act(async () => {
      toggle.click();
    });
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    const row = (await within(card).findByText("上游版本变化")).closest("li") as HTMLElement;
    // 徽章是「已确认」，不是「警告」——这一组不催
    expect(within(row).getByText("已确认")).not.toBeNull();
    expect(within(row).queryByText("警告")).toBeNull();
    // 三个绝对时刻都在，带 UTC 后缀；「最近评估」晚于「已确认」就是仍在成立的证据
    expect(row.textContent).toContain("已确认 2026-09-06 23:45:00 UTC");
    expect(row.textContent).toContain("仍在成立：最近评估 2026-09-07 09:59:00 UTC");
    expect(row.textContent).toContain("打开 2026-09-06 21:00:00 UTC");
  });

  it("未处理的行也带打开与最近评估的绝对时刻", async () => {
    stubAlerts([activeAlert()]);
    renderWorkbench("/");
    const row = (await within(workCard()).findByText("指标同步失败")).closest("li") as HTMLElement;
    expect(row.textContent).toContain("打开 2026-09-07 09:00:00 UTC · 最近评估 2026-09-07 09:30:00 UTC");
  });

  it("「故障」筛选下折叠组也在；别的筛选下不冒出来", async () => {
    stubAlerts([ackedAlert()]);
    renderWorkbench("/?work=incidents");
    expect(
      await within(workCard()).findByRole("button", { name: /已确认，等待自愈（1 条）/ }),
    ).not.toBeNull();
  });

  it("「失败任务」筛选下没有这一组", async () => {
    stubAlerts([ackedAlert()]);
    renderWorkbench("/?work=jobs");
    const card = workCard();
    await within(card).findByText("没有待处理事项");
    expect(within(card).queryByRole("button", { name: /已确认，等待自愈/ })).toBeNull();
  });

  it("「紧急」格不数已确认的严重告警，并把口径写在副行", async () => {
    stubAlerts([{ ...ackedAlert(), severity: "critical" }]);
    renderWorkbench("/");
    const urgent = (await screen.findByRole("heading", { name: "紧急", level: 3 })).closest(
      "article",
    ) as HTMLElement;
    expect(within(urgent).getByText("0")).not.toBeNull();
    expect(within(urgent).getByText("当前没有未处理的严重告警（已确认的不算）")).not.toBeNull();
  });

  it("有未处理的严重告警时「紧急」照常数，对照组不能少", async () => {
    stubAlerts([activeAlert(), { ...ackedAlert(), severity: "critical" }]);
    renderWorkbench("/");
    const urgent = (await screen.findByRole("heading", { name: "紧急", level: 3 })).closest(
      "article",
    ) as HTMLElement;
    expect(within(urgent).getByText("1")).not.toBeNull();
  });
});
