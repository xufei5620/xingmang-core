import { useQuery } from "@tanstack/react-query";
import {
  DataTableV2,
  describeFreshness,
  formatFreshnessDetail,
  formatFreshnessNote,
  formatUtcTimestamp,
  navItemByPath,
  navLabel,
  PageHeader,
  PageState,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge, Tabs } from "@xingmang/ui-primitives";
import { Link, useSearchParams } from "react-router";
import { getOpsOverview, type OpsOverview } from "../api/ops";
import { ApiStateView } from "../components/ApiStateView";
import { buildOpsHealthRows, type OpsHealthRow } from "../lib/ops";

const OPS_SUB_TABS = (navItemByPath("/ops")?.item.subTabs ?? []).map(
  (tab) => [tab.id, tab.label] as const,
);

/** 默认子页 = 唯一接了真实数据的那一格。 */
const OPS_DEFAULT_SUB = "health";

/** 运行保障页（XM-OPS0）。
 *
 *  六个子页签里只有「控制平面健康」接了真实数据——它读的是控制平面自己的
 *  健康端点（`GET /api/v1/ops/overview`），回答的是「平台自己好不好」，
 *  不是任何被管平台（Sub2API/NewAPI/CPA）的数据。其余五格（稳定性、备份恢复、
 *  故障手册、迁移对比、模型质量）诚实显示「尚未接入」，不拿别的数据充数。 */
export function OpsPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const rawSub = searchParams.get("sub");
  const activeSub = rawSub === null || rawSub.trim() === "" ? OPS_DEFAULT_SUB : rawSub;
  const known = OPS_SUB_TABS.some(([value]) => value === activeSub);

  if (!known) {
    return (
      <section>
        <PageHeader
          title={navLabel("/ops")}
          description="运行保障的子页按里程碑逐步接入；未知地址不会静默回落到控制平面健康。"
        />
        <PageState
          kind="unavailable"
          title={`「${rawSub}」子页尚未接入`}
          description="请从已定义的运行保障子页中选择。"
          action={
            <Link
              to={`/ops?sub=${OPS_DEFAULT_SUB}`}
              className="text-sm font-medium text-accent hover:underline"
            >
              返回控制平面健康
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
      items={OPS_SUB_TABS.map(([value, label]) => ({
        value,
        label,
        content:
          value === OPS_DEFAULT_SUB ? (
            <OpsHealthPage />
          ) : (
            <OpsUnavailablePage tabId={value} label={label} />
          ),
      }))}
    />
  );
}

function OpsHealthPage() {
  const query = useQuery({
    queryKey: ["ops-overview"],
    queryFn: ({ signal }) => getOpsOverview(undefined, undefined, signal),
  });

  return (
    <section>
      <PageHeader
        title={navLabel("/ops")}
        description="控制平面自身的健康状态：worker 心跳、两条采集链路、两个连接器健康检查与保留期清理任务的数据新鲜度。"
        onRefresh={() => void query.refetch()}
        refreshing={query.isFetching}
        lastRefreshedAt={query.dataUpdatedAt || undefined}
      />
      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
        {query.data ? <OpsHealthView data={query.data} /> : null}
      </ApiStateView>
    </section>
  );
}

function OpsHealthView({ data }: { data: OpsOverview }) {
  return (
    <div className="flex flex-col gap-3">
      <OpsSummaryStrip data={data} />
      <DataTableV2
        caption="控制平面组件健康"
        columns={OPS_HEALTH_COLUMNS}
        rows={buildOpsHealthRows(data)}
        rowKey={(row) => row.id}
        emptyState={<PageState kind="empty" title="没有可用的控制平面组件" />}
      />
    </div>
  );
}

/** build 信息 + 告警投递渠道 + 数据库连通性的一句话摘要，摆在表格上方。
 *
 *  这三样是「控制平面健康」表之外、同一份响应里剩下的事实：build 是「现在
 *  跑的是哪个版本」，后两样是布尔值，本来就配不出一整列，放进表格反而要
 *  硬凑出「组件」「依赖」这类不适用的列。 */
function OpsSummaryStrip({ data }: { data: OpsOverview }) {
  return (
    <div className="flex flex-wrap items-center gap-x-6 gap-y-2 rounded-lg border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted">
      <span>
        版本 <span className="font-mono text-fg">{data.build.version || "-"}</span>
      </span>
      <span>
        Commit <span className="font-mono text-fg">{data.build.commit || "-"}</span>
      </span>
      <span>
        环境 <span className="font-mono text-fg">{data.build.environment || "-"}</span>
      </span>
      <Badge tone={data.alert_delivery.telegram_configured ? "success" : "neutral"}>
        {data.alert_delivery.telegram_configured ? "Telegram 已配置" : "Telegram 未配置"}
      </Badge>
      <Badge tone={data.alert_delivery.webhook_configured ? "success" : "neutral"}>
        {data.alert_delivery.webhook_configured ? "Webhook 已配置" : "Webhook 未配置"}
      </Badge>
      <Badge tone={data.database.connected ? "success" : "danger"}>
        {data.database.connected ? "数据库：已连接" : "数据库：不可达"}
      </Badge>
    </div>
  );
}

const OPS_HEALTH_COLUMNS: DataTableColumn<OpsHealthRow>[] = [
  {
    id: "component",
    header: "组件",
    primary: true,
    value: (row) => row.component,
    cell: (row) => <span className="font-medium">{row.component}</span>,
  },
  {
    id: "environment",
    header: "环境",
    value: (row) => row.environment,
    cell: (row) => <span>{row.environment || "-"}</span>,
  },
  {
    id: "status",
    header: "状态",
    value: (row) => describeFreshness(row.freshness.state).label,
    cell: (row) => {
      const state = describeFreshness(row.freshness.state);
      return (
        <div className="flex flex-col items-start gap-1">
          <Badge tone={state.tone} title={state.hint}>
            {state.label}
          </Badge>
          {/* 采集模式那一格可能带一句悬浮说明：「模式未知」四个字自己解释不了
              「那现在到底在跑哪个模式、我该去哪配」。没有说明时不挂空 title
              ——一个空的 tooltip 会让人以为鼠标停错了地方。 */}
          {row.note ? (
            <Badge tone={row.noteTone} title={row.noteHint || undefined}>
              {row.note}
            </Badge>
          ) : null}
        </div>
      );
    },
  },
  {
    id: "freshness",
    header: "数据新鲜度",
    value: (row) => formatFreshnessNote(row.freshness),
    cell: (row) => (
      <span title={formatFreshnessDetail(row.freshness)}>{formatFreshnessNote(row.freshness)}</span>
    ),
  },
  {
    id: "heartbeat",
    header: "最近心跳",
    value: (row) => row.freshness.observed_at,
    cell: (row) => <span>{formatUtcTimestamp(row.freshness.observed_at)}</span>,
  },
  {
    id: "dependency",
    header: "依赖",
    value: (row) => row.dependency,
    cell: (row) => <span className="font-mono text-xs break-all">{row.dependency}</span>,
  },
  {
    id: "lastError",
    header: "最近错误",
    value: (row) => row.freshness.last_error_code,
    cell: (row) =>
      row.freshness.last_error_code ? (
        <span className="font-mono text-xs break-all text-danger">{row.freshness.last_error_code}</span>
      ) : (
        <span className="text-fg-muted">-</span>
      ),
  },
];

/** 5 个尚未接入子页的文案，逐字取自 OPS_BLUEPRINT（blueprints/governance.ts）的
 *  `tabs[].source`。两处不一致时以 blueprints.test.ts 的对账断言为准。 */
const OPS_UNAVAILABLE_COPY: Readonly<Record<string, string>> = {
  stability: "SLI / SLO 与异故障域的外部探测（Uptime Kuma）；随 M1 上线。",
  backup: "备份对象、异地副本与恢复演练记录；恢复演练属于 Platform Lifecycle Operation。",
  runbook: "手册本体在仓库里，这一页只登记版本、演练记录与关联告警。",
  migration:
    "SoloAI → 星芒的影子对比进度。判据由 cmd/platform-shadow 产出并归档在 docs/shadow-reports/（XM-0037e）。",
  "model-quality": "上游模型的身份一致性与漂移检测证据；随 M1.5 的渠道保障上线。",
};

/** 尚未接入的子页签的统一诚实占位（结构镜像 AuditPage.tsx 的 AuditUnavailablePage）。 */
function OpsUnavailablePage({ tabId, label }: { tabId: string; label: string }) {
  const description = OPS_UNAVAILABLE_COPY[tabId] ?? "该子页尚未接入。";
  return (
    <section>
      <PageHeader title={label} description={description} />
      <PageState kind="unavailable" title={`「${label}」尚未接入`} description={description} />
    </section>
  );
}
