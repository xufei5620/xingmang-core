import { useInfiniteQuery } from "@tanstack/react-query";
import { Badge, Button, EmptyState } from "@xingmang/ui-primitives";
import { Fragment, useState } from "react";
import { AUDIT_PAGE_SIZE, listAuditEvents, type AuditEventItem } from "../api/platform";
import { ApiStateView } from "../components/ApiStateView";
import { PageHeader } from "../components/PageHeader";
import {
  chainLinkBetween,
  describeChainLink,
  formatSummary,
  toAuditRow,
  type ChainLink,
} from "../lib/audit";

const TH = "px-3 py-2 text-left text-xs font-medium text-fg-muted";
const TD = "px-3 py-2 align-top text-sm text-fg";

/** 审计事件页（规格 §4.4 / ADR-013：哈希链）。
 *
 *  这一页把每条事件的 event_hash 与 prev_hash 摆出来，让人看得见链的形状；
 *  它**不是**校验工具。后端的链是全局的，本页按环境过滤，序号出现缺口是正常的，
 *  这时相邻两行的哈希本就不必相等（Codex #6）；响应也不含 canonical 全字段，
 *  本页根本算不出 hash。所以：序号真的相邻时才做比对，其余情形只如实说明，
 *  完整校验以 audit-verify 工具与链根签名为准。 */
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
          {events.map((event, index) => {
            const row = toAuditRow(event);
            const open = expanded.has(row.sequence);
            // 列表倒序：紧跟其后的那一行序号更小，才是链上「上一条」的候选
            const link = chainLinkBetween(event, events[index + 1]);
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
                    <ChainCell row={row} link={link} />
                  </td>
                  <td className={TD}>
                    {/* 展开按钮不再以「有没有摘要」为条件：完整哈希也在里面，
                        而哈希是每行都有的（Codex #9：不能只给 8 位前缀 + hover） */}
                    <Button
                      variant="ghost"
                      size="sm"
                      aria-expanded={open}
                      onClick={() => toggle(row.sequence)}
                    >
                      {open ? "收起" : "详情"}
                    </Button>
                  </td>
                </tr>
                {open ? (
                  <tr className="border-b border-edge bg-surface-muted last:border-b-0">
                    <td className={TD} colSpan={8}>
                      <DetailPanel event={event} link={link} />
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
