import { describe, expect, it } from "vitest";
import type { AlertItem, AlertNotifyStatus, AlertStatus } from "../api/alerts";
import { isDelivered, rollupNotifyDelivery } from "./alertNotify";

function alert(id: string, status: AlertStatus, notify: AlertNotifyStatus | string): AlertItem {
  return {
    id,
    rule_key: "metric.sync.failed",
    dedup_key: `metric.sync.failed:development:${id}`,
    severity: "warning",
    status,
    title: `告警 ${id}`,
    detail: "",
    environment: "development",
    source_metric_key: "",
    opened_at: "2026-09-01T10:00:00Z",
    last_seen_at: "2026-09-01T10:05:00Z",
    acknowledged_at: null,
    resolved_at: null,
    fire_count: 1,
    notify_status: notify as AlertNotifyStatus,
    notify_error: notify === "failed" ? "telegram: HTTP 502" : "",
    notified_at: notify === "delivered" ? "2026-09-01T10:00:05Z" : null,
  };
}

describe("rollupNotifyDelivery", () => {
  it("按三个投递状态分别计数", () => {
    const rollup = rollupNotifyDelivery([
      alert("a", "OPEN", "delivered"),
      alert("b", "OPEN", "failed"),
      alert("c", "OPEN", "pending"),
      alert("d", "RESOLVED", "delivered"),
    ]);

    expect(rollup.delivered).toBe(2);
    expect(rollup.failed).toBe(1);
    expect(rollup.pending).toBe(1);
    expect(rollup.total).toBe(4);
  });

  it("认不出来的投递状态单独一格，不并进「未投递」", () => {
    // 并进 pending 等于替服务端下结论说「它还在排队」，而这一页恰恰是用来
    // 发现结论下错了的
    const rollup = rollupNotifyDelivery([alert("a", "OPEN", "throttled")]);

    expect(rollup.unknown).toBe(1);
    expect(rollup.pending).toBe(0);
    expect(rollup.delivered).toBe(0);
    expect(rollup.failed).toBe(0);
  });

  it("「没解决又没送达」只收未解决的那些", () => {
    const rollup = rollupNotifyDelivery([
      alert("open-failed", "OPEN", "failed"),
      alert("silenced-pending", "SILENCED", "pending"),
      alert("reopened-unknown", "REOPENED", "throttled"),
      alert("open-delivered", "OPEN", "delivered"),
      alert("resolved-pending", "RESOLVED", "pending"),
    ]);

    expect(rollup.unreachedActive.map((a) => a.id)).toEqual([
      "open-failed",
      "silenced-pending",
      "reopened-unknown",
    ]);
  });

  it("静默的算未解决——它只是被捂住了嘴，窗口过期还会回来", () => {
    const rollup = rollupNotifyDelivery([alert("s", "SILENCED", "pending")]);

    expect(rollup.unreachedActive.map((a) => a.id)).toEqual(["s"]);
  });

  it("已解决但从未送达的不进告警块，但仍计入投递计数", () => {
    // 它是历史，不是「现在没人知道」；可它确实发生过，所以计数不能少
    const rollup = rollupNotifyDelivery([alert("r", "RESOLVED", "failed")]);

    expect(rollup.unreachedActive).toEqual([]);
    expect(rollup.failed).toBe(1);
  });

  it("空列表不报「有人没收到」", () => {
    const rollup = rollupNotifyDelivery([]);

    expect(rollup.total).toBe(0);
    expect(rollup.unreachedActive).toEqual([]);
  });
});

describe("isDelivered", () => {
  it("只有 delivered 算送达", () => {
    expect(isDelivered(alert("a", "OPEN", "delivered"))).toBe(true);
    // pending 不算：一个渠道都没配的部署会让告警永远停在这个状态
    expect(isDelivered(alert("b", "OPEN", "pending"))).toBe(false);
    expect(isDelivered(alert("c", "OPEN", "failed"))).toBe(false);
  });
});
