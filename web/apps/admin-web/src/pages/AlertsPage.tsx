import { useQuery } from "@tanstack/react-query";
import {
  DataTableV2,
  formatUtcTimestamp,
  navLabel,
  PageHeader,
  PageState,
  navItemByPath,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge, Tabs } from "@xingmang/ui-primitives";
import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import {
  ALERT_STATUS_ALL,
  listAlerts,
  listAlertsPage,
  ruleLabel,
  type AlertItem,
} from "../api/alerts";
import { AcknowledgeAlertButton } from "../components/AcknowledgeAlertButton";
import { AlertNotifyDeliveries } from "../components/AlertNotifyDeliveries";
import { AlertSilences } from "../components/AlertSilences";
import { ApiStateView } from "../components/ApiStateView";
import { BulkAckReceipt, BulkAcknowledgeAlerts } from "../components/BulkAcknowledgeAlerts";
import { CreateSilenceDialog } from "../components/CreateSilenceDialog";
import type { BulkAckSummary } from "../lib/alertBulkAck";
import {
  describeNotifyStatus,
  describeSeverity,
  describeStatus,
  sortForDisplay,
} from "../lib/alerts";
import { OVERVIEW_POLL_INTERVAL_MS, useAutoRefresh } from "../lib/autoRefresh";
import { AlertRulesPage } from "./AlertRulesPage";

/** react-query 的缓存键前缀。总览页的告警卡也用它，两处共用一份缓存。 */
export const ALERTS_QUERY_KEY = "alerts";

const ALERT_SUB_TABS = (navItemByPath("/alerts")?.item.subTabs ?? []).map((tab) => [tab.id, tab.label] as const);

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
  const [searchParams, setSearchParams] = useSearchParams();
  const rawSub = searchParams.get("sub");
  const sub = rawSub && rawSub.trim() !== "" ? rawSub : "alerts";

  if (!ALERT_SUB_TABS.some(([value]) => value === sub)) {
    return (
      <section>
        <PageHeader title={navLabel("/alerts")} description="告警与故障的其它子页仍在规划中，当前没有可断言的数据源。" />
        <PageState
          kind="unavailable"
          title={`「${sub}」子页尚未接入`}
          description="为避免把活跃告警误当成故障事件、通知或暂停记录，本页不会回落到告警列表。"
          action={<Link to="/alerts?sub=alerts" className="text-sm font-medium text-accent hover:underline">返回告警</Link>}
        />
      </section>
    );
  }
  const setSub = (next: string) => {
    const params = new URLSearchParams(searchParams);
    params.set("sub", next);
    setSearchParams(params, { replace: true });
  };
  return (
    <Tabs
      value={sub}
      onValueChange={setSub}
      items={ALERT_SUB_TABS.map(([value, label]) => ({
        value,
        label,
        content: value === "alerts" ? <AlertsListPage /> : value === "rules" ? <AlertRulesPage /> : value === "notifications" ? <AlertNotifyDeliveries /> : value === "silences" ? <AlertSilences /> : (
          // 「故障事件」仍是占位，且必须保持占位：Incident 对象今天不在平台里，
          // 拿告警凑数就是把活跃告警误当成故障事件。
          //
          // 「暂停告警」曾经和它一起挂在这里，理由是「静默记录没有列表端点」。
          // XM-SILENCE-LIST 把端点补上了（GET /api/v1/alerts/silences），
          // 那条理由不再成立，于是它搬去了 AlertSilences。故障事件没有跟着搬，
          // 因为它缺的不是端点而是对象本身。
          <section>
            <PageHeader title={label} description="该子页尚未接入稳定的数据源。" />
            <PageState kind="unavailable" title={`「${label}」尚未接入`} description="当前不会把其它告警数据误归类到这里。" />
          </section>
        ),
      }))}
    />
  );
}

function AlertsListPage() {
  const [scope, setScope] = useState<Scope>("active");
  const [notice, setNotice] = useState<string | null>(null);
  // 批量回执单独一份状态，不塞进 notice：它是结构化的（成功名单 + 失败名单 +
  // 跳过名单），压成一行字就丢掉了「哪几条、为什么」——那正是它的全部内容。
  const [bulkSummary, setBulkSummary] = useState<BulkAckSummary | null>(null);

  const query = useQuery({
    queryKey: [ALERTS_QUERY_KEY, scope],
    // 用带信封的那个（XM-ALERTS-LIST-TRUNCATED）：**这一页的职责就是"看全部"**，
    // 而它原先不传 limit、吃服务端默认值，被截断时一个字都不说。
    queryFn: ({ signal }) =>
      listAlertsPage({ signal, ...(scope === "all" ? { status: ALERT_STATUS_ALL } : {}) }),
  });

  const refresh = () => {
    void query.refetch();
  };
  useAutoRefresh(refresh);

  // 两种回执互斥地显示：单条确认之后还挂着一张上一次批量的名单，会让人
  // 分不清刚才那一下到底做了什么。后写的那个说了算，另一个清掉。
  const afterWrite = (message: string) => {
    setBulkSummary(null);
    setNotice(message);
    refresh();
  };

  const afterBulk = (summary: BulkAckSummary) => {
    setNotice(null);
    setBulkSummary(summary);
    refresh();
  };

  const body = (
    <ApiStateView isPending={query.isPending} error={query.error} onRetry={refresh}>
      {query.data?.truncated ? (
        <p role="status" className="mb-2 text-xs text-warning">
          这一页可能不是全部：服务端一次最多返回 {query.data.limit || "若干"} 条，本次正好取满。
          请用状态页签或时间范围收窄之后再看。
        </p>
      ) : null}
      <AlertsTable
        items={query.data?.items ?? []}
        scope={scope}
        onAcknowledged={afterWrite}
        onBulkAcknowledged={afterBulk}
      />
    </ApiStateView>
  );

  return (
    <section>
      <PageHeader
        title={navLabel("/alerts")}
        description={`规则命中后按去重键收敛成一条告警并计数；条件不再成立会自动恢复。每 ${OVERVIEW_POLL_INTERVAL_MS / 1000} 秒自动刷新（页面不可见时暂停）。`}
        onRefresh={refresh}
        refreshing={query.isFetching}
        lastRefreshedAt={query.dataUpdatedAt || undefined}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Link to="/alerts?sub=rules" className="inline-flex h-(--xm-control-h-md) items-center rounded-md border border-edge bg-surface px-3 text-sm font-medium text-fg hover:bg-surface-muted">告警与故障规则</Link>
            <CreateSilenceDialog
              onCreated={(runId) =>
                afterWrite(`已创建静默窗口，run_id=${runId}；审计事件通常几秒内出现在审计页`)
              }
            />
          </div>
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

      {/* 批量回执放在页级而不是选择条里：选择条会随「清除选择」一起消失，
          而那一下会把刚拿到的 run_id 名单一并带走。 */}
      {bulkSummary ? <BulkAckReceipt summary={bulkSummary} /> : null}

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

/** 严重度的排序等级。显示的是「严重/警告/提示」，但按中文比较排出来是
 *  「严重 < 提示 < 警告」——一个毫无意义的顺序。 */
const SEVERITY_RANK: Record<string, number> = { critical: 0, warning: 1, info: 2 };

function alertColumns(
  onAcknowledged: (message: string) => void,
): DataTableColumn<AlertItem>[] {
  return [
    {
      id: "severity",
      header: "严重度",
      value: (alert) => describeSeverity(alert.severity).label,
      // 未知严重度按最高排，与 describeSeverity 的判断一致
      sortAs: (alert) => SEVERITY_RANK[alert.severity] ?? -1,
      cell: (alert) => {
        const severity = describeSeverity(alert.severity);
        return (
          <Badge tone={severity.tone} title={severity.hint}>
            {severity.label}
          </Badge>
        );
      },
    },
    {
      id: "status",
      header: "状态",
      value: (alert) => describeStatus(alert.status).label,
      cell: (alert) => {
        const status = describeStatus(alert.status);
        return (
          <Badge tone={status.tone} title={status.hint}>
            {status.label}
          </Badge>
        );
      },
    },
    {
      id: "title",
      header: "告警",
      primary: true,
      // 规则键与来源指标键都进搜索：运维记得的往往是指标名而不是标题
      value: (alert) =>
        [alert.title, ruleLabel(alert.rule_key), alert.detail, alert.source_metric_key]
          .filter(Boolean)
          .join(" "),
      cell: (alert) => (
        <>
          <span className="font-medium">{alert.title}</span>
          <p className="mt-0.5 text-xs text-fg-muted">{ruleLabel(alert.rule_key)}</p>
          {alert.detail ? <p className="mt-1 text-xs text-fg-muted">{alert.detail}</p> : null}
          {alert.source_metric_key ? (
            <p className="mt-1 font-mono text-xs break-all text-fg-muted">
              {alert.source_metric_key}
            </p>
          ) : null}
        </>
      ),
    },
    {
      id: "seen",
      header: "首次 / 最近",
      // 排序按「最近」：人找的是「还在响的」，不是「最早开始的」
      value: (alert) => alert.last_seen_at,
      cell: (alert) => (
        <>
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
        </>
      ),
    },
    {
      id: "fireCount",
      header: "次数",
      numeric: true,
      value: (alert) => alert.fire_count,
      cell: (alert) => (
        <span title="被去重合并掉的命中次数（含首次）">{alert.fire_count}</span>
      ),
    },
    {
      id: "notify",
      header: "投递",
      value: (alert) => describeNotifyStatus(alert.notify_status).label,
      cell: (alert) => {
        const notify = describeNotifyStatus(alert.notify_status);
        return (
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
        );
      },
    },
    {
      id: "actions",
      header: "操作",
      // 没有 value：这一列只有按钮，既不该排序也不该进搜索
      cell: (alert) => (
        <AcknowledgeAlertButton
          alert={alert}
          onAcknowledged={(runId) =>
            onAcknowledged(`已确认，run_id=${runId}；审计事件通常几秒内出现在审计页`)
          }
        />
      ),
    },
  ];
}

function AlertsTable({
  items,
  scope,
  onAcknowledged,
  onBulkAcknowledged,
}: {
  items: AlertItem[];
  scope: Scope;
  onAcknowledged: (message: string) => void;
  onBulkAcknowledged: (summary: BulkAckSummary) => void;
}) {
  return (
    <DataTableV2
      caption="告警列表：严重度、状态、首次与最近发现、命中次数与投递结果"
      columns={alertColumns(onAcknowledged)}
      // 默认顺序仍是「最严重的在最上面」：DataTableV2 不排序时保持入参顺序
      rows={sortForDisplay(items)}
      rowKey={(alert) => alert.id}
      pageSize={20}
      searchable
      filters={[
        { columnId: "severity", label: "严重度", options: ["严重", "警告", "提示"] },
        { columnId: "status", label: "状态", options: ["未处理", "已确认", "已静默", "已解决", "复发"] },
      ]}
      selectable
      bulkActions={(keys) => (
        // 批量确认走的仍是 alerts.alert.acknowledge@1（宪法 2 条：写操作只经
        // Action），只是循环调 N 次。它**不需要审批**——那个 Action 声明的是
        // L0（internal/platform/alerts/actions.go），单条确认一直可用就是证据。
        <BulkAcknowledgeAlerts selectedKeys={keys} items={items} onCompleted={onBulkAcknowledged} />
      )}
      emptyState={
        <PageState
          kind="empty"
          title={scope === "active" ? "无活动告警" : "暂无告警记录"}
          description={
            scope === "active"
              ? "该环境下当前没有活跃告警。这是好消息，但请确认 Platform Worker 的评估任务在跑——一个停掉的评估器同样显示为零告警。"
              : "该环境下还没有产生过任何告警；采集任务跑起来并出现异常后会出现在这里"
          }
        />
      }
    />
  );
}
