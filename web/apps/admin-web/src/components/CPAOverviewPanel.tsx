import { useQuery } from "@tanstack/react-query";
import { MetricCard, StatTile, type FreshnessContract } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import {
  CPA_ACCOUNTS_METRIC_KEY,
  CPA_COST_METRIC_KEY,
  CPA_REQUESTS_METRIC_KEY,
  type CPAAccountsHealthValue,
  type CPACostValue,
  type CPARequestsValue,
} from "../api/cpa";
import { listMetrics, type MetricItem } from "../api/platform";
import { formatCount, formatScaledMinorUnits } from "../lib/money";
import { ApiStateView } from "./ApiStateView";

const UNINITIALIZED_FRESHNESS: FreshnessContract = {
  state: "uninitialized",
  staleness_seconds: null,
  threshold_seconds: 1800,
  is_partial: false,
  observed_at: null,
  last_success: null,
  last_error_code: "",
};

/** CPA 概览页(第一片真实数据,XM-CPA0)。
 *
 *  三张卡对应 worker 每 5 分钟写入的三条观测(cpa.requests.daily /
 *  cpa.cost.daily / cpa.accounts.health);第四条 cpa.keys.usage 只是
 *  「用户管理」页签的新鲜度信号,概览页不重复摆一张同义的卡。
 *
 *  没有原型可对齐——CPA 在原型里是占位平台,这一页是从观测形状直接搭的,
 *  版式照 Sub2API/NewAPI 概览同一套三卡网格 + 说明条,不编原型没画过的格子。 */
export function CPAOverviewPanel() {
  const query = useQuery({
    queryKey: ["metrics"],
    queryFn: ({ signal }) => listMetrics({ signal }),
  });

  const byKey = new Map((query.data ?? []).map((m) => [m.metric_key, m]));
  const requests = byKey.get(CPA_REQUESTS_METRIC_KEY);
  const cost = byKey.get(CPA_COST_METRIC_KEY);
  const accounts = byKey.get(CPA_ACCOUNTS_METRIC_KEY);

  return (
    <div className="flex flex-col gap-4">
      <p className="text-xs text-fg-muted">
        CLI Proxy API + cpa-manager-plus 的用量、成本折算与账号健康；数据源是宿主机只读挂载的
        usage.sqlite（cpa_sync 每 5 分钟同步一次，不参与实时请求路径）。
      </p>
      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
          <RequestsCard item={requests} />
          <CostCard item={cost} />
          <AccountsCard item={accounts} />
        </div>
        <UnpricedNote item={cost} />
      </ApiStateView>
    </div>
  );
}

function RequestsCard({ item }: { item: MetricItem | undefined }) {
  if (!item) {
    return (
      <MetricCard
        label="今日请求量"
        value="—"
        unavailable
        secondary="没有采到 cpa.requests.daily；cpa_sync 任务跑起来后会出现在这里"
        freshness={UNINITIALIZED_FRESHNESS}
      />
    );
  }
  const value = (item.value ?? {}) as CPARequestsValue;
  const uninitialized = item.freshness.state === "uninitialized";
  const count = value.total_request_count;
  const unavailable = uninitialized || count === undefined;
  return (
    <MetricCard
      label="今日请求量"
      metricKey={item.metric_key}
      value={unavailable ? "—" : formatCount(count)}
      unavailable={unavailable}
      secondary={value.business_day ? `业务日 ${value.business_day}` : undefined}
      freshness={item.freshness}
      source={item.source}
      watermark={item.watermark}
    />
  );
}

function CostCard({ item }: { item: MetricItem | undefined }) {
  if (!item) {
    return (
      <MetricCard
        label="今日成本（折算）"
        value="—"
        unavailable
        secondary="没有采到 cpa.cost.daily"
        freshness={UNINITIALIZED_FRESHNESS}
      />
    );
  }
  const value = (item.value ?? {}) as CPACostValue;
  const uninitialized = item.freshness.state === "uninitialized";
  const known = value.total_cost_minor_units !== undefined && Boolean(value.currency);
  const unavailable = uninitialized || !known;
  const unpriced = value.unpriced_request_count ?? 0;

  const secondaryParts = [
    value.business_day ? `业务日 ${value.business_day}` : undefined,
    unpriced > 0 ? `${formatCount(unpriced)} 次请求所用模型未配价` : undefined,
    !known && value.total_omitted_reason ? "所有请求都命中未配价模型，无法折算" : undefined,
  ].filter((p): p is string => Boolean(p));

  return (
    <MetricCard
      label="今日成本（折算）"
      metricKey={item.metric_key}
      value={
        unavailable || value.total_cost_minor_units === undefined || !value.currency
          ? "—"
          : formatScaledMinorUnits(value.total_cost_minor_units, value.currency, 6)
      }
      unavailable={unavailable}
      secondary={secondaryParts.length > 0 ? secondaryParts.join(" · ") : undefined}
      freshness={item.freshness}
      source={item.source}
      watermark={item.watermark}
    />
  );
}

function AccountsCard({ item }: { item: MetricItem | undefined }) {
  if (!item) {
    return (
      <StatTile
        label="账号健康"
        value="—"
        unavailable
        note="没有采到 cpa.accounts.health"
        status={<Badge tone="neutral">未接入</Badge>}
      />
    );
  }
  const value = (item.value ?? {}) as CPAAccountsHealthValue;
  const uninitialized = item.freshness.state === "uninitialized";
  const total = value.account_count;
  const disabled = value.disabled_count ?? 0;
  const anomalies = value.anomaly_count ?? 0;
  const unavailable = uninitialized || total === undefined;

  if (unavailable) {
    return (
      <StatTile
        label="账号健康"
        value="—"
        unavailable
        note="尚未观测到任何巡检结果（codex_inspection_runs 里还没有一轮完成）"
        status={<Badge tone="neutral">未接入</Badge>}
      />
    );
  }

  const tone = anomalies > 0 ? "warning" : disabled > 0 ? "warning" : "success";
  return (
    <StatTile
      label="账号健康"
      value={`${formatCount(total)} 个账号`}
      note={`禁用 ${formatCount(disabled)} · 异常 ${formatCount(anomalies)}${value.run_id ? ` · 巡检 ${value.run_id}` : ""}`}
      status={
        <Badge tone={tone}>
          {anomalies > 0 ? `${formatCount(anomalies)} 项待处理` : "正常"}
        </Badge>
      }
    />
  );
}

/** 未配价模型提示条(团队要求的"未配价模型提示")。
 *
 *  只在**已知有未配价用量**时出现——不是常驻提示,而是「这批钱算不出来」
 *  这件事本身需要被看见(宪法 12 条:成本折算不完整不能装作完整)。 */
function UnpricedNote({ item }: { item: MetricItem | undefined }) {
  if (!item) return null;
  const value = (item.value ?? {}) as CPACostValue;
  const models = value.unpriced_models ?? [];
  if (models.length === 0) return null;
  return (
    <p
      role="status"
      className="rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg"
    >
      <span aria-hidden="true">⚠</span> 以下 provider/model 在 model_prices
      里没有配置价格，其用量已计入请求量但**未计入**成本折算：{models.join("、")}。
    </p>
  );
}
