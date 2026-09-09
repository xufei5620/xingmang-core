import { describe, expect, it } from "vitest";
import type { AlertItem, AlertStatus } from "../api/alerts";
import { ApiError } from "../api/client";
import {
  alertLabel,
  describeAckFailure,
  planBulkAcknowledge,
  summarizeBulkAcknowledge,
} from "./alertBulkAck";

function alert(id: string, status: AlertStatus, title = `告警 ${id}`): AlertItem {
  return {
    id,
    rule_key: "metric.sync.failed",
    dedup_key: `metric.sync.failed:development:${id}`,
    severity: "critical",
    status,
    title,
    detail: "",
    environment: "development",
    source_metric_key: "",
    opened_at: "2026-09-01T10:00:00Z",
    last_seen_at: "2026-09-01T10:05:00Z",
    acknowledged_at: null,
    resolved_at: null,
    fire_count: 1,
    notify_status: "delivered",
    notify_error: "",
    notified_at: "2026-09-01T10:00:05Z",
  };
}

describe("planBulkAcknowledge", () => {
  it("只把 OPEN 与 REOPENED 排进要发的请求（与后端 WHERE 子句同一条规则）", () => {
    const items = [
      alert("a", "OPEN"),
      alert("b", "ACKNOWLEDGED"),
      alert("c", "REOPENED"),
      alert("d", "SILENCED"),
      alert("e", "RESOLVED"),
    ];
    const plan = planBulkAcknowledge(["a", "b", "c", "d", "e"], items);

    expect(plan.actionable.map((a) => a.id)).toEqual(["a", "c"]);
    expect(plan.skipped.map((s) => s.id)).toEqual(["b", "d", "e"]);
  });

  it("跳过的每一条都说清是因为什么状态，且用界面上那个词", () => {
    const plan = planBulkAcknowledge(["b"], [alert("b", "SILENCED", "渠道乙 余额不足")]);

    expect(plan.skipped).toEqual([
      {
        id: "b",
        label: "渠道乙 余额不足",
        reason: "当前状态「已静默」不可确认，只有未处理与复发可以",
      },
    ]);
  });

  it("选中之后列表刷新掉的那些键报成跳过，而不是被悄悄丢掉", () => {
    // DataTableV2 的选中集不随 rows 一起清（它没有那个 effect），所以这不是
    // 假想场景：确认完自动刷新一次，转走的行留下的就是这种孤儿键。
    const plan = planBulkAcknowledge(["a", "gone"], [alert("a", "OPEN")]);

    expect(plan.actionable.map((a) => a.id)).toEqual(["a"]);
    expect(plan.skipped).toEqual([
      {
        id: "gone",
        label: "gone",
        reason: "这一条已不在当前列表里，可能刚被解决或被筛掉；刷新后重新选择",
      },
    ]);
  });

  it("名单顺序跟表格走，不跟点选顺序走", () => {
    const items = [alert("a", "OPEN"), alert("b", "OPEN"), alert("c", "OPEN")];
    // 倒着点选，回执仍要按表格顺序排——否则名单和屏幕上的行对不上
    const plan = planBulkAcknowledge(["c", "b", "a"], items);

    expect(plan.actionable.map((a) => a.id)).toEqual(["a", "b", "c"]);
  });

  it("没选中的行一条都不进计划", () => {
    const plan = planBulkAcknowledge(["a"], [alert("a", "OPEN"), alert("b", "OPEN")]);

    expect(plan.actionable.map((a) => a.id)).toEqual(["a"]);
    expect(plan.skipped).toEqual([]);
  });
});

describe("alertLabel", () => {
  it("用标题指认告警", () => {
    expect(alertLabel(alert("a", "OPEN", "指标同步失败"))).toBe("指标同步失败");
  });

  it("标题为空时退回 id——那时它是唯一还能指认这一条的东西", () => {
    expect(alertLabel(alert("aaaa-1111", "OPEN", "   "))).toBe("aaaa-1111");
  });
});

describe("describeAckFailure", () => {
  it("403 说清错误码、缺哪个权限与 request_id", () => {
    const error = new ApiError(403, "PERMISSION_DENIED", "缺少权限 alerts.alert.manage", "req-9");

    expect(describeAckFailure(error)).toBe(
      "缺少权限 alerts.alert.manage（权限不足，错误码 PERMISSION_DENIED），需要权限 alerts.alert.manage，request_id=req-9",
    );
  });

  it("非 403 不掺权限那一句", () => {
    const error = new ApiError(409, "CONFLICT", "该告警已不可确认", "req-7");

    expect(describeAckFailure(error)).toBe(
      "该告警已不可确认（状态冲突，错误码 CONFLICT），request_id=req-7",
    );
  });

  it("没有 request_id 时不留一个空的 request_id=", () => {
    expect(describeAckFailure(new ApiError(500, "INTERNAL", "服务端错误"))).toBe(
      "服务端错误（服务端内部错误，错误码 INTERNAL）",
    );
  });

  it("不是 ApiError 时用它自己的话，而不是一句「未知错误」", () => {
    expect(describeAckFailure(new Error("该动作需要审批，已受理为审批单 ap-1"))).toBe(
      "该动作需要审批，已受理为审批单 ap-1",
    );
  });

  it("连话都没有时才说未知错误", () => {
    expect(describeAckFailure("扔了个字符串")).toBe("未知错误");
    expect(describeAckFailure(new Error(""))).toBe("未知错误");
  });
});

describe("summarizeBulkAcknowledge", () => {
  const ok = (n: number) =>
    Array.from({ length: n }, (_, i) => ({ id: `ok${i}`, label: `告警 ${i}`, runId: `run-${i}` }));
  const bad = (n: number) =>
    Array.from({ length: n }, (_, i) => ({ id: `bad${i}`, label: `坏 ${i}`, reason: "失败了" }));

  it("部分失败时两个数字都出现，不会只报成功", () => {
    expect(summarizeBulkAcknowledge(ok(7), bad(2), []).headline).toBe(
      "已确认 7 条，2 条失败；审计事件通常几秒内出现在审计页",
    );
  });

  it("全成功时只报成功数", () => {
    expect(summarizeBulkAcknowledge(ok(3), [], []).headline).toBe(
      "已确认 3 条；审计事件通常几秒内出现在审计页",
    );
  });

  it("一条都没成时明说「0 条确认成功」，不只写失败数", () => {
    // 只写「3 条失败」会读成「大部分成了、个别没成」——正好读反
    const summary = summarizeBulkAcknowledge([], bad(3), []);

    expect(summary.headline).toBe("0 条确认成功，3 条失败");
    // 缺席断言（已变异验证）：一次都没写成，就不该提审计事件——
    // 去审计页找一条不存在的记录是纯浪费
    expect(summary.headline).not.toContain("审计事件");
  });

  it("跳过的条数进总账，不被当成成功也不被当成失败", () => {
    expect(summarizeBulkAcknowledge(ok(2), bad(1), bad(4)).headline).toBe(
      "已确认 2 条，1 条失败，4 条跳过；审计事件通常几秒内出现在审计页",
    );
  });

  it("三个名单原样带出去，回执才能逐条列出是哪些", () => {
    const acknowledged = ok(1);
    const failed = bad(2);
    const skipped = bad(1);
    const summary = summarizeBulkAcknowledge(acknowledged, failed, skipped);

    expect(summary.acknowledged).toEqual(acknowledged);
    expect(summary.failed).toEqual(failed);
    expect(summary.skipped).toEqual(skipped);
  });
});
