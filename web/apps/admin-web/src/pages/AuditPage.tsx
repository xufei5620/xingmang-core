import { useInfiniteQuery } from "@tanstack/react-query";
import {
  DataTableV2,
  navItemByPath,
  navLabel,
  PageHeader,
  PageState,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge, Button, Tabs } from "@xingmang/ui-primitives";
import { AUDIT_PAGE_SIZE, listAuditEvents, type AuditEventItem } from "../api/platform";
import { ApiStateView } from "../components/ApiStateView";
import { PersistentDataTable, SAVED_VIEW_TABLE_KEYS } from "../components/PersistentDataTable";
import {
  chainLinkBetween,
  describeChainLink,
  formatSummary,
  toAuditRow,
  type ChainLink,
} from "../lib/audit";
import { Link, useSearchParams } from "react-router";

const AUDIT_SUB_TABS = (navItemByPath("/audit")?.item.subTabs ?? []).map(
  (tab) => [tab.id, tab.label] as const,
);

/** 审计事件页（规格 §4.4 / ADR-013：哈希链）。
 *
 *  这一页把每条事件的 event_hash 与 prev_hash 摆出来，让人看得见链的形状；
 *  它**不是**校验工具。后端的链是全局的，本页按环境过滤，序号出现缺口是正常的，
 *  这时相邻两行的哈希本就不必相等（Codex #6）；响应也不含 canonical 全字段，
 *  本页根本算不出 hash。所以：序号真的相邻时才做比对，其余情形只如实说明，
 *  完整校验以 audit-verify 工具与链根签名为准。 */
export function AuditPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const rawSub = searchParams.get("sub");
  const activeSub = rawSub === null || rawSub.trim() === "" ? "events" : rawSub;
  const known = AUDIT_SUB_TABS.some(([value]) => value === activeSub);

  if (!known) {
    return (
      <section>
        <PageHeader
          title={navLabel("/audit")}
          description="审计记录、操作证据与审计链验证分开呈现；未知子页不会回落到事件列表。"
        />
        <PageState
          kind="unavailable"
          title={`「${rawSub}」子页尚未接入`}
          description="请从已定义的审计子页中选择；系统不会把未知地址误当成审计事件。"
          action={
            <Link
              to="/audit?sub=events"
              className="text-sm font-medium text-accent hover:underline"
            >
              返回审计记录
            </Link>
          }
        />
      </section>
    );
  }

  const selectSub = (value: string) => {
    const next = new URLSearchParams(searchParams);
    next.set("sub", value);
    setSearchParams(next, { replace: true });
  };

  return (
    <Tabs
      value={activeSub}
      onValueChange={selectSub}
      items={AUDIT_SUB_TABS.map(([value, label]) => ({
        value,
        label,
        content:
          value === "events" ? (
            <AuditEventsPage />
          ) : (
            <AuditUnavailablePage tabId={value} label={label} />
          ),
      }))}
    />
  );
}

function AuditEventsPage() {
  const query = useInfiniteQuery({
    queryKey: ["audit-events"],
    queryFn: ({ signal, pageParam }) =>
      listAuditEvents({
        signal,
        ...(pageParam === undefined ? {} : { beforeSeq: pageParam }),
      }),
    // 首页不带游标：从最新一条开始
    initialPageParam: undefined as number | undefined,
    // next_before 为 null 表示到底了，react-query 据此禁用「加载更多」
    getNextPageParam: (last) => last.nextBefore ?? undefined,
  });

  const events = (query.data?.pages ?? []).flatMap((p) => p.items);

  return (
    <section>
      <PageHeader
        title={navLabel("/audit")}
        description={`按序号倒序，每次加载 ${AUDIT_PAGE_SIZE} 条；每行给出事件哈希与它记录的前序哈希。本页按环境过滤的是一条全局链，序号出现缺口属正常，缺口两侧的哈希不必相等。完整性校验以 audit-verify 工具与链根签名为准，本页仅展示。`}
        onRefresh={() => void query.refetch()}
        refreshing={query.isFetching && !query.isFetchingNextPage}
        lastRefreshedAt={query.dataUpdatedAt || undefined}
      />
      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        <AuditView
          events={events}
          hasMore={query.hasNextPage}
          loadingMore={query.isFetchingNextPage}
          onLoadMore={() => void query.fetchNextPage()}
        />
      </ApiStateView>
    </section>
  );
}

function AuditUnavailablePage({ tabId, label }: { tabId: string; label: string }) {
  const chainOnly = tabId === "chain";
  return (
    <section>
      <PageHeader
        title={label}
        description={
          chainOnly
            ? "审计链验证属于受控 CLI 校验，不在浏览器请求链中执行全链扫描。"
            : "操作证据的归档与检索尚未接入控制台，本页不连接对象存储，也不创建或修改任何记录。"
        }
      />
      <PageState
        kind="unavailable"
        title={chainOnly ? "审计链验证仅支持 CLI" : "操作证据尚未接入"}
        description={
          chainOnly
            ? "当前页面只展示审计事件；完整性校验请在受控环境运行 audit-verify，并核对链根签名。"
            : "目前可用的操作证据仍以审计事件中的前后摘要与 request_id 为准；对象、manifest 与冷读接口尚未建立。"
        }
        footnote={chainOnly ? "audit-verify（CLI-only）" : "UI-only read surface · no object-store access"}
      />
    </section>
  );
}

interface AuditViewProps {
  events: AuditEventItem[];
  hasMore: boolean;
  loadingMore: boolean;
  onLoadMore: () => void;
}

/** 一行 = 一条审计事件 + 它与**序号上的前一条**的链关系。
 *
 *  链关系在进表之前就算好，而不是在渲染时看「下一行是谁」：表格可以被排序,
 *  排完之后相邻的行不再是序号相邻的行。先算好之后，每一行说的都是它自己与
 *  自己前序的关系——按任何一列排序，这句话都还成立。 */
interface AuditRowModel {
  event: AuditEventItem;
  row: ReturnType<typeof toAuditRow>;
  link: ChainLink;
}

function buildAuditRows(events: AuditEventItem[]): AuditRowModel[] {
  return events.map((event, index) => ({
    event,
    row: toAuditRow(event),
    // 列表倒序：紧跟其后的那一行序号更小，才是链上「上一条」的候选
    link: chainLinkBetween(event, events[index + 1]),
  }));
}

const AUDIT_COLUMNS: DataTableColumn<AuditRowModel>[] = [
  {
    id: "sequence",
    header: "序号",
    primary: true,
    numeric: true,
    value: ({ row }) => row.sequence,
    cell: ({ row }) => <span className="font-mono">{row.sequence}</span>,
  },
  {
    id: "time",
    header: "时间",
    value: ({ event }) => event.occurred_at,
    // 正文本地时间（人拿它和自己的记忆对），权威 UTC 在悬停里
    cell: ({ row }) => <span title={row.utcTime}>{row.localTime}</span>,
  },
  {
    id: "principal",
    header: "主体",
    value: ({ row }) => `${row.principalId} ${row.principalType}`,
    cell: ({ row }) => (
      <>
        <span className="font-medium">{row.principalId}</span>
        {row.principalType ? <p className="text-xs text-fg-muted">{row.principalType}</p> : null}
      </>
    ),
  },
  {
    id: "action",
    header: "动作",
    value: ({ row }) => `${row.action} ${row.runId}`,
    cell: ({ row }) => (
      <>
        <span className="font-mono text-xs">{row.action}</span>
        {row.runId ? (
          <p className="font-mono text-xs text-fg-muted" title={`run_id ${row.runId}`}>
            run {row.runId.slice(0, 8)}
          </p>
        ) : null}
      </>
    ),
  },
  {
    id: "resource",
    header: "资源",
    value: ({ row }) => `${row.resource} ${row.environment}`,
    cell: ({ row }) => (
      <>
        <span className="font-mono text-xs break-all">{row.resource}</span>
        {row.environment ? <p className="text-xs text-fg-muted">{row.environment}</p> : null}
      </>
    ),
  },
  {
    id: "result",
    header: "结果",
    value: ({ row }) => `${row.resultLabel} ${row.errorCode}`,
    cell: ({ row }) => (
      <>
        <Badge tone={row.resultTone}>{row.resultLabel}</Badge>
        {row.errorCode ? <p className="font-mono text-xs text-danger">{row.errorCode}</p> : null}
      </>
    ),
  },
  {
    id: "hash",
    header: "哈希",
    value: ({ row }) => row.hashShort,
    cell: ({ row, link }) => <ChainCell row={row} link={link} />,
  },
];

function AuditView({ events, hasMore, loadingMore, onLoadMore }: AuditViewProps) {
  return (
    <PersistentDataTable
      tableKey={SAVED_VIEW_TABLE_KEYS.auditEvents}
      caption="审计事件：按序号倒序，每行给出事件哈希与它记录的前序哈希"
      columns={AUDIT_COLUMNS}
      rows={buildAuditRows(events)}
      rowKey={({ row }) => String(row.sequence)}
      // 不传 pageSize：这一页自己在按游标翻（「加载更多」），
      // 再叠一层客户端分页，人要点两种「下一页」而它们翻的不是同一批东西
      searchable
      filters={[{ columnId: "result", label: "结果", options: ["成功", "失败"] }]}
      renderExpanded={({ event, link }) => <DetailPanel event={event} link={link} />}
      footerExtra={
        hasMore ? (
          <Button variant="secondary" size="sm" onClick={onLoadMore} loading={loadingMore}>
            加载更多
          </Button>
        ) : (
          <span>已到最早一条</span>
        )
      }
      emptyState={
        <PageState kind="empty" title="还没有审计事件" description="在资源目录页执行一次动作试试" />
      }
    />
  );
}

/** 链关系标记的语气 → 令牌类。danger 只留给「序号相邻却对不上」这一种真信号。 */
const LINK_TONE_CLASS: Record<"neutral" | "success" | "danger", string> = {
  neutral: "text-fg-muted",
  success: "text-success",
  danger: "text-danger",
};

/** 哈希单元格：本行事件哈希前缀 + 与相邻行的关系。
 *
 *  完整哈希放在展开区里（不是只有 hover title），因为它是这一页唯一能拿去
 *  跟 audit-verify 对账的东西，而键盘用户碰不到 title。 */
function ChainCell({ row, link }: { row: ReturnType<typeof toAuditRow>; link: ChainLink }) {
  const shown = describeChainLink(link);
  return (
    <div className="flex flex-col gap-0.5">
      <span className="font-mono text-xs">{row.hashShort}</span>
      <span className={`text-xs ${LINK_TONE_CLASS[shown.tone]}`}>{shown.label}</span>
    </div>
  );
}

function DetailPanel({ event, link }: { event: AuditEventItem; link: ChainLink }) {
  const shown = describeChainLink(link);
  return (
    <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
      <div className="min-w-0 md:col-span-2">
        <p className="mb-1 text-xs font-medium text-fg">哈希链</p>
        <dl className="flex flex-col gap-1 text-xs">
          <div className="flex flex-col gap-0.5">
            <dt className="text-fg-muted">事件哈希 event_hash</dt>
            <dd className="font-mono break-all text-fg">{event.event_hash || "（缺失）"}</dd>
          </div>
          <div className="flex flex-col gap-0.5">
            <dt className="text-fg-muted">前序哈希 prev_hash</dt>
            <dd className="font-mono break-all text-fg">{event.prev_hash || "（缺失）"}</dd>
          </div>
        </dl>
        <p className={`mt-1 text-xs ${LINK_TONE_CLASS[shown.tone]}`}>
          {shown.label}：{shown.detail}
        </p>
      </div>
      <SummaryBlock title="变更前" summary={event.before_summary} />
      <SummaryBlock title="变更后" summary={event.after_summary} />
      {event.request_id ? (
        <p className="font-mono text-xs text-fg-muted md:col-span-2">
          request_id: {event.request_id}
        </p>
      ) : null}
    </div>
  );
}

function SummaryBlock({
  title,
  summary,
}: {
  title: string;
  summary: Record<string, unknown> | null;
}) {
  return (
    <div className="min-w-0">
      <p className="mb-1 text-xs font-medium text-fg">{title}</p>
      {/* 摘要原样显示成 JSON：这是进了哈希的那份内容，任何「美化」都可能
          让人以为链里存的是另一个形状 */}
      <pre className="overflow-x-auto rounded-md border border-edge bg-surface p-2 font-mono text-xs text-fg">
        {formatSummary(summary)}
      </pre>
    </div>
  );
}
