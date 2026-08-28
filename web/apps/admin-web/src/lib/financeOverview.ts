import type { ChannelSummary, Money } from "../api/finance";

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
}

/** Shared money policy for every channel-summary aggregate: integer strings only, one currency, one scale. */
export function sumMoneyValues(values: (Money | null)[]): Money | null {
  if (values.length === 0) return null;
  let amount = 0n;
  let currency = "";
  let scale: number | null = null;
  for (const value of values) {
    if (!value || !/^-?\d+$/.test(value.amountMinor) || !value.currency || !Number.isInteger(value.scale) || value.scale < 0) return null;
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
  const observations = rows.map((item) => item.observed.updatedAt).filter((value): value is string => Boolean(value)).sort();
  const base = { contributingRows: rows.length, coverage, oldestObservedAt: observations[0] ?? null, source };
  if (rows.length === 0) return { ...base, money: null };

  return { ...base, money: sumMoneyValues(rows.map((row) => row[field])) };
}
