import { describe, expect, it, vi } from "vitest";
import {
  SYNC_JOB_KINDS,
  getJobsOverview,
  jobKindLabel,
  jobStateLabel,
  listJobRuns,
  type JobsOverview,
} from "./jobs";
import type { ApiClient } from "./client";
import type { PlatformApiConfig } from "./config";

const config: PlatformApiConfig = {
  baseUrl: "",
  principalId: "dev-operator",
  principalType: "HUMAN",
  scopes: ["ops.read"],
};

function fakeClient(body: unknown): ApiClient {
  return {
    get: vi.fn().mockResolvedValue(body),
    post: vi.fn().mockResolvedValue(body),
  } as unknown as ApiClient;
}

const emptyOverview: JobsOverview = {
  environment: "development",
  generated_at: "2026-08-31T00:00:00Z",
  schedules: [],
  queue_backlog: [],
  worker_heartbeat: { last_seen_at: null, seconds_ago: null, state: "", environment: "platform", known: false },
};

describe("getJobsOverview", () => {
  it("environment 跟随配置，请求一次性快照端点", async () => {
    const client = fakeClient(emptyOverview);
    await getJobsOverview({}, client, config);
    expect(client.get).toHaveBeenCalledWith("/api/v1/jobs/overview", {
      searchParams: { environment: undefined },
    });
  });

  it("environment 跟随配置——前端不自己猜环境（跨环境读取后端会拒）", async () => {
    const client = fakeClient(emptyOverview);
    await getJobsOverview({}, client, { ...config, environment: "production" });
    expect(client.get).toHaveBeenCalledWith("/api/v1/jobs/overview", {
      searchParams: { environment: "production" },
    });
  });

  it("原样返回响应体", async () => {
    const client = fakeClient(emptyOverview);
    await expect(getJobsOverview({}, client, config)).resolves.toEqual(emptyOverview);
  });
});

describe("listJobRuns", () => {
  it("items 为 null 时按空数组处理，页面不会炸", async () => {
    const client = fakeClient({ items: null });
    const page = await listJobRuns({}, client, config);
    expect(page.items).toEqual([]);
  });

  it("不传 kinds/state/before 时不带这些查询参数；limit 用默认页大小", async () => {
    const client = fakeClient({ items: [] });
    await listJobRuns({}, client, config);
    expect(client.get).toHaveBeenCalledWith("/api/v1/jobs/runs", {
      searchParams: {
        environment: undefined,
        kind: undefined,
        state: undefined,
        before: undefined,
        limit: "50",
      },
    });
  });

  it("多个 kind 拼成逗号分隔（服务端按「其中任意一个」匹配）", async () => {
    const client = fakeClient({ items: [] });
    await listJobRuns({ kinds: SYNC_JOB_KINDS }, client, config);
    const call = (client.get as ReturnType<typeof vi.fn>).mock.calls[0];
    expect(call?.[1]?.searchParams?.kind).toBe("sub2api_sync,newapi_sync,finance_cost_sync");
  });

  it("空 kinds 数组等同于不筛（不传 kind 参数）", async () => {
    const client = fakeClient({ items: [] });
    await listJobRuns({ kinds: [] }, client, config);
    const call = (client.get as ReturnType<typeof vi.fn>).mock.calls[0];
    expect(call?.[1]?.searchParams?.kind).toBeUndefined();
  });

  it("state 原样透传", async () => {
    const client = fakeClient({ items: [] });
    await listJobRuns({ state: "discarded" }, client, config);
    const call = (client.get as ReturnType<typeof vi.fn>).mock.calls[0];
    expect(call?.[1]?.searchParams?.state).toBe("discarded");
  });

  it("before=0 会被当成游标传出去（首页请求应完全不传 before，而不是传 0）", async () => {
    const client = fakeClient({ items: [] });
    await listJobRuns({ before: 0 }, client, config);
    const call = (client.get as ReturnType<typeof vi.fn>).mock.calls[0];
    // 传了 0（哪怕是显式的 0）就该原样带出去；调用方想要「首页」应该干脆不传 before，
    // 这条用例只锁定「不做静默改写」这一件事
    expect(call?.[1]?.searchParams?.before).toBe("0");
  });

  it("next_before 为 0 或缺失都映射成 null（表示已经翻到底）", async () => {
    const client1 = fakeClient({ items: [], next_before: 0 });
    await expect(listJobRuns({}, client1, config)).resolves.toMatchObject({ nextBefore: null });

    const client2 = fakeClient({ items: [] });
    await expect(listJobRuns({}, client2, config)).resolves.toMatchObject({ nextBefore: null });
  });

  it("next_before 为正数时原样保留，供下一页请求使用", async () => {
    const client = fakeClient({ items: [], next_before: 42 });
    await expect(listJobRuns({}, client, config)).resolves.toMatchObject({ nextBefore: 42 });
  });
});

describe("kind/state 中文名", () => {
  it("已知 kind 翻成中文，未知 kind 原样返回", () => {
    expect(jobKindLabel("platform_heartbeat")).toBe("平台心跳");
    expect(jobKindLabel("sub2api_sync")).toBe("Sub2API 同步");
    // XM-OPS-TAILS0 前 reqlog_metrics/connector_probe/cpa_sync 三个已注册
    // 任务还没有中文名，靠"未知则原样显示"兜底；本片补了三个，这里改用一个
    // 真正不存在的 kind 验证兜底路径仍然有效。
    expect(jobKindLabel("a-kind-that-does-not-exist")).toBe("a-kind-that-does-not-exist");
  });

  it("XM-OPS-TAILS0 新补的三个周期任务 kind 中文名", () => {
    expect(jobKindLabel("reqlog_metrics")).toBe("请求量指标聚合");
    expect(jobKindLabel("connector_probe")).toBe("连接器健康探测");
    expect(jobKindLabel("cpa_sync")).toBe("CPA 用量同步");
  });

  it("已知 state 翻成中文，未知 state 显式标注而不是隐藏", () => {
    expect(jobStateLabel("retryable")).toBe("等待重试");
    expect(jobStateLabel("discarded")).toBe("多次失败（已放弃）");
    expect(jobStateLabel("bogus")).toBe("未知状态（bogus）");
  });

  it("同步批次覆盖三个数据连接器同步任务", () => {
    expect(SYNC_JOB_KINDS).toEqual(["sub2api_sync", "newapi_sync", "finance_cost_sync"]);
  });
});
