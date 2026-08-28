import type { ChannelSummary, Money } from "../api/finance";
import type { FreshnessContract } from "@xingmang/ui-admin";

export type FinancePeriodMode = "day" | "week" | "month";

export interface DateOnlyRange {
  from: string;
  to: string;
}

/** Date-only values are parsed in UTC fields so browser timezone offsets cannot move a business date. */
function dateOnly(value: string): Date | null {
  const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
  if (!match) return null;
  const date = new Date(Date.UTC(Number(match[1]), Number(match[2]) - 1, Number(match[3])));
  return date.getUTCFullYear() === Number(match[1]) && date.getUTCMonth() === Number(match[2]) - 1 && date.getUTCDate() === Number(match[3]) ? date : null;
}

function dateText(date: Date): string {
  return `${date.getUTCFullYear().toString().padStart(4, "0")}-${(date.getUTCMonth() + 1).toString().padStart(2, "0")}-${date.getUTCDate().toString().padStart(2, "0")}`;
}

/** Stable calendar ranges for Query inputs; a week always begins on Monday. */
export function periodRangeFor(dateTextValue: string, mode: FinancePeriodMode): DateOnlyRange {
  const selected = dateOnly(dateTextValue);
  if (!selected) throw new Error(`invalid date-only value: ${dateTextValue}`);
  if (mode === "day") return { from: dateTextValue, to: dateTextValue };
  if (mode === "month") {
    return {
      from: dateText(new Date(Date.UTC(selected.getUTCFullYear(), selected.getUTCMonth(), 1))),
      to: dateText(new Date(Date.UTC(selected.getUTCFullYear(), selected.getUTCMonth() + 1, 0))),
    };
  }
  const weekday = selected.getUTCDay() || 7;
  const monday = new Date(selected);
  monday.setUTCDate(selected.getUTCDate() - weekday + 1);
  const sunday = new Date(monday);
  sunday.setUTCDate(monday.getUTCDate() + 6);
  return { from: dateText(monday), to: dateText(sunday) };
}

export type ChannelMoneyField = "usageRevenue" | "supplyCost" | "grossProfit";

export interface ChannelMoneyAggregate {
  money: Money | null;
  contributingRows: number;
  coverage: { completeRows: number; totalRows: number };
  oldestObservedAt: string | null;
  source: string;
  failureReasons: AggregationFailure[];
}

export type AggregationFailure =
  | "empty"
  | "missing-money"
  | "invalid-money"
  | "currency-mismatch"
  | "scale-mismatch"
  | "missing-observation"
  | "invalid-observation";

const MAX_MONEY_SCALE = 18;

/** Shared money policy for every channel-summary aggregate: integer strings only, one currency, one scale. */
export function sumMoneyValues(values: (Money | null)[]): Money | null {
  if (values.length === 0) return null;
  let amount = 0n;
  let currency = "";
  let scale: number | null = null;
  for (const value of values) {
    if (!value || !/^-?\d+$/.test(value.amountMinor) || !value.currency || !Number.isInteger(value.scale) || value.scale < 0 || value.scale > MAX_MONEY_SCALE) return null;
    if (scale === null) { currency = value.currency; scale = value.scale; }
    else if (currency !== value.currency || scale !== value.scale) return null;
    amount += BigInt(value.amountMinor);
  }
  return { amountMinor: amount.toString(), currency, scale: scale ?? 0 };
}

/**
 * Integer-only aggregation for a platform window. Any malformed amount, missing value,
 * currency mismatch, or scale mismatch fails closed rather than publishing a plausible sum.
 */
export function aggregateChannelMoney(
  items: ChannelSummary[],
  field: ChannelMoneyField,
  systemType = "sub2api",
): ChannelMoneyAggregate {
  const rows = items.filter((item) => item.systemType === systemType);
  const coverage = { completeRows: rows.filter((item) => item.coverage.complete).length, totalRows: rows.length };
  const source = [...new Set(rows.map((item) => item.observed.source).filter(Boolean))].join(" · ");
  const moneyValues = rows.map((row) => row[field]);
  const moneyFailure = aggregateMoneyFailure(moneyValues);
  const observationFailure = aggregateObservationFailure(rows);
  const failureReasons = [moneyFailure, observationFailure].filter(
    (value): value is AggregationFailure => value !== null,
  );
  const oldestObservation = rows.reduce<{ text: string; instant: number } | null>((oldest, row) => {
    const text = row.observed.updatedAt;
    if (!text) return oldest;
    const instant = Date.parse(text);
    return oldest === null || instant < oldest.instant ? { text, instant } : oldest;
  }, null);
  const base = {
    contributingRows: rows.length,
    coverage,
    oldestObservedAt: observationFailure ? null : oldestObservation?.text ?? null,
    source,
    failureReasons,
  };
  if (rows.length === 0) return { ...base, money: null };

  return { ...base, money: moneyFailure ? null : sumMoneyValues(moneyValues) };
}

function aggregateMoneyFailure(values: (Money | null)[]): AggregationFailure | null {
  if (values.length === 0) return "empty";
  let currency = "";
  let scale: number | null = null;
  for (const value of values) {
    if (!value) return "missing-money";
    if (!/^-?\d+$/.test(value.amountMinor) || !value.currency || !Number.isInteger(value.scale) || value.scale < 0 || value.scale > MAX_MONEY_SCALE) return "invalid-money";
    if (scale === null) { currency = value.currency; scale = value.scale; }
    else if (currency !== value.currency) return "currency-mismatch";
    else if (scale !== value.scale) return "scale-mismatch";
  }
  return null;
}

function aggregateObservationFailure(rows: ChannelSummary[]): AggregationFailure | null {
  for (const row of rows) {
    const observedAt = row.observed.updatedAt;
    if (!observedAt) return "missing-observation";
    if (Number.isNaN(Date.parse(observedAt))) return "invalid-observation";
  }
  return null;
}

/** A value or its freshness is only trustworthy when every contributing row is valid. */
export function aggregateFreshness(aggregate: ChannelMoneyAggregate, now: number): FreshnessContract {
  const complete = aggregate.coverage.totalRows > 0 && aggregate.coverage.completeRows === aggregate.coverage.totalRows;
  const observedAt = aggregate.oldestObservedAt;
  if (!aggregate.money || aggregate.failureReasons.length > 0 || !observedAt) {
    return {
      state: "uninitialized",
      threshold_seconds: 1800,
      staleness_seconds: null,
      is_partial: true,
      observed_at: null,
      last_success: null,
      last_error_code: aggregate.failureReasons[0] ?? "aggregate-unavailable",
    };
  }
  const seconds = Math.max(0, Math.round((now - Date.parse(observedAt)) / 1000));
  return {
    state: complete ? (seconds >= 1800 ? "stale" : "fresh") : "partial",
    threshold_seconds: 1800,
    staleness_seconds: seconds,
    is_partial: !complete,
    observed_at: observedAt,
    last_success: observedAt,
    last_error_code: "",
  };
}

export function aggregateFailureText(aggregate: ChannelMoneyAggregate): string | null {
  const reason = aggregate.failureReasons[0];
  switch (reason) {
    case "empty": return "没有可汇总的 Sub2API 渠道";
    case "missing-money": return "金额缺失";
    case "invalid-money": return "金额或标度无效";
    case "currency-mismatch": return "币种不一致";
    case "scale-mismatch": return "金额标度不一致";
    case "missing-observation": return "缺少观测时间";
    case "invalid-observation": return "观测时间无效";
    default: return null;
  }
}
