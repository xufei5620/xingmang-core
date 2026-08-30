import { useInfiniteQuery } from "@tanstack/react-query";
import { FreshnessBadge, FreshnessNote, PageState } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { listPlatformUserKeys, type KeyMetadataBody } from "../api/users";
import { ApiStateView } from "./ApiStateView";

function statusLabel(status: string): { label: string; tone: "success" | "warning" | "danger" | "neutral" } {
  switch (status) {
    case "active":
      return { label: "启用", tone: "success" };
    case "revoked":
      return { label: "已撤销", tone: "danger" };
    case "disabled":
      return { label: "已禁用", tone: "neutral" };
    case "limited":
      return { label: "受限", tone: "warning" };
    default:
      return { label: "未知", tone: "neutral" };
  }
}

function timestampText(value: string | null): string {
  if (!value) return "—";
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? "时间格式异常" : parsed.toLocaleString("zh-CN", { hour12: false });
}

function KeyRow({ item }: { item: KeyMetadataBody }) {
  const status = statusLabel(item.status);
  return (
    <tr className="border-b border-edge last:border-b-0">
      <td className="px-3 py-2 font-mono text-xs text-fg">{item.prefix || "—"}</td>
      <td className="px-3 py-2"><Badge tone={status.tone}>{status.label}</Badge></td>
      <td className="px-3 py-2 text-xs tabular-nums text-fg-muted">{timestampText(item.created_at)}</td>
      <td className="px-3 py-2 text-xs tabular-nums text-fg-muted">{timestampText(item.last_used_at)}</td>
      <td className="px-3 py-2 text-right text-xs tabular-nums text-fg">
        {item.today_peak_rpm.value === null ? "—" : item.today_peak_rpm.value}
      </td>
    </tr>
  );
}

/** 元数据-only Key 清单。完整 Key、复制、导出及 credentialRef 均不在该组件中。 */
export function KeyMetadataPanel({ platform, userId }: { platform: string; userId: string }) {
  const query = useInfiniteQuery({
    queryKey: ["platform-user-keys", platform, userId],
    queryFn: ({ pageParam, signal }) => listPlatformUserKeys(platform, userId, {
      limit: 50,
      ...(pageParam ? { cursor: pageParam } : {}),
      signal,
    }),
    initialPageParam: "",
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    retry: false,
  });
  const pages = query.data?.pages ?? [];
  const page = pages.length > 0 ? pages[pages.length - 1] : undefined;
  const items = pages.flatMap((entry) => entry.items);
  const freshness = page
    ? {
        state: page.snapshot.is_partial ? ("partial" as const) : ("fresh" as const),
        staleness_seconds: null,
        threshold_seconds: 60,
        is_partial: page.snapshot.is_partial,
        observed_at: page.snapshot.observed_at,
        last_success: page.snapshot.observed_at,
        last_error_code: "",
      }
    : null;

  return (
    <section className="min-w-0 overflow-hidden rounded-lg border border-edge bg-surface shadow-sm" aria-labelledby="key-metadata-title">
      <div className="flex flex-wrap items-start justify-between gap-3 border-b border-edge px-4 py-3">
        <div className="min-w-0">
          <h3 id="key-metadata-title" className="text-sm font-semibold text-fg">API Key 元数据</h3>
          <p className="mt-1 max-w-3xl text-xs leading-5 text-fg-muted">
            仅显示前缀、状态、创建时间、最近使用与今日峰值请求；完整 Key 永不返回，也不提供复制或导出。
          </p>
        </div>
        {freshness ? <FreshnessBadge freshness={freshness} /> : null}
      </div>

      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()} compact>
        {page ? (
          <>
            <div className="overflow-x-auto">
              <table className="w-full min-w-[42rem] border-collapse text-left">
                <caption className="sr-only">用户 API Key 元数据列表</caption>
                <thead className="bg-surface-muted text-xs text-fg-muted">
                  <tr>
                    <th scope="col" className="px-3 py-2 font-medium">前缀</th>
                    <th scope="col" className="px-3 py-2 font-medium">状态</th>
                    <th scope="col" className="px-3 py-2 font-medium">创建时间</th>
                    <th scope="col" className="px-3 py-2 font-medium">最近使用</th>
                    <th scope="col" className="px-3 py-2 text-right font-medium">今日峰值 RPM</th>
                  </tr>
                </thead>
                <tbody>
                  {items.length > 0 ? items.map((item) => <KeyRow key={item.id} item={item} />) : (
                    <tr><td colSpan={5} className="px-3 py-6"><PageState kind="empty" title="没有可展示的 Key 元数据" compact /></td></tr>
                  )}
                </tbody>
              </table>
            </div>
            <div className="flex flex-wrap items-center justify-between gap-3 border-t border-edge px-4 py-3 text-xs text-fg-muted">
              <FreshnessNote freshness={freshness!} />
              {query.hasNextPage ? (
                <button
                  type="button"
                  className="min-h-9 rounded-md border border-edge px-3 py-1 font-medium text-accent hover:bg-accent-soft disabled:cursor-not-allowed disabled:opacity-50"
                  onClick={() => void query.fetchNextPage()}
                  disabled={query.isFetchingNextPage}
                >
                  {query.isFetchingNextPage ? "加载中…" : "加载更多"}
                </button>
              ) : (
                <span>共 {items.length} 条</span>
              )}
            </div>
          </>
        ) : null}
      </ApiStateView>
    </section>
  );
}
