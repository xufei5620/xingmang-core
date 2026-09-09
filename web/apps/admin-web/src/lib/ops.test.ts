import { describe, expect, it } from "vitest";
import type { OpsFreshness, OpsMetricSnapshot, OpsOverview, OpsSyncPipeline } from "../api/ops";
import {
  buildOpsHealthRows,
  describeSyncMode,
  MODE_SOURCE_UNKNOWN_HINT,
  readConnectorHealthValue,
} from "./ops";

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
    failed_jobs_by_kind: [],
    failed_jobs_status: "ok",
    failed_jobs_window_hours: 24,
    ...partial,
  };
}

describe("readConnectorHealthValue", () => {
  it("字段缺失或类型不对时给出安全默认值，不抛错", () => {
    expect(readConnectorHealthValue({})).toEqual({
      healthy: null,
      version: "",
      supported: null,
      kind: "",
      latencyMs: null,
      checkedAt: "",
    });
    expect(
      readConnectorHealthValue({
        healthy: "yes",
        version: 1,
        supported: "true",
        latency_ms: "12",
        checked_at: 0,
      }),
    ).toEqual({
      healthy: null,
      version: "",
      supported: null,
      kind: "",
      latencyMs: null,
      checkedAt: "",
    });
  });

  it("类型对得上时原样取出六个字段", () => {
    expect(
      readConnectorHealthValue({
        version: "0.1.152",
        supported: true,
        healthy: true,
        kind: "",
        latency_ms: 12,
        checked_at: "2026-08-31T03:04:05Z",
      }),
    ).toEqual({
      healthy: true,
      version: "0.1.152",
      supported: true,
      kind: "",
      latencyMs: 12,
      checkedAt: "2026-08-31T03:04:05Z",
    });
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
      // 确定的答案不挂悬浮说明：没什么要解释的
      hint: "",
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

  // XM-WORKBENCH-WIRE-OPS：后端从 XM-OPS-TRUTH 子片 A 起多给一个
  // effective_mode_source。它此前在库里没有这一行时硬答 "fake"——恰好因为生产的
  // env 缺省也是 fake 才没出事，那是 2026-09-08 事故报告里三个互相矛盾的答案
  // 之一。**在场与缺席两条都要有**：只测在场，老后端那条路是死是活没人答得上来。
  describe("effective_mode_source：在场 / 缺席", () => {
    it("database：按 effective_mode 说，与没有这个字段时逐字相同", () => {
      const real = describeSyncMode({
        config_available: true,
        effective_mode: "real",
        effective_mode_source: "database",
      });
      expect(real).toEqual({ label: "真实对接", tone: "success", hint: "" });
      const fake = describeSyncMode({
        config_available: true,
        effective_mode: "fake",
        effective_mode_source: "database",
      });
      expect(fake).toEqual({ label: "模拟数据", tone: "warning", hint: "" });
    });

    it("unknown：显示「模式未知」，并挂上那句「去哪配」的说明", () => {
      // 契约里这一支的 effective_mode 是空串（子片 A 点名要加这条用例：
      // 别让 `=== 'fake'` 之类的判断把空串落进未定义分支）。
      const got = describeSyncMode({
        config_available: true,
        effective_mode: "",
        effective_mode_source: "unknown",
      });
      expect(got.label).toBe("模式未知");
      expect(got.hint).toBe(MODE_SOURCE_UNKNOWN_HINT);
      // 「不知道」绝不能显示成一个具体的模式——尤其不能是 fake：那正是后端
      // 改掉的那个硬答案，也是事故里那三个互相矛盾的答案之一。
      expect(got.label).not.toBe("模拟数据");
      expect(got.label).not.toBe("真实对接");
      // 也不能是 success 语气：一个答不出来的格子不该看着像一切正常
      expect(got.tone).not.toBe("success");
    });

    it("source 说不知道就是不知道，哪怕 mode 带着一个值（契约违例时以 source 为准）", () => {
      // source 回答「这个答案算不算数」，mode 回答「答案是什么」。判据顺序反
      // 过来的话，这一条会显示成一个确定的「真实对接」。
      const got = describeSyncMode({
        config_available: true,
        effective_mode: "real",
        effective_mode_source: "unknown",
      });
      expect(got.label).toBe("模式未知");
      expect(got.hint).toBe(MODE_SOURCE_UNKNOWN_HINT);
    });

    it("缺席（老后端还没这一列）：退回只看 effective_mode 的旧口径，不编说明", () => {
      const real = describeSyncMode({ config_available: true, effective_mode: "real" });
      expect(real).toEqual({ label: "真实对接", tone: "success", hint: "" });
      // 空串这时只能说「不知道」，但**不挂那句悬浮说明**：我们并不知道它为什么
      // 是空的，编一句「该平台未在后台配置接入模式」出来是造假。
      const blank = describeSyncMode({ config_available: true, effective_mode: "" });
      expect(blank.label).toBe("模式未知");
      expect(blank.hint).toBe("");
    });

    it("模块没挂载时两个字段都不看：那是另一回事，也不挂说明", () => {
      const got = describeSyncMode({
        config_available: false,
        effective_mode: "",
        effective_mode_source: "unknown",
      });
      expect(got.label).toBe("凭据模块未挂载");
      expect(got.hint).toBe("");
    });
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

  // XM-CREDS-TAB-PROBE：supported 是采集链路的判据，判假时采集会停。
  // 它以前只存在于指标 value 里，界面上一个字都没有。
  it("矩阵未声明支持这个上游版本时，备注要说出来并染成警告色", () => {
    const rows = buildOpsHealthRows(
      overview({
        connector_health: [
          snapshot({
            metric_key: "sub2api.connector.health",
            value: { version: "0.2.1", supported: false, healthy: true, kind: "", latency_ms: 12 },
          }),
          snapshot({
            metric_key: "newapi.connector.health",
            value: { version: "1.0.0", supported: true, healthy: true, kind: "", latency_ms: 20 },
          }),
        ],
      }),
    );
    expect(rows[3]?.note).toContain("矩阵未声明支持");
    expect(rows[3]?.note).toContain("v0.2.1");
    expect(rows[3]?.noteTone).toBe("warning");
    // 判真的那条不该多出这句话
    expect(rows[4]?.note).not.toContain("矩阵");
  });

  // 「不知道」不是「不支持」：旧样本没有 supported 字段时不能染色也不能报警。
  it("supported 缺字段时不显示矩阵判定，也不改变色调", () => {
    const rows = buildOpsHealthRows(
      overview({
        connector_health: [
          snapshot({
            metric_key: "sub2api.connector.health",
            value: { version: "0.2.1", healthy: true, kind: "", latency_ms: 12 },
          }),
          snapshot({
            metric_key: "newapi.connector.health",
            value: { version: "1.0.0", healthy: true, kind: "", latency_ms: 20 },
          }),
        ],
      }),
    );
    expect(rows[3]?.note).not.toContain("矩阵");
    expect(rows[3]?.noteTone).toBe("neutral");
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

  // XM-WORKBENCH-WIRE-OPS：判据对不等于它被送到了行上。这一层单独钉一次，
  // 否则 describeSyncMode 算出来的 hint 在 pipelineRow 里被丢掉也不会有人知道。
  it("采集链路那两行把「模式未知」的悬浮说明带上，别的行不带", () => {
    const rows = buildOpsHealthRows(
      overview({
        sync_pipelines: [
          pipeline({ platform: "sub2api", effective_mode: "", effective_mode_source: "unknown" }),
          pipeline({
            platform: "newapi",
            kind: "newapi_sync",
            effective_mode: "real",
            effective_mode_source: "database",
          }),
        ],
      }),
    );
    expect(rows[1]?.note).toBe("模式未知");
    expect(rows[1]?.noteHint).toBe(MODE_SOURCE_UNKNOWN_HINT);
    // 确定的那一行不挂：一句永远都在的说明等于没有说明
    expect(rows[2]?.note).toBe("真实对接");
    expect(rows[2]?.noteHint).toBe("");
    // 别的四行本来就没有 note，更不该凭空长出 hint
    expect(rows.filter((r) => r.noteHint !== "")).toHaveLength(1);
  });
});
