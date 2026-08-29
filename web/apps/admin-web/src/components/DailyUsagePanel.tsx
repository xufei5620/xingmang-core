import { useQuery } from "@tanstack/react-query";
import { FreshnessBadge, FreshnessNote, PageState } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { listPlatformUserDailyUsage } from "../api/users";
import { appDemoDataConfig, DEMO_BANNER_TEXT, shouldShowDemoBanner } from "../lib/demoData";
import { formatMinorUnits, toIntegerValue } from "../lib/money";
import { ApiStateView } from "./ApiStateView";

function amountText(minorUnits: string | null, currency: string): string {
  return minorUnits === null ? "—" : formatMinorUnits(minorUnits, currency);
}

export function DailyUsagePanel({ platform, userId }: { platform: string; userId: string }) {
  const query = useQuery({
    queryKey: ["platform-user-daily-usage", platform, userId],
    queryFn: ({ signal }) => listPlatformUserDailyUsage(platform, userId, { days: 7, signal }),
    retry: false,
  });
  const series = query.data;
  const fake = shouldShowDemoBanner(series ? [series.snapshot.source] : [], appDemoDataConfig);
  // 金额值保持在 BigInt 域；这里只把已经压缩到 0..100 的比例转成 CSS
  // 百分比，避免把可能超过 JS 安全整数范围的最小单位金额转成 Float。
  const knownValues = (series?.points ?? [])
    .map((point) => point.consumed.minor_units === null ? null : toIntegerValue(point.consumed.minor_units))
    .filter((value): value is bigint => value !== null && value >= 0n);
  const max = knownValues.reduce((current, value) => value > current ? value : current, 1n);

  return (
    <section className="min-w-0 rounded-lg border border-edge bg-surface p-4 shadow-sm">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold text-fg">近 7 天消费趋势</h3>
          <p className="mt-1 text-xs leading-5 text-fg-muted">
            每日序列来自 platformusers 的独立 capability；已知 0 与缺失日分开显示，缺失日不会被补成 0。
          </p>
        </div>
        {series ? <FreshnessBadge freshness={{ state: series.snapshot.is_partial ? "partial" : "fresh", staleness_seconds: null, threshold_seconds: 60, is_partial: series.snapshot.is_partial, observed_at: series.snapshot.observed_at, last_success: series.snapshot.observed_at, last_error_code: "" }} /> : null}
      </div>

      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()} compact>
        {series ? (
          <div className="mt-3 flex flex-col gap-3">
            {fake ? (
              <p role="status" className="rounded-md border border-warning bg-warning/10 px-3 py-2 text-xs text-fg">
                {DEMO_BANNER_TEXT} · 趋势来源 {series.snapshot.source}
              </p>
            ) : null}
            <div className="flex flex-wrap items-center gap-2 text-xs text-fg-muted">
              <span>{series.from} ~ {series.to}</span>
              <Badge tone={series.coverage.complete ? "success" : "warning"}>
                覆盖 {series.coverage.covered_days} / {series.coverage.expected_days} 天
              </Badge>
              <span>{series.coverage.complete ? "序列完整" : "数据不完整，缺失日保持未知"}</span>
            </div>
            <div className="overflow-x-auto rounded-md border border-edge">
              <table className="w-full min-w-96 border-collapse text-xs">
                <caption className="sr-only">用户每日消费趋势</caption>
                <thead className="border-b border-edge bg-surface-muted">
                  <tr>
                    <th className="px-3 py-2 text-left font-medium text-fg-muted">业务日</th>
                    <th className="px-3 py-2 text-right font-medium text-fg-muted">消费</th>
                    <th className="px-3 py-2 text-right font-medium text-fg-muted">请求</th>
                    <th className="px-3 py-2 text-left font-medium text-fg-muted">相对量</th>
                  </tr>
                </thead>
                <tbody>
                  {series.points.map((point) => {
                    const value = point.consumed.minor_units === null ? null : toIntegerValue(point.consumed.minor_units);
                    const known = value !== null && value >= 0n;
                    const width = known ? Math.max(2, Number((value * 100n) / max)) : 0;
                    return (
                      <tr key={point.day} className="border-b border-edge last:border-b-0">
                        <td className="px-3 py-2 font-mono text-fg">{point.day}</td>
                        <td className="px-3 py-2 text-right tabular-nums text-fg">{amountText(point.consumed.minor_units, point.consumed.currency)}</td>
                        <td className="px-3 py-2 text-right tabular-nums text-fg">{point.requests.value === null ? "—" : point.requests.value}</td>
                        <td className="px-3 py-2">
                          {known ? <div className="h-2 min-w-24 rounded-full bg-accent-soft"><div className="h-2 rounded-full bg-accent" style={{ width: `${width}%` }} /></div> : <span className="text-fg-muted">未知</span>}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
            <FreshnessNote freshness={{ state: series.snapshot.is_partial ? "partial" : "fresh", staleness_seconds: null, threshold_seconds: 60, is_partial: series.snapshot.is_partial, observed_at: series.snapshot.observed_at, last_success: series.snapshot.observed_at, last_error_code: "" }} />
          </div>
        ) : null}
      </ApiStateView>
    </section>
  );
}
