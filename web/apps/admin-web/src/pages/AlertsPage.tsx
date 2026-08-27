import { useQuery } from "@tanstack/react-query";
import { formatUtcTimestamp, PageHeader } from "@xingmang/ui-admin";
import { Badge, EmptyState, Tabs } from "@xingmang/ui-primitives";
import { useState } from "react";
import { ALERT_STATUS_ALL, listAlerts, ruleLabel, type AlertItem } from "../api/alerts";
import { AcknowledgeAlertButton } from "../components/AcknowledgeAlertButton";
import { ApiStateView } from "../components/ApiStateView";
import { CreateSilenceDialog } from "../components/CreateSilenceDialog";
import {
  describeNotifyStatus,
  describeSeverity,
  describeStatus,
  sortForDisplay,
} from "../lib/alerts";
import { OVERVIEW_POLL_INTERVAL_MS, useAutoRefresh } from "../lib/autoRefresh";

const TH = "px-3 py-2 text-left text-xs font-medium text-fg-muted";
const TD = "px-3 py-2 align-top text-sm text-fg";

/** react-query 的缓存键前缀。总览页的告警卡也用它，两处共用一份缓存。 */
export const ALERTS_QUERY_KEY = "alerts";

type Scope = "active" | "all";

/** 告警中心（规格 §9.3 告警生命周期 / §9.4 告警渠道，XM-0033）。
 *
 *  这一页要同时回答两个问题，所以每行都带两组状态：
 *    - 这个问题现在怎么样了？（severity + status + 首见/最近 + fire_count）
 *    - 有人被通知到了吗？（notify_status）
 *
 *  第二个问题单独占一列，是因为「OPEN 但没投递出去」是本模块最危险的状态——
 *  运维以为告警会找上门，实际上没有任何人收到（规格 §9.4 的闭环在那时是断的）。 */
export function AlertsPage() {
  const [scope, setScope] = useState<Scope>("active");
  const [notice, setNotice] = useState<string | null>(null);

  const query = useQuery({
    queryKey: [ALERTS_QUERY_KEY, scope],
    queryFn: ({ signal }) =>
      listAlerts({ signal, ...(scope === "all" ? { status: ALERT_STATUS_ALL } : {}) }),
  });

  const refresh = () => {
    void query.refetch();
  };
  useAutoRefresh(refresh);

  const afterWrite = (message: string) => {
    setNotice(message);
    refresh();
  };

  const body = (
    <ApiStateView isPending={query.isPending} error={query.error} onRetry={refresh}>
      <AlertsTable items={query.data ?? []} scope={scope} onAcknowledged={afterWrite} />
    </ApiStateView>
  );

  return (
    <section>
      <PageHeader
        title="告警中心"
        description={`规则命中后按去重键收敛成一条告警并计数；条件不再成立会自动恢复。每 ${OVERVIEW_POLL_INTERVAL_MS / 1000} 秒自动刷新（页面不可见时暂停）。`}
        onRefresh={refresh}
        refreshing={query.isFetching}
        lastRefreshedAt={query.dataUpdatedAt || undefined}
        actions={
          <CreateSilenceDialog
            onCreated={(runId) =>
              afterWrite(`已创建静默窗口，run_id=${runId}；审计事件通常几秒内出现在审计页`)
            }
          />
        }
      />

      {notice ? (
        <p
          role="status"
          className="mb-3 rounded-md border border-success bg-success/10 px-3 py-2 text-xs text-success"
        >
          {notice}
        </p>
      ) : null}

      {/* 表格作为两个页签共用的 content：只有一个 useQuery，切页签换的是它的
          查询参数，而不是再开一条并行的数据流。Radix 只挂载当前页签的内容，
          所以同一个节点传给两边不会渲染两遍。 */}
      <Tabs
        items={[
          { value: "active", label: "活跃告警", content: body },
          { value: "all", label: "含已解决", content: body },
        ]}
        value={scope}
        onValueChange={(next) => setScope(next as Scope)}
      />
    </section>
  );
}

function AlertsTable({
  items,
  scope,
  onAcknowledged,
}: {
  items: AlertItem[];
  scope: Scope;
  onAcknowledged: (message: string) => void;
}) {
  if (items.length === 0) {
    return (
      <EmptyState
        title={scope === "active" ? "无活动告警" : "暂无告警记录"}
        description={
          scope === "active"
            ? "该环境下当前没有活跃告警。这是好消息，但请确认 Platform Worker 的评估任务在跑——一个停掉的评估器同样显示为零告警。"
            : "该环境下还没有产生过任何告警；采集任务跑起来并出现异常后会出现在这里"
        }
      />
    );
  }

  return (
    <div className="overflow-x-auto rounded-lg border border-edge bg-surface shadow-sm">
      <table className="w-full border-collapse">
        <thead className="border-b border-edge bg-surface-muted">
          <tr>
            <th className={TH}>严重度</th>
            <th className={TH}>状态</th>
            <th className={TH}>告警</th>
            <th className={TH}>首次 / 最近</th>
            <th className={TH}>次数</th>
            <th className={TH}>投递</th>
            <th className={TH}>
              <span className="sr-only">操作</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {sortForDisplay(items).map((alert) => (
            <AlertRow key={alert.id} alert={alert} onAcknowledged={onAcknowledged} />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function AlertRow({
  alert,
  onAcknowledged,
}: {
  alert: AlertItem;
  onAcknowledged: (message: string) => void;
}) {
  const severity = describeSeverity(alert.severity);
  const status = describeStatus(alert.status);
  const notify = describeNotifyStatus(alert.notify_status);

  return (
    <tr className="border-b border-edge last:border-b-0">
      <td className={TD}>
        <Badge tone={severity.tone} title={severity.hint}>
          {severity.label}
        </Badge>
      </td>
      <td className={TD}>
        <Badge tone={status.tone} title={status.hint}>
          {status.label}
        </Badge>
      </td>
      <td className={TD}>
        <span className="font-medium">{alert.title}</span>
        <p className="mt-0.5 text-xs text-fg-muted">{ruleLabel(alert.rule_key)}</p>
        {alert.detail ? <p className="mt-1 text-xs text-fg-muted">{alert.detail}</p> : null}
        {alert.source_metric_key ? (
          <p className="mt-1 font-mono text-xs break-all text-fg-muted">
            {alert.source_metric_key}
          </p>
        ) : null}
      </td>
      <td className={TD}>
        {/* 两个时刻都显示：只有一个就答不出「这个问题持续了多久」，
            而那正是判断要不要升级处理的第一个依据。 */}
        <span className="block text-xs text-fg-muted">
          首次 {formatUtcTimestamp(alert.opened_at)}
        </span>
        <span className="block text-xs text-fg-muted">
          最近 {formatUtcTimestamp(alert.last_seen_at)}
        </span>
        {alert.resolved_at ? (
          <span className="block text-xs text-fg-muted">
            恢复 {formatUtcTimestamp(alert.resolved_at)}
          </span>
        ) : null}
      </td>
      <td className={TD}>
        <span className="tabular-nums" title="被去重合并掉的命中次数（含首次）">
          {alert.fire_count}
        </span>
      </td>
      <td className={TD}>
        <div className="flex flex-col gap-1">
          <Badge tone={notify.tone} title={notify.hint}>
            {notify.label}
          </Badge>
          {alert.notified_at ? (
            <span className="text-xs text-fg-muted">{formatUtcTimestamp(alert.notified_at)}</span>
          ) : null}
          {/* 失败原因原样显示：它已由服务端脱敏（绝不含 Bot Token），
              而「为什么没投出去」是运维此刻唯一需要的信息。 */}
          {alert.notify_error ? (
            <span className="text-xs break-all text-danger">{alert.notify_error}</span>
          ) : null}
        </div>
      </td>
      <td className={TD}>
        <AcknowledgeAlertButton
          alert={alert}
          onAcknowledged={(runId) =>
            onAcknowledged(`已确认，run_id=${runId}；审计事件通常几秒内出现在审计页`)
          }
        />
      </td>
    </tr>
  );
}
