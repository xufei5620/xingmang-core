import { useMemo } from "react";
import { useSearchParams } from "react-router";

import type { ChannelSummary } from "../api/finance";
import type { MetricItem } from "../api/platform";
import { appDemoDataConfig, shouldShowDemoBanner } from "../lib/demoData";
import { periodRangeFor, type FinancePeriodMode } from "../lib/financeOverview";
import { parseBusinessDay, parseGranularity } from "../lib/period";

/** 平台财务子页签之间共用的那几件东西。
 *
 *  它存在的理由只有一个：`NewApiFinanceOverview`（资金与订单）与
 *  `ChannelProfitView`（利润核算）都要用这里的四样，而让其中一方去 import
 *  另一方会形成模块环。环今天能跑，靠的是「这几个都是提升的函数声明、且只在
 *  render 期调用」——那是**实现细节在兜底**，不是设计成立：谁哪天把
 *  `DemoBanner` 改成 `const DemoBanner = () => …`，或者打包器换个求值顺序，
 *  它就在运行时炸，而且炸在一个跟改动看起来毫不相干的地方。
 *
 *  所以放这里。这个文件只收**确实被两边共用**的东西——只服务单一调用方的
 *  辅助不要搬进来，那会把它变成又一个什么都能装的 `utils`。
 *
 *  ⚠️ `businessTodayDateOnly` 在 `Sub2ApiOrdersPanel.tsx` 里还有一份同样实现
 *  的导出（`Sub2ApiRefundsPanel` 与 `pages/FinancePage` 从那边取）。合并那一份
 *  会牵动 `components/` 以外的文件，不在本片范围内。 */

export function validDateOnly(value: string): boolean {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return false;
  const date = new Date(`${value}T00:00:00Z`);
  return !Number.isNaN(date.getTime()) && date.toISOString().slice(0, 10) === value;
}

export function businessTodayDateOnly(): string {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: "Asia/Shanghai",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(new Date());
  const values = Object.fromEntries(parts.map((part) => [part.type, part.value]));
  return `${values.year ?? "1970"}-${values.month ?? "01"}-${values.day ?? "01"}`;
}

export interface FinancePeriodState {
  day: string;
  mode: FinancePeriodMode;
  range: { from: string; to: string };
  setDay: (value: string) => void;
  setMode: (value: FinancePeriodMode) => void;
}

/**
 * Keep the period shareable in the URL, matching the user-management pages.
 * An omitted day is interpreted as the current China business day for the
 * display/query boundary; it is not derived from the browser's local zone.
 */
export function useFinancePeriod(initialDate: string): FinancePeriodState {
  const [searchParams, setSearchParams] = useSearchParams();
  const fallback = validDateOnly(initialDate) ? initialDate : businessTodayDateOnly();
  const parsed = parseBusinessDay(searchParams.get("day"));
  const day = parsed || fallback;
  const mode = parseGranularity(searchParams.get("granularity"));
  const range = useMemo(() => periodRangeFor(day, mode), [day, mode]);

  const setParam = (key: string, value: string) => {
    const next = new URLSearchParams(searchParams);
    if (value.trim() === "") next.delete(key);
    else next.set(key, value);
    setSearchParams(next, { replace: true });
  };

  return {
    day,
    mode,
    range,
    setDay: (value) => setParam("day", validDateOnly(value) ? value : ""),
    setMode: (value) => setParam("granularity", value),
  };
}

/** 演示数据横幅。「资金与订单」与「利润核算」共用。
 *
 *  `platformName` 是必填而不是默认 "NewAPI"：这句话点名让人去哪里换掉 Fake
 *  连接器，说错平台名会把人指到另一个平台的凭据页去。 */
export function DemoBanner({
  metrics,
  channels,
  platformName,
}: {
  metrics: readonly MetricItem[];
  channels: readonly ChannelSummary[];
  platformName: string;
}) {
  const sources = [
    ...metrics.map((item) => item.source),
    ...channels.map((item) => item.observed.source),
  ].filter(Boolean);
  if (!shouldShowDemoBanner(sources, appDemoDataConfig)) return null;
  return (
    <p
      role="status"
      className="rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg"
    >
      {`当前展示的是演示数据（Fake 连接器），非真实运营数据；请在「连接与凭据」配置真实 ${platformName} 实例。`}
    </p>
  );
}

/** 刷新失败但保留上次成功数据的提示。「资金与订单」与「利润核算」共用。 */
export function RefreshErrorNotice({ label, error, onRetry }: { label: string; error: unknown; onRetry: () => void }) {
  const message = error instanceof Error ? error.message : "暂时无法读取最新数据";
  return (
    <div role="alert" className="flex flex-wrap items-center gap-2 rounded-md border border-danger bg-danger/10 px-3 py-2 text-xs text-danger">
      <span>{label}刷新失败：{message}；页面保留上一次成功数据。</span>
      <button type="button" onClick={onRetry} className="font-medium underline underline-offset-2 focus-visible:outline-2 focus-visible:outline-accent">
        重试
      </button>
    </div>
  );
}
