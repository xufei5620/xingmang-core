import { Button, Input } from "@xingmang/ui-primitives";
import { useEffect, useState } from "react";
import {
  buildCustomRequestRange,
  buildRequestPresetRange,
  localToday,
  toLocalDateTimeInput,
  type RequestPeriodMode,
  type RequestTimeRange,
} from "../lib/requestPeriod";

const PERIOD_OPTIONS: readonly { value: RequestPeriodMode; label: string }[] = [
  { value: "day", label: "日" },
  { value: "week", label: "周" },
  { value: "month", label: "月" },
  { value: "custom", label: "自定义" },
];

export function RequestPeriodControl({
  mode,
  since,
  until,
  onModeChange,
  onApply,
  onClear,
}: {
  mode: RequestPeriodMode;
  since: string;
  until: string;
  onModeChange: (mode: RequestPeriodMode) => void;
  onApply: (mode: RequestPeriodMode, range: RequestTimeRange) => void;
  onClear: () => void;
}) {
  const [anchor, setAnchor] = useState(() => toLocalDateTimeInput(since).slice(0, 10) || localToday());
  const [customSince, setCustomSince] = useState(() => toLocalDateTimeInput(since));
  const [customUntil, setCustomUntil] = useState(() => toLocalDateTimeInput(until));
  const [error, setError] = useState("");
  const shownSince = toLocalDateTimeInput(since);
  const shownUntil = toLocalDateTimeInput(until);
  const rangeDescription = shownSince
    ? shownUntil
      ? `${shownSince} → ${shownUntil}`
      : `自 ${shownSince} 起`
    : shownUntil
      ? `截至 ${shownUntil}`
      : "尚未限定，覆盖保留期内全部记录";

  useEffect(() => {
    const nextSince = toLocalDateTimeInput(since);
    const nextUntil = toLocalDateTimeInput(until);
    setCustomSince(nextSince);
    setCustomUntil(nextUntil);
    if (nextSince) setAnchor(nextSince.slice(0, 10));
  }, [since, until]);

  const applyPreset = (nextMode: Exclude<RequestPeriodMode, "custom">) => {
    const result = buildRequestPresetRange(nextMode, anchor);
    if (!result.ok) {
      setError(result.error);
      return;
    }
    setError("");
    onApply(nextMode, result.range);
  };

  const applyCustom = () => {
    const result = buildCustomRequestRange(customSince, customUntil);
    if (!result.ok) {
      setError(result.error);
      return;
    }
    setError("");
    onApply("custom", result.range);
  };

  return (
    <section className="rounded-lg border border-edge bg-surface p-3" aria-labelledby="request-period-title">
      <div className="flex flex-wrap items-end gap-3">
        <div className="mr-auto min-w-48">
          <h2 id="request-period-title" className="text-sm font-semibold text-fg">
            统计区间
          </h2>
          <p className="text-xs text-fg-muted">{rangeDescription}</p>
        </div>

        <div role="group" aria-label="请求统计区间模式" className="flex flex-wrap gap-1">
          {PERIOD_OPTIONS.map((option) => {
            const active = option.value === mode;
            return (
              <button
                key={option.value}
                type="button"
                aria-pressed={active}
                onClick={() => {
                  onModeChange(option.value);
                  if (option.value !== "custom") applyPreset(option.value);
                }}
                className={
                  active
                    ? "rounded-md border border-accent bg-accent-soft px-3 py-2 text-xs font-medium text-accent"
                    : "rounded-md border border-edge px-3 py-2 text-xs text-fg-muted hover:bg-surface-muted focus:outline-2 focus:outline-accent"
                }
              >
                {option.label}
              </button>
            );
          })}
        </div>

        {mode === "custom" ? (
          <>
            <label className="flex flex-col gap-1 text-xs text-fg-muted">
              <span>起始时间（含）</span>
              <Input
                type="datetime-local"
                value={customSince}
                onChange={(event) => setCustomSince(event.target.value)}
                className="w-52"
              />
            </label>
            <label className="flex flex-col gap-1 text-xs text-fg-muted">
              <span>结束时间（不含）</span>
              <Input
                type="datetime-local"
                value={customUntil}
                onChange={(event) => setCustomUntil(event.target.value)}
                className="w-52"
              />
            </label>
            <Button type="button" variant="secondary" size="sm" onClick={applyCustom}>
              应用区间
            </Button>
          </>
        ) : (
          <>
            <label className="flex flex-col gap-1 text-xs text-fg-muted">
              <span>统计日期</span>
              <Input
                type="date"
                value={anchor}
                onChange={(event) => setAnchor(event.target.value)}
                className="w-40"
              />
            </label>
            <Button type="button" variant="secondary" size="sm" onClick={() => applyPreset(mode)}>
              应用区间
            </Button>
          </>
        )}
        {since || until ? (
          <Button type="button" variant="secondary" size="sm" onClick={onClear}>
            清除区间
          </Button>
        ) : null}
      </div>
      {error ? (
        <p role="alert" className="mt-2 text-xs text-danger">
          {error}；原查询区间未改变。
        </p>
      ) : null}
    </section>
  );
}
