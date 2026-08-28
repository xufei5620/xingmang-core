import { Input } from "@xingmang/ui-primitives";

/** Shared business-period vocabulary used by operator-facing pages. */
export type PeriodGranularity = "day" | "week" | "month";

/** Resolved closed business-day range supplied by the owning page.
 *
 * The component deliberately displays this value instead of deriving a
 * week/month itself. The owning contract decides whether the range is echoed by
 * the server or resolved before a query; timezone policy never lives here. */
export interface PeriodRange {
  day: string;
  granularity: PeriodGranularity;
  from: string;
  to: string;
}

export const GRANULARITY_OPTIONS: readonly {
  value: PeriodGranularity;
  label: string;
}[] = [
  { value: "day", label: "日" },
  { value: "week", label: "周" },
  { value: "month", label: "月" },
];

export function granularityLabel(granularity: PeriodGranularity): string {
  switch (granularity) {
    case "week":
      return "按周查看";
    case "month":
      return "按月查看";
    default:
      return "按日查看";
  }
}

/** Human-readable resolved range. Pending/missing evidence stays `—`. */
export function describePeriod(period: PeriodRange | undefined): string {
  if (period === undefined || period.day === "") return "—";
  const suffix = granularityLabel(period.granularity);
  if (period.granularity === "day" || period.from === period.to) {
    return `${period.day} · ${suffix}`;
  }
  if (period.from === "" || period.to === "") return `${period.day} · ${suffix}`;
  return `${period.from} ~ ${period.to} · ${suffix}`;
}

export interface PeriodControlsProps {
  /** Empty means "let the server choose its current business day". */
  day: string;
  granularity: PeriodGranularity;
  /** Exact range resolved by the owner; undefined while unavailable. */
  period: PeriodRange | undefined;
  onDayChange: (next: string) => void;
  onGranularityChange: (next: PeriodGranularity) => void;
  title?: string;
  dateLabel?: string;
  dateAriaLabel?: string;
  resetLabel?: string;
  granularityAriaLabel?: string;
}

/** Controlled period selector for financial and operational pages.
 *
 * It owns interaction, responsive layout and accessibility, but it does not
 * calculate dates, mutate URL state or fetch data. Those concerns remain with
 * the consuming page so this component can be reused without inventing a
 * second business-day implementation. */
export function PeriodControls({
  day,
  granularity,
  period,
  onDayChange,
  onGranularityChange,
  title = "统计区间",
  dateLabel = "日期",
  dateAriaLabel = `${title}的日期`,
  resetLabel = "回到今天",
  granularityAriaLabel = "统计粒度",
}: PeriodControlsProps) {
  return (
    <section className="rounded-lg border border-edge bg-surface p-3" aria-label={title}>
      <div className="flex flex-wrap items-center gap-2">
        <div className="mr-auto min-w-48">
          <b className="text-sm font-semibold text-fg">{title}</b>{" "}
          <span aria-live="polite" className="text-xs text-fg-muted tabular-nums">
            {describePeriod(period)}
          </span>
        </div>

        <label className="flex items-center gap-1.5 text-xs text-fg-muted">
          <span>{dateLabel}</span>
          <Input
            type="date"
            aria-label={dateAriaLabel}
            value={day}
            onChange={(event) => onDayChange(event.target.value)}
            className="w-40"
          />
        </label>

        {day === "" ? null : (
          <button
            type="button"
            onClick={() => onDayChange("")}
            className="min-h-9 rounded-md border border-edge px-2 py-1 text-xs text-fg-muted transition-colors hover:bg-surface-muted focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
          >
            {resetLabel}
          </button>
        )}

        <div role="group" aria-label={granularityAriaLabel} className="flex gap-1">
          {GRANULARITY_OPTIONS.map((option) => {
            const active = option.value === granularity;
            return (
              <button
                key={option.value}
                type="button"
                aria-pressed={active}
                onClick={() => onGranularityChange(option.value)}
                className={
                  active
                    ? "min-h-9 rounded-md border border-accent bg-accent-soft px-3 py-1 text-xs font-medium text-accent focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
                    : "min-h-9 rounded-md border border-edge px-3 py-1 text-xs text-fg-muted transition-colors hover:bg-surface-muted focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent"
                }
              >
                {option.label}
              </button>
            );
          })}
        </div>
      </div>
    </section>
  );
}
