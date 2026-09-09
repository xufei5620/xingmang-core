import { describe, expect, it } from "vitest";
import {
  buildCustomRequestRange,
  buildRequestPresetRange,
  parseRequestPeriodMode,
  type RequestTimeRange,
} from "./requestPeriod";

function localDay(rfc3339: string): string {
  const value = new Date(rfc3339);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${value.getFullYear()}-${pad(value.getMonth() + 1)}-${pad(value.getDate())}`;
}

function expectRange(result: ReturnType<typeof buildRequestPresetRange>): RequestTimeRange {
  expect(result.ok).toBe(true);
  if (!result.ok) throw new Error(result.error);
  return result.range;
}

describe("请求统计预设区间", () => {
  it("日区间按本地自然日生成闭开 RFC3339 边界", () => {
    const range = expectRange(buildRequestPresetRange("day", "2026-08-27"));
    expect(localDay(range.since)).toBe("2026-08-27");
    expect(localDay(range.until)).toBe("2026-08-28");
  });

  it("周从周一开始，即使锚点是周日", () => {
    const range = expectRange(buildRequestPresetRange("week", "2026-08-30"));
    expect(localDay(range.since)).toBe("2026-08-24");
    expect(localDay(range.until)).toBe("2026-08-31");
  });

  it("月区间从当月一号到下月一号", () => {
    const range = expectRange(buildRequestPresetRange("month", "2026-08-31"));
    expect(localDay(range.since)).toBe("2026-08-01");
    expect(localDay(range.until)).toBe("2026-09-01");
  });

  it("非法或清空的日期不返回空边界，避免静默放宽查询", () => {
    for (const value of ["", "2026-02-30", "not-a-day"]) {
      const result = buildRequestPresetRange("day", value);
      expect(result.ok).toBe(false);
      if (!result.ok) expect(result.error).toMatch(/日期/);
    }
  });
});

describe("请求统计自定义区间", () => {
  it("合法区间转换为 RFC3339，保持闭开语义", () => {
    const result = buildCustomRequestRange("2026-08-27T09:30", "2026-08-27T10:30");
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(new Date(result.range.since).getTime()).toBe(new Date("2026-08-27T09:30").getTime());
    expect(new Date(result.range.until).getTime()).toBe(new Date("2026-08-27T10:30").getTime());
  });

  it("清空、非法和起止颠倒都返回错误，不产生可提交区间", () => {
    const cases: Array<[string, string]> = [
      ["", "2026-08-27T10:30"],
      ["2026-08-27T09:30", ""],
      ["not-a-time", "2026-08-27T10:30"],
      ["2026-08-27T10:30", "2026-08-27T09:30"],
      ["2026-08-27T10:30", "2026-08-27T10:30"],
    ];
    for (const [since, until] of cases) {
      expect(buildCustomRequestRange(since, until).ok).toBe(false);
    }
  });
});

describe("区间模式 URL 解析", () => {
  it("只接受四种已知模式，其他值回到自定义而不是猜", () => {
    expect(parseRequestPeriodMode("day")).toBe("day");
    expect(parseRequestPeriodMode("week")).toBe("week");
    expect(parseRequestPeriodMode("month")).toBe("month");
    expect(parseRequestPeriodMode("custom")).toBe("custom");
    expect(parseRequestPeriodMode("weekly")).toBe("custom");
    expect(parseRequestPeriodMode(null)).toBe("custom");
  });
});
