import { useQuery } from "@tanstack/react-query";
import {
  DataTableV2,
  FreshnessBadge,
  FreshnessNote,
  PageState,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import type { ChannelSummary } from "../api/finance";
import { listMetrics, type MetricItem } from "../api/platform";
import { ApiStateView } from "./ApiStateView";
import { channelEconomicsColumns } from "./ChannelEconomicsColumns";
import { ChannelScopeNote } from "./ChannelScopeNote";
import { indexChannelSummaries } from "../lib/channelEconomics";
import {
  channelTotal,
  CHANNEL_BALANCE_METRIC_KEY,
  metricLabel,
  readChannelRows,
  type ChannelRow,
} from "../lib/metrics";
import { formatMinorUnits } from "../lib/money";

/** 渠道明细：把总览卡片上那句「N 个渠道」摊开成逐渠道的余额与令牌状态。
 *
 *  数据来源是 `sub2api.channels.balance` 这一条指标的 value，不是另一个接口——
 *  因此它的新鲜度就是那条指标的新鲜度，必须原样带上（规格 §9.1）:
 *  一张看着很具体的明细表最容易让人忘记问「这是什么时候的数」。
 *
 *  XM-0034 起它从独立页面（/channels）变成 Sub2API 平台详情的「渠道管理」
 *  页签内容，所以自己不再画页头——页头归平台页所有。但新鲜度徽章跟着搬进来了:
 *  它原先挂在页头上，页头没了不等于这条约束可以跟着没。 */
export function ChannelsPanel() {
  const query = useQuery({
    queryKey: ["metrics"],
    queryFn: ({ signal }) => listMetrics({ signal }),
  });

  const metric = (query.data ?? []).find((m) => m.metric_key === CHANNEL_BALANCE_METRIC_KEY);

  return (
    <section className="flex flex-col gap-3">
      <header className="flex items-start justify-between gap-3">
        <p className="text-xs text-fg-muted">
          一行 = 一个 Sub2API 账号 / 一把 Key。余额与令牌状态取自渠道余额指标的最近一次观测。
        </p>
        {metric ? <FreshnessBadge freshness={metric.freshness} /> : null}
      </header>
      <ChannelScopeNote platform="sub2api" />
      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        <ChannelsView metric={metric} />
      </ApiStateView>
    </section>
  );
}

function ChannelsView({ metric }: { metric: MetricItem | undefined }) {
  if (!metric) {
    return (
      <PageState
        kind="empty"
        title="暂无渠道余额指标"
        description={`该环境下还没有 ${CHANNEL_BALANCE_METRIC_KEY} 的观测；Sub2API 采集任务跑起来后会出现在这里`}
      />
    );
  }

  // 从未成功采集时不渲染表格：后端此时 value 里可能还留着上一版结构或空数组,
  // 画成一张「0 个渠道」的表就是编数据（宪法 12 条）
  if (metric.freshness.state === "uninitialized") {
    return (
      <PageState
        kind="empty"
        title="未初始化"
        description={`${metricLabel(metric.metric_key)} 从未成功采集过，没有可信的渠道明细可显示`}
      />
    );
  }

  const rows = readChannelRows(metric.value);
  const sum = channelTotal(rows);

  return (
    <div className="flex flex-col gap-3">
      <div className="rounded-lg border border-edge bg-surface p-3">
        <FreshnessNote freshness={metric.freshness} />
        <p className="text-xs text-fg-muted">
          来源 {metric.source || "—"}
          {metric.watermark ? ` · 水位 ${metric.watermark}` : null}
          {` · ${rows.length} 个渠道`}
          {/* 币种不一致就没有合计——把不同币种的最小单位加起来是纯粹的错数 */}
          {sum ? ` · 合计 ${formatMinorUnits(sum.total, sum.currency)}` : " · 币种不一致，不给合计"}
        </p>
      </div>

      <ChannelTable rows={rows} />
    </div>
  );
}

/** 令牌状态的文本形态。排序、搜索与筛选都用它——
 *  徽章是给眼睛看的，这三件事要的是同一个词的可比较版本。 */
function tokenText(valid: boolean | null): string {
  if (valid === null) return "未知";
  return valid ? "有效" : "失效";
}

function channelColumns(
  summaries: Map<string, ChannelSummary>,
): DataTableColumn<ChannelRow>[] {
  return [
    ...CHANNEL_COLUMNS,
    ...channelEconomicsColumns<ChannelRow>((row) => row.channelId, summaries),
  ];
}

const CHANNEL_COLUMNS: DataTableColumn<ChannelRow>[] = [
  {
    id: "channel",
    header: "Sub2API 账号",
    primary: true,
    value: (row) => row.channelName || row.channelId || "",
    cell: (row) => (
      <>
        <span className="font-medium">{row.channelName || row.channelId || "—"}</span>
        {row.channelName && row.channelId ? (
          <p className="font-mono text-xs text-fg-muted">{row.channelId}</p>
        ) : null}
      </>
    ),
  },
  {
    id: "balance",
    header: "余额",
    numeric: true,
    // 排序用最小单位的整数，而不是格式化后的「¥100.00」：后者要靠解析字符串
    // 才能比大小，多一层就多一处能出错
    value: (row) => row.balanceMinorUnits,
    cell: (row) => formatMinorUnits(row.balanceMinorUnits, row.currency),
  },
  {
    id: "currency",
    header: "币种",
    value: (row) => row.currency || "",
    cell: (row) => row.currency || "—",
  },
  {
    id: "token",
    header: "令牌状态",
    value: (row) => tokenText(row.tokenValid),
    cell: (row) => <TokenBadge valid={row.tokenValid} />,
  },
];

function ChannelTable({ rows }: { rows: ChannelRow[] }) {
  // XM-0037d 的 ChannelSummary 端点还没上线。空索引 = 经营三列显示「未接入」;
  // 接线时这一行换成 useQuery，列定义与表结构都不用动
  const summaries = indexChannelSummaries(undefined);

  return (
    <DataTableV2
      caption="Sub2API 逐账号余额、令牌状态与经营口径"
      columns={channelColumns(summaries)}
      rows={rows}
      // channel_id 可能缺失（上游没给），退到下标兜底，避免 key 冲突
      rowKey={(row) => row.channelId || `#${rows.indexOf(row)}`}
      pageSize={10}
      searchable
      filters={[{ columnId: "token", label: "令牌状态", options: ["有效", "失效", "未知"] }]}
      emptyState={
        <PageState kind="empty" title="没有渠道" description="这次观测里 channels 为空数组" />
      }
    />
  );
}

/** 令牌状态徽章。
 *
 *  字段缺失显示「未知」而不是默认「有效」：一个失效的令牌被画成绿色,
 *  比不显示更糟——人会据此排除掉真正的故障原因。 */
function TokenBadge({ valid }: { valid: boolean | null }) {
  if (valid === null) {
    return (
      <Badge tone="warning" title="上游没有给出 token_valid 字段">
        未知
      </Badge>
    );
  }
  return valid ? (
    <Badge tone="success" title="渠道令牌可用">
      有效
    </Badge>
  ) : (
    <Badge tone="danger" title="渠道令牌已失效，该渠道可能无法调用">
      失效
    </Badge>
  );
}
