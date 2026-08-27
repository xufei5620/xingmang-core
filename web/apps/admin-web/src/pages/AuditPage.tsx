import { useInfiniteQuery } from "@tanstack/react-query";
import { Badge, Button, EmptyState } from "@xingmang/ui-primitives";
import { Fragment, useState } from "react";
import { AUDIT_PAGE_SIZE, listAuditEvents, type AuditEventItem } from "../api/platform";
import { ApiStateView } from "../components/ApiStateView";
import { PageHeader } from "../components/PageHeader";
import { formatSummary, toAuditRow } from "../lib/audit";

const TH = "px-3 py-2 text-left text-xs font-medium text-fg-muted";
const TD = "px-3 py-2 align-top text-sm text-fg";

/** 审计事件页（规格 §4.4 / ADR-013：哈希链）。
 *
 *  这一页存在的理由不是「有个列表好看」，而是让哈希链**可被人验证**：
 *  每行给出自己的 event_hash 与它记录的 prev_hash，倒序排列时前序哈希应当
 *  等于下一行的事件哈希。对不上就意味着链断了，这件事必须肉眼可查，
 *  而不是藏在一个只有后端才会跑的校验任务里。 */
export function AuditPage() {
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
        title="审计事件"
        description={`按序号倒序，每次加载 ${AUDIT_PAGE_SIZE} 条；每行给出事件哈希与它记录的前序哈希，用于核对哈希链是否连续。`}
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

interface AuditViewProps {
  events: AuditEventItem[];
  hasMore: boolean;
  loadingMore: boolean;
  onLoadMore: () => void;
}

function AuditView({ events, hasMore, loadingMore, onLoadMore }: AuditViewProps) {
  if (events.length === 0) {
    return (
      <EmptyState title="还没有审计事件" description="在服务清单页执行一次动作试试" />
    );
  }
  return (
    <div className="flex flex-col gap-3">
      <AuditTable events={events} />
      <div className="flex items-center justify-center gap-3">
        {hasMore ? (
          <Button variant="secondary" size="sm" onClick={onLoadMore} loading={loadingMore}>
            加载更多
          </Button>
        ) : (
          <span className="text-xs text-fg-muted">已到最早一条（共 {events.length} 条）</span>
        )}
      </div>
    </div>
  );
}

function AuditTable({ events }: { events: AuditEventItem[] }) {
  // 展开态按 sequence 记：翻页追加数据时下标会变，用下标记会让展开跳到别的行上
  const [expanded, setExpanded] = useState<ReadonlySet<number>>(new Set());
  const toggle = (sequence: number) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (!next.delete(sequence)) next.add(sequence);
      return next;
    });

  return (
    <div className="overflow-x-auto rounded-lg border border-edge bg-surface shadow-sm">
      <table className="w-full border-collapse">
        <thead className="border-b border-edge bg-surface-muted">
          <tr>
            <th className={TH}>序号</th>
            <th className={TH}>时间</th>
            <th className={TH}>主体</th>
            <th className={TH}>动作</th>
            <th className={TH}>资源</th>
            <th className={TH}>结果</th>
            <th className={TH}>哈希</th>
            <th className={TH}>
              <span className="sr-only">详情</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {events.map((event) => {
            const row = toAuditRow(event);
            const open = expanded.has(row.sequence);
            return (
              <Fragment key={row.sequence}>
                <tr className="border-b border-edge last:border-b-0">
                  <td className={`${TD} font-mono tabular-nums`}>{row.sequence}</td>
                  <td className={TD}>
                    {/* 正文本地时间（人拿它和自己的记忆对），权威 UTC 在悬停里 */}
                    <span title={row.utcTime}>{row.localTime}</span>
                  </td>
                  <td className={TD}>
                    <span className="font-medium">{row.principalId}</span>
                    {row.principalType ? (
                      <p className="text-xs text-fg-muted">{row.principalType}</p>
                    ) : null}
                  </td>
                  <td className={TD}>
                    <span className="font-mono text-xs">{row.action}</span>
                    {row.runId ? (
                      <p className="font-mono text-xs text-fg-muted" title={`run_id ${row.runId}`}>
                        run {row.runId.slice(0, 8)}
                      </p>
                    ) : null}
                  </td>
                  <td className={TD}>
                    <span className="font-mono text-xs break-all">{row.resource}</span>
                    {row.environment ? (
                      <p className="text-xs text-fg-muted">{row.environment}</p>
                    ) : null}
                  </td>
                  <td className={TD}>
                    <Badge tone={row.resultTone}>{row.resultLabel}</Badge>
                    {row.errorCode ? (
                      <p className="font-mono text-xs text-danger">{row.errorCode}</p>
                    ) : null}
                  </td>
                  <td className={TD}>
                    <ChainCell row={row} />
                  </td>
                  <td className={TD}>
                    {row.hasDetail ? (
                      <Button
                        variant="ghost"
                        size="sm"
                        aria-expanded={open}
                        onClick={() => toggle(row.sequence)}
                      >
                        {open ? "收起" : "前后摘要"}
                      </Button>
                    ) : (
                      <span className="text-xs text-fg-muted">无摘要</span>
                    )}
                  </td>
                </tr>
                {open ? (
                  <tr className="border-b border-edge bg-surface-muted last:border-b-0">
                    <td className={TD} colSpan={8}>
                      <SummaryPanel event={event} />
                    </td>
                  </tr>
                ) : null}
              </Fragment>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

/** 哈希单元格：本行事件哈希 + 它记录的前序哈希。
 *
 *  列表是倒序的，所以「前序」指的是**下一行**。把这句话写进 title 而不是
 *  指望人自己推断——一条对不上的链是事故信号，不该靠猜。 */
function ChainCell({ row }: { row: ReturnType<typeof toAuditRow> }) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="font-mono text-xs" title={row.hashFull || "事件哈希缺失"}>
        {row.hashShort}
      </span>
      {row.isGenesis ? (
        <span className="text-xs text-fg-muted" title="链首事件，prev_hash 为全 0">
          链首
        </span>
      ) : (
        <span
          className="font-mono text-xs text-fg-muted"
          title={`前序哈希 ${row.prevHashFull}；列表按序号倒序，它应当与下一行的事件哈希相同`}
        >
          ↓ {row.prevHashShort}
        </span>
      )}
    </div>
  );
}

function SummaryPanel({ event }: { event: AuditEventItem }) {
  return (
    <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
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
