import { useQuery } from "@tanstack/react-query";
import { DataTableV2, PageState, StatTile, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";
import {
  CPA_ACCOUNTS_METRIC_KEY,
  type CPAAccountAnomaly,
  type CPAAccountsHealthValue,
} from "../api/cpa";
import { listMetrics, type MetricItem } from "../api/platform";
import { formatCount } from "../lib/money";
import { ApiStateView } from "./ApiStateView";

/** CPA 的"渠道保障"页签内容(带 M1.5 徽标,ADMIN-IA CPA 第 4 格,XM-CPA0)。
 *
 *  **这不是模型路由验证系统。** Sub2API/NewAPI 未来的"渠道保障"要做的是
 *  "这条渠道能不能正确路由到这个模型"(PlatformAssurancePanel.tsx 的蓝图态,
 *  仍是纯 UI、无数据)。CPA 这里读的是 codex_inspection 对上游账号的巡检
 *  结果,语义是"这些账号本身是否健康"——两件事完全不同,只是团队把它落在
 *  同一个页签位置(见 contracts/connectors/cpa.read.v1.md §9 的裁定说明)。
 *
 *  只接管 "overview" 子页签:probes(检测任务)/history(保障历史)两个子页签
 *  CPA 没有对应的数据形状(不存在按模型区分的"检测任务"或"保障历史"概念,
 *  巡检只产出"最近一轮的账号快照"),继续落回共享的蓝图占位
 *  (PlatformDetailPage 里 assuranceSubTab 的回落逻辑),不硬凑假数据冒充。 */
export function cpaAssuranceSubTab(subId: string): ReactNode | undefined {
  if (subId !== "overview") return undefined;
  return <CPAAccountHealthOverview />;
}

function CPAAccountHealthOverview() {
  const query = useQuery({
    queryKey: ["metrics"],
    queryFn: ({ signal }) => listMetrics({ signal }),
  });
  const item = (query.data ?? []).find((m) => m.metric_key === CPA_ACCOUNTS_METRIC_KEY);

  return (
    <div className="flex flex-col gap-3">
      <p className="text-xs text-fg-muted">
        codex_inspection 对 CLI Proxy API 上游账号的最近一轮巡检结果：账号是否被禁用、巡检给出的处置建议。
        不是模型路由验证——渠道 ↔ 模型映射的检测能力尚未接入。
      </p>
      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        {item ? <AccountHealthBody item={item} /> : <NoRunYet />}
      </ApiStateView>
    </div>
  );
}

function NoRunYet() {
  return (
    <PageState
      kind="empty"
      title="还没有巡检结果"
      description="没有采到 cpa.accounts.health；codex_inspection 还没有跑完一轮，或 cpa_sync 尚未启用。"
    />
  );
}

function AccountHealthBody({ item }: { item: MetricItem }) {
  if (item.freshness.state === "uninitialized") return <NoRunYet />;
  const value = (item.value ?? {}) as CPAAccountsHealthValue;
  const total = value.account_count ?? 0;
  const disabled = value.disabled_count ?? 0;
  const anomalies = value.anomalies ?? [];
  const anomalyCount = value.anomaly_count ?? anomalies.length;

  return (
    <div className="flex flex-col gap-3">
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <StatTile
          label="账号总数"
          value={formatCount(total)}
          note={value.run_id ? `巡检批次 ${value.run_id}` : "最近一轮巡检"}
        />
        <StatTile
          label="已禁用"
          value={formatCount(disabled)}
          note="被巡检标记为禁用的账号"
          status={disabled > 0 ? <Badge tone="warning">需关注</Badge> : <Badge tone="success">0</Badge>}
        />
        <StatTile
          label="异常 / 待处理"
          value={formatCount(anomalyCount)}
          note="有处置建议或已禁用的账号"
          status={
            anomalyCount > 0 ? (
              <Badge tone="warning">{formatCount(anomalyCount)}</Badge>
            ) : (
              <Badge tone="success">正常</Badge>
            )
          }
        />
      </div>
      <AnomalyTable
        anomalies={anomalies}
        truncated={value.truncated ?? false}
        anomalyCount={anomalyCount}
      />
    </div>
  );
}

const COLUMN_ACCOUNT: DataTableColumn<CPAAccountAnomaly> = {
  id: "account",
  header: "账号",
  primary: true,
  value: (a) => `${a.display_account} ${a.account_key}`,
  cell: (a) => (
    <>
      <span className="font-medium">{a.display_account || a.account_key || "—"}</span>
      {a.account_key ? <p className="font-mono text-xs text-fg-muted">{a.account_key}</p> : null}
    </>
  ),
};

const COLUMN_PROVIDER: DataTableColumn<CPAAccountAnomaly> = {
  id: "provider",
  header: "Provider",
  value: (a) => a.provider,
  cell: (a) => a.provider || "—",
};

const COLUMN_STATUS: DataTableColumn<CPAAccountAnomaly> = {
  id: "status",
  header: "状态",
  value: (a) => (a.disabled ? "已禁用" : a.status),
  cell: (a) => (
    <Badge tone={a.disabled ? "danger" : "warning"} title={a.state ? `state: ${a.state}` : undefined}>
      {a.disabled ? "已禁用" : a.status || "未知"}
    </Badge>
  ),
};

const COLUMN_ACTION: DataTableColumn<CPAAccountAnomaly> = {
  id: "action",
  header: "巡检建议",
  value: (a) => a.action,
  cell: (a) => (
    <>
      <span>{a.action || "—"}</span>
      {a.action_reason ? <p className="text-xs text-fg-muted">{a.action_reason}</p> : null}
    </>
  ),
};

function AnomalyTable({
  anomalies,
  truncated,
  anomalyCount,
}: {
  anomalies: CPAAccountAnomaly[];
  truncated: boolean;
  anomalyCount: number;
}) {
  return (
    <div className="flex flex-col gap-2">
      <DataTableV2
        caption="CPA 账号巡检异常：已禁用或带处置建议的上游账号"
        columns={[COLUMN_ACCOUNT, COLUMN_PROVIDER, COLUMN_STATUS, COLUMN_ACTION]}
        rows={anomalies}
        rowKey={(a) => a.account_key || a.display_account}
        emptyState={
          <PageState
            kind="empty"
            title="没有异常账号"
            description="最近一轮巡检里所有账号都正常，没有被禁用或标记处置建议。"
          />
        }
      />
      {/* 列表卡上限：截断要显示"还有 N 条" + 真实总数，不能只静默切掉
          （XM-0051 的教训：不设上限的列表能把整页撑到几千像素）。 */}
      {truncated ? (
        <p className="text-xs text-fg-muted">
          列表已按上限截断，最近一轮共 {formatCount(anomalyCount)} 条异常；完整清单见
          codex_inspection_results 原始记录。
        </p>
      ) : null}
    </div>
  );
}
