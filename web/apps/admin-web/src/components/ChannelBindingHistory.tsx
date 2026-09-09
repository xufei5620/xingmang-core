import { useQuery } from "@tanstack/react-query";
import { formatUtcTimestamp, PageState } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import {
  channelBindingHistoryStateNote,
  getChannelBindingHistory,
  CHANNEL_BINDING_HISTORY_QUERY_KEY,
  type ChannelBindingHistoryResult,
  type ChannelBindingRecord,
} from "../api/platformChannelBindings";
import { ApiStateView } from "./ApiStateView";

/** `provenance` 的两个取值。库层 CHECK 约束把它锁死在这两个上
 *  （db/migrations/000016_finance_platform_channel_binding.up.sql:32），
 *  所以这里不需要兜底成「其它」——但**认不出来的值原样显示**，不吞掉：
 *  哪天库里多出第三种，屏幕上要能看见那个陌生的词，而不是显示成一个
 *  看起来很正常的中文标签。 */
const PROVENANCE_LABELS: Readonly<Record<string, string>> = {
  manual: "人工确认",
  token_map_backfill: "令牌映射回填",
};

/** 绑定历史（`GET /api/v1/finance/platform-channel-bindings?include_history=true`）。
 *
 *  这条端点从挂载起就没有任何前端调用方——「上游映射」卡片显示的一直是
 *  **当前态**（来自渠道目录 `/platforms/{platform}/channels`），改绑过几次、
 *  上一次绑的是谁，页面上一个字都没有。这一块把它补上。
 *
 *  ── 这份数据能回答什么、不能回答什么 ────────────────────────────────────
 *  能：这条渠道**先后被绑到过哪些上游账号**、每一次从什么时候开始生效、
 *      当时写的理由是什么、是谁写的、是人工确认还是令牌映射回填。
 *  不能：
 *    1. **每一次绑定什么时候结束**——响应结构里没有 `valid_to`（库表有这一
 *       列，`channelBindingResponse` 没有序列化它）。也**不能**拿下一条的
 *       生效时间去推：解绑之后可以长期没有任何绑定，那段空档在这份数据里
 *       完全不可见。
 *    2. **解绑这件事本身**——解绑只把当前那条的 `valid_to` 写上，不插入新
 *       行（finance/channel_binding_store.go 的 `Remove`），所以历史里不会
 *       多出一条「已解绑」；解绑时写的理由也不在这张表里（`Close` 只改
 *       valid_to），它在审计链上。
 *
 *  这两条不写在屏幕上的话，一份「最后一条是绑到 X」的列表会被读成「现在绑
 *  在 X 上」——而实际可能早就解绑了。所以下面的说明段是必须的，不是装饰。 */
export function ChannelBindingHistory({
  serviceId,
  externalChannelId,
}: {
  serviceId: string;
  externalChannelId: string;
}) {
  const query = useQuery({
    queryKey: [CHANNEL_BINDING_HISTORY_QUERY_KEY, serviceId, externalChannelId],
    queryFn: ({ signal }) =>
      getChannelBindingHistory({ serviceId, externalChannelId }, { signal }),
  });

  return (
    <section className="mt-4 border-t border-edge pt-3">
      <header className="mb-2 flex flex-wrap items-baseline justify-between gap-2">
        <h4 className="text-sm font-medium text-fg">绑定历史</h4>
        <p className="text-xs text-fg-muted">
          GET /api/v1/finance/platform-channel-bindings（include_history）
        </p>
      </header>

      <p className="mb-2 text-xs leading-5 text-fg-muted">
        每一行是<span className="text-fg">一次绑定的建立</span>，最新的在最前。
        这份数据里没有「失效时间」，也<span className="text-fg">不包含解绑</span>
        ——解绑只关闭当前那条记录、不新增行，理由记在审计链上。所以
        <span className="text-fg">不要用下一行的生效时间当作上一行的结束时间</span>：
        解绑之后可能长期没有任何绑定，那段空档在这里看不出来。当前生效的是哪一条，
        以带「当前生效」标记的那一行为准。
      </p>

      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
        compact
      >
        {query.data ? (
          <ChannelBindingHistoryBody result={query.data} externalChannelId={externalChannelId} />
        ) : null}
      </ApiStateView>
    </section>
  );
}

function ChannelBindingHistoryBody({
  result,
  externalChannelId,
}: {
  result: ChannelBindingHistoryResult;
  externalChannelId: string;
}) {
  if (result.state !== "ok") {
    return (
      <PageState
        kind="unavailable"
        compact
        title="没有取到这条渠道的绑定历史"
        description={channelBindingHistoryStateNote(result, externalChannelId)}
      />
    );
  }

  if (result.history.length === 0) {
    // empty 而不是 unavailable：查到了，里面确实没有——这条渠道从来没有被
    // 确认过上游映射。两者混起来就是让人以为「这块我们还没接」
    return (
      <PageState
        kind="empty"
        compact
        title="这条渠道没有绑定记录"
        description="从来没有确认过上游映射。这是查出来的事实，不是尚未接入。"
      />
    );
  }

  return (
    <ol className="flex flex-col gap-2">
      {result.history.map((record) => (
        <li key={record.id}>
          <ChannelBindingHistoryRow
            record={record}
            isCurrent={record.id === result.currentBindingId}
          />
        </li>
      ))}
    </ol>
  );
}

function ChannelBindingHistoryRow({
  record,
  isCurrent,
}: {
  record: ChannelBindingRecord;
  isCurrent: boolean;
}) {
  return (
    <article className="rounded-md border border-edge bg-surface-muted p-3">
      <header className="flex flex-wrap items-baseline justify-between gap-2">
        <p className="font-mono text-xs break-all text-fg">{record.upstreamAccountId || "—"}</p>
        {isCurrent ? <Badge tone="success">当前生效</Badge> : null}
      </header>
      <dl className="mt-1.5 grid grid-cols-[5rem_1fr] gap-x-3 gap-y-1 text-xs">
        <dt className="text-fg-muted">生效时间</dt>
        <dd className="text-fg">{formatUtcTimestamp(record.validFrom || null)}</dd>
        <dt className="text-fg-muted">来源</dt>
        <dd className="text-fg">{PROVENANCE_LABELS[record.provenance] ?? (record.provenance || "—")}</dd>
        <dt className="text-fg-muted">操作人</dt>
        <dd className="break-all text-fg">{record.createdBy || "—"}</dd>
        <dt className="text-fg-muted">理由</dt>
        <dd className="break-all text-fg">{record.reason || "—"}</dd>
      </dl>
    </article>
  );
}
