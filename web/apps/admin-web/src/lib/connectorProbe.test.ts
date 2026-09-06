import { describe, expect, it } from "vitest";
import type { MetricHistoryItem } from "../api/platform";
import {
  buildProbeRows,
  connectorHealthMetricKey,
  describeProbeHealth,
  describeUpstreamSupport,
  formatLatency,
  latestProbe,
} from "./connectorProbe";

function sample(partial: Partial<MetricHistoryItem> = {}): MetricHistoryItem {
  return {
    observed_at: "2026-09-06T01:00:00Z",
    synced_at: "2026-09-06T01:00:05Z",
    source: "connector",
    status: "ok",
    is_partial: false,
    watermark: "fp-1",
    last_error_code: "",
    value: {
      version: "0.2.1",
      supported: true,
      healthy: true,
      kind: "",
      latency_ms: 12,
      checked_at: "2026-09-06T00:59:58Z",
    },
    ...partial,
  };
}

describe("connectorHealthMetricKey", () => {
  it("两个已知平台各自的指标键", () => {
    expect(connectorHealthMetricKey("sub2api")).toBe("sub2api.connector.health");
    expect(connectorHealthMetricKey("newapi")).toBe("newapi.connector.health");
  });

  it("不认识的平台返回空串，让调用方不要发一个注定查不到的查询", () => {
    expect(connectorHealthMetricKey("cpa")).toBe("");
    expect(connectorHealthMetricKey("")).toBe("");
  });
});

describe("describeUpstreamSupport", () => {
  it("判假时说明这是矩阵的问题而不是上游坏了，并带上探测到的版本", () => {
    const got = describeUpstreamSupport(false, "0.2.1");
    expect(got.tone).toBe("warning");
    expect(got.label).toBe("矩阵未声明支持");
    expect(got.detail).toContain("0.2.1");
    expect(got.detail).toContain("不等于上游坏了");
  });

  it("判假但没探测到版本号时不编一个版本进去", () => {
    expect(describeUpstreamSupport(false, "").detail).not.toContain("undefined");
    expect(describeUpstreamSupport(false, "").detail).toContain("没有覆盖这次探测到的版本");
  });

  it("null 是「不知道」，不是「不支持」——不染成警告色", () => {
    const got = describeUpstreamSupport(null, "0.2.1");
    expect(got.tone).toBe("neutral");
    expect(got.label).toBe("未知");
    expect(got.detail).toContain("不知道不等于不支持");
  });

  it("判真是成功色", () => {
    expect(describeUpstreamSupport(true, "0.2.1").tone).toBe("success");
  });
});

describe("buildProbeRows", () => {
  it("最近一次在最前——后端按升序返回，表格里人先看最新", () => {
    const rows = buildProbeRows([
      sample({ synced_at: "2026-09-06T01:00:05Z", value: { ...sample().value, version: "0.1.179" } }),
      sample({ synced_at: "2026-09-06T02:00:05Z", value: { ...sample().value, version: "0.2.1" } }),
    ]);
    expect(rows.map((row) => row.version)).toEqual(["0.2.1", "0.1.179"]);
    expect(latestProbe(rows)?.version).toBe("0.2.1");
  });

  it("优先用 value.checked_at 当探测时刻，并标出它取自哪个字段", () => {
    const row = buildProbeRows([sample()])[0];
    expect(row?.checkedAt).toBe("2026-09-06T00:59:58Z");
    expect(row?.checkedAtSource).toBe("探测时刻");
  });

  it("没有 checked_at 时退回 observed_at，再退回 synced_at，并如实标注", () => {
    const noChecked = buildProbeRows([
      sample({ value: { version: "0.2.1", healthy: true, kind: "", latency_ms: 1 } }),
    ])[0];
    expect(noChecked?.checkedAt).toBe("2026-09-06T01:00:00Z");
    expect(noChecked?.checkedAtSource).toBe("观测时刻");

    const noObserved = buildProbeRows([
      sample({
        observed_at: null,
        value: { version: "0.2.1", healthy: true, kind: "", latency_ms: 1 },
      }),
    ])[0];
    expect(noObserved?.checkedAt).toBe("2026-09-06T01:00:05Z");
    expect(noObserved?.checkedAtSource).toBe("落库时刻");
  });

  it("value 为 null 的失败样本不炸，字段落到安全默认值且保留失败信息", () => {
    const row = buildProbeRows([
      sample({ value: null, status: "failed", last_error_code: "auth" }),
    ])[0];
    expect(row?.version).toBe("");
    expect(row?.healthy).toBeNull();
    expect(row?.supported).toBeNull();
    expect(row?.latencyMs).toBeNull();
    expect(row?.status).toBe("failed");
    expect(row?.lastErrorCode).toBe("auth");
  });

  it("没有样本时 latestProbe 返回 null，而不是一行全是破折号的假数据", () => {
    expect(buildProbeRows([])).toEqual([]);
    expect(latestProbe([])).toBeNull();
  });

  it("逐点来源原样带出——一条曲线可能混着 fake 与 real", () => {
    const rows = buildProbeRows([sample({ source: "fake" }), sample({ source: "connector" })]);
    expect(rows.map((row) => row.source)).toEqual(["connector", "fake"]);
  });
});

describe("formatLatency 与 describeProbeHealth", () => {
  it("0 ms 是合法值，不能被当成空值显示成破折号", () => {
    expect(formatLatency(0)).toBe("0 ms");
    expect(formatLatency(12)).toBe("12 ms");
    expect(formatLatency(null)).toBe("—");
  });

  it("不健康时把错误类别带进标签，健康与未知各自的色调", () => {
    expect(describeProbeHealth(false, "auth")).toEqual({ label: "不健康 · auth", tone: "danger" });
    expect(describeProbeHealth(false, "")).toEqual({ label: "不健康", tone: "danger" });
    expect(describeProbeHealth(true, "")).toEqual({ label: "健康", tone: "success" });
    expect(describeProbeHealth(null, "")).toEqual({ label: "未知", tone: "neutral" });
  });
});
