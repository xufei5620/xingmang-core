import { describe, expect, it } from "vitest";
import type { OpsFreshness, OpsMetricSnapshot, OpsOverview, OpsSyncPipeline } from "../api/ops";
import { buildOpsHealthRows, describeSyncMode, readConnectorHealthValue } from "./ops";

function freshness(partial: Partial<OpsFreshness> = {}): OpsFreshness {
  return {
    state: "fresh",
    staleness_seconds: 5,
    threshold_seconds: 120,
    is_partial: false,
    observed_at: "2026-08-31T03:04:05Z",
    last_success: "2026-08-31T03:04:05Z",
    last_error_code: "",
    ...partial,
  };
}

function snapshot(partial: Partial<OpsMetricSnapshot> = {}): OpsMetricSnapshot {
  return {
    metric_key: "platform.heartbeat",
    source: "platform-worker",
    value: {},
    freshness: freshness(),
    ...partial,
  };
}

function pipeline(partial: Partial<OpsSyncPipeline> = {}): OpsSyncPipeline {
  return {
    kind: "sub2api_sync",
    platform: "sub2api",
    config_available: true,
    effective_mode: "real",
    config_updated_at: "2026-08-31T03:00:00Z",
    sample_metric_key: "sub2api.channels.status",
    source: "sub2api",
    freshness: freshness(),
    ...partial,
  };
}

function overview(partial: Partial<OpsOverview> = {}): OpsOverview {
  return {
    build: { version: "1.2.3", commit: "abcdef1", environment: "development" },
    worker_heartbeat: snapshot({
      metric_key: "platform.heartbeat",
      value: { job_id: 1, attempt: 1 },
    }),
    sync_pipelines: [
      pipeline({ platform: "sub2api", kind: "sub2api_sync", sample_metric_key: "sub2api.channels.status" }),
      pipeline({
        platform: "newapi",
        kind: "newapi_sync",
        effective_mode: "fake",
        sample_metric_key: "newapi.channels.status",
      }),
    ],
    connector_health: [
      snapshot({
        metric_key: "sub2api.connector.health",
        value: {
          version: "0.1.152",
          supported: true,
          healthy: true,
          kind: "",
          latency_ms: 12,
          checked_at: "2026-08-31T03:04:05Z",
        },
      }),
      snapshot({
        metric_key: "newapi.connector.health",
        value: {
          version: "0.2.0",
          supported: true,
          healthy: false,
          kind: "auth",
          latency_ms: 20,
          checked_at: "2026-08-31T03:04:05Z",
        },
      }),
    ],
    alert_delivery: { telegram_configured: true, webhook_configured: false },
    retention: snapshot({
      metric_key: "platform.retention.last_run",
      value: {
        metric_samples_deleted: 0,
        resolved_alerts_deleted: 0,
        sample_retention_days: 90,
        alert_retention_days: 180,
      },
    }),
    database: { connected: true },
    ...partial,
  };
}

describe("readConnectorHealthValue", () => {
  it("字段缺失或类型不对时给出安全默认值，不抛错", () => {
    expect(readConnectorHealthValue({})).toEqual({
      healthy: null,
      version: "",
      kind: "",
      latencyMs: null,
    });
    expect(readConnectorHealthValue({ healthy: "yes", version: 1, latency_ms: "12" })).toEqual({
      healthy: null,
      version: "",
      kind: "",
      latencyMs: null,
    });
  });

  it("类型对得上时原样取出四个字段", () => {
    expect(
      readConnectorHealthValue({
        version: "0.1.152",
        supported: true,
        healthy: true,
        kind: "",
        latency_ms: 12,
        checked_at: "2026-08-31T03:04:05Z",
      }),
    ).toEqual({ healthy: true, version: "0.1.152", kind: "", latencyMs: 12 });
  });
});

describe("describeSyncMode", () => {
  it("config_available=false 时统一显示模块未挂载，不看 effective_mode", () => {
    const got = describeSyncMode({ config_available: false, effective_mode: "" });
    expect(got.label).toBe("凭据模块未挂载");
    expect(got.tone).toBe("neutral");
  });

  it("real 是 success 语气", () => {
    expect(describeSyncMode({ config_available: true, effective_mode: "real" })).toEqual({
      label: "真实对接",
      tone: "success",
    });
  });

  it("fake 不能显示成和 real 一样的语气——运营会拿它去核对真实告警", () => {
    const got = describeSyncMode({ config_available: true, effective_mode: "fake" });
    expect(got.label).toBe("模拟数据");
    expect(got.tone).not.toBe("success");
    expect(got.tone).toBe("warning");
  });

  it("未知的 effective_mode 原样显示，不吞掉信息", () => {
    const got = describeSyncMode({ config_available: true, effective_mode: "shadow" });
    expect(got.label).toBe("shadow");
  });
});

describe("buildOpsHealthRows", () => {
  it("固定产出 6 行，顺序与组件名逐一对应，rowKey 互不相同", () => {
    const rows = buildOpsHealthRows(overview());
    expect(rows.map((r) => r.component)).toEqual([
      "worker 心跳",
      "sub2api 采集链路",
      "newapi 采集链路",
      "sub2api 连接器健康",
      "newapi 连接器健康",
      "保留期清理",
    ]);
    expect(new Set(rows.map((r) => r.id)).size).toBe(rows.length);
  });

  it("环境统一取 build.environment，不逐行猜测", () => {
    const rows = buildOpsHealthRows(overview({ build: { version: "x", commit: "y", environment: "staging" } }));
    expect(rows.every((r) => r.environment === "staging")).toBe(true);
  });

  it("worker 心跳与保留期清理没有依赖，依赖列是「-」而不是空串", () => {
    const rows = buildOpsHealthRows(overview());
    expect(rows[0]?.dependency).toBe("-");
    expect(rows[5]?.dependency).toBe("-");
    expect(rows[0]?.note).toBe("");
  });

  it("采集链路的依赖是 sample_metric_key，状态备注携带采集模式", () => {
    const rows = buildOpsHealthRows(overview());
    expect(rows[1]?.dependency).toBe("sub2api.channels.status");
    expect(rows[1]?.note).toBe("真实对接");
    expect(rows[2]?.dependency).toBe("newapi.channels.status");
    expect(rows[2]?.note).toBe("模拟数据");
  });

  it("连接器健康的依赖是 value.kind，为空串时是「-」", () => {
    const rows = buildOpsHealthRows(overview());
    // fixture 里 sub2api 的 kind 是空串（健康），newapi 的是 "auth"（鉴权失败）
    expect(rows[3]?.dependency).toBe("-");
    expect(rows[4]?.dependency).toBe("auth");
    expect(rows[3]?.note).toContain("健康");
    expect(rows[4]?.note).toContain("不健康");
    expect(rows[4]?.noteTone).toBe("danger");
  });

  it("契约被破坏（sync_pipelines 缺一条）时抛错，不静默把数据画到错的行上", () => {
    expect(() =>
      buildOpsHealthRows(overview({ sync_pipelines: [pipeline({ platform: "sub2api" })] })),
    ).toThrow(/sync_pipelines\[1\]/);
  });

  it("契约被破坏（connector_health 缺一条）时同样抛错", () => {
    expect(() =>
      buildOpsHealthRows(
        overview({
          connector_health: [
            snapshot({ metric_key: "sub2api.connector.health", value: { healthy: true } }),
          ],
        }),
      ),
    ).toThrow(/connector_health\[1\]/);
  });
});
