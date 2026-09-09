import { DataTableV2, PageState, formatUtcTimestamp, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import {
  describeProbeHealth,
  describeUpstreamSupport,
  formatLatency,
  type ConnectorProbeRow,
} from "../lib/connectorProbe";
import { ApiStateView } from "./ApiStateView";

/** XM-CREDS-TAB-PROBE：「连接与凭据」页签的健康探测历史。
 *
 *  数据是 `ConnectorProbeWorker` 每次探测写下的观测序列。这一格原来写着
 *  「独立探测历史 Query 尚未提供」——但 `/api/v1/metrics/history` 一直都在，
 *  连接器健康就是它的一个指标键。占位比缺功能更糟：它让人以为这件事没做，
 *  于是没人去看本来就有的数据。
 *
 *  纯展示：行由 `lib/connectorProbe` 派生，本组件不发查询、不做判断。 */

const COLUMNS: DataTableColumn<ConnectorProbeRow>[] = [
  {
    id: "checkedAt",
    header: "执行时间",
    primary: true,
    value: (row) => row.checkedAt,
    cell: (row) => (
      <div className="min-w-44">
        <span className="font-mono text-xs text-fg">{formatUtcTimestamp(row.checkedAt)}</span>
        {/* 取自哪个字段要看得见：checked_at 缺失时退回观测/落库时刻，
            两者可能差很远，不标注就成了一个说不清来历的时间。 */}
        <p className="text-xs text-fg-muted">{row.checkedAtSource}</p>
      </div>
    ),
  },
  {
    id: "version",
    header: "上游版本",
    value: (row) => row.version,
    cell: (row) => (
      <span className="font-mono text-xs text-fg">{row.version || "—"}</span>
    ),
  },
  {
    id: "supported",
    header: "兼容矩阵",
    value: (row) => describeUpstreamSupport(row.supported, row.version).label,
    cell: (row) => {
      const support = describeUpstreamSupport(row.supported, row.version);
      return <Badge tone={support.tone}>{support.label}</Badge>;
    },
  },
  {
    id: "healthy",
    header: "结果",
    value: (row) => describeProbeHealth(row.healthy, row.kind).label,
    cell: (row) => {
      const health = describeProbeHealth(row.healthy, row.kind);
      return <Badge tone={health.tone}>{health.label}</Badge>;
    },
  },
  {
    id: "latency",
    header: "延迟",
    value: (row) => formatLatency(row.latencyMs),
    cell: (row) => (
      <span className="font-mono text-xs text-fg tabular-nums">{formatLatency(row.latencyMs)}</span>
    ),
  },
  {
    id: "status",
    header: "采集状态",
    value: (row) => `${row.status} ${row.lastErrorCode}`,
    cell: (row) => (
      <div className="min-w-28">
        <span className="text-xs text-fg">{row.status || "—"}</span>
        {row.lastErrorCode ? (
          <p className="font-mono text-xs text-danger">{row.lastErrorCode}</p>
        ) : null}
      </div>
    ),
  },
  {
    id: "source",
    header: "来源",
    value: (row) => row.source,
    cell: (row) => <span className="font-mono text-xs text-fg-muted">{row.source || "—"}</span>,
  },
];

export interface ConnectorProbeHistoryCardProps {
  label: string;
  rows: ConnectorProbeRow[];
  hours: number;
  isPending: boolean;
  error: unknown;
  onRetry: () => void;
  /** 平台标识不认识时（没有对应的指标键）为 false：这时不发查询，
   *  也不能显示「还没探测过」——那是两件不同的事。 */
  metricKnown: boolean;
}

export function ConnectorProbeHistoryCard({
  label,
  rows,
  hours,
  isPending,
  error,
  onRetry,
  metricKnown,
}: ConnectorProbeHistoryCardProps) {
  return (
    <article className="rounded-lg border border-edge bg-surface p-4 shadow-sm">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold text-fg">健康探测历史</h3>
          <p className="mt-1 text-xs leading-5 text-fg-muted">
            连接器探测每次写下的观测：探测执行时间、上游版本、兼容矩阵判定、健康结果、延迟与采集状态。
            这与上表的「最近观测」不是一回事——上表说的是服务注册表被更新过，这里说的是连接器真的连上去问过。
          </p>
        </div>
        <Badge tone="info">近 {hours} 小时</Badge>
      </div>
      <div className="mt-4">
        {metricKnown ? (
          <ApiStateView isPending={isPending} error={error} onRetry={onRetry}>
            <DataTableV2
              caption={`${label} 连接器探测历史（近 ${hours} 小时，最近一次在前）`}
              columns={COLUMNS}
              rows={rows}
              rowKey={(row) => row.id}
              emptyState={
                <PageState
                  kind="empty"
                  title={`近 ${hours} 小时没有 ${label} 的探测记录`}
                  description="连接器探测只在这个平台已配置为真实对接时才会运行。仍是模拟数据、或凭据尚未配置时，这里本来就没有记录——这不是故障。"
                />
              }
            />
          </ApiStateView>
        ) : (
          <p className="rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted">
            {label} 没有对应的连接器探测指标：探测目前只覆盖 Sub2API 与 NewAPI 两个连接器。
          </p>
        )}
      </div>
    </article>
  );
}
