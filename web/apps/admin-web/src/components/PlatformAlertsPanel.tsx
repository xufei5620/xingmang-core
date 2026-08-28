import { useQuery } from "@tanstack/react-query";
import {
  DataTableV2,
  PageState,
  formatUtcTimestamp,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { ALERT_STATUS_ALL, listAlerts, ruleLabel, type AlertItem } from "../api/alerts";
import {
  describeNotifyStatus,
  describeSeverity,
  describeStatus,
  sortForDisplay,
} from "../lib/alerts";
import { ApiStateView } from "./ApiStateView";

const PLATFORM_ALERT_LIMIT = 200;

const PLATFORM_LABELS: Readonly<Record<string, string>> = {
  sub2api: "Sub2API",
  newapi: "NewAPI",
};

function platformLabel(platform: string): string {
  return PLATFORM_LABELS[platform] ?? platform;
}

/** 平台归属只能来自稳定的指标命名空间。
 *
 * 不看 title/detail/dedup_key：这些自由文本提到另一个平台很常见，用它们归属会把
 *  「Server 影响 NewAPI」误放进 NewAPI 页，也会让全局规则被重复计入多个平台。 */
export function alertBelongsToPlatform(alert: AlertItem, platform: string): boolean {
  return platform !== "" && alert.source_metric_key.startsWith(`${platform}.`);
}

const SEVERITY_RANK: Record<string, number> = { critical: 0, warning: 1, info: 2 };

const COLUMNS: DataTableColumn<AlertItem>[] = [
  {
    id: "severity",
    header: "严重度",
    value: (alert) => describeSeverity(alert.severity).label,
    sortAs: (alert) => SEVERITY_RANK[alert.severity] ?? -1,
    cell: (alert) => {
      const shown = describeSeverity(alert.severity);
      return <Badge tone={shown.tone} title={shown.hint}>{shown.label}</Badge>;
    },
  },
  {
    id: "status",
    header: "状态",
    value: (alert) => describeStatus(alert.status).label,
    cell: (alert) => {
      const shown = describeStatus(alert.status);
      return <Badge tone={shown.tone} title={shown.hint}>{shown.label}</Badge>;
    },
  },
  {
    id: "alert",
    header: "告警",
    primary: true,
    value: (alert) =>
      [alert.title, alert.detail, alert.rule_key, alert.source_metric_key].filter(Boolean).join(" "),
    cell: (alert) => (
      <div className="min-w-64 max-w-xl">
        <strong className="font-medium text-fg">{alert.title}</strong>
        <p className="mt-0.5 text-xs text-fg-muted">{ruleLabel(alert.rule_key)}</p>
        {alert.detail ? <p className="mt-1 text-xs leading-5 text-fg-muted">{alert.detail}</p> : null}
        <p className="mt-1 font-mono text-xs break-all text-fg-muted">
          {alert.source_metric_key}
        </p>
      </div>
    ),
  },
  {
    id: "seen",
    header: "首次 / 最近",
    value: (alert) => alert.last_seen_at,
    cell: (alert) => (
      <div className="min-w-44 text-xs tabular-nums text-fg-muted">
        <span className="block">首次 {formatUtcTimestamp(alert.opened_at)}</span>
        <span className="block">最近 {formatUtcTimestamp(alert.last_seen_at)}</span>
        {alert.resolved_at ? (
          <span className="block">恢复 {formatUtcTimestamp(alert.resolved_at)}</span>
        ) : null}
      </div>
    ),
  },
  {
    id: "count",
    header: "次数",
    numeric: true,
    value: (alert) => alert.fire_count,
    cell: (alert) => <span title="含首次及被去重合并的重复命中">{alert.fire_count}</span>,
  },
  {
    id: "notify",
    header: "投递",
    value: (alert) => describeNotifyStatus(alert.notify_status).label,
    cell: (alert) => {
      const shown = describeNotifyStatus(alert.notify_status);
      return (
        <div className="flex min-w-28 flex-col items-start gap-1">
          <Badge tone={shown.tone} title={shown.hint}>{shown.label}</Badge>
          {alert.notified_at ? (
            <span className="text-xs tabular-nums text-fg-muted">
              {formatUtcTimestamp(alert.notified_at)}
            </span>
          ) : null}
          {alert.notify_error ? (
            <span className="text-xs break-all text-danger">{alert.notify_error}</span>
          ) : null}
        </div>
      );
    },
  },
];

/** Sub2API / NewAPI 页签内的只读告警视图。
 *
 * 全局告警中心负责确认和静默；平台页只负责“这家平台有什么问题”的聚焦阅读，
 * 因而没有写按钮，避免用户在窄上下文里误以为自己处理的是全部相关告警。 */
export function PlatformAlertsPanel({ platform }: { platform: string }) {
  const label = platformLabel(platform);
  const query = useQuery({
    queryKey: ["alerts", "platform", platform, ALERT_STATUS_ALL, PLATFORM_ALERT_LIMIT],
    queryFn: ({ signal }) =>
      listAlerts({ signal, status: ALERT_STATUS_ALL, limit: PLATFORM_ALERT_LIMIT }),
  });

  const rows = sortForDisplay(
    (query.data ?? []).filter((alert) => alertBelongsToPlatform(alert, platform)),
  );

  return (
    <section className="flex flex-col gap-4">
      <div className="rounded-lg border border-edge bg-surface px-4 py-3 shadow-sm">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 className="text-base font-semibold text-fg">{label} 平台告警</h2>
            <p className="mt-1 max-w-4xl text-xs leading-5 text-fg-muted">
              展示含已解决记录在内的最近最多 200 条告警，再按来源指标前缀
              <code className="mx-1 font-mono text-fg">{platform}.</code>
              精确归属。没有稳定来源指标的平台归属告警仍留在全局告警中心。
            </p>
          </div>
          <Badge tone="info">平台只读视图</Badge>
        </div>
      </div>

      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        <DataTableV2
          caption={`${label} 告警：严重度、状态、来源指标、首次与最近发现、次数及投递结果`}
          columns={COLUMNS}
          rows={rows}
          rowKey={(alert) => alert.id}
          pageSize={20}
          searchable
          filters={[
            { columnId: "severity", label: "严重度", options: ["严重", "警告", "提示"] },
            { columnId: "status", label: "状态", options: ["未处理", "已确认", "已静默", "已解决", "复发"] },
          ]}
          emptyState={
            <PageState
              kind="empty"
              title={`没有可归属到 ${label} 的告警`}
              description={`最近最多 ${PLATFORM_ALERT_LIMIT} 条记录中，没有 source_metric_key 以 ${platform}. 开头的告警；这不代表全局没有告警，请到全局告警中心核对无指标来源与跨平台规则。`}
            />
          }
        />
      </ApiStateView>

      <p className="text-xs leading-5 text-fg-muted">
        平台页不提供确认、静默或批量操作；需要处置时前往全局「告警中心」，由 Action 与权限边界执行并保留审计。
      </p>
    </section>
  );
}
