import { describe, expect, it } from "vitest";
import {
  describeFreshness,
  describeServiceStatus,
  formatDuration,
  formatFreshnessDetail,
  formatFreshnessNote,
  formatLocalTimestamp,
  formatUtcTimestamp,
  type FreshnessContract,
} from "./freshness";

function freshness(over: Partial<FreshnessContract> = {}): FreshnessContract {
  return {
    state: "fresh",
    staleness_seconds: 12,
    threshold_seconds: 1800,
    is_partial: false,
    observed_at: "2026-08-26T10:00:00Z",
    last_success: "2026-08-26T10:00:00Z",
    last_error_code: "",
    ...over,
  };
}

describe("describeFreshness：state → 文案/语气", () => {
  it("五个已知状态各有固定文案与语气", () => {
    expect(describeFreshness("uninitialized")).toMatchObject({
      label: "未初始化",
      tone: "neutral",
    });
    expect(describeFreshness("failed")).toMatchObject({ label: "同步失败", tone: "danger" });
    expect(describeFreshness("stale")).toMatchObject({ label: "数据延迟", tone: "warning" });
    expect(describeFreshness("partial")).toMatchObject({ label: "数据不完整", tone: "warning" });
    expect(describeFreshness("fresh")).toMatchObject({ label: "数据新鲜", tone: "success" });
  });

  it("stale / partial / failed 都不是 neutral/success —— 必须醒目", () => {
    for (const s of ["failed", "stale", "partial"]) {
      const tone = describeFreshness(s).tone;
      expect(["warning", "danger"]).toContain(tone);
    }
  });

  it("未初始化不会被当成正常状态显示", () => {
    expect(describeFreshness("uninitialized").label).toBe("未初始化");
    expect(describeFreshness("uninitialized").label).not.toBe("0");
  });

  it("未知状态显式暴露，且不降级成看起来正常的语气", () => {
    const d = describeFreshness("teleported");
    expect(d.label).toContain("teleported");
    expect(d.tone).toBe("warning");
  });
});

describe("describeServiceStatus", () => {
  it("三个服务状态各有语气", () => {
    expect(describeServiceStatus("active")).toMatchObject({ label: "运行中", tone: "success" });
    expect(describeServiceStatus("degraded")).toMatchObject({ label: "降级", tone: "warning" });
    expect(describeServiceStatus("retired")).toMatchObject({ label: "已下线", tone: "neutral" });
  });
  it("未知状态走 warning 兜底", () => {
    expect(describeServiceStatus("zombie").tone).toBe("warning");
  });
});

describe("formatDuration", () => {
  it("按量级取一个单位", () => {
    expect(formatDuration(0)).toBe("0 秒");
    expect(formatDuration(59)).toBe("59 秒");
    expect(formatDuration(60)).toBe("1 分钟");
    expect(formatDuration(3599)).toBe("59 分钟");
    expect(formatDuration(3600)).toBe("1 小时");
    expect(formatDuration(86399)).toBe("23 小时");
    expect(formatDuration(86400)).toBe("1 天");
    expect(formatDuration(90061)).toBe("1 天");
  });
  it("负数与非数值都钳到 0，不显示「落后 -5 秒」", () => {
    expect(formatDuration(-5)).toBe("0 秒");
    expect(formatDuration(Number.NaN)).toBe("0 秒");
  });
});

describe("formatUtcTimestamp", () => {
  it("固定输出 UTC 并写明时区，不随本机时区变化", () => {
    expect(formatUtcTimestamp("2026-08-26T10:00:00Z")).toBe("2026-08-26 10:00:00 UTC");
    // 带偏移的输入换算回 UTC
    expect(formatUtcTimestamp("2026-08-26T18:00:00+08:00")).toBe("2026-08-26 10:00:00 UTC");
    expect(formatUtcTimestamp("2026-01-02T03:04:05Z")).toBe("2026-01-02 03:04:05 UTC");
  });
  it("空值显示占位符，非法值原样返回", () => {
    expect(formatUtcTimestamp(null)).toBe("—");
    expect(formatUtcTimestamp("不是时间")).toBe("不是时间");
  });
});

describe("formatFreshnessNote", () => {
  it("同时给出数据时间与落后时长", () => {
    expect(formatFreshnessNote(freshness({ staleness_seconds: 7200 }))).toBe(
      "数据时间 2026-08-26 10:00:00 UTC · 落后 2 小时",
    );
  });
  it("从未采集时说清楚是「从未」，不显示 0", () => {
    const note = formatFreshnessNote(
      freshness({ state: "uninitialized", observed_at: null, staleness_seconds: null }),
    );
    expect(note).toContain("从未成功采集");
    expect(note).not.toContain("落后");
  });
});

describe("formatFreshnessDetail：精确秒数不丢", () => {
  it("正文取量级，精确秒数与阈值仍然给得出来（卡在阈值上时要靠它判断）", () => {
    const f = freshness({ staleness_seconds: 1799, threshold_seconds: 1800 });
    expect(formatFreshnessNote(f)).toContain("落后 29 分钟");
    expect(formatFreshnessDetail(f)).toBe("落后 1799 秒；阈值 1800 秒");
  });

  it("从未采集时说明里也不出现 0 秒", () => {
    const detail = formatFreshnessDetail(freshness({ staleness_seconds: null }));
    expect(detail).toBe("从未成功采集；阈值 1800 秒");
  });
});

describe("formatLocalTimestamp：本地时间但时区显式", () => {
  it("形状为 `YYYY-MM-DD HH:mm:ss (UTC±HH:mm)`，绝不省略时区", () => {
    expect(formatLocalTimestamp("2026-08-26T10:00:00Z")).toMatch(
      /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} \(UTC[+-]\d{2}:\d{2}\)$/,
    );
  });

  it("年月日时分秒取运行环境的本地时区（不断言具体时区，CI 时区未知）", () => {
    const iso = "2026-08-26T10:00:00Z";
    const d = new Date(iso);
    const pad = (n: number) => String(n).padStart(2, "0");
    expect(formatLocalTimestamp(iso)).toContain(
      `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
        `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`,
    );
  });

  it("空值与非法值的处理与 UTC 版一致：不吞信息也不编时间", () => {
    expect(formatLocalTimestamp(null)).toBe("—");
    expect(formatLocalTimestamp("")).toBe("—");
    expect(formatLocalTimestamp("不是时间")).toBe("不是时间");
  });
});
