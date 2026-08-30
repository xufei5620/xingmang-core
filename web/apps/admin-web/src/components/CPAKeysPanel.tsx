import { useQuery } from "@tanstack/react-query";
import {
  DataTableV2,
  FreshnessBadge,
  FreshnessNote,
  PageState,
  formatUtcTimestamp,
  type DataTableColumn,
  type FreshnessContract,
} from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { listCPAKeys, type CPAKeyUsageItem, type CPAKeyUsagePage, type MoneyBody } from "../api/cpa";
import { formatScaledMinorUnits, formatCount, toIntegerValue } from "../lib/money";
import { ApiStateView } from "./ApiStateView";

/** CPA「用户管理」页签(原型字面保留这个名字——ADMIN-IA §8.6 裁定 #5;
 *  这里的"用户"其实是 API Key,不是终端用户身份)。
 *
 *  一行 = 一把 api_key_hash。数据源是 usage.sqlite 当日聚合，不分页——
 *  CPA 部署的活跃 key 数量级远小于 Sub2API/NewAPI 的终端用户表，
 *  一次性把全表拉回来渲染比再搭一套游标分页更诚实（没有隐藏在"下一页"
 *  后面的数据）。 */
export function CPAKeysPanel() {
  const query = useQuery({
    queryKey: ["cpa", "keys"],
    queryFn: ({ signal }) => listCPAKeys({ signal }),
  });

  return (
    <div className="flex flex-col gap-3">
      <p className="text-xs text-fg-muted">
        按 API Key 哈希聚合的当日用量；哈希是单向摘要，从不还原成真实 Key
        （凭据文件从不进入本平台容器）。
      </p>
      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
        {query.data ? <CPAKeysTable page={query.data} /> : null}
      </ApiStateView>
    </div>
  );
}

function amountText(a: MoneyBody | null): string {
  if (a === null) return "—";
  return formatScaledMinorUnits(a.amount_minor, a.currency, a.scale);
}

function keyLabel(item: CPAKeyUsageItem): string {
  return item.alias || (item.api_key_hash ? `${item.api_key_hash.slice(0, 12)}…` : "(无 key 归属)");
}

const COLUMN_KEY: DataTableColumn<CPAKeyUsageItem> = {
  id: "key",
  header: "Key",
  primary: true,
  value: (item) => `${item.alias} ${item.api_key_hash}`,
  cell: (item) => (
    <>
      <span className="font-medium">{keyLabel(item)}</span>
      {item.alias ? (
        <p className="font-mono text-xs text-fg-muted" title={item.api_key_hash}>
          {item.api_key_hash.slice(0, 16)}…
        </p>
      ) : null}
    </>
  ),
};

const COLUMN_REQUESTS: DataTableColumn<CPAKeyUsageItem> = {
  id: "requests",
  header: "请求数",
  numeric: true,
  value: (item) => BigInt(item.request_count),
  cell: (item) => <span className="tabular-nums">{formatCount(item.request_count)}</span>,
};

const COLUMN_TOKENS: DataTableColumn<CPAKeyUsageItem> = {
  id: "tokens",
  header: "Token（入/出/缓存）",
  numeric: true,
  value: (item) => BigInt(item.tokens_in + item.tokens_out),
  cell: (item) => {
    const cache = item.tokens_cache_read + item.tokens_cache_creation;
    return (
      <span className="tabular-nums text-xs">
        {formatCount(item.tokens_in)} / {formatCount(item.tokens_out)}
        {cache > 0 ? ` / ${formatCount(cache)}` : ""}
      </span>
    );
  },
};

const COLUMN_COST: DataTableColumn<CPAKeyUsageItem> = {
  id: "cost",
  header: "花费",
  numeric: true,
  value: (item) => (item.cost === null ? null : toIntegerValue(item.cost.amount_minor)),
  cell: (item) => (
    <span className={item.cost === null ? "text-fg-muted" : "tabular-nums"}>
      {amountText(item.cost)}
      {item.unpriced_request_count > 0 ? (
        <span className="ml-1 text-xs text-warning" title={`${item.unpriced_request_count} 次请求使用了未配价的模型，花费为部分合计`}>
          （部分）
        </span>
      ) : null}
    </span>
  ),
};

const COLUMN_LAST_USED: DataTableColumn<CPAKeyUsageItem> = {
  id: "lastUsed",
  header: "最后使用",
  value: (item) => item.last_used_at,
  cell: (item) =>
    item.last_used_at === null ? (
      <span className="text-fg-muted">当日无请求</span>
    ) : (
      <span className="text-xs tabular-nums">{formatUtcTimestamp(item.last_used_at)}</span>
    ),
};

function CPAKeysTable({ page }: { page: CPAKeyUsagePage }) {
  const freshness: FreshnessContract = {
    state: page.snapshot.is_partial ? "partial" : "fresh",
    staleness_seconds: null,
    threshold_seconds: 1800,
    is_partial: page.snapshot.is_partial,
    observed_at: page.snapshot.observed_at,
    last_success: page.snapshot.observed_at,
    last_error_code: "",
  };

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2 text-xs text-fg-muted">
        <FreshnessBadge freshness={freshness} />
        <FreshnessNote freshness={freshness} />
        <span>业务日 {page.business_day}</span>
        <span>· 来源 {page.snapshot.source}</span>
        <Badge tone="neutral">{page.total_key_count} 把 Key</Badge>
      </div>
      <DataTableV2
        caption="CPA API Key 当日用量：请求数、Token 与折算花费"
        columns={[COLUMN_KEY, COLUMN_REQUESTS, COLUMN_TOKENS, COLUMN_COST, COLUMN_LAST_USED]}
        rows={page.items}
        rowKey={(item) => item.api_key_hash || keyLabel(item)}
        searchable
        emptyState={
          <PageState
            kind="empty"
            title="当日没有用量"
            description="usage.sqlite 里这个业务日没有任何请求记录，或采集尚未开始。"
          />
        }
      />
      {page.truncated ? (
        <p className="text-xs text-fg-muted">结果已被截断，仅展示部分 Key。</p>
      ) : null}
    </div>
  );
}
