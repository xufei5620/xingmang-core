import { useQuery } from "@tanstack/react-query";
import { PageState, PeriodControls } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { useMemo, useState, type ReactNode } from "react";

import { listChannelSummaries, type ChannelSummary } from "../api/finance";
import { formatScaledMinorUnits } from "../lib/money";
import { ApiStateView } from "./ApiStateView";
import { DemoBanner, RefreshErrorNotice, useFinancePeriod } from "./financeShared";

/** 逐渠道的利润核算表（我方计费 / 上游成本 / 毛利 / 毛利率）。
 *
 *  Sub2API 与 NewAPI 的这一格是**同一张表**：端点
 *  `GET /api/v1/finance/channels/summary` 无条件挂载，一次返回两个平台的行，
 *  用 `system_type` 区分。所以这里没有「两套实现」，只有一个平台参数——
 *  两边分头写会让口径悄悄漂开，而漂开的是钱。
 *
 *  这个文件的全部内容原本长在 `NewApiFinanceOverview.tsx` 里（`ProfitView`
 *  及只服务于它的辅助）。搬过来的东西一行没改，改的只有平台名的来源。
 *  `useFinancePeriod` / `DemoBanner` / `RefreshErrorNotice` 是与 NewAPI
 *  「资金与订单」共用的，在 `financeShared.tsx`——两边都从那里取，
 *  谁也不 import 谁。 */

/** 这一格支持的平台。**不是**全部平台：CPA 与服务器没有上游账号，
 *  给它们挂一张恒为空的利润表等于把「这里本来就没有这个概念」显示成缺口。 */
export type ChannelProfitPlatform = "sub2api" | "newapi";

/** 平台文案表。
 *
 *  两个平台的文案差别只有一个平台名，但它出现在**六处**（说明、刷新失败
 *  标签、表 aria-label、空态标题、空态说明、演示横幅）。散成一串三元的话，
 *  下一次加平台时漏掉其中一处不会报错，只会让某一行文案说着另一个平台。 */
const PLATFORM_NAME: Record<ChannelProfitPlatform, string> = {
  sub2api: "Sub2API",
  newapi: "NewAPI",
};

interface LedgerFilter {
  id: string;
  label: string;
  options: readonly string[];
}

interface LedgerColumn<T> {
  id: string;
  label: string;
  numeric?: boolean;
  value: (row: T) => string;
  cell: (row: T) => ReactNode;
}

/**
 * 财务表的轻量交互壳。
 *
 * 筛选刻意先渲染，搜索放在最后；这是原型约定，也让窄屏下的操作顺序稳定。
 * 它不制造分页数据，订单端点尚未接入时仍保留完整表头与空态。
 */
function InteractiveLedgerTable<T>({
  title,
  tableLabel,
  columns,
  rows,
  filters,
  emptyTitle,
  emptyDescription,
}: {
  title: string;
  tableLabel: string;
  columns: readonly LedgerColumn<T>[];
  rows: readonly T[];
  filters: readonly LedgerFilter[];
  emptyTitle: string;
  emptyDescription: string;
}) {
  const [query, setQuery] = useState("");
  const [filterValues, setFilterValues] = useState<Record<string, string>>({});
  const normalizedQuery = query.trim().toLocaleLowerCase("zh-CN");
  const visibleRows = useMemo(
    () =>
      rows.filter((row) => {
        const matchesSearch =
          normalizedQuery === "" ||
          columns.some((column) => column.value(row).toLocaleLowerCase("zh-CN").includes(normalizedQuery));
        const matchesFilters = filters.every((filter) => {
          const selected = filterValues[filter.id] ?? "";
          return selected === "" || filterValuesForRow(filter.id, row, columns, selected);
        });
        return matchesSearch && matchesFilters;
      }),
    [columns, filterValues, filters, normalizedQuery, rows],
  );
  const hasCriteria = normalizedQuery !== "" || Object.values(filterValues).some(Boolean);

  return (
    <section className="overflow-hidden rounded-lg border border-edge bg-surface shadow-sm" aria-label={title}>
      <header className="border-b border-edge px-4 py-3">
        <h2 className="text-base font-semibold text-fg">{title}</h2>
        <p className="text-xs text-fg-muted">金额只展示已接入的来源；未知、缺失或未接入不会被折算成 0。</p>
      </header>
      <div
        role="toolbar"
        aria-label={`${title}筛选与搜索`}
        className="flex flex-wrap items-end gap-2 border-b border-edge bg-surface-muted px-3 py-2"
      >
        {filters.map((filter) => (
          <label key={filter.id} className="flex min-w-28 flex-col gap-1 text-xs text-fg-muted">
            <span>{filter.label}</span>
            <select
              aria-label={`${filter.label}筛选`}
              value={filterValues[filter.id] ?? ""}
              onChange={(event) =>
                setFilterValues((previous) => ({ ...previous, [filter.id]: event.target.value }))
              }
              className="min-h-9 rounded-md border border-edge-strong bg-surface px-2 py-1 text-xs text-fg outline-none focus-visible:outline-2 focus-visible:outline-accent"
            >
              <option value="">全部</option>
              {filter.options.map((option) => (
                <option key={option} value={option}>
                  {option}
                </option>
              ))}
            </select>
          </label>
        ))}
        <label className="ml-auto flex min-w-44 flex-col gap-1 text-xs text-fg-muted">
          <span>搜索</span>
          <input
            type="search"
            aria-label={`搜索${title}`}
            placeholder="搜索订单号 / 用户 / 来源"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            className="h-9 w-52 rounded-md border border-edge-strong bg-surface px-2 text-xs text-fg outline-none placeholder:text-fg-muted focus-visible:outline-2 focus-visible:outline-accent"
          />
        </label>
        {hasCriteria ? (
          <button
            type="button"
            onClick={() => {
              setQuery("");
              setFilterValues({});
            }}
            className="min-h-9 rounded-md border border-edge-strong px-2 py-1 text-xs text-fg-muted hover:bg-surface focus-visible:outline-2 focus-visible:outline-accent"
          >
            清除条件
          </button>
        ) : null}
      </div>
      <div className="overflow-x-auto">
        <table className="w-full min-w-[760px] text-left text-sm" aria-label={tableLabel}>
          <caption className="sr-only">{tableLabel}</caption>
          <thead className="bg-surface-muted text-xs text-fg-muted">
            <tr>
              {columns.map((column) => (
                <th key={column.id} scope="col" className={`whitespace-nowrap px-3 py-2 font-medium ${column.numeric ? "text-right" : ""}`}>
                  {column.label}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {visibleRows.length > 0 ? (
              visibleRows.map((row, index) => (
                <tr key={rowKeyFor(row, index)} className="border-t border-edge">
                  {columns.map((column) => (
                    <td key={column.id} className={`px-3 py-2 ${column.numeric ? "text-right tabular-nums" : ""}`}>
                      {column.cell(row)}
                    </td>
                  ))}
                </tr>
              ))
            ) : (
              <tr>
                <td colSpan={columns.length} className="p-3">
                  <PageState
                    kind={rows.length === 0 ? "unavailable" : "empty"}
                    compact
                    title={rows.length === 0 ? emptyTitle : "没有匹配的记录"}
                    description={rows.length === 0 ? emptyDescription : "请调整筛选或搜索条件后再试。"}
                  />
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      <p className="border-t border-edge px-3 py-2 text-xs text-fg-muted" aria-live="polite">
        {rows.length === 0 ? "暂无可展示记录；空态不代表金额为 0。" : `显示 ${visibleRows.length} / ${rows.length} 条`}
      </p>
    </section>
  );
}

function rowKeyFor<T>(row: T, index: number): string {
  if (typeof row === "object" && row !== null) {
    const candidate = (row as { id?: unknown }).id;
    if (typeof candidate === "string" && candidate) return candidate;
  }
  return String(index);
}

/** Filters are intentionally represented by visible column values. */
function filterValuesForRow<T>(
  id: string,
  row: T,
  columns: readonly LedgerColumn<T>[],
  selected: string,
): boolean {
  const column = columns.find((candidate) => candidate.id === id);
  return column ? column.value(row).toLocaleLowerCase("zh-CN").includes(selected.toLocaleLowerCase("zh-CN")) : true;
}

function hostOf(baseUrl: string): string {
  if (!baseUrl) return "来源未声明";
  try {
    return new URL(baseUrl).host || baseUrl;
  } catch {
    return baseUrl;
  }
}

function accessMethodText(method: string): string {
  switch (method) {
    case "upstream_key":
      return "上游 Key";
    case "subscription_account":
      return "订阅账号";
    case "official_api":
      return "官方 API";
    default:
      return method || "接入方式未知";
  }
}

function statusText(status: string): string {
  switch (status) {
    case "active":
      return "正常";
    case "disabled":
    case "retired":
      return "已停用";
    default:
      return status || "状态未知";
  }
}

function amountCell(value: ChannelSummary["usageRevenue"] | ChannelSummary["supplyCost"] | ChannelSummary["grossProfit"]) {
  if (!value) return <span className="text-fg-muted">—</span>;
  return <span>{formatScaled(value)}</span>;
}

function formatScaled(value: NonNullable<ChannelSummary["usageRevenue"]>): string {
  // 这里统一走 scale-aware formatter；财务台账金额是 scale-6 微单位，不能按
  // 普通货币最小单位直接除 100。
  return formatScaledMinorUnits(value.amountMinor, value.currency, value.scale);
}

const PROFIT_COLUMNS: readonly LedgerColumn<ChannelSummary>[] = [
  {
    id: "channel",
    label: "渠道",
    value: (row) => `${row.name} ${row.id}`,
    cell: (row) => (
      <span>
        <strong className="font-medium">{row.name || "未命名渠道"}</strong>
        <span className="block font-mono text-xs text-fg-muted">{row.id}</span>
      </span>
    ),
  },
  {
    id: "upstream",
    label: "上游",
    value: (row) => `${hostOf(row.baseUrl)} ${row.observed.source}`,
    cell: (row) => (
      <span>
        <span>{hostOf(row.baseUrl)}</span>
        <span className="block text-xs text-fg-muted">来源 {row.observed.source || "未声明"}</span>
      </span>
    ),
  },
  {
    id: "group",
    label: "分组 / 倍率",
    value: (row) => `${row.groupRate ?? ""} ${accessMethodText(row.accessMethod)}`,
    cell: (row) => (
      <span>
        <span className="font-medium">{row.groupRate ? `${row.groupRate}×` : "未配置倍率"}</span>
        <span className="block text-xs text-fg-muted">{accessMethodText(row.accessMethod)}</span>
      </span>
    ),
  },
  {
    id: "revenue",
    label: "我方计费",
    numeric: true,
    value: (row) => row.usageRevenue?.amountMinor ?? "",
    cell: (row) => amountCell(row.usageRevenue),
  },
  {
    id: "cost",
    label: "上游成本",
    numeric: true,
    value: (row) => row.supplyCost?.amountMinor ?? "",
    cell: (row) => amountCell(row.supplyCost),
  },
  {
    id: "profit",
    label: "毛利",
    numeric: true,
    value: (row) => row.grossProfit?.amountMinor ?? "",
    cell: (row) => amountCell(row.grossProfit),
  },
  {
    id: "margin",
    label: "毛利率",
    numeric: true,
    value: (row) => row.grossMargin ?? "",
    cell: (row) => (row.grossMargin ? formatMargin(row.grossMargin) : <span className="text-fg-muted">—</span>),
  },
  {
    id: "status",
    label: "状态",
    value: (row) => statusText(row.status),
    cell: (row) => <Badge tone={row.status === "active" ? "success" : row.status === "retired" ? "neutral" : "warning"}>{statusText(row.status)}</Badge>,
  },
];

function formatMargin(value: string): string {
  const match = /^(-?)(\d+)(?:\.(\d+))?$/.exec(value.trim());
  if (!match) return "毛利率未知";
  const [, sign, whole = "0", fraction = ""] = match;
  const digits = `${whole}${fraction}`;
  const point = whole.length + 2;
  const padded = digits.padEnd(Math.max(point + 2, digits.length), "0");
  const integer = padded.slice(0, point).replace(/^0+(?=\d)/, "");
  const decimal = padded.slice(point, point + 2).padEnd(2, "0");
  return `${sign}${integer}.${decimal}%`;
}

/** 平台参数化的「利润核算」子页签。
 *
 *  `initialDate` 省略或不合法时由 `useFinancePeriod` 自己回落到当前业务日
 *  （它内部就有这条回落），所以这里不再重复算一次「今天」。 */
export function ChannelProfitView({
  platform,
  initialDate = "",
}: {
  platform: ChannelProfitPlatform;
  initialDate?: string;
}) {
  const platformName = PLATFORM_NAME[platform];
  const { day, mode, range, setDay, setMode } = useFinancePeriod(initialDate);
  const summaryQuery = useQuery({
    queryKey: ["finance", "channels", "summary", range.from, range.to],
    queryFn: ({ signal }) => listChannelSummaries({ ...range, signal }),
  });
  // 端点一次返回两个平台的行；这一步是这一格唯一的平台差异所在。
  const rows = (summaryQuery.data?.items ?? []).filter((item) => item.systemType === platform);
  const upstreamOptions = [...new Set(rows.map((row) => hostOf(row.baseUrl)).filter(Boolean))].sort();
  const accessOptions = [...new Set(rows.map((row) => accessMethodText(row.accessMethod)).filter(Boolean))].sort();
  const statusOptions = [...new Set(rows.map((row) => statusText(row.status)).filter(Boolean))];

  return (
    <div className="flex flex-col gap-4">
      <p className="text-xs text-fg-muted">{`按 ${platformName} 渠道逐行核算我方计费、上游成本、毛利与毛利率；同一上游下的多个渠道不会在这里合并。`}</p>
      <PeriodControls
        day={day}
        granularity={mode}
        period={{ day, granularity: mode, ...range }}
        dateLabel="统计日期"
        dateAriaLabel="统计日期"
        granularityAriaLabel="统计模式"
        onDayChange={setDay}
        onGranularityChange={setMode}
      />
      <ApiStateView
        isPending={summaryQuery.isPending}
        error={summaryQuery.error && !summaryQuery.isRefetchError ? summaryQuery.error : null}
        onRetry={() => void summaryQuery.refetch()}
      >
        {summaryQuery.error && summaryQuery.isRefetchError ? (
          <RefreshErrorNotice label={`${platformName} 渠道利润`} error={summaryQuery.error} onRetry={() => void summaryQuery.refetch()} />
        ) : null}
        <DemoBanner metrics={[]} channels={rows} platformName={platformName} />
        <InteractiveLedgerTable<ChannelSummary>
          title="利润核算明细"
          tableLabel={`${platformName} 渠道利润核算`}
          columns={PROFIT_COLUMNS}
          rows={rows}
          filters={[
            { id: "upstream", label: "上游", options: upstreamOptions },
            { id: "group", label: "接入方式", options: accessOptions },
            { id: "status", label: "状态", options: statusOptions.length > 0 ? statusOptions : ["正常", "已停用"] },
          ]}
          emptyTitle={`暂无 ${platformName} 利润明细`}
          emptyDescription={`finance/channels/summary 已读取，但当前统计区间没有可归属的 ${platformName} 渠道；这不等于利润为 0。`}
        />
      </ApiStateView>
    </div>
  );
}
