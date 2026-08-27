import { useQuery } from "@tanstack/react-query";
import { FreshnessBadge, FreshnessNote } from "@xingmang/ui-admin";
import { Badge, EmptyState } from "@xingmang/ui-primitives";
import { listMetrics, type MetricItem } from "../api/platform";
import { ApiStateView } from "../components/ApiStateView";
import { PageHeader } from "../components/PageHeader";
import {
  channelTotal,
  CHANNEL_BALANCE_METRIC_KEY,
  metricLabel,
  readChannelRows,
  type ChannelRow,
} from "../lib/metrics";
import { formatMinorUnits } from "../lib/money";

const TH = "px-3 py-2 text-left text-xs font-medium text-fg-muted";
const TD = "px-3 py-2 align-top text-sm text-fg";

/** 渠道明细：把总览卡片上那句「N 个渠道」摊开成逐渠道的余额与令牌状态。
 *
 *  数据来源是 `sub2api.channels.balance` 这一条指标的 value，不是另一个接口——
 *  因此它的新鲜度就是那条指标的新鲜度，页头必须原样带上（规格 §9.1）：
 *  一张看着很具体的明细表最容易让人忘记问「这是什么时候的数」。 */
export function ChannelsPage() {
  const query = useQuery({
    queryKey: ["metrics"],
    queryFn: ({ signal }) => listMetrics({ signal }),
  });

  const metric = (query.data ?? []).find((m) => m.metric_key === CHANNEL_BALANCE_METRIC_KEY);

  return (
    <section>
      <PageHeader
        title="渠道余额"
        description="逐渠道余额与令牌状态，取自渠道余额指标的最近一次观测。"
        onRefresh={() => void query.refetch()}
        refreshing={query.isFetching}
        lastRefreshedAt={query.dataUpdatedAt || undefined}
        {...(metric ? { actions: <FreshnessBadge freshness={metric.freshness} /> } : {})}
      />
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
      <EmptyState
        title="暂无渠道余额指标"
        description={`该环境下还没有 ${CHANNEL_BALANCE_METRIC_KEY} 的观测；Sub2API 采集任务跑起来后会出现在这里`}
      />
    );
  }

  // 从未成功采集时不渲染表格：后端此时 value 里可能还留着上一版结构或空数组，
  // 画成一张「0 个渠道」的表就是编数据（宪法 12 条）
  if (metric.freshness.state === "uninitialized") {
    return (
      <EmptyState
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

      {rows.length === 0 ? (
        <EmptyState title="没有渠道" description="这次观测里 channels 为空数组" />
      ) : (
        <ChannelTable rows={rows} />
      )}
    </div>
  );
}

function ChannelTable({ rows }: { rows: ChannelRow[] }) {
  return (
    <div className="overflow-x-auto rounded-lg border border-edge bg-surface shadow-sm">
      <table className="w-full border-collapse">
        <thead className="border-b border-edge bg-surface-muted">
          <tr>
            <th className={TH}>渠道</th>
            <th className={TH}>余额</th>
            <th className={TH}>币种</th>
            <th className={TH}>令牌状态</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row, index) => (
            // channel_id 可能缺失（上游没给），退到下标兜底，避免 key 冲突
            <tr key={row.channelId || `#${index}`} className="border-b border-edge last:border-b-0">
              <td className={TD}>
                <span className="font-medium">{row.channelName || row.channelId || "—"}</span>
                {row.channelName && row.channelId ? (
                  <p className="font-mono text-xs text-fg-muted">{row.channelId}</p>
                ) : null}
              </td>
              <td className={`${TD} tabular-nums`}>
                {formatMinorUnits(row.balanceMinorUnits, row.currency)}
              </td>
              <td className={TD}>{row.currency || "—"}</td>
              <td className={TD}>
                <TokenBadge valid={row.tokenValid} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** 令牌状态徽章。
 *
 *  字段缺失显示「未知」而不是默认「有效」：一个失效的令牌被画成绿色，
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
