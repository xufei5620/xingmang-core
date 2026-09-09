import { useQuery } from "@tanstack/react-query";
import {
  PageState,
  formatUtcTimestamp,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { Link, useSearchParams } from "react-router";
import {
  ALERT_STATUS_ALL,
  alertAgeAnchor,
  describeFireCount,
  estimatedPrefix,
  FIRE_COUNT_HEADER,
  FIRE_COUNT_MEANING,
  listAlerts,
  ruleLabel,
  type AlertItem,
} from "../api/alerts";
import {
  describeNotifyStatus,
  describeSeverity,
  describeStatus,
  sortForDisplay,
} from "../lib/alerts";
import {
  ALERT_WINDOW_OPTIONS,
  ALERT_WINDOW_PARAM,
  alertDurationMs,
  alertWithinWindow,
  formatAlertDuration,
  parseAlertWindow,
  type AlertWindowKey,
} from "../lib/alertWindow";
import { ApiStateView } from "./ApiStateView";
import { PersistentDataTable, platformSavedViewTableKey } from "./PersistentDataTable";

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

/** 编号：给人在群里/工单里指认「就是这一条」用的短码。
 *
 *  原型画的是 `AL-2608-0119` 那种带序号的编号，**后端没有这个字段**，序号也
 *  不能由前端编——两个人打开同一页会看到不同的序号。这里显示的是告警自身
 *  UUID 的前八位（完整值在 title 属性里，可复制）：它是真的、稳定的、能一眼
 *  对上的。带序号的可读编号需要后端加一列，已登记为待办。 */
function alertShortCode(alert: AlertItem): string {
  const id = alert.id.replace(/-/g, "");
  return id.length >= 8 ? id.slice(0, 8).toUpperCase() : alert.id.toUpperCase();
}

function alertColumns(now: number): DataTableColumn<AlertItem>[] {
  return [
  {
    id: "code",
    header: "编号",
    headerTitle: "告警 ID 前八位；完整 ID 在单元格提示里",
    value: (alert) => alertShortCode(alert),
    cell: (alert) => (
      <span className="font-mono text-xs text-fg-muted" title={alert.id}>
        {alertShortCode(alert)}
      </span>
    ),
  },
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
    cell: (alert) => {
      // 「首次」取 first_opened_at（与告警中心同一列同一判据，见
      // api/alerts 的 alertAgeAnchor）：opened_at 在复发时归零，拿它当「首次」
      // 会让一条抖了三天的告警每次都显示成刚开始。
      const age = alertAgeAnchor(alert);
      return (
      <div className="min-w-44 text-xs tabular-nums text-fg-muted">
        <span className="block" title={age.hint ?? undefined}>
          首次 {estimatedPrefix(age)}
          {formatUtcTimestamp(age.since)}
        </span>
        <span className="block">最近 {formatUtcTimestamp(alert.last_seen_at)}</span>
        {/* 确认时刻在场才显示（与告警中心同一列同一写法）：null 是没人确认过 */}
        {alert.acknowledged_at ? (
          <span className="block">确认 {formatUtcTimestamp(alert.acknowledged_at)}</span>
        ) : null}
        {alert.resolved_at ? (
          <span className="block">恢复 {formatUtcTimestamp(alert.resolved_at)}</span>
        ) : null}
      </div>
      );
    },
  },
  {
    id: "count",
    // 与告警中心那一列共用同一份表头与说明（api/alerts 的 FIRE_COUNT_*）：
    // 同一个数在两个页面上叫两个名字，人会以为看的是两个量。
    header: FIRE_COUNT_HEADER,
    numeric: true,
    value: (alert) => alert.fire_count,
    cell: (alert) => {
      // 数字列里只放数字（与告警中心那一列同一写法）：表头已是「评估轮次」，
      // 整句留给悬停与表格说明。
      const counts = describeFireCount(alert);
      return (
        <span title={`${counts.combined}。${FIRE_COUNT_MEANING}`} className="block">
          {counts.figure}
        </span>
      );
    },
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
  {
    id: "duration",
    header: "持续",
    // 原型这一列的用处是一眼分出「刚抖了一下」和「已经烧了一小时」。
    // 未恢复的按「到此刻」算，于是同样时长里还在烧的排在前面。
    //
    // 起点与左边那一列的「首次」**是同一个时刻**（同一个 alertAgeAnchor）。
    // 这一列原本从 opened_at 算：接上 first_opened_at 之后，同一行会一边写着
    // 「首次 09-05」一边写着「持续 5 分」——两个数字互相打脸，而打脸的那个
    // 正是本片要修的病（复发把计时清零，「持续」系统性偏小）。
    headerTitle: "首次发现到恢复；未恢复的算到此刻",
    numeric: true,
    value: (alert) => alertDurationMs(alertAgeAnchor(alert).since, alert.resolved_at, now),
    cell: (alert) => {
      const age = alertAgeAnchor(alert);
      // 这里**不**再标一次「约」：同一行左边那一列已经标过，而这是一列右对齐
      // 的 tabular-nums 数字列，格子里多两个字会让整列对不齐。为什么可能不准，
      // 由悬停说。
      return (
        <span className="tabular-nums text-xs text-fg-muted" title={age.hint ?? undefined}>
          {formatAlertDuration(age.since, alert.resolved_at, now)}
        </span>
      );
    },
  },
  ];
}

/** Sub2API / NewAPI 页签内的只读告警视图。
 *
 * 全局告警中心负责确认和静默；平台页只负责“这家平台有什么问题”的聚焦阅读，
 * 因而没有写按钮，避免用户在窄上下文里误以为自己处理的是全部相关告警。 */
export function PlatformAlertsPanel({ platform }: { platform: string }) {
  const label = platformLabel(platform);
  const [searchParams, setSearchParams] = useSearchParams();
  const window = parseAlertWindow(searchParams.get(ALERT_WINDOW_PARAM));
  const query = useQuery({
    queryKey: ["alerts", "platform", platform, ALERT_STATUS_ALL, PLATFORM_ALERT_LIMIT],
    queryFn: ({ signal }) =>
      listAlerts({ signal, status: ALERT_STATUS_ALL, limit: PLATFORM_ALERT_LIMIT }),
  });
  // 「持续」与时间窗口都相对**取数那一刻**算，不用 Date.now() 逐次渲染重算：
  // 每次重绘都往前走一秒会让排序在人眼前跳动，也让测试变成碰运气。
  const now = query.dataUpdatedAt || Date.now();
  const belonging = (query.data ?? []).filter((alert) => alertBelongsToPlatform(alert, platform));
  const rows = sortForDisplay(
    belonging.filter((alert) => alertWithinWindow(alert, window, now)),
  );
  const hiddenByWindow = belonging.length - rows.length;
  const setWindow = (next: AlertWindowKey) => {
    const params = new URLSearchParams(searchParams);
    if (next === "all") params.delete(ALERT_WINDOW_PARAM);
    else params.set(ALERT_WINDOW_PARAM, next);
    setSearchParams(params, { replace: true });
  };
  const savedViewKey =
    platform === "sub2api" || platform === "newapi"
      ? platformSavedViewTableKey(platform, "alerts")
      : undefined;

  return (
    <section className="flex flex-col gap-4">
      <div className="rounded-lg border border-edge bg-surface px-4 py-3 shadow-sm">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            {/* 冻结 IA 第 7 格的逐字标题。 */}
            <h2 className="text-base font-semibold text-fg">{label} · 告警</h2>
            <p className="mt-1 max-w-4xl text-xs leading-5 text-fg-muted">
              展示含已解决记录在内的最近最多 {PLATFORM_ALERT_LIMIT} 条告警，再按来源指标前缀
              <code className="mx-1 font-mono text-fg">{platform}.</code>
              精确归属。没有稳定来源指标的平台归属告警仍留在全局告警中心。
            </p>
          </div>
          <Badge tone="info">平台只读视图</Badge>
        </div>
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <label className="flex items-center gap-2 text-xs text-fg-muted">
          <span>时间</span>
          <select
            aria-label="按时间范围收窄"
            value={window}
            onChange={(event) => setWindow(event.target.value as AlertWindowKey)}
            className="rounded-md border border-edge-strong bg-surface px-2 py-1 text-xs text-fg hover:border-accent focus:outline-2 focus:outline-accent"
          >
            {ALERT_WINDOW_OPTIONS.map((option) => (
              <option key={option.key} value={option.key}>
                {option.label}
              </option>
            ))}
          </select>
        </label>
        {/* 窗口是对已取回那一批的收窄，不是查询条件。说清楚，否则选了「最近
            30 天」却看不到第 201 条会被当成数据丢了（宪法 12 条）。 */}
        <p className="text-xs text-fg-muted">
          按**最近一次发现**收窄这 {belonging.length} 条已取回的记录
          {hiddenByWindow > 0 ? `（当前隐藏 ${hiddenByWindow} 条）` : ""}；
          更早的记录不在这一页，去全局告警中心查。
        </p>
      </div>

      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        <PersistentDataTable
          tableKey={savedViewKey ?? platformSavedViewTableKey("sub2api", "alerts")}
          caption={`${label} 告警：编号、严重度、状态、来源指标、首次与最近发现、持续时长、${FIRE_COUNT_HEADER}及投递结果`}
          columns={alertColumns(now)}
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
              description={`最近最多 ${PLATFORM_ALERT_LIMIT} 条记录中，没有 source_metric_key 以 ${platform}. 开头且落在所选时间范围内的告警；这不代表全局没有告警，请到全局告警中心核对无指标来源与跨平台规则。`}
            />
          }
        />
      </ApiStateView>
      <p className="text-xs leading-5 text-fg-muted">
        平台页不提供确认、静默或批量操作；需要处置时前往
        <Link to="/alerts" className="mx-1 text-accent underline underline-offset-2 hover:text-accent-strong">
          全局告警中心
        </Link>
        ，由 Action 与权限边界执行并保留审计。
      </p>
    </section>
  );
}
