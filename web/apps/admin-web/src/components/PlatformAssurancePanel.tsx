import { useQuery } from "@tanstack/react-query";
import {
  DataTableV2,
  FreshnessBadge,
  FreshnessNote,
  PageState,
  StatTile,
  formatUtcTimestamp,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { useState, type ReactNode } from "react";
import {
  ASSURANCE_WINDOWS,
  ASSURANCE_WINDOW_LABELS,
  getPlatformAssuranceHistory,
  getPlatformAssuranceOverview,
  type AssuranceCoverage,
  type AssuranceHistory as AssuranceHistoryData,
  type AssuranceHistoryDay,
  type AssuranceLatencyPercentiles,
  type AssuranceModelRow,
  type AssuranceOverview as AssuranceOverviewData,
  type AssuranceStatusClasses,
  type AssuranceWindow,
} from "../api/assurance";
import { formatCount } from "../lib/money";
import { ApiStateView } from "./ApiStateView";

/** 渠道保障（原型孤儿页 `V["s2/model"]`，ADMIN-IA v3 §8.1 裁定 #1 恢复为页签）。
 *
 *  XM-ASSURE0 第一片把「保障概览」「历史记录」两个子页签接上**被动**指标
 *  ——从请求审计索引（reqlog）按窗口/按天聚合出的请求量、状态分类、延迟
 *  百分位、按模型拆分，数据源与「请求详情」页完全相同，只是这里读的是
 *  服务端聚合结果，不是逐条列表。
 *
 *  **不支持按渠道拆分**：请求审计的磁盘格式本身不采集渠道/上游字段——
 *  不是这一片没做，是这条数据源从写入那一刻起就没有这个维度（后端
 *  `connectors/reqlog` 的 `ChannelBreakdownUnsupportedReason`，服务端在每次
 *  响应里显式给出这句话，本文件原样转述，不重新编一遍）。
 *
 *  「检测任务」（主动探测）仍是纯 UI 蓝图——声明 → 探针 → 结论需要一个真正
 *  发起请求的 Action，且必须带 Kill Switch（宪法 26 条），设计与实现划给
 *  XM-ASSURE1，本片不冒充已上线。 */

// --- 共享格式化辅助（保障概览与历史记录两个子页签共用同一套口径） ---

function formatMs(v: number | null): string {
  return v === null ? "—" : `${formatCount(v)}ms`;
}

/** 成功率。total===0 时不是「0%」——那是「没有请求」，与「全部失败」
 *  是完全不同的两件事（与 lib/metrics.ts 的 renderSuccessRate24h 同一条
 *  纪律）。这里的除法只用于**展示**，不进入任何持久化或再计算，不受
 *  宪法 13 条（金额禁止 float）约束——那条铁律管的是货币，不是延迟/计数
 *  的展示态百分比。 */
function successRateText(c: AssuranceStatusClasses): string {
  const total = c.success + c.clientError + c.serverError + c.disconnected + c.other;
  if (total === 0) return "无请求";
  return `${((c.success / total) * 100).toFixed(1)}%`;
}

/** 失败构成的紧凑文案，只列非零项——4xx/5xx/连接中断三者需要运营采取的
 *  动作完全不同，合并成一个「失败」会抹掉这个区别（见 assurance.go 的
 *  StatusClassCounts 注释）。 */
function failureBreakdownText(c: AssuranceStatusClasses): string {
  const parts: string[] = [];
  if (c.clientError > 0) parts.push(`4xx ${formatCount(c.clientError)}`);
  if (c.serverError > 0) parts.push(`5xx ${formatCount(c.serverError)}`);
  if (c.disconnected > 0) parts.push(`连接中断 ${formatCount(c.disconnected)}`);
  if (c.other > 0) parts.push(`其他 ${formatCount(c.other)}`);
  return parts.length > 0 ? parts.join(" · ") : "无失败";
}

function percentileSummary(p: AssuranceLatencyPercentiles): string {
  if (p.sampleCount === 0) return "无样本";
  return `P50 ${formatMs(p.p50Ms)} · P95 ${formatMs(p.p95Ms)} · P99 ${formatMs(p.p99Ms)}`;
}

/** 「按渠道拆分」这条限制的说明条——保障概览与历史记录共用，文案取自
 *  服务端响应而不是本地硬编码第二份，避免两处各写一份、迟早对不上。 */
function ChannelBreakdownNotice({ reason }: { reason: string }) {
  return (
    <p
      role="status"
      className="flex flex-wrap items-center gap-2 rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted"
    >
      <Badge tone="neutral">按渠道拆分 · 未接入</Badge>
      <span>{reason}</span>
    </p>
  );
}

// --- 保障概览 ---

const ASSURANCE_WINDOW_OPTIONS = ASSURANCE_WINDOWS.map((value) => ({
  value,
  label: ASSURANCE_WINDOW_LABELS[value],
}));

function AssuranceWindowControl({
  value,
  onChange,
}: {
  value: AssuranceWindow;
  onChange: (next: AssuranceWindow) => void;
}) {
  return (
    <div role="group" aria-label="保障概览统计窗口" className="flex flex-wrap gap-1">
      {ASSURANCE_WINDOW_OPTIONS.map((option) => {
        const active = option.value === value;
        return (
          <button
            key={option.value}
            type="button"
            aria-pressed={active}
            onClick={() => onChange(option.value)}
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
  );
}

const MODEL_COLUMNS: DataTableColumn<AssuranceModelRow>[] = [
  {
    id: "model",
    header: "模型",
    primary: true,
    value: (m) => m.model,
    cell: (m) => (m.model ? m.model : <span className="text-fg-muted">(未知模型)</span>),
  },
  {
    id: "requests",
    header: "请求数",
    numeric: true,
    value: (m) => m.requestCount,
    cell: (m) => formatCount(m.requestCount),
  },
  {
    id: "success",
    header: "成功率",
    value: (m) => successRateText(m.statusClasses),
    cell: (m) => successRateText(m.statusClasses),
  },
  {
    id: "failures",
    header: "失败构成",
    cell: (m) => failureBreakdownText(m.statusClasses),
  },
  {
    id: "duration",
    header: "总耗时",
    headerTitle: "整次请求耗时的 P50/P95/P99",
    cell: (m) => percentileSummary(m.durationMs),
  },
  {
    id: "ttfb",
    header: "首字节",
    headerTitle: "只统计测量过首字节的请求；非流式请求通常没有这个值",
    cell: (m) =>
      m.ttfbMs.sampleCount > 0
        ? `${percentileSummary(m.ttfbMs)}（测得 ${formatCount(m.ttfbMs.sampleCount)}/${formatCount(m.requestCount)}）`
        : "未测量",
  },
];

function CoverageFooter({
  coverage,
  retentionDays,
  children,
}: {
  coverage: AssuranceCoverage;
  retentionDays: number;
  children: ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-center gap-2 text-xs text-fg-muted">
      {children}
      <span aria-hidden="true">·</span>
      <span>
        {coverage.missingDays > 0
          ? `索引覆盖不全：跨越 ${coverage.spannedDays} 天目录，其中 ${coverage.missingDays} 天缺失（记录代理未产生该日数据，或已过 ${retentionDays} 天保留期）`
          : `索引覆盖完整（保留期 ${retentionDays} 天）`}
      </span>
      {coverage.badLines > 0 ? <span>· 跳过 {formatCount(coverage.badLines)} 行坏索引</span> : null}
    </div>
  );
}

function AssuranceOverviewBody({ overview }: { overview: AssuranceOverviewData }) {
  return (
    <div className="flex flex-col gap-3">
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatTile
          label="请求量"
          value={formatCount(overview.requestCount)}
          note={`窗口 ${ASSURANCE_WINDOW_LABELS[overview.window]} · 截至 ${formatUtcTimestamp(overview.until)}`}
        />
        <StatTile
          label="成功率"
          value={successRateText(overview.statusClasses)}
          unavailable={overview.requestCount === 0}
          note={
            overview.requestCount === 0
              ? "窗口内没有请求"
              : `失败构成：${failureBreakdownText(overview.statusClasses)}`
          }
        />
        <StatTile
          label="总耗时"
          value={overview.durationMs.sampleCount > 0 ? `P50 ${formatMs(overview.durationMs.p50Ms)}` : "—"}
          unavailable={overview.durationMs.sampleCount === 0}
          note={
            overview.durationMs.sampleCount > 0
              ? `P95 ${formatMs(overview.durationMs.p95Ms)} · P99 ${formatMs(overview.durationMs.p99Ms)}`
              : "窗口内无请求"
          }
        />
        <StatTile
          label="首字节耗时"
          value={overview.ttfbMs.sampleCount > 0 ? `P50 ${formatMs(overview.ttfbMs.p50Ms)}` : "—"}
          unavailable={overview.ttfbMs.sampleCount === 0}
          note={
            overview.ttfbMs.sampleCount > 0
              ? `P95 ${formatMs(overview.ttfbMs.p95Ms)} · 已测量 ${formatCount(overview.ttfbMs.sampleCount)}/${formatCount(overview.requestCount)}`
              : "本窗口没有测得首字节的记录（通常是非流式请求，或响应在收到任何字节前就中断）"
          }
        />
      </div>

      <ChannelBreakdownNotice reason={overview.channelBreakdownReason} />

      <DataTableV2
        caption={`按模型统计（窗口 ${ASSURANCE_WINDOW_LABELS[overview.window]}）`}
        columns={MODEL_COLUMNS}
        rows={overview.models}
        rowKey={(m) => m.model || "(unknown-model)"}
        emptyState={
          <PageState
            kind="empty"
            title="窗口内没有请求"
            description="选定窗口内没有匹配到任何请求记录，换一个更长的窗口试试。"
          />
        }
      />
      {overview.modelsTruncated ? (
        <p className="text-xs text-fg-muted">
          按模型的明细已超过展示上限，只显示请求数最多的前 {formatCount(overview.models.length)} 个模型。
        </p>
      ) : null}

      <CoverageFooter coverage={overview.coverage} retentionDays={overview.retentionDays}>
        <FreshnessBadge freshness={overview.freshness} />
        <FreshnessNote freshness={overview.freshness} />
      </CoverageFooter>
    </div>
  );
}

function AssuranceOverview({ platform }: { platform: string }) {
  const [window, setWindow] = useState<AssuranceWindow>("1h");
  const query = useQuery({
    queryKey: ["assurance", "overview", platform, window],
    queryFn: ({ signal }) => getPlatformAssuranceOverview(platform, window, { signal }),
  });

  return (
    <div className="flex flex-col gap-3">
      <AssuranceWindowControl value={window} onChange={setWindow} />
      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        {query.data ? <AssuranceOverviewBody overview={query.data} /> : null}
      </ApiStateView>
    </div>
  );
}

// --- 检测任务（主动探测，划给 XM-ASSURE1，本片只保留蓝图） ---

const PROBES_NOT_MOUNTED_DESCRIPTION =
  "主动探测（声明 → 探针 → 结论）需要一个真正发起请求的 Action，且必须带停用/Kill Switch（宪法 26 条：所有生产写动作必须可以停用）；设计与实现是独立切片 XM-ASSURE1，不在本片（XM-ASSURE0，只做被动指标）范围内。这里只保留原型的列结构，不显示任何检测结果——编一行探测记录会让人以为真的探测已经在跑。";

/** 只有表头的蓝图表。画出列结构而不是一句「敬请期待」：蓝图的内容就是
 *  「将来这里有哪几列」，而空态说清楚为什么现在没有行。 */
function BlueprintTable({
  caption,
  columns,
  emptyTitle,
  emptyDescription,
}: {
  caption: string;
  columns: string[];
  emptyTitle: string;
  emptyDescription: string;
}) {
  return (
    <div className="flex flex-col overflow-hidden rounded-lg border border-edge bg-surface shadow-sm">
      <div className="max-w-full overflow-x-auto">
        <table className="w-full border-collapse text-sm">
          <caption className="sr-only">{caption}</caption>
          <thead className="border-b border-edge bg-surface-muted">
            <tr>
              {columns.map((c) => (
                <th
                  key={c}
                  scope="col"
                  className="px-3 py-2 text-left text-xs font-medium text-fg-muted whitespace-nowrap"
                >
                  {c}
                </th>
              ))}
            </tr>
          </thead>
        </table>
      </div>
      <PageState kind="unavailable" title={emptyTitle} description={emptyDescription} compact />
    </div>
  );
}

function AssuranceProbes() {
  return (
    <div className="flex flex-col gap-3">
      <p role="status" className="rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg">
        {PROBES_NOT_MOUNTED_DESCRIPTION}
      </p>
      <p className="text-xs text-fg-muted">支持不定时抽检，也保证最低检测频率——这是 XM-ASSURE1 的设计目标，尚未实现。</p>
      <BlueprintTable
        caption="检测任务"
        columns={["任务", "渠道", "目标模型", "策略", "最近一次", "结果"]}
        emptyTitle="还没有检测任务"
        emptyDescription="需要 XM-ASSURE1（主动探测 Action + Kill Switch）完成后才会有数据。"
      />
    </div>
  );
}

// --- 历史记录 ---

const HISTORY_COLUMNS: DataTableColumn<AssuranceHistoryDay>[] = [
  { id: "day", header: "业务日", primary: true, value: (d) => d.day, cell: (d) => d.day },
  {
    id: "coverage",
    header: "覆盖",
    value: (d) => (d.missing ? "missing" : "complete"),
    cell: (d) =>
      d.missing ? <Badge tone="warning">目录缺失</Badge> : <Badge tone="success">完整</Badge>,
  },
  {
    id: "requests",
    header: "请求量",
    numeric: true,
    value: (d) => d.requestCount,
    cell: (d) => formatCount(d.requestCount),
  },
  {
    id: "success",
    header: "成功率",
    value: (d) => successRateText(d.statusClasses),
    cell: (d) => successRateText(d.statusClasses),
  },
  { id: "failures", header: "失败构成", cell: (d) => failureBreakdownText(d.statusClasses) },
  { id: "duration", header: "总耗时", cell: (d) => percentileSummary(d.durationMs) },
  { id: "ttfb", header: "首字节", cell: (d) => percentileSummary(d.ttfbMs) },
  {
    id: "models",
    header: "涉及模型数",
    numeric: true,
    value: (d) => d.models.length,
    cell: (d) => formatCount(d.models.length),
  },
];

function AssuranceHistoryBody({ history }: { history: AssuranceHistoryData }) {
  const missingCount = history.days.filter((d) => d.missing).length;
  return (
    <div className="flex flex-col gap-3">
      <ChannelBreakdownNotice reason={history.channelBreakdownReason} />
      <DataTableV2
        caption="渠道保障历史：按业务日聚合的请求量/成功率/延迟"
        columns={HISTORY_COLUMNS}
        rows={history.days}
        rowKey={(d) => d.day}
        emptyState={
          <PageState kind="empty" title="没有历史数据" description="最近 7 天内没有可用的索引目录。" />
        }
      />
      <div className="flex flex-wrap items-center gap-2 text-xs text-fg-muted">
        <FreshnessBadge freshness={history.freshness} />
        <FreshnessNote freshness={history.freshness} />
        <span aria-hidden="true">·</span>
        <span>
          {missingCount > 0
            ? `近 7 天中有 ${missingCount} 天索引目录缺失（未产生数据，或已过 ${history.retentionDays} 天保留期）`
            : `近 7 天索引目录均完整（保留期 ${history.retentionDays} 天）`}
        </span>
      </div>
    </div>
  );
}

function AssuranceHistory({ platform }: { platform: string }) {
  const query = useQuery({
    queryKey: ["assurance", "history", platform],
    queryFn: ({ signal }) => getPlatformAssuranceHistory(platform, { signal }),
  });
  return (
    <div className="flex flex-col gap-3">
      <p className="text-xs text-fg-muted">
        最近 7 天的被动保障指标，按业务日（Asia/Shanghai）聚合。没有可用的降采样汇总时这是唯一的历史来源，因此固定
        7 天，不支持更长区间——不让这个 Query 被当成数据导出口。
      </p>
      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        {query.data ? <AssuranceHistoryBody history={query.data} /> : null}
      </ApiStateView>
    </div>
  );
}

/** 按子页签 id 取内容。认不出的返回 undefined，由调用方回落到通用占位。
 *
 *  platform 是 Sub2API/NewAPI 的 serviceType（"sub2api" / "newapi"）——两个
 *  平台共用同一套组件，数据源与聚合逻辑完全对称，只是查询参数不同。 */
export function assuranceSubTab(subId: string, platform: string): ReactNode | undefined {
  switch (subId) {
    case "overview":
      return <AssuranceOverview platform={platform} />;
    case "probes":
      return <AssuranceProbes />;
    case "history":
      return <AssuranceHistory platform={platform} />;
    default:
      return undefined;
  }
}
