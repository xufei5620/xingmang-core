import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { JobsPage } from "./JobsPage";

function jsonResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

const emptyOverviewBody = {
  environment: "development",
  generated_at: "2026-08-31T10:00:00Z",
  schedules: [
    {
      id: "platform_heartbeat",
      kind: "platform_heartbeat",
      queue: "maintenance",
      schedule_config_env: "HEARTBEAT_INTERVAL",
      side_effect_class: "audit_append",
      last_run: null,
      observed_interval_seconds: null,
      next_run_estimated_at: null,
      activity: "never_observed",
      configured_enabled: null,
      configured_interval_seconds: null,
      configured_source: null,
      configured_mode: null,
      configured_mode_source: null,
    },
  ],
  queue_backlog: [
    { queue: "default", available: 0, running: 0, retryable: 0, scheduled: 0, completed_24h: 0, discarded: 0 },
    { queue: "maintenance", available: 0, running: 0, retryable: 0, scheduled: 0, completed_24h: 0, discarded: 0 },
  ],
  worker_heartbeat: { last_seen_at: null, seconds_ago: null, state: "", environment: "platform", known: false },
};

const populatedOverviewBody = {
  environment: "development",
  generated_at: "2026-08-31T10:00:00Z",
  schedules: [
    {
      id: "platform_heartbeat",
      kind: "platform_heartbeat",
      queue: "maintenance",
      schedule_config_env: "HEARTBEAT_INTERVAL",
      side_effect_class: "audit_append",
      last_run: {
        id: 42,
        kind: "platform_heartbeat",
        queue: "maintenance",
        state: "completed",
        attempt: 1,
        max_attempts: 3,
        created_at: "2026-08-31T09:59:30Z",
        scheduled_at: "2026-08-31T09:59:30Z",
        attempted_at: "2026-08-31T09:59:30Z",
        finalized_at: "2026-08-31T09:59:30Z",
        duration_ms: 200,
        error_count: 0,
        last_error: null,
        args: {},
      },
      observed_interval_seconds: 60,
      next_run_estimated_at: "2026-08-31T10:00:30Z",
      activity: "activity_observed",
      configured_enabled: true,
      configured_interval_seconds: 60,
      configured_source: "always",
      configured_mode: null,
      configured_mode_source: null,
    },
    {
      id: "finance_cost_sync",
      kind: "finance_cost_sync",
      queue: "maintenance",
      schedule_config_env: "XM_FINANCE_COLLECT_INTERVAL",
      side_effect_class: "upstream_read_then_finance_transaction",
      last_run: null,
      observed_interval_seconds: null,
      next_run_estimated_at: null,
      activity: "never_observed",
      configured_enabled: false,
      configured_interval_seconds: 300,
      configured_source: "XM_FINANCE_COLLECT_ENABLED",
      configured_mode: null,
      configured_mode_source: null,
    },
    {
      id: "sub2api_sync",
      kind: "sub2api_sync",
      queue: "maintenance",
      schedule_config_env: "XM_SUB2API_SYNC_INTERVAL",
      side_effect_class: "upstream_read_then_db_transaction",
      last_run: null,
      observed_interval_seconds: null,
      next_run_estimated_at: null,
      // 故意与另外两个 fixture 条目的 activity 取值不同（never_observed /
      // activity_observed 都已被那两条各自独占断言），避免 getByText 在
      // "定时任务页签"既有测试里因为多一行同值而从唯一匹配变成多重匹配。
      activity: "no_recent_activity",
      configured_enabled: true,
      configured_interval_seconds: 300,
      configured_source: "XM_SUB2API_SYNC_ENABLED",
      configured_mode: "real",
      configured_mode_source: "database",
    },
  ],
  queue_backlog: [
    { queue: "default", available: 0, running: 0, retryable: 0, scheduled: 0, completed_24h: 0, discarded: 0 },
    { queue: "maintenance", available: 1, running: 2, retryable: 3, scheduled: 0, completed_24h: 10, discarded: 4 },
  ],
  worker_heartbeat: {
    last_seen_at: "2026-08-31T09:59:30Z",
    seconds_ago: 30,
    state: "completed",
    environment: "platform",
    known: true,
  },
};

const emptyRunsBody = { items: [], next_before: 0 };

function decodedUrl(call: unknown[]): string {
  return decodeURIComponent(String(call[0]));
}

function renderJobs(
  initialEntry = "/jobs",
  responses: { overview?: unknown; runs?: unknown } = {},
) {
  const overview = responses.overview ?? emptyOverviewBody;
  const runs = responses.runs ?? emptyRunsBody;
  const fetchImpl = vi.fn(async (url: string) => {
    if (url.includes("/api/v1/jobs/overview")) return jsonResponse(overview);
    if (url.includes("/api/v1/jobs/runs")) return jsonResponse(runs);
    throw new Error(`意外的请求: ${url}`);
  });
  vi.stubGlobal("fetch", fetchImpl);
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <JobsPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return fetchImpl;
}

afterEach(() => vi.unstubAllGlobals());

describe("后台任务页导航", () => {
  it("展示五个导航同源页签，默认进入运行中", async () => {
    renderJobs();

    for (const label of ["运行中", "定时任务", "同步批次", "失败与重试", "多次失败任务"]) {
      expect(screen.getByRole("tab", { name: label })).not.toBeNull();
    }
    expect(screen.getByRole("tab", { name: "运行中", selected: true })).not.toBeNull();
    await screen.findByText("暂无最近运行记录");
  });

  it("按 ?sub 深链选择对应页签，未知值保持 Not Found 语义", async () => {
    const fetchImpl = renderJobs("/jobs?sub=failures");
    expect(screen.getByRole("tab", { name: "失败与重试", selected: true })).not.toBeNull();
    await screen.findByText("当前没有等待重试的任务");
    await waitFor(() => expect(fetchImpl).toHaveBeenCalled());

    renderJobs("/jobs?sub=not-a-job-tab");
    expect(screen.getByRole("heading", { name: "页面不存在", level: 2 })).not.toBeNull();
    expect(screen.getByText(/没有名为 not-a-job-tab 的子页签/)).not.toBeNull();
  });

  it("不伪装任何写操作入口——本页只读", async () => {
    renderJobs();
    await screen.findByText("暂无最近运行记录");
    for (const label of ["重试", "重新入队", "暂停任务", "取消任务", "执行任务", "重放死信"]) {
      expect(screen.queryByRole("button", { name: label })).toBeNull();
    }
  });
});

describe("顶部四格", () => {
  it("空快照时诚实显示零与「从未观测到」，不冒充成一个读数", async () => {
    renderJobs("/jobs", { overview: emptyOverviewBody });
    await screen.findByText("运行中任务");
    expect(screen.getByText("Worker 心跳").closest("article")).not.toBeNull();
    const heartbeatTile = screen.getByText("Worker 心跳").closest("article");
    expect(heartbeatTile).not.toBeNull();
    expect(within(heartbeatTile as HTMLElement).getByText("—")).not.toBeNull();
    expect(within(heartbeatTile as HTMLElement).getByText(/还没有出现过心跳记录/)).not.toBeNull();
  });

  it("根据队列积压求和展示运行中/失败待重试/多次失败任务", async () => {
    renderJobs("/jobs", { overview: populatedOverviewBody });
    const runningTile = (await screen.findByText("运行中任务")).closest("article") as HTMLElement;
    expect(within(runningTile).getByText("2")).not.toBeNull(); // 两个队列 running 之和：0+2

    const retryTile = screen.getByText("失败待重试").closest("article") as HTMLElement;
    expect(within(retryTile).getByText("3")).not.toBeNull();

    // 「多次失败任务」同时是格子标题与页签名——用 heading 角色限定在格子标题上，
    // 不然 getByText 会连页签按钮一起撞上。
    const deadTile = screen.getByRole("heading", { name: "多次失败任务" }).closest("article") as HTMLElement;
    expect(within(deadTile).getByText("4")).not.toBeNull();
    expect(within(deadTile).getByText(/需要人工检查/)).not.toBeNull();

    const heartbeatTile = screen.getByText("Worker 心跳").closest("article") as HTMLElement;
    expect(within(heartbeatTile).getByText(/30 秒前/)).not.toBeNull();
  });

  it("展示队列积压小表，default 与 maintenance 总是出现", async () => {
    renderJobs("/jobs", { overview: emptyOverviewBody });
    await screen.findByText("队列积压");
    // 队列名用 <th scope="row">（rowheader），不是 <td>（cell）
    expect(screen.getByRole("rowheader", { name: "default" })).not.toBeNull();
    expect(screen.getByRole("rowheader", { name: "maintenance" })).not.toBeNull();
  });
});

describe("定时任务页签", () => {
  it("展示周期任务目录，不出现「已启用」这类装出来的断言", async () => {
    renderJobs("/jobs?sub=scheduled", { overview: populatedOverviewBody });

    expect(await screen.findByText("平台心跳")).not.toBeNull();
    expect(screen.getByText("近期活跃")).not.toBeNull();
    expect(screen.getByText("从未观测到")).not.toBeNull();
    // 精确匹配整段文本，避免撞上说明文案里"因此不显示「已启用/已停用」"那句解释——
    // 那句话正是在说明为什么没有这个断言，本身不构成一个装出来的「已启用」徽章。
    expect(screen.queryByText("已启用")).toBeNull();
    expect(screen.queryByText("已停用")).toBeNull();
  });

  it("「部署状态」列区分部署声明与观测活跃度，不同两件事混着说", async () => {
    renderJobs("/jobs?sub=scheduled", { overview: populatedOverviewBody });
    await screen.findByText("平台心跳");

    // 心跳与 sub2api_sync 在本 fixture 里都是 configured_enabled=true，
    // 两行都渲染"已配置启用"——用 getAllByText 而不是要求唯一匹配。
    expect(screen.getAllByText("已配置启用").length).toBe(2);
    expect(screen.getByText("always")).not.toBeNull();
    // finance_cost_sync 在本 fixture 里配置为停用。
    expect(screen.getByText("已配置停用")).not.toBeNull();
    expect(screen.getByText("XM_FINANCE_COLLECT_ENABLED")).not.toBeNull();
    // sub2api_sync 的 configured_mode=real，应显示"真实接入"徽章而不是
    // fake 的"演示数据"。
    expect(screen.getByText("真实接入")).not.toBeNull();
    expect(screen.queryByText("演示数据")).toBeNull();
  });

  it("configured_enabled 为 null 时「部署状态」列显示未接数据源，而不是猜一个停用", async () => {
    renderJobs("/jobs?sub=scheduled", { overview: emptyOverviewBody });
    await screen.findByText("平台心跳");

    expect(screen.queryByText("已配置启用")).toBeNull();
    expect(screen.queryByText("已配置停用")).toBeNull();
    // ConfiguredCell 在没有数据源时渲染一个带 title 说明的 "—"。
    const dash = screen.getByTitle("本次装配没有接部署配置读数");
    expect(dash.textContent).toBe("—");
  });

  it("configured_mode_source 为 unavailable 时明确提示读取失败，而不是悄悄不显示", async () => {
    const overview = {
      ...populatedOverviewBody,
      schedules: [
        {
          ...populatedOverviewBody.schedules[0],
          configured_mode: null,
          configured_mode_source: "unavailable",
        },
      ],
    };
    renderJobs("/jobs?sub=scheduled", { overview });
    await screen.findByText("平台心跳");

    expect(screen.getByText("connector_config 读取失败")).not.toBeNull();
  });
});

describe("同步批次 / 失败与重试 / 多次失败任务页签的服务端过滤", () => {
  it("同步批次固定请求三个数据连接器同步任务的 kind", async () => {
    const fetchImpl = renderJobs("/jobs?sub=batches");
    await waitFor(() => {
      const call = fetchImpl.mock.calls.find((c) => decodedUrl(c).includes("/api/v1/jobs/runs"));
      expect(call).toBeDefined();
      expect(decodedUrl(call as unknown[])).toContain("kind=sub2api_sync,newapi_sync,finance_cost_sync");
    });
  });

  it("失败与重试固定请求 state=retryable", async () => {
    const fetchImpl = renderJobs("/jobs?sub=failures");
    await waitFor(() => {
      const call = fetchImpl.mock.calls.find((c) => decodedUrl(c).includes("/api/v1/jobs/runs"));
      expect(call).toBeDefined();
      expect(decodedUrl(call as unknown[])).toContain("state=retryable");
    });
  });

  it("多次失败任务固定请求 state=discarded", async () => {
    const fetchImpl = renderJobs("/jobs?sub=repeated");
    await waitFor(() => {
      const call = fetchImpl.mock.calls.find((c) => decodedUrl(c).includes("/api/v1/jobs/runs"));
      expect(call).toBeDefined();
      expect(decodedUrl(call as unknown[])).toContain("state=discarded");
    });
  });

  it("运行中不带 kind/state 过滤（展示全部最近记录）", async () => {
    const fetchImpl = renderJobs("/jobs?sub=running");
    await waitFor(() => {
      const call = fetchImpl.mock.calls.find((c) => decodedUrl(c).includes("/api/v1/jobs/runs"));
      expect(call).toBeDefined();
      const url = decodedUrl(call as unknown[]);
      expect(url).not.toContain("kind=");
      expect(url).not.toContain("state=");
    });
  });
});

describe("运行记录表格", () => {
  const runsBody = {
    items: [
      {
        id: 7,
        kind: "sub2api_sync",
        queue: "maintenance",
        state: "retryable",
        attempt: 2,
        max_attempts: 5,
        created_at: "2026-08-31T09:00:00Z",
        scheduled_at: "2026-08-31T09:00:00Z",
        attempted_at: "2026-08-31T09:00:01Z",
        finalized_at: null,
        duration_ms: null,
        error_count: 1,
        last_error: { at: "2026-08-31T09:00:01Z", message: "上游超时", truncated: false, original_length: 6 },
        args: { run_id: "test-1" },
      },
    ],
    next_before: 0,
  };

  it("展示任务类型中文名、状态与错误信息", async () => {
    renderJobs("/jobs?sub=batches", { runs: runsBody });
    const kindCell = await screen.findByText("Sub2API 同步");
    // 「等待重试」同时是状态徽章文案、表头与筛选下拉的选项文案——限定在这一行
    // 的表格单元格内，避免撞上表头/下拉框里同名的文本。
    const row = kindCell.closest("tr") as HTMLElement;
    expect(within(row).getByText("等待重试")).not.toBeNull();
    expect(within(row).getByText("上游超时")).not.toBeNull();
    expect(within(row).getByText(/尝试 2 \/ 5/)).not.toBeNull();
  });

  it("next_before 非零时展示「加载更多」并携带游标发起下一页请求", async () => {
    const withMore = { ...runsBody, next_before: 6 };
    const fetchImpl = renderJobs("/jobs?sub=batches", { runs: withMore });
    const loadMore = await screen.findByRole("button", { name: "加载更多" });

    loadMore.click();

    await waitFor(() => {
      const call = fetchImpl.mock.calls.find((c) => decodedUrl(c).includes("before=6"));
      expect(call).toBeDefined();
    });
  });

  it("next_before 为 0 时展示「已到最早一条」而不是加载更多按钮", async () => {
    renderJobs("/jobs?sub=batches", { runs: runsBody });
    await screen.findByText("已到最早一条");
    expect(screen.queryByRole("button", { name: "加载更多" })).toBeNull();
  });
});
