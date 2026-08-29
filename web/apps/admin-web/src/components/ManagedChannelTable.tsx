import { useQuery } from "@tanstack/react-query";
import {
  DataTableV2,
  FreshnessBadge,
  PageState,
  StatTile,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { listPlatformChannels, type PlatformChannelRow } from "../api/platformChannels";
import { ApiStateView } from "./ApiStateView";

const CANDIDATE_LABELS: Record<string, { label: string; tone: "success" | "warning" | "danger" | "neutral" }> = {
  unmapped: { label: "未映射", tone: "neutral" },
  candidate: { label: "待确认", tone: "warning" },
  conflict: { label: "冲突", tone: "danger" },
  orphan: { label: "孤儿记录", tone: "warning" },
};

function platformLabel(platform: "sub2api" | "newapi") {
  return platform === "sub2api" ? "Sub2API" : "NewAPI";
}

function candidateText(row: PlatformChannelRow): string {
  return [candidateLabel(row.candidate.state), row.candidate.evidenceStatus, ...row.candidate.reasonCodes].join(" ");
}

function candidateLabel(state: string): string {
  return CANDIDATE_LABELS[state]?.label ?? state;
}

function bindingCell(row: PlatformChannelRow) {
  const shown = CANDIDATE_LABELS[row.candidate.state] ?? { label: row.candidate.state, tone: "neutral" as const };
  return (
    <div className="min-w-48">
      <Badge tone={shown.tone}>{row.binding ? "已绑定" : shown.label}</Badge>
      {row.binding ? (
        <p className="mt-1 font-mono text-xs break-all text-fg-muted">
          上游账号 {row.binding.upstreamAccountId}
        </p>
      ) : row.candidate.upstreamAccountIds.length > 0 ? (
        <p className="mt-1 text-xs text-fg-muted">
          候选 {row.candidate.upstreamAccountIds.length} 个上游账号
        </p>
      ) : null}
      {row.candidate.platformAssignmentMissing ? (
        <p className="mt-1 text-xs text-warning">平台归属尚未登记</p>
      ) : null}
    </div>
  );
}

function healthCell(row: PlatformChannelRow) {
  if (!row.health) return <span className="text-xs text-fg-muted">未接入健康字段</span>;
  return (
    <div className="min-w-36 text-xs">
        <Badge tone={row.observed.isStale ? "warning" : "success"}>
        {row.observed.isStale ? "数据延迟" : "已观测"}
      </Badge>
      <p className="mt-1 text-fg-muted">来源 {row.observed.source || "—"}</p>
      <p className="mt-1 font-mono text-fg-muted">{row.observed.observedAt || "观测时间未接入"}</p>
    </div>
  );
}

function modelsCell(row: PlatformChannelRow, platform: "sub2api" | "newapi") {
  if (platform === "sub2api") return <span className="text-xs text-fg-muted">未接入 · M1.5</span>;
  if (!row.models) return <span className="text-xs text-fg-muted">模型信息未接入</span>;
  return <span className="text-xs">{String(row.models.count ?? "—")} 个模型（仅数量）</span>;
}

function economicsCell(row: PlatformChannelRow) {
  if (!row.economics) {
    return (
      <span className="text-xs text-fg-muted" title={row.economicsState}>
        未接入 · {row.economicsState || "暂无可断言口径"}
      </span>
    );
  }
  return <span className="text-xs text-fg-muted">服务端已提供经营对象，详情字段按契约展开</span>;
}

function runwayCell(row: PlatformChannelRow) {
  if (!row.runway) return <span className="text-xs text-fg-muted">未接入</span>;
  return <span className="text-xs">{String(row.runway.days ?? "—")} 天</span>;
}

function columns(platform: "sub2api" | "newapi"): DataTableColumn<PlatformChannelRow>[] {
  return [
    {
      id: "channel",
      header: "渠道",
      primary: true,
      value: (row) => `${row.name} ${row.channelRef.externalChannelId}`,
      cell: (row) => (
        <div className="min-w-44">
          <strong className="font-medium">{row.name || "未命名渠道"}</strong>
          <p className="font-mono text-xs text-fg-muted">{row.channelRef.externalChannelId}</p>
        </div>
      ),
    },
    {
      id: "binding",
      header: "上游映射",
      value: candidateText,
      cell: bindingCell,
    },
    {
      id: "health",
      header: "健康观测",
      value: (row) => `${row.observed.source} ${row.observed.observedAt ?? ""}`,
      cell: healthCell,
    },
    {
      id: "models",
      header: "模型能力",
      value: (row) => (typeof row.models?.count === "number" ? row.models.count : null),
      cell: (row) => modelsCell(row, platform),
    },
    {
      id: "economics",
      header: "经营核算",
      value: (row) => row.economicsState,
      cell: economicsCell,
    },
    {
      id: "runway",
      header: "共享余额 / 可用天数",
      value: (row) => (typeof row.runway?.days === "number" ? row.runway.days : null),
      cell: runwayCell,
    },
  ];
}

export function ManagedChannelTable({
  platform,
  serviceId,
}: {
  platform: "sub2api" | "newapi";
  serviceId: string;
}) {
  const label = platformLabel(platform);
  const query = useQuery({
    queryKey: ["platform-channels", platform, serviceId],
    queryFn: ({ signal }) => listPlatformChannels(platform, serviceId, { signal }),
  });
  const page = query.data;
  const rows = page?.items ?? [];
  const mapped = rows.filter((row) => row.binding !== null).length;
  const conflicts = rows.filter((row) => row.candidate.state === "conflict").length;

  return (
    <section className="flex flex-col gap-3">
      <div className="rounded-lg border border-edge bg-surface px-4 py-3 shadow-sm">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 className="text-base font-semibold text-fg">{label} 渠道目录</h2>
            <p className="mt-1 text-xs leading-5 text-fg-muted">
              一行对应一个 managed channel。上游映射由人工确认，候选、冲突、孤儿与未映射都保留；共享余额只作为引用，不在渠道行重复合计。
            </p>
          </div>
          <Badge tone="info">渠道粒度</Badge>
        </div>
      </div>

      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
        {page ? (
          <div className="flex flex-col gap-3">
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
              <StatTile label="目录渠道" value={String(rows.length)} note="来自当前 service 的完整渠道目录" />
              <StatTile label="已确认映射" value={String(mapped)} note="binding 由人工 Action 确认" />
              <StatTile label="待处理冲突" value={String(conflicts)} note="冲突不参与经营核算" status={conflicts > 0 ? <Badge tone="danger">需处理</Badge> : undefined} />
              <StatTile label="目录完整性" value={page.inventory.complete ? "完整" : "未知"} note={`已取 ${page.inventory.fetchedCount} 条 · ${page.inventory.coveragePartial ? "字段覆盖不全" : "字段覆盖完整"}`} status={<FreshnessBadge freshness={{ state: page.inventory.complete ? "fresh" : "stale", staleness_seconds: null, threshold_seconds: 1800, is_partial: page.inventory.coveragePartial, observed_at: page.inventory.observedAt, last_success: page.inventory.observedAt, last_error_code: "" }} />} />
            </div>

            <DataTableV2
              caption={`${label} 渠道目录：映射、健康、模型能力、经营核算与共享余额引用`}
              columns={columns(platform)}
              rows={rows}
              rowKey={(row) => `${row.channelRef.serviceId}:${row.channelRef.externalChannelId}`}
              searchable
              filters={[{ columnId: "binding", label: "映射状态", options: ["未映射", "待确认", "冲突", "孤儿记录"] }]}
              views={[
                { name: "需处理", state: { query: "", filters: { binding: "冲突" }, sort: null, visibleColumns: columns(platform).map((column) => column.id), density: "compact" } },
              ]}
              renderExpanded={(row) => (
                <div className="grid grid-cols-1 gap-2 text-xs md:grid-cols-2">
                  <p><span className="text-fg-muted">ChannelRef：</span><span className="font-mono">{row.channelRef.serviceId}:{row.channelRef.externalChannelId}</span></p>
                  <p><span className="text-fg-muted">证据：</span>{row.candidate.evidenceStatus}</p>
                  <p><span className="text-fg-muted">原因：</span>{row.candidate.reasonCodes.join("、") || "—"}</p>
                  <p><span className="text-fg-muted">上游候选：</span>{row.candidate.upstreamAccountIds.join("、") || "—"}</p>
                  <p className="md:col-span-2 text-fg-muted">绑定确认与解绑需在后续 L1 Action 面板完成；当前页面只读，不自动写入。</p>
                </div>
              )}
              emptyState={<PageState kind="empty" title={`${label} 还没有渠道目录`} description="当前 service 的渠道目录为空或尚未成功采集；这不等于上游没有渠道。" />}
            />
          </div>
        ) : null}
      </ApiStateView>
    </section>
  );
}
