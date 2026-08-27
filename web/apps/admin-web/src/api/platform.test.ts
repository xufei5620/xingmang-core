import { describe, expect, it, vi } from "vitest";
import type { ApiClient } from "./client";
import type { PlatformApiConfig } from "./config";
import {
  AUDIT_PAGE_SIZE,
  createService,
  executeAction,
  listAuditEvents,
  listMetricHistory,
  listMetrics,
  listServices,
  METRIC_HISTORY_HOURS,
  observeService,
  serviceFreshness,
  SERVICE_STALENESS_THRESHOLD_SECONDS,
  type ServiceItem,
} from "./platform";

const config: PlatformApiConfig = {
  baseUrl: "",
  principalId: "dev-operator",
  principalType: "HUMAN",
  scopes: ["registry.read", "ops.read"],
};

function service(over: Partial<ServiceItem> = {}): ServiceItem {
  return {
    id: "11111111-1111-1111-1111-111111111111",
    service_type: "sub2api",
    instance_id: "sub2api-prod",
    environment: "production",
    endpoint: "https://sub2api.example.com",
    owner: "平台组",
    status: "active",
    source_watermark: "wm-1",
    observed_at: "2026-08-26T10:00:00Z",
    stale_seconds: 60,
    ...over,
  };
}

function fakeClient(body: unknown): ApiClient {
  return {
    get: vi.fn().mockResolvedValue(body),
    post: vi.fn().mockResolvedValue(body),
  } as unknown as ApiClient;
}

describe("listServices / listMetrics", () => {
  it("items 为 null 时按空数组处理，页面不会炸", async () => {
    expect(await listServices({}, fakeClient({ items: null }), config)).toEqual([]);
    expect(await listMetrics({}, fakeClient({ items: null }), config)).toEqual([]);
  });

  it("配置里没有 environment 时不拼这个查询参数", async () => {
    const client = fakeClient({ items: [] });
    await listServices({}, client, config);
    expect(client.get).toHaveBeenCalledWith("/api/v1/services", {
      searchParams: { environment: undefined },
    });
  });

  it("显式配置了 environment 才传（传错会被后端 403）", async () => {
    const client = fakeClient({ items: [] });
    await listMetrics({}, client, { ...config, environment: "staging" });
    expect(client.get).toHaveBeenCalledWith("/api/v1/metrics", {
      searchParams: { environment: "staging" },
    });
  });
});

describe("serviceFreshness：把采集时间折算成统一新鲜度形状", () => {
  it("从未采集 → uninitialized，且不给落后秒数", () => {
    const f = serviceFreshness(service({ observed_at: null, stale_seconds: null }));
    expect(f.state).toBe("uninitialized");
    expect(f.staleness_seconds).toBeNull();
    expect(f.observed_at).toBeNull();
  });

  it("落后未超阈值 → fresh", () => {
    const f = serviceFreshness(service({ stale_seconds: SERVICE_STALENESS_THRESHOLD_SECONDS - 1 }));
    expect(f.state).toBe("fresh");
  });

  it("落后达到阈值 → stale", () => {
    const f = serviceFreshness(service({ stale_seconds: SERVICE_STALENESS_THRESHOLD_SECONDS }));
    expect(f.state).toBe("stale");
    expect(f.threshold_seconds).toBe(SERVICE_STALENESS_THRESHOLD_SECONDS);
  });

  it("绝不凭空造出 failed / partial —— Service 记录里没有这些信息", () => {
    for (const stale of [0, 100, 999999]) {
      const state = serviceFreshness(service({ stale_seconds: stale })).state;
      expect(["fresh", "stale"]).toContain(state);
    }
    expect(serviceFreshness(service()).is_partial).toBe(false);
    expect(serviceFreshness(service()).last_error_code).toBe("");
  });
});

describe("listMetricHistory", () => {
  it("带上 metric_key 与 hours；默认回看 24 小时", async () => {
    const client = fakeClient({ items: [] });
    await listMetricHistory("sub2api.revenue.daily", {}, client, config);
    expect(client.get).toHaveBeenCalledWith("/api/v1/metrics/history", {
      searchParams: {
        metric_key: "sub2api.revenue.daily",
        hours: String(METRIC_HISTORY_HOURS),
        environment: undefined,
      },
    });
  });

  it("hours 可覆盖", async () => {
    const client = fakeClient({ items: [] });
    await listMetricHistory("k", { hours: 6 }, client, config);
    expect(client.get).toHaveBeenCalledWith(
      "/api/v1/metrics/history",
      expect.objectContaining({ searchParams: expect.objectContaining({ hours: "6" }) }),
    );
  });

  it("items 为 null 时按空数组处理", async () => {
    expect(await listMetricHistory("k", {}, fakeClient({ items: null }), config)).toEqual([]);
  });
});

describe("listAuditEvents：游标翻页", () => {
  it("首页不传 before_seq —— 传 0 等于「只要 sequence < 0」，一条都没有", async () => {
    const client = fakeClient({ items: [], next_before: 0 });
    await listAuditEvents({}, client);
    expect(client.get).toHaveBeenCalledWith("/api/v1/audit/events", {
      searchParams: { limit: String(AUDIT_PAGE_SIZE), before_seq: undefined },
    });
  });

  it("翻页时把游标带上", async () => {
    const client = fakeClient({ items: [] });
    await listAuditEvents({ beforeSeq: 122, limit: 10 }, client);
    expect(client.get).toHaveBeenCalledWith("/api/v1/audit/events", {
      searchParams: { limit: "10", before_seq: "122" },
    });
  });

  it("next_before 为 0 或缺失都表示到底了", async () => {
    expect((await listAuditEvents({}, fakeClient({ items: [], next_before: 0 }))).nextBefore).toBeNull();
    expect((await listAuditEvents({}, fakeClient({ items: [] }))).nextBefore).toBeNull();
    expect(
      (await listAuditEvents({}, fakeClient({ items: [], next_before: 122 }))).nextBefore,
    ).toBe(122);
  });

  it("items 为 null 时按空数组处理", async () => {
    expect((await listAuditEvents({}, fakeClient({ items: null }))).items).toEqual([]);
  });
});

describe("executeAction：写路径唯一入口", () => {
  it("路径按 actionID/version 拼；请求体只有 params", async () => {
    const client = fakeClient({ action_run_id: "run-1", result: { id: "x" } });
    await executeAction(
      {
        actionId: "registry.service.create",
        version: "1",
        params: { instance_id: "sub2api-dev" },
        requestId: "req-7",
      },
      {},
      client,
    );
    expect(client.post).toHaveBeenCalledWith(
      "/api/v1/actions/registry.service.create/versions/1/execute",
      { params: { instance_id: "sub2api-dev" } },
      { requestId: "req-7" },
    );
  });

  it("没传 request_id 时现生成一个（后端要求非空）", async () => {
    const client = fakeClient({ action_run_id: "run-1" });
    await executeAction({ actionId: "a.b.c", version: "1", params: {} }, {}, client);
    const options = (client.post as unknown as { mock: { calls: unknown[][] } }).mock.calls[0]?.[2];
    expect((options as { requestId: string }).requestId).toMatch(/^[A-Za-z0-9._:-]{1,128}$/);
  });

  it("取响应里的 action_run_id（httpapi.executeActionResponse 的字段名）", async () => {
    const run = await executeAction(
      { actionId: "a.b.c", version: "1", params: {} },
      {},
      fakeClient({ action_run_id: "run-42", result: { ok: true } }),
    );
    expect(run.runId).toBe("run-42");
    expect(run.result).toEqual({ ok: true });
  });

  it("响应里没有 run_id 时给空串，不编一个", async () => {
    const run = await executeAction({ actionId: "a.b.c", version: "1", params: {} }, {}, fakeClient({}));
    expect(run.runId).toBe("");
  });
});

describe("createService / observeService", () => {
  it("createService 走 registry.service.create@1", async () => {
    const client = fakeClient({ action_run_id: "run-1" });
    await createService({ instance_id: "sub2api-dev" }, {}, client);
    expect(client.post).toHaveBeenCalledWith(
      "/api/v1/actions/registry.service.create/versions/1/execute",
      { params: { instance_id: "sub2api-dev" } },
      expect.anything(),
    );
  });

  it("observeService 走 registry.service.observe@1", async () => {
    const client = fakeClient({ action_run_id: "run-2" });
    await observeService({ watermark: "wm-1", status: "active" }, {}, client);
    expect(client.post).toHaveBeenCalledWith(
      "/api/v1/actions/registry.service.observe/versions/1/execute",
      { params: { watermark: "wm-1", status: "active" } },
      expect.anything(),
    );
  });
});
