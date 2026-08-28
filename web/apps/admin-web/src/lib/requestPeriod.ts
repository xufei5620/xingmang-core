export type RequestPeriodMode = "day" | "week" | "month" | "custom";

export interface RequestTimeRange {
  since: string;
  until: string;
}

export type RequestRangeResult =
  | { ok: true; range: RequestTimeRange }
  | { ok: false; error: string };

export function parseRequestPeriodMode(raw: string | null): RequestPeriodMode {
  switch (raw) {
    case "day":
    case "week":
    case "month":
    case "custom":
      return raw;
    default:
      return "custom";
  }
}

function parseLocalDay(raw: string): Date | null {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(raw.trim());
  if (!match) return null;
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const value = new Date(year, month - 1, day, 0, 0, 0, 0);
  if (
    Number.isNaN(value.getTime()) ||
    value.getFullYear() !== year ||
    value.getMonth() !== month - 1 ||
    value.getDate() !== day
  ) {
    return null;
  }
  return value;
}

/** 日/周/月预设按操作者本地日历切分；周一为每周第一天。 */
export function buildRequestPresetRange(
  mode: Exclude<RequestPeriodMode, "custom">,
  anchorDay: string,
): RequestRangeResult {
  const anchor = parseLocalDay(anchorDay);
  if (anchor === null) return { ok: false, error: "请选择有效日期" };

  const since = new Date(anchor);
  if (mode === "week") {
    const daysSinceMonday = (since.getDay() + 6) % 7;
    since.setDate(since.getDate() - daysSinceMonday);
  } else if (mode === "month") {
    since.setDate(1);
  }

  const until = new Date(since);
  if (mode === "day") until.setDate(until.getDate() + 1);
  else if (mode === "week") until.setDate(until.getDate() + 7);
  else until.setMonth(until.getMonth() + 1);

  return { ok: true, range: { since: since.toISOString(), until: until.toISOString() } };
}

function parseLocalDateTime(raw: string): Date | null {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(raw.trim());
  if (!match) return null;
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const hour = Number(match[4]);
  const minute = Number(match[5]);
  const value = new Date(year, month - 1, day, hour, minute, 0, 0);
  if (
    Number.isNaN(value.getTime()) ||
    value.getFullYear() !== year ||
    value.getMonth() !== month - 1 ||
    value.getDate() !== day ||
    value.getHours() !== hour ||
    value.getMinutes() !== minute
  ) {
    return null;
  }
  return value;
}

/** 自定义区间只有两端都合法且 until>since 时才产生可提交值。 */
export function buildCustomRequestRange(sinceLocal: string, untilLocal: string): RequestRangeResult {
  const since = parseLocalDateTime(sinceLocal);
  const until = parseLocalDateTime(untilLocal);
  if (since === null || until === null) {
    return { ok: false, error: "请完整填写有效的起始与结束时间" };
  }
  if (until.getTime() <= since.getTime()) {
    return { ok: false, error: "结束时间必须晚于起始时间" };
  }
  return { ok: true, range: { since: since.toISOString(), until: until.toISOString() } };
}

export function toLocalDateTimeInput(rfc3339: string): string {
  if (rfc3339.trim() === "") return "";
  const value = new Date(rfc3339);
  if (Number.isNaN(value.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${value.getFullYear()}-${pad(value.getMonth() + 1)}-${pad(value.getDate())}T${pad(value.getHours())}:${pad(value.getMinutes())}`;
}

export function localToday(): string {
  const value = new Date();
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${value.getFullYear()}-${pad(value.getMonth() + 1)}-${pad(value.getDate())}`;
}
