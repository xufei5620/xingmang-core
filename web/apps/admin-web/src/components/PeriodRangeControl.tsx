import type { FinancePeriodMode } from "../lib/financeOverview";

const LABELS: Record<FinancePeriodMode, string> = { day: "日", week: "周", month: "月" };

export function PeriodRangeControl({
  date,
  mode,
  range,
  onDateChange,
  onModeChange,
}: {
  date: string;
  mode: FinancePeriodMode;
  range: { from: string; to: string };
  onDateChange: (value: string) => void;
  onModeChange: (value: FinancePeriodMode) => void;
}) {
  return (
    <section className="flex flex-wrap items-end gap-3 rounded-lg border border-edge bg-surface p-3" aria-label="统计区间">
      <div className="mr-auto flex min-w-44 flex-col gap-0.5">
        <h2 className="text-sm font-semibold text-fg">统计区间</h2>
        <p className="text-xs text-fg-muted">{range.from} 至 {range.to}</p>
      </div>
      <label className="flex flex-col gap-1 text-xs text-fg-muted">
        统计日期
        <input aria-label="统计日期" type="date" value={date} onChange={(event) => onDateChange(event.target.value)} className="h-9 rounded-md border border-edge-strong bg-surface px-2 text-sm text-fg outline-none focus-visible:outline-2 focus-visible:outline-accent" />
      </label>
      <div className="flex rounded-md border border-edge p-0.5" role="group" aria-label="统计模式">
        {(Object.keys(LABELS) as FinancePeriodMode[]).map((value) => (
          <button key={value} type="button" aria-pressed={mode === value} onClick={() => onModeChange(value)} className="min-h-8 rounded-sm px-3 text-sm text-fg hover:bg-surface-muted focus-visible:outline-2 focus-visible:outline-accent aria-pressed:bg-accent/10 aria-pressed:text-accent">
            {LABELS[value]}
          </button>
        ))}
      </div>
    </section>
  );
}
