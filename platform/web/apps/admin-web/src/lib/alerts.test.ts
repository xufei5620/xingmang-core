import { describe, expect, it } from "vitest";
import type { AlertItem, SilenceItem } from "../api/alerts";
import {
  canAcknowledge,
  countBySeverity,
  describeNotifyStatus,
  describeSeverity,
  describeSilenceState,
  describeStatus,
  formatSilenceRemaining,
  SILENCE_DURATION_OPTIONS,
  sortForDisplay,
  sortSilencesForDisplay,
} from "./alerts";

function alert(partial: Partial<AlertItem>): AlertItem {
  return {
    id: "id",
    rule_key: "metric.sync.failed",
    dedup_key: "metric.sync.failed:development:x",
    severity: "warning",
    status: "OPEN",
    title: "标题",
    detail: "",
    environment: "development",
    source_metric_key: "",
    opened_at: "2026-08-26T10:00:00Z",
    last_seen_at: "2026-08-26T10:00:00Z",
    acknowledged_at: null,
    resolved_at: null,
    fire_count: 1,
    notify_status: "pending",
    notify_error: "",
    notified_at: null,
    ...partial,
  };
}

describe("严重度展示", () => {
  it("critical 用 danger，warning 用 warning", () => {
    expect(describeSeverity("critical").tone).toBe("danger");
    expect(describeSeverity("warning").tone).toBe("warning");
    expect(describeSeverity("info").tone).toBe("info");
  });

  it("未知严重度按最高级别显示，不按最低", () => {
    // 后端加了新级别而前端没跟上时，显示成「提示」会让一条可能是灾难级的
    // 告警安静地躺在列表底部。不认识就当严重的处理才是安全的默认。
    const got = describeSeverity("fatal");
    expect(got.tone).toBe("danger");
    expect(got.label).toBe("fatal");
  });
});

describe("状态展示", () => {
  it("SILENCED 不能显示成成功色——静默不是解决", () => {
    // 这是本文件最重要的一条：静默的告警仍然活着，窗口过期后会转回 OPEN
    // 并重新投递。绿色会让人以为问题已经没了。
    const got = describeStatus("SILENCED");
    expect(got.tone).not.toBe("success");
    expect(got.tone).toBe("neutral");
    expect(got.label).toBe("已静默");
    expect(got.hint).toContain("转回");
  });

  it("只有 RESOLVED 是成功色", () => {
    expect(describeStatus("RESOLVED").tone).toBe("success");
    for (const s of ["OPEN", "REOPENED", "ACKNOWLEDGED", "SILENCED"]) {
      expect(describeStatus(s).tone).not.toBe("success");
    }
  });

  it("OPEN 与 REOPENED 都是危险色（都还没人处理完）", () => {
    expect(describeStatus("OPEN").tone).toBe("danger");
    expect(describeStatus("REOPENED").tone).toBe("danger");
  });

  it("五个状态都有中文名，没有一个漏成原始英文", () => {
    for (const s of ["OPEN", "ACKNOWLEDGED", "SILENCED", "RESOLVED", "REOPENED"]) {
      expect(describeStatus(s).label).not.toBe(s);
      expect(describeStatus(s).label.length).toBeGreaterThan(0);
    }
  });
});

describe("投递状态展示", () => {
  it("pending 是中性色不是成功色——没投出去就是没投出去", () => {
    const got = describeNotifyStatus("pending");
    expect(got.tone).toBe("neutral");
    expect(got.label).toBe("未投递");
    // 一个渠道都没配的部署会让告警永远停在这个状态，提示要说出这件事
    expect(got.hint).toContain("渠道");
  });

  it("failed 是危险色，delivered 是成功色", () => {
    expect(describeNotifyStatus("failed").tone).toBe("danger");
    expect(describeNotifyStatus("delivered").tone).toBe("success");
  });
});

describe("可确认判定", () => {
  it("只有 OPEN 与 REOPENED 能确认（与后端 WHERE 子句同一条规则）", () => {
    expect(canAcknowledge(alert({ status: "OPEN" }))).toBe(true);
    expect(canAcknowledge(alert({ status: "REOPENED" }))).toBe(true);
    expect(canAcknowledge(alert({ status: "ACKNOWLEDGED" }))).toBe(false);
    expect(canAcknowledge(alert({ status: "SILENCED" }))).toBe(false);
    expect(canAcknowledge(alert({ status: "RESOLVED" }))).toBe(false);
  });
});

describe("按严重度计数", () => {
  it("已解决的不计入，已静默的计入", () => {
    // 静默期间总览显示零告警，正是最容易出事的时候——静默必须计入。
    const counts = countBySeverity([
      alert({ severity: "critical", status: "OPEN" }),
      alert({ severity: "warning", status: "SILENCED" }),
      alert({ severity: "warning", status: "ACKNOWLEDGED" }),
      alert({ severity: "critical", status: "RESOLVED" }),
    ]);
    expect(counts).toEqual({ critical: 1, warning: 2, info: 0, total: 3 });
  });

  it("空清单给全零", () => {
    expect(countBySeverity([])).toEqual({ critical: 0, warning: 0, info: 0, total: 0 });
  });
});

describe("列表排序", () => {
  it("严重度降序优先，同级按最近发现降序", () => {
    const sorted = sortForDisplay([
      alert({ id: "w-old", severity: "warning", last_seen_at: "2026-08-26T09:00:00Z" }),
      alert({ id: "c", severity: "critical", last_seen_at: "2026-08-26T08:00:00Z" }),
      alert({ id: "w-new", severity: "warning", last_seen_at: "2026-08-26T11:00:00Z" }),
      alert({ id: "i", severity: "info", last_seen_at: "2026-08-26T12:00:00Z" }),
    ]);
    // 最严重的在最上面：一屏装不下时被挤到下面的必须是最不要紧的
    expect(sorted.map((a) => a.id)).toEqual(["c", "w-new", "w-old", "i"]);
  });

  it("不修改入参", () => {
    const input = [alert({ id: "a", severity: "info" }), alert({ id: "b", severity: "critical" })];
    sortForDisplay(input);
    expect(input.map((a) => a.id)).toEqual(["a", "b"]);
  });
});

describe("静默时长选项", () => {
  it("上限是 7 天，与后端 maxSilenceMinutes 一致", () => {
    const values = SILENCE_DURATION_OPTIONS.map((o) => Number(o.value));
    expect(Math.max(...values)).toBe(7 * 24 * 60);
    // 全部为正：一个 0 分钟的窗口不会静默任何东西，但创建者以为静默了
    expect(values.every((v) => v > 0)).toBe(true);
  });
});

describe("静默窗口三态", () => {
  function sil(partial: Partial<SilenceItem>): SilenceItem {
    return {
      id: "s",
      rule_key: "metric.sync.failed",
      environment: "development",
      reason: "理由",
      starts_at: "2026-09-08T11:00:00Z",
      ends_at: "2026-09-08T13:00:00Z",
      created_by: "staff_alice",
      created_at: "2026-09-08T10:59:00Z",
      state: "active",
      ...partial,
    };
  }

  it("生效中用 danger——告警此刻是哑的，那不是「一切正常」", () => {
    // 绿色会让人一眼扫过去以为没事，而静默期间恰恰最容易出事
    expect(describeSilenceState("active").tone).toBe("danger");
    expect(describeSilenceState("active").label).toBe("生效中");
  });

  it("未开始与已过期各有自己的说法，不压成一句「不生效」", () => {
    // 前者要等，后者要重建。混成一个会让人对着还没生效的窗口反复重建
    expect(describeSilenceState("scheduled").label).toBe("未开始");
    expect(describeSilenceState("expired").label).toBe("已过期");
    expect(describeSilenceState("scheduled").label).not.toBe(describeSilenceState("expired").label);
  });

  it("后端新增取值时显示原值并标成 warning，不冒充已知态", () => {
    const unknown = describeSilenceState("cancelled");
    expect(unknown.label).toBe("cancelled");
    expect(unknown.tone).toBe("warning");
  });

  it("排序：生效中置顶，同组内最快解除的在前", () => {
    const sorted = sortSilencesForDisplay([
      sil({ id: "expired", state: "expired", ends_at: "2026-09-01T00:00:00Z" }),
      sil({ id: "active-late", state: "active", ends_at: "2026-09-08T20:00:00Z" }),
      sil({ id: "scheduled", state: "scheduled", ends_at: "2026-09-09T00:00:00Z" }),
      sil({ id: "active-soon", state: "active", ends_at: "2026-09-08T13:00:00Z" }),
    ]);
    expect(sorted.map((s) => s.id)).toEqual(["active-soon", "active-late", "scheduled", "expired"]);
  });

  it("排序不修改入参", () => {
    const input = [sil({ id: "a", state: "expired" }), sil({ id: "b", state: "active" })];
    sortSilencesForDisplay(input);
    expect(input.map((s) => s.id)).toEqual(["a", "b"]);
  });

  it("剩余时间按服务端的两个时刻算，跨小时与跨天都说得出来", () => {
    expect(formatSilenceRemaining("2026-09-08T12:30:00Z", "2026-09-08T12:00:00Z")).toBe("还剩 30 分钟");
    expect(formatSilenceRemaining("2026-09-08T14:00:00Z", "2026-09-08T12:00:00Z")).toBe("还剩 2 小时");
    expect(formatSilenceRemaining("2026-09-08T14:30:00Z", "2026-09-08T12:00:00Z")).toBe("还剩 2 小时 30 分钟");
    expect(formatSilenceRemaining("2026-09-10T12:00:00Z", "2026-09-08T12:00:00Z")).toBe("还剩 2 天");
    expect(formatSilenceRemaining("2026-09-10T15:00:00Z", "2026-09-08T12:00:00Z")).toBe("还剩 2 天 3 小时");
  });

  it("算不出来就不显示，绝不显示一个算错的数", () => {
    // 已经过期、缺时刻、时刻不可解析——三种都返回空串
    expect(formatSilenceRemaining("2026-09-08T11:00:00Z", "2026-09-08T12:00:00Z")).toBe("");
    expect(formatSilenceRemaining("2026-09-08T12:00:00Z", "")).toBe("");
    expect(formatSilenceRemaining("", "2026-09-08T12:00:00Z")).toBe("");
    expect(formatSilenceRemaining("不是时间", "2026-09-08T12:00:00Z")).toBe("");
  });
});
