import { useQuery } from "@tanstack/react-query";
import {
  DataTableV2,
  formatUtcTimestamp,
  PageHeader,
  PageState,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { ALERT_STATUS_ALL, listAlertsPage, ruleLabel, type AlertItem } from "../api/alerts";
import { describeNotifyStatus, describeSeverity, describeStatus, sortForDisplay } from "../lib/alerts";
import { rollupNotifyDelivery, type NotifyRollup } from "../lib/alertNotify";
import { OVERVIEW_POLL_INTERVAL_MS, useAutoRefresh } from "../lib/autoRefresh";
import { ApiStateView } from "./ApiStateView";

/** 「通知」子页自己的缓存键。
 *
 *  和告警列表分开而不是共用 `[ALERTS_QUERY_KEY, "all"]`：两处取的确实是同一
 *  个端点，但共用一格缓存会让这一页的取数口径跟着列表页的改动无声地变。
 *  两个子页签不会同时挂载（Radix 只渲染当前页），多出的那一次请求没人感知。 */
const NOTIFY_QUERY_KEY = ["alerts", "notify"] as const;

/** 通知投递（规格 §9.4）。
 *
 *  这一页只回答一个问题：**有人被通知到了吗**。它此前落在通用占位「尚未接入
 *  稳定的数据源」上，但数据源一直就在 `/api/v1/alerts` 的响应里——告警列表
 *  同一页的「投递」列读的就是它。
 *
 *  取 `status=all` 而不是只取活跃：投递是件历史性的事，「昨天那条炸了的告警
 *  到底发出去没有」和「现在这条发出去没有」同样是运营要问的。 */
export function AlertNotifyDeliveries() {
  const query = useQuery({
    queryKey: NOTIFY_QUERY_KEY,
    queryFn: ({ signal }) => listAlertsPage({ signal, status: ALERT_STATUS_ALL }),
  });

  const refresh = () => {
    void query.refetch();
  };
  useAutoRefresh(refresh);

  const items = query.data?.items ?? [];
  const rollup = rollupNotifyDelivery(items);

  return (
    <section>
      <PageHeader
        title="通知"
        description={`告警发出去之后有没有人收到。每 ${OVERVIEW_POLL_INTERVAL_MS / 1000} 秒自动刷新（页面不可见时暂停）。`}
        onRefresh={refresh}
        refreshing={query.isFetching}
        lastRefreshedAt={query.dataUpdatedAt || undefined}
      />

      <NotifySourceNote />

      <ApiStateView isPending={query.isPending} error={query.error} onRetry={refresh}>
        {query.data?.truncated ? (
          <p role="status" className="mb-2 text-xs text-warning">
            这一页可能不是全部：服务端一次最多返回 {query.data.limit || "若干"} 条，本次正好取满。
            下面的计数只覆盖取回来的这些。
          </p>
        ) : null}
        <NotifyRollupCards rollup={rollup} />
        <NotifyDeliveryTable items={items} />
      </ApiStateView>
    </section>
  );
}

/** 数据来源与它的三条已知局限。
 *
 *  写出来是因为这一页最容易被当成「通知流水」来读，而它不是：平台今天没有
 *  按渠道逐条记账的地方。把局限说在前面，好过让人按一份并不存在的账去排查。 */
function NotifySourceNote() {
  return (
    <div className="mb-3 rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted">
      <p>
        投递状态来自告警本身（<code className="font-mono">GET /api/v1/alerts</code> 的{" "}
        <code className="font-mono">notify_status</code> /{" "}
        <code className="font-mono">notified_at</code> /{" "}
        <code className="font-mono">notify_error</code>），
        <b className="text-fg">不是一份按渠道记账的通知流水</b>——平台今天没有那张表。因此：
      </p>
      <ul className="mt-1 list-disc space-y-0.5 pl-4">
        <li>
          「已投递」只表示<b className="text-fg">至少一个渠道</b>收下了。同一轮里另一个渠道失败不会记进来，
          只落在服务端日志的 <code className="font-mono">alert_notify_channel_failed</code> 里。
        </li>
        <li>一条告警只有一个最新投递状态，看不到重试次数与历史。</li>
        <li>这一页与告警列表读同一批记录，所以同样受服务端条数上限影响。</li>
      </ul>
    </div>
  );
}

function NotifyRollupCards({ rollup }: { rollup: NotifyRollup }) {
  const cards: { label: string; value: number; hint: string; tone: string }[] = [
    {
      label: "已投递",
      value: rollup.delivered,
      hint: "至少一个渠道确认收下了",
      tone: "text-success",
    },
    {
      label: "投递失败",
      value: rollup.failed,
      hint: "上一轮全部渠道都失败，下一轮会自动重试",
      tone: "text-danger",
    },
    {
      label: "未投递",
      value: rollup.pending,
      hint: "还没投递出去；若一直是这个状态，多半是没有配置任何告警渠道",
      tone: "text-fg",
    },
  ];
  return (
    <>
      <div className="mb-3 grid grid-cols-2 gap-3 sm:grid-cols-4">
        {/* role=group + aria-label 把标签和数字绑在一起：否则读屏软件读到的是
            「已投递」和「1」两段互不相干的文本，而这一页全靠这个对应关系。 */}
        {cards.map((card) => (
          <div
            key={card.label}
            role="group"
            aria-label={card.label}
            className="rounded-md border border-edge bg-surface px-3 py-2"
            title={card.hint}
          >
            <p className="text-xs text-fg-muted">{card.label}</p>
            <p className={`text-lg font-semibold ${card.tone}`}>{card.value}</p>
          </div>
        ))}
        {/* 未知状态只有出现时才占一格：常态下它恒为 0，一直摆着会让人以为
            那是一类正常结果。它不为零意味着后端加了新状态而前端没跟上。 */}
        {rollup.unknown > 0 ? (
          <div
            role="group"
            aria-label="未知状态"
            className="rounded-md border border-warning bg-warning/10 px-3 py-2"
            title="前端不认识的投递状态，多半是后端新增了取值"
          >
            <p className="text-xs text-fg-muted">未知状态</p>
            <p className="text-lg font-semibold text-warning">{rollup.unknown}</p>
          </div>
        ) : null}
      </div>

      {/* 这一页存在的理由。放在计数下面、表格上面：先看见「有没有人漏掉」，
          再去表里找是哪几条。 */}
      {rollup.unreachedActive.length > 0 ? (
        <p role="status" className="mb-3 rounded-md border border-danger bg-danger/10 px-3 py-2 text-xs text-danger">
          有 {rollup.unreachedActive.length} 条还没解决的告警没有送达任何人。
          在没有人收到通知的这段时间里，告警等于不存在——先确认渠道配置与投递失败原因。
        </p>
      ) : null}
    </>
  );
}

function notifyColumns(): DataTableColumn<AlertItem>[] {
  return [
    {
      id: "notify",
      header: "投递",
      primary: true,
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
            ) : (
              // 空着会读成「刚刚投的」。没有时刻就是没投出去过，说出来。
              <span className="text-xs text-fg-muted">从未投递成功</span>
            )}
          </div>
        );
      },
    },
    {
      id: "reason",
      header: "失败原因",
      // 原样显示：它已由服务端脱敏（SanitizeNotifyError，绝不含 Bot Token），
      // 而「为什么没投出去」是这一页唯一要给出的答案
      value: (alert) => alert.notify_error,
      cell: (alert) =>
        alert.notify_error ? (
          <span className="text-xs break-all text-danger">{alert.notify_error}</span>
        ) : (
          <span className="text-xs text-fg-muted">—</span>
        ),
    },
    {
      id: "title",
      header: "告警",
      value: (alert) => [alert.title, ruleLabel(alert.rule_key), alert.source_metric_key].filter(Boolean).join(" "),
      cell: (alert) => (
        <>
          <span className="font-medium">{alert.title}</span>
          <p className="mt-0.5 text-xs text-fg-muted">{ruleLabel(alert.rule_key)}</p>
        </>
      ),
    },
    {
      id: "severity",
      header: "严重度",
      value: (alert) => describeSeverity(alert.severity).label,
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
      header: "告警状态",
      // 与投递状态**正交**，两列都要：一条 RESOLVED 却从未投递出去说明这次
      // 故障全程没人知道，而只看投递列会把它读成一条无关紧要的旧记录
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
  ];
}

function NotifyDeliveryTable({ items }: { items: AlertItem[] }) {
  return (
    <DataTableV2
      caption="通知投递：投递状态与时刻、失败原因、对应告警的严重度与状态"
      columns={notifyColumns()}
      rows={sortForDisplay(items)}
      rowKey={(alert) => alert.id}
      pageSize={20}
      searchable
      filters={[
        { columnId: "notify", label: "投递", options: ["已投递", "未投递", "投递失败"] },
        { columnId: "severity", label: "严重度", options: ["严重", "警告", "提示"] },
      ]}
      emptyState={
        <PageState
          kind="empty"
          title="暂无可查的投递记录"
          description="该环境下还没有产生过告警，因此也没有任何投递。这不代表通知渠道配好了——第一条告警发出来之前，渠道有没有配对是看不出来的。"
        />
      }
    />
  );
}
