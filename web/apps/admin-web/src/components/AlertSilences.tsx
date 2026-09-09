import { useQuery } from "@tanstack/react-query";
import {
  DataTableV2,
  formatUtcTimestamp,
  PageHeader,
  PageState,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import {
  listSilencesPage,
  ruleLabel,
  SILENCE_STATE_ALL,
  type SilenceItem,
} from "../api/alerts";
import {
  describeSilenceState,
  formatSilenceRemaining,
  GLOBAL_SILENCE_RULE_LABEL,
  sortSilencesForDisplay,
} from "../lib/alerts";
import { OVERVIEW_POLL_INTERVAL_MS, useAutoRefresh } from "../lib/autoRefresh";
import { ApiStateView } from "./ApiStateView";

/** 「生效中」与「全部」各自一格缓存。
 *
 *  两格而不是一格，是因为这一页要发两次请求，理由见 AlertSilences 的注释。 */
const SILENCES_ACTIVE_QUERY_KEY = ["alerts", "silences", "active"] as const;
const SILENCES_ALL_QUERY_KEY = ["alerts", "silences", "all"] as const;

/** 暂停告警（静默窗口，规格 §9.3「静默」）。
 *
 *  这一页此前落在通用占位「尚未接入稳定的数据源」上，而那句话当时是对的：
 *  静默**只能建不能看**——后端有 ListActiveSilences / ListSilences 两个方法，
 *  却没有任何列出端点。XM-SILENCE-LIST 把端点补上，这一页才建得起来。
 *
 *  它只回答一个问题：**现在有什么在压着告警**。一个正在生效的静默窗口意味着
 *  命中的规则不会投递给任何人；看不见的静默窗口比看得见的危险得多。
 *
 *  发两次请求（state=active 与 state=all）而不是取一次全部再在前端分组：
 *  含历史的那条按 starts_at 倒序取前 N 条，一批「未开始」的窗口排在最前面时，
 *  正在生效的那条可能被挤出这一页——于是页面会说「当前没有静默生效」，
 *  而实际上告警正被压着。那是这一页唯一不能出的错，多一次请求换掉它划算。 */
export function AlertSilences() {
  const activeQuery = useQuery({
    queryKey: SILENCES_ACTIVE_QUERY_KEY,
    // 不传 state：服务端默认就是「只看此刻生效的」，走的是带时间条件的那条 SQL。
    queryFn: ({ signal }) => listSilencesPage({ signal }),
  });
  const allQuery = useQuery({
    queryKey: SILENCES_ALL_QUERY_KEY,
    queryFn: ({ signal }) => listSilencesPage({ signal, state: SILENCE_STATE_ALL }),
  });

  const refresh = () => {
    void activeQuery.refetch();
    void allQuery.refetch();
  };
  useAutoRefresh(refresh);

  const activeItems = activeQuery.data?.items ?? [];
  const allItems = allQuery.data?.items ?? [];
  const asOf = activeQuery.data?.as_of ?? "";

  return (
    <section>
      <PageHeader
        title="暂停告警"
        description={`静默窗口：窗口内命中的规则不投递给任何人。每 ${OVERVIEW_POLL_INTERVAL_MS / 1000} 秒自动刷新（页面不可见时暂停）。`}
        onRefresh={refresh}
        refreshing={activeQuery.isFetching || allQuery.isFetching}
        lastRefreshedAt={activeQuery.dataUpdatedAt || undefined}
      />

      {/* 「现在压着几条」单独一块，且用「生效中」那条查询的结果。
          它是这一页存在的理由，不能跟着下面那张表一起被条数上限影响。 */}
      <ApiStateView isPending={activeQuery.isPending} error={activeQuery.error} onRetry={refresh}>
        <ActiveSilenceBanner count={activeItems.length} asOf={asOf} />
      </ApiStateView>

      <SilenceSourceNote />

      <ApiStateView isPending={allQuery.isPending} error={allQuery.error} onRetry={refresh}>
        {allQuery.data?.truncated ? (
          <p role="status" className="mb-2 text-xs text-warning">
            这一页可能不是全部：服务端一次最多返回 {allQuery.data.limit || "若干"} 条，本次正好取满。
            上面的「生效中」计数不受这条影响——它来自单独一次只查生效窗口的请求。
          </p>
        ) : null}
        <SilenceTable items={allItems} asOf={asOf} />
      </ApiStateView>
    </section>
  );
}

/** 「此刻有没有告警被压着」的一句话结论。 */
function ActiveSilenceBanner({ count, asOf }: { count: number; asOf: string }) {
  const suffix = asOf ? `（截至 ${formatUtcTimestamp(asOf)}）` : "";
  if (count === 0) {
    return (
      <p
        role="status"
        className="mb-3 rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted"
      >
        当前没有静默窗口生效，所有规则都会正常投递{suffix}。
      </p>
    );
  }
  return (
    <p
      role="status"
      className="mb-3 rounded-md border border-danger bg-danger/10 px-3 py-2 text-xs text-danger"
    >
      现在有 {count} 个静默窗口正在压着告警{suffix}：窗口内命中的规则不会通知任何人。
      下表中标着「生效中」的就是它们。
    </p>
  );
}

/** 数据来源与两条必须说清的局限。 */
function SilenceSourceNote() {
  return (
    <div className="mb-3 rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted">
      <p>
        窗口来自 <code className="font-mono">GET /api/v1/alerts/silences</code>，
        「生效中 / 未开始 / 已过期」由服务端按同一个判据算出
        （与投递侧压制告警用的是同一段代码），前端不自行比对时间。
      </p>
      <ul className="mt-1 list-disc space-y-0.5 pl-4">
        <li>
          <b className="text-fg">窗口只会自己到期，没有「取消」</b>
          ——平台今天没有撤销静默的能力。按早了就只能等它过期。
        </li>
        <li>
          创建窗口在「告警」子页签的「暂停告警」按钮，是一个需要理由的 L1 操作，
          每一次都会进审计。
        </li>
      </ul>
    </div>
  );
}

function silenceColumns(asOf: string): DataTableColumn<SilenceItem>[] {
  return [
    {
      id: "state",
      header: "状态",
      primary: true,
      value: (item) => describeSilenceState(item.state).label,
      cell: (item) => {
        const display = describeSilenceState(item.state);
        const remaining = item.state === "active" ? formatSilenceRemaining(item.ends_at, asOf) : "";
        return (
          <div className="flex flex-col gap-1">
            <Badge tone={display.tone} title={display.hint}>
              {display.label}
            </Badge>
            {remaining ? <span className="text-xs text-fg-muted">{remaining}</span> : null}
          </div>
        );
      },
    },
    {
      id: "rule",
      header: "被压住的规则",
      value: (item) => (item.rule_key === "" ? GLOBAL_SILENCE_RULE_LABEL : ruleLabel(item.rule_key)),
      cell: (item) =>
        item.rule_key === "" ? (
          // 全局窗口压住的是**所有**规则，是爆炸半径最大的那一种。
          // 显示成空白或原始空串会让它看起来像一条普通的单规则窗口。
          <span className="font-medium text-danger">{GLOBAL_SILENCE_RULE_LABEL}</span>
        ) : (
          <>
            <span className="font-medium">{ruleLabel(item.rule_key)}</span>
            <p className="mt-0.5 font-mono text-xs text-fg-muted">{item.rule_key}</p>
          </>
        ),
    },
    {
      id: "reason",
      header: "理由",
      // 理由是硬约束（后端与库层都挡住空理由）：没有理由的静默在事后复盘时
      // 与「有人手滑」不可区分，所以它一定有值，直接显示。
      value: (item) => item.reason,
      cell: (item) => <span className="text-xs break-all">{item.reason}</span>,
    },
    {
      id: "created_by",
      header: "谁按的",
      value: (item) => item.created_by,
      cell: (item) => <span className="font-mono text-xs">{item.created_by}</span>,
    },
    {
      id: "window",
      header: "起止时间",
      value: (item) => `${item.starts_at} ${item.ends_at}`,
      cell: (item) => (
        <div className="flex flex-col text-xs text-fg-muted">
          <span>起 {formatUtcTimestamp(item.starts_at)}</span>
          <span>止 {formatUtcTimestamp(item.ends_at)}</span>
        </div>
      ),
    },
  ];
}

function SilenceTable({ items, asOf }: { items: SilenceItem[]; asOf: string }) {
  return (
    <DataTableV2
      caption="静默窗口：此刻是否生效、压住哪条规则、理由、谁按的、起止时间"
      columns={silenceColumns(asOf)}
      rows={sortSilencesForDisplay(items)}
      rowKey={(item) => item.id}
      pageSize={20}
      searchable
      filters={[{ columnId: "state", label: "状态", options: ["生效中", "未开始", "已过期"] }]}
      emptyState={
        <PageState
          kind="empty"
          title="这个环境还没有人按过暂停"
          description="没有任何静默窗口（包括已过期的）。所有规则命中后都会正常投递。"
        />
      }
    />
  );
}
