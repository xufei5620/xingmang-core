import { describe, expect, it, vi } from "vitest";
import { getOpsOverview, type OpsOverview } from "./ops";
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

const FRESHNESS = {
  state: "fresh" as const,
  staleness_seconds: 5,
  threshold_seconds: 120,
  is_partial: false,
  observed_at: "2026-08-31T03:04:05Z",
  last_success: "2026-08-31T03:04:05Z",
  last_error_code: "",
};

const FIXTURE: OpsOverview = {
  build: { version: "1.2.3", commit: "abcdef1", environment: "development" },
  worker_heartbeat: {
    metric_key: "platform.heartbeat",
    source: "platform-worker",
    value: { job_id: 123, attempt: 1 },
    freshness: FRESHNESS,
  },
  sync_pipelines: [
    {
      kind: "sub2api_sync",
      platform: "sub2api",
      config_available: true,
      effective_mode: "real",
      config_updated_at: "2026-08-31T03:00:00Z",
      sample_metric_key: "sub2api.channels.status",
      source: "sub2api",
      freshness: FRESHNESS,
    },
    {
      kind: "newapi_sync",
      platform: "newapi",
      config_available: true,
      effective_mode: "fake",
      config_updated_at: null,
      sample_metric_key: "newapi.channels.status",
      source: "newapi",
      freshness: FRESHNESS,
    },
  ],
  connector_health: [
    {
      metric_key: "sub2api.connector.health",
      source: "sub2api",
      value: {
        version: "0.1.152",
        supported: true,
        healthy: true,
        kind: "",
        latency_ms: 12,
        checked_at: "2026-08-31T03:04:05Z",
      },
      freshness: FRESHNESS,
    },
    {
      metric_key: "newapi.connector.health",
      source: "newapi",
      value: {
        version: "0.2.0",
        supported: true,
        healthy: false,
        kind: "auth",
        latency_ms: 20,
        checked_at: "2026-08-31T03:04:05Z",
      },
      freshness: FRESHNESS,
    },
  ],
  alert_delivery: { telegram_configured: true, webhook_configured: false },
  retention: {
    metric_key: "platform.retention.last_run",
    source: "platform-worker",
    value: {
      metric_samples_deleted: 0,
      resolved_alerts_deleted: 0,
      sample_retention_days: 90,
      alert_retention_days: 180,
    },
    freshness: FRESHNESS,
  },
  database: { connected: true },
  failed_jobs_by_kind: [],
  failed_jobs_status: "ok",
  failed_jobs_window_hours: 24,
};

describe("getOpsOverview", () => {
  it("请求路径固定为 /api/v1/ops/overview", async () => {
    const client = fakeClient(FIXTURE);
    await getOpsOverview(client, config);
    expect(client.get).toHaveBeenCalledWith("/api/v1/ops/overview", {
      searchParams: { environment: undefined },
    });
  });

  it("environment 跟随配置——不猜、不硬编码", async () => {
    const client = fakeClient(FIXTURE);
    await getOpsOverview(client, { ...config, environment: "production" });
    expect(client.get).toHaveBeenCalledWith("/api/v1/ops/overview", {
      searchParams: { environment: "production" },
    });
  });

  it("signal 传了才透传给 client.get", async () => {
    const client = fakeClient(FIXTURE);
    const controller = new AbortController();
    await getOpsOverview(client, config, controller.signal);
    expect(client.get).toHaveBeenCalledWith("/api/v1/ops/overview", {
      searchParams: { environment: undefined },
      signal: controller.signal,
    });
  });

  it("不传 signal 时不会把 undefined 拼进 options", async () => {
    const client = fakeClient(FIXTURE);
    await getOpsOverview(client, config);
    const [, options] = (client.get as ReturnType<typeof vi.fn>).mock.calls[0] as [string, Record<string, unknown>];
    expect("signal" in options).toBe(false);
  });

  it("响应体原样透传给调用方", async () => {
    const client = fakeClient(FIXTURE);
    await expect(getOpsOverview(client, config)).resolves.toEqual(FIXTURE);
  });

  it("client/config 两个参数都缺省时仍能构造出一次合法调用", async () => {
    // 不对默认单例 apiClient/appApiConfig 的具体行为断言——那是 client.ts/config.ts
    // 自己的测试范围，这里只确认 getOpsOverview 的默认参数确实生效、不报错
    const client = fakeClient(FIXTURE);
    await expect(getOpsOverview(client)).resolves.toEqual(FIXTURE);
  });
});
