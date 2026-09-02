import { useQuery } from "@tanstack/react-query";
import {
  FreshnessBadge,
  FreshnessNote,
  MetricCard,
  PageState,
  Sparkline,
  StatTile,
  type FreshnessContract,
  type SparkSample,
} from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";
import { Link } from "react-router";
import { ACTIVE_ALERT_STATUSES, listAlerts } from "../api/alerts";
import {
  listChannelSummaries,
  listUpstreamAccounts,
  UPSTREAM_ACCOUNTS_QUERY,
} from "../api/finance";
import { listMetrics, type MetricItem } from "../api/platform";
import {
  listPlatformChannels,
  platformChannelRowKey,
  type PlatformChannelPage,
  type PlatformChannelRow,
} from "../api/platformChannels";
import { channelFieldNullReason } from "../lib/channelFieldReasons";
import { appDemoDataConfig, shouldShowDemoBanner } from "../lib/demoData";
import {
  connectionHealth,
  subscriptionHealth,
  limitWorkItems,
  sub2ApiChannelHealth,
  toWorkItems,
  workItemLabel,
  type HealthRow,
} from "../lib/overview";
import {
  CHANNEL_BALANCE_METRIC_KEY,
  NEWAPI_CHANNELS_METRIC_KEY,
  SUB2API_CHANNEL_STATUS_METRIC_KEY,
  metricPrimaryValue,
  presentMetric,
  readChannelRows,
  readNewApiChannelRows,
  readRequestsTrendDays,
  readSub2ApiChannelStatusRows,
  toTrendSparkSamples,
  type NewApiChannelRow,
} from "../lib/metrics";
import { formatErrorRatePPM, formatScaledMinorUnits } from "../lib/money";
import {
  aggregateChannelMoney,
  aggregateFailureText,
  aggregateFreshness,
  type ChannelMoneyAggregate,
} from "../lib/financeOverview";
import { platformOfMetricKey } from "../lib/platforms";
import { channelDetailPath } from "../pages/ChannelDetailPage";
import { ApiStateView } from "./ApiStateView";
import { FinanceSummaryCards } from "./FinanceSummaryCards";
import { MetricSparkline } from "./MetricSparkline";

/** 概览页的大图窗口：168 小时正好七天，也是后端 `maxHistoryHours` 的上限。 */
const SEVEN_DAYS_HOURS = 168;

/** NewAPI 概览使用的指标键。集中声明避免各处把平台归属写成相似但不一致的字符串。 */
const NEWAPI_USERS_TOTAL_METRIC_KEY = "newapi.users.total";
/** XM-NEWAPI-OVERVIEW0：「近 7 日请求量」从 newapi.models.usage（模型用量,
 *  经 metric-history 管线）换成 newapi.requests.trend_7d——与 Sub2API「近 7 日
 *  调用量」同一条 reqlog 管线、同一个内嵌逐日数组形状，缺数据的日子有显式
 *  missing 标记，而不是靠历史轮询间隔猜断点。newapi.models.usage 仍然是已注册
 *  指标（其它地方要用随时可用），只是这张卡不再消费它。 */
const NEWAPI_REQUESTS_TREND_7D_METRIC_KEY = "newapi.requests.trend_7d";

/** Sub2API 概览新增的请求量三件套（XM-OVERVIEW-UI）：今日调用量、24h 成功率、
 *  近 7 日调用量趋势，均来自请求审计线（reqlog）按业务日/滚动窗口聚合而来。 */
const SUB2API_REQUESTS_DAILY_METRIC_KEY = "sub2api.requests.daily";
const SUB2API_SUCCESS_RATE_24H_METRIC_KEY = "sub2api.requests.success_rate_24h";
const SUB2API_REQUESTS_TREND_7D_METRIC_KEY = "sub2api.requests.trend_7d";

/** 底部大图的画布。与卡片里的迷你图是**同一个组件换个盒子**——
 *  另写一个图表组件的话，两处的失败样本与部分数据画法迟早会漂开。 */
const BIG_CHART_BOX = { width: 960, height: 180, padding: 8 };

/** 财务汇总尚未给出可信读数时使用的固定新鲜度形状。
 *
 *  这不是一条观测：它只是让四张 NewAPI 财务卡在 Query 加载/失败时仍能
 *  按原型占位，并且让 MetricCard 明确显示「未初始化」而不是裸数字。 */
const UNAVAILABLE_FINANCE_FRESHNESS: FreshnessContract = {
  state: "uninitialized",
  staleness_seconds: null,
  threshold_seconds: 1800,
  is_partial: false,
  observed_at: null,
  last_success: null,
  last_error_code: "",
};

const FAILED_FINANCE_FRESHNESS: FreshnessContract = {
  state: "failed",
  staleness_seconds: null,
  threshold_seconds: 1800,
  is_partial: false,
  observed_at: null,
  last_success: null,
  last_error_code: "finance-summary-unavailable",
};

/** 哪些平台有按原型对齐的概览页。
 *
 *  只有 sub2api / newapi：原型给这两个平台各画了一版**结构不同**的概览
 *  (Sub2API 是「今天有没有事」，NewAPI 是「用户/渠道/利润样例总览」),
 *  CPA 与服务器走各自的蓝图或占位。 */
const PLATFORMS_WITH_OVERVIEW = new Set(["sub2api", "newapi"]);

export function platformHasPrototypeOverview(serviceType: string): boolean {
  return PLATFORMS_WITH_OVERVIEW.has(serviceType);
}

/** 平台概览页（原型 `V["s2/overview"]` / `V["newapi/overview"]` 的渲染态）。
 *
 *  版式逐格照原型，**数字诚实**：原型有格而平台没有数据源的（调用量、成功率、
 *  请求量趋势），按原型的位置摆出卡片但显示「未接入」并写明数据源属于哪条线。
 *  布局对齐优先，编数字一次都不行（宪法 12 条）。 */
export function PlatformOverviewPanel({
  serviceType,
  label,
  serviceId,
  serviceStatus,
}: {
  serviceType: string;
  label: string;
  /** 恰好一个已登记且 active 的 service 时才有值（与 ChannelTable.tsx 的
   *  usesChannelRefGrain 同一个判据）。目前只有 NewAPI 的渠道健康卡需要它
   *  读取真实渠道目录；Sub2API 分支忽略这两个参数。 */
  serviceId?: string;
  serviceStatus?: string;
}) {
  const metricsQuery = useQuery({
    queryKey: ["metrics"],
    queryFn: ({ signal }) => listMetrics({ signal }),
  });

  const all = metricsQuery.data ?? [];
  const mine = all.filter((m) => platformOfMetricKey(m.metric_key) === serviceType);
  const byKey = new Map(mine.map((m) => [m.metric_key, m]));
  // 演示判据只看**本平台**的指标来源：别的平台接了真实实例，
  // 不代表这一页上的数字是真的
  const demo = shouldShowDemoBanner(
    mine.map((m) => m.source),
    appDemoDataConfig,
  );

  return (
    <div className="flex flex-col gap-4">
      <p className="text-xs text-fg-muted">{overviewLead(serviceType)}</p>

      <ApiStateView
        isPending={metricsQuery.isPending}
        error={metricsQuery.error}
        onRetry={() => void metricsQuery.refetch()}
      >
        <div className="flex flex-col gap-4">
          <SampleDataBanner platform={serviceType} demo={demo} hasMetrics={mine.length > 0} />
          {serviceType === "newapi" ? (
            <NewApiOverview
              byKey={byKey}
              label={label}
              demo={demo}
              hasMetrics={mine.length > 0}
              serviceId={serviceId}
              serviceStatus={serviceStatus}
            />
          ) : (
            <Sub2ApiOverview byKey={byKey} label={label} demo={demo} hasMetrics={mine.length > 0} />
          )}
        </div>
      </ApiStateView>
    </div>
  );
}

/** 页头下面那句话，逐字照原型的 `phead` 第三参。 */
function overviewLead(serviceType: string): string {
  return serviceType === "newapi"
    ? "NewAPI 的用户、渠道、上游、请求与利润总览。"
    : "存量主平台，用户量最大。这一页回答「Sub2API 今天有没有事」。";
}

/** 样例数据提示条（原型的 `credBanner` / NewAPI 的 `warnbar`）。
 *
 *  **判得出来才挂**：原型把这条横幅写死在页面里，那是因为它整站都是样例。
 *  真实产品里挂一条永远在的警告，等于没有警告——所以这里沿用
 *  `lib/demoData` 的判据（指标 source 是不是演示实例），
 *  与顶部全局横幅同一条规矩，只是范围收到这一个平台。 */
function SampleDataBanner({
  platform,
  demo,
  hasMetrics,
}: {
  platform: string;
  demo: boolean;
  hasMetrics: boolean;
}) {
  if (!hasMetrics || !demo) return null;
  return (
    <div
      role="status"
      className="flex flex-wrap items-center gap-1 rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg"
    >
      <span aria-hidden="true">⚠</span>
      <span>当前展示为样例数据。只读凭据尚未配置，去</span>
      <Link
        to={`/platforms/${platform}?tab=creds`}
        className="underline underline-offset-2"
      >
        连接与凭据
      </Link>
      <span>配置后才是真实数据。</span>
    </div>
  );
}

// --- Sub2API：原型 `V["s2/overview"]` ---

function Sub2ApiOverview({
  byKey,
  label,
  demo,
  hasMetrics,
}: {
  byKey: Map<string, MetricItem>;
  label: string;
  demo: boolean;
  hasMetrics: boolean;
}) {
  const revenue = byKey.get("sub2api.revenue.daily");
  const cost = byKey.get("sub2api.cost.daily");
  const channelBalance = byKey.get(CHANNEL_BALANCE_METRIC_KEY);
  const channelStatus = byKey.get(SUB2API_CHANNEL_STATUS_METRIC_KEY);
  const requestsDaily = byKey.get(SUB2API_REQUESTS_DAILY_METRIC_KEY);
  const successRate24h = byKey.get(SUB2API_SUCCESS_RATE_24H_METRIC_KEY);
  const requestsTrend = usableTrendDaysMetric(byKey.get(SUB2API_REQUESTS_TREND_7D_METRIC_KEY));
  const trendDays = requestsTrend ? readRequestsTrendDays(requestsTrend.value) : [];

  return (
    <>
      {/* 四张统计卡，顺序逐字照原型 */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <MetricTile
          label="今日调用量"
          item={requestsDaily}
          note="按业务日聚合的请求量（XM-0039 reqlog 只读网关）"
          missingNote="调用量属于请求审计那条线（XM-0039 reqlog 只读网关）：今天只有逐条请求记录，没有按业务日聚合的指标"
          // 下方已经有一张专门的「近 7 日调用量」大图；卡片自己再叠一条基于
          // 历史接口的迷你折线只会是同一件事的第二个、口径还不一样的版本
          sparkline={false}
        />
        <MetricTile
          label="成功率（24h）"
          item={successRate24h}
          note="滚动 24 小时窗口统计，不是自然日"
          missingNote="Sub2API 侧还没有成功率指标。NewAPI 的渠道状态里有逐渠道错误率，但那是另一个平台、另一个口径，不能顶替"
          sparkline={false}
        />
        <MetricTile
          label="今日充值"
          item={revenue}
          // 契约写得明白：这条是当天支付订单 pay_amount 的累加（毛收入，不扣退款），
          // 正是原型说的「今日充值」。指标注册表里它叫「日收入」——那个名字
          // 与 §9.8「用户充值不是当期收入」冲突，这里按契约口径叫它充值
          note="当天支付订单累加（毛额，不扣退款）；充值不是当期收入，使用消费时才确认收入"
          missingNote="没有采到 sub2api.revenue.daily"
        />
        <MetricTile
          label="今日成本"
          item={cost}
          note="Sub2API 自己面板口径，与「今日充值」同一份快照；平台自建的成本台账见下方一行"
          missingNote="没有采到 sub2api.cost.daily"
        />
      </div>

      {/* 中部两栏 */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <WorkCard platform="sub2api" />
        <HealthCard
          platform="sub2api"
          channelBalance={channelBalance}
          channelStatus={channelStatus}
          demo={demo}
          hasMetrics={hasMetrics}
        />
      </div>

      {/* 底部大图 */}
      <TrendCard
        title="近 7 日调用量"
        item={requestsTrend}
        pendingNote="同上：调用量还没有按日聚合的指标。已接的充值与成本趋势在上面两张卡的迷你折线里"
        samples={requestsTrend ? toTrendSparkSamples(trendDays) : undefined}
      />

      <CostLineRow systemType="sub2api" label={label} />
      <DispositionNote platform="sub2api" />
    </>
  );
}

// --- NewAPI：原型 `V["newapi/overview"]` ---

function NewApiOverview({
  byKey,
  label,
  demo,
  hasMetrics,
  serviceId,
  serviceStatus,
}: {
  byKey: Map<string, MetricItem>;
  label: string;
  demo: boolean;
  hasMetrics: boolean;
  serviceId?: string;
  serviceStatus?: string;
}) {
  const users = byKey.get(NEWAPI_USERS_TOTAL_METRIC_KEY);
  const channels = byKey.get(NEWAPI_CHANNELS_METRIC_KEY);
  // 只在恰好一个已登记且 active 的 service 时启用真实渠道目录：与
  // ChannelTable.tsx 的 usesChannelRefGrain 同一个判据，多实例/未登记场景
  // 没有 serviceId 可用，猜一个是错的（见 NewApiChannelHealthCard 的说明）。
  const usesChannelRefGrain = Boolean(serviceId && serviceStatus === "active");
  const requestsTrend = usableTrendDaysMetric(byKey.get(NEWAPI_REQUESTS_TREND_7D_METRIC_KEY));
  const trendDays = requestsTrend ? readRequestsTrendDays(requestsTrend.value) : [];

  // NewAPI 的「我方计费 / 上游成本 / 毛利」不是充值指标：它们来自同一份
  // 渠道汇总快照，按 system_type 过滤后再分别聚合。这样页面不会把用户充值
  // 错当成使用收入，也不会拿 Sub2API 的行混进来。
  const financeQuery = useQuery({
    queryKey: ["finance", "channels", "summary"],
    queryFn: ({ signal }) => listChannelSummaries({ signal }),
  });
  const financeChannels = (financeQuery.data?.items ?? []).filter(
    (item) => item.systemType === "newapi",
  );
  const billing = aggregateChannelMoney(financeChannels, "usageRevenue", "newapi");
  const supplyCost = aggregateChannelMoney(financeChannels, "supplyCost", "newapi");
  const grossProfit = aggregateChannelMoney(financeChannels, "grossProfit", "newapi");
  const financeRange = financeRangeLabel(financeQuery.data?.from, financeQuery.data?.to);
  const financeDemo = shouldShowDemoBanner(
    financeChannels.map((item) => item.observed.source),
    appDemoDataConfig,
  );

  return (
    <>
      {/* 原型四格：用户总数 / 今日我方计费 / 今日上游成本 / 今日毛利。
          财务三格都从同一份 NewAPI 渠道汇总读取；Query 还没成功时保留卡位，
          显示「— + 未接入」，绝不把空数组渲染成 ¥0。 */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <NewApiUsersMetricCard item={users} />
        <NewApiFinanceMetricCard
          label="今日我方计费"
          aggregate={billing}
          queryPending={financeQuery.isPending}
          queryError={financeQuery.error}
          range={financeRange}
          note="按 NewAPI 渠道使用计费收入汇总；不含用户充值"
        />
        <NewApiFinanceMetricCard
          label="今日上游成本"
          aggregate={supplyCost}
          queryPending={financeQuery.isPending}
          queryError={financeQuery.error}
          range={financeRange}
          note="按 NewAPI 渠道供给成本汇总；倍率已在成本台账折算"
        />
        <NewApiFinanceMetricCard
          label="今日毛利"
          aggregate={grossProfit}
          queryPending={financeQuery.isPending}
          queryError={financeQuery.error}
          range={financeRange}
          note="使用收入 − 上游成本；覆盖不全时不显示合计"
        />
      </div>

      {financeDemo && !demo ? (
        <SampleDataBanner platform="newapi" demo hasMetrics={financeChannels.length > 0} />
      ) : null}

      {financeQuery.error ? (
        <p
          role="alert"
          className="rounded-md border border-danger bg-danger/10 px-3 py-2 text-xs text-danger"
        >
          NewAPI 财务汇总读取失败；三张金额卡暂不提供可信读数，页面保留上一次成功数据（如有）。
        </p>
      ) : null}

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <NewApiChannelHealthCard
          item={channels}
          serviceId={usesChannelRefGrain ? serviceId : undefined}
        />
        <TrendCard
          title="近 7 日请求量"
          item={requestsTrend}
          pendingNote="没有采到 newapi.requests.trend_7d；请求量趋势归请求审计线（reqlog），接入后按自然日显示请求数，缺数据的日子在图上断开、不用 0 顶替。"
          samples={requestsTrend ? toTrendSparkSamples(trendDays) : undefined}
        />
      </div>

      <p className="rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted">
        {label} 概览金额均按今日业务日读取；{financeRange}。财务卡的覆盖率、来源与新鲜度
        分别在卡片底部展示，渠道映射字段尚未接入时不会用空值冒充。
      </p>
    </>
  );
}

/** NewAPI 用户总数卡：沿用指标的来源 / 水位 / 新鲜度契约。 */
function NewApiUsersMetricCard({ item }: { item: MetricItem | undefined }) {
  if (!item) {
    return (
      <PendingTile
        label="用户总数"
        note="没有采到 newapi.users.total；NewAPI 用户 Connector 成功观测后才显示。"
      />
    );
  }

  const shown = presentMetric(item);
  const primary = metricPrimaryValue(item.metric_key, item.value);
  const unavailable = shown.unavailable || primary.raw === null;
  return (
    <MetricCard
      label="用户总数"
      metricKey={item.metric_key}
      value={unavailable ? "—" : shown.primary}
      unavailable={unavailable}
      secondary={shown.secondary ?? "活跃用户数未返回"}
      freshness={item.freshness}
      source={item.source}
      watermark={item.watermark}
    />
  );
}

interface NewApiFinanceMetricCardProps {
  label: string;
  aggregate: ChannelMoneyAggregate;
  queryPending: boolean;
  queryError: unknown;
  range: string;
  note: string;
}

/** NewAPI 三张金额卡的共同渲染。
 *
 *  金额由渠道汇总端点按字段分别聚合；任一渠道缺金额、观测不完整、币种/标度
 *  不一致时，aggregate.money 可能为空（或 freshness 不可信），两者都必须
 *  触发「— + 原因」。这样用户能区分「今天是 0」与「尚未接入」。 */
function NewApiFinanceMetricCard({
  label,
  aggregate,
  queryPending,
  queryError,
  range,
  note,
}: NewApiFinanceMetricCardProps) {
  const failure = newApiAggregateFailureText(aggregate);
  const coverage = newApiCoverageText(aggregate);
  const hasPreviousSnapshot = aggregate.money !== null && aggregate.failureReasons.length === 0;
  const queryUnavailable = queryPending || (Boolean(queryError) && !hasPreviousSnapshot);
  const aggregateAvailable = !queryUnavailable && hasPreviousSnapshot;
  const aggregateFreshnessValue = aggregateFreshness(aggregate, Date.now());
  const freshness = queryUnavailable
    ? queryError
      ? FAILED_FINANCE_FRESHNESS
      : UNAVAILABLE_FINANCE_FRESHNESS
    : queryError
      ? {
          ...aggregateFreshnessValue,
          state: "failed",
          last_error_code: "finance-summary-refresh-failed",
        }
      : aggregateFreshnessValue;
  const value = aggregateAvailable && aggregate.money
    ? formatScaledMoney(aggregate.money)
    : "—";
  const secondary = queryPending
    ? "渠道汇总加载中…"
    : queryError
      ? hasPreviousSnapshot
        ? `最近刷新失败，显示上次成功快照 · ${note} · ${coverage}`
        : "渠道汇总读取失败；未接入可信金额"
      : [note, coverage, failure].filter(Boolean).join(" · ");
  const source = queryError
    ? hasPreviousSnapshot
      ? aggregate.source || "渠道汇总来源未声明"
      : "渠道汇总不可用"
    : aggregate.source || "渠道汇总来源未声明";

  return (
    <MetricCard
      label={label}
      value={value}
      unavailable={!aggregateAvailable}
      secondary={secondary || undefined}
      freshness={freshness}
      source={source}
      link={
        <span className="text-xs text-fg-muted">
          {range} · {coverage}
        </span>
      }
    />
  );
}

function formatScaledMoney(value: { amountMinor: string; currency: string; scale: number }): string {
  // 延迟导入会让金额卡与 Sub2API 卡走同一套整数格式化；这里通过本文件顶部
  // 的 `formatScaledMinorUnits` 直接调用，绝不把 scale-6 微单位转成 float。
  return formatScaledMinorUnits(value.amountMinor, value.currency, value.scale);
}

function newApiCoverageText(aggregate: ChannelMoneyAggregate): string {
  const { completeRows, totalRows } = aggregate.coverage;
  if (totalRows === 0) return "覆盖 0/0 条渠道";
  return completeRows === totalRows
    ? `覆盖完整 ${completeRows}/${totalRows} 条渠道`
    : `覆盖不全 ${completeRows}/${totalRows} 条渠道`;
}

function newApiAggregateFailureText(aggregate: ChannelMoneyAggregate): string | null {
  const failure = aggregateFailureText(aggregate);
  return failure ? failure.replace(/Sub2API/g, "NewAPI") : null;
}

function financeRangeLabel(from: string | undefined, to: string | undefined): string {
  if (!from || !to) return "业务日范围未返回";
  return from === to ? `业务日 ${from}` : `业务日 ${from} ~ ${to}`;
}

/** trend_7d 的可用性判断不能走「主数值非 null」那一套（`metricPrimaryValue`
 *  假设「主数值」是个扁平字段，未登记时 raw 会是 null）：trend_7d 的 value
 *  是内嵌的逐日数组，根本没有这个意义上的主数值，套用那套判据会永远判成
 *  「不可用」（兜底口径找不到能当主数值的标量字段）。存在且初始化过就交给
 *  Sparkline 自己判断样本够不够画线：不足两个可画点时它会显示「暂无趋势」，
 *  不需要在这里重复判断一遍。Sub2API/NewAPI 的「近 7 日调用量/请求量」两张
 *  趋势卡共用这一个判据（XM-OVERVIEW-UI 引入，XM-NEWAPI-OVERVIEW0 复用）。 */
function usableTrendDaysMetric(item: MetricItem | undefined): MetricItem | undefined {
  if (!item || item.freshness.state === "uninitialized") return undefined;
  return item;
}

// --- 通用块 ---

/** 已接指标的统计卡：数值走 `presentMetric`，与指标卡口径逐字一致。
 *
 *  **新鲜度徽章必须在**（规格 §9.1）。原型这四格画的是环比涨跌，
 *  平台没有环比这个数（要两个业务日的口径一致才算得出），于是那个位置
 *  换成新鲜度——一个没有新鲜度的金额就是裸数字，而这一页正是拿来做决定的。
 *  折线放在底部槽位，与原型的 tile 内嵌 sparkline 同一个位置。 */
function MetricTile({
  label,
  item,
  note,
  missingNote,
  sparkline = true,
}: {
  label: string;
  item: MetricItem | undefined;
  note: string;
  missingNote: string;
  /** false 时不挂迷你趋势图（默认 true，与原有卡片行为一致）。
   *
   *  「今日调用量」「成功率（24h）」两张卡传 false：下面已经有一张专门的
   *  「近 7 日调用量」大图消费 trend_7d，卡片自己再叠一条基于历史接口
   *  （metric-history）的迷你折线，只会是同一件事的第二个、口径还不一样
   *  的版本——两条线时间粒度、数据来源都不同，放在一起只会让人怀疑
   *  是不是哪条画错了。 */
  sparkline?: boolean;
}) {
  if (!item) return <PendingTile label={label} note={missingNote} />;
  const shown = presentMetric(item);
  return (
    <StatTile
      label={label}
      value={shown.primary}
      unavailable={shown.unavailable}
      note={shown.secondary ? `${shown.secondary} · ${note}` : note}
      status={<FreshnessBadge freshness={item.freshness} />}
      link={
        <div className="flex flex-col gap-1">
          <FreshnessNote freshness={item.freshness} />
          {sparkline ? <MetricSparkline item={item} hours={SEVEN_DAYS_HOURS} /> : null}
        </div>
      }
    />
  );
}

/** 原型有格、平台没有数据源的那几格。
 *
 *  **摆出来但不给数字**：位置照原型（布局对齐优先），值给「—」+「未接入」徽章,
 *  并说清数据源归哪条线。删掉这一格会让页面看起来什么都不缺；
 *  给个 0 会让人以为今天真的没有调用。 */
function PendingTile({ label, note }: { label: string; note: string }) {
  return (
    <StatTile
      label={label}
      value="—"
      unavailable
      note={note}
      status={<Badge tone="neutral">未接入</Badge>}
    />
  );
}

function Card({
  title,
  hint,
  children,
}: {
  title: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <section className="flex flex-col gap-2 rounded-lg border border-edge bg-surface p-4">
      <header className="flex flex-wrap items-baseline gap-2">
        <h3 className="text-sm font-medium text-fg">{title}</h3>
        {hint ? <span className="text-xs text-fg-muted">{hint}</span> : null}
      </header>
      {children}
    </section>
  );
}

/** 「需要处理的事」——原型的左栏。数据源是活跃告警。 */
function WorkCard({ platform }: { platform: string }) {
  const query = useQuery({
    queryKey: ["alerts", ACTIVE_ALERT_STATUSES.join(",")],
    queryFn: ({ signal }) => listAlerts({ signal, status: ACTIVE_ALERT_STATUSES }),
  });

  const all = toWorkItems(query.data ?? [], platform, platformOfMetricKey);
  // 截断：告警会堆，不设上限时这一栏能把整页撑到几千像素高（浏览器实测）
  const { shown, hidden } = limitWorkItems(all);

  return (
    <Card title="需要处理的事" hint="只读阶段仅展示">
      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
        compact
      >
        {shown.length === 0 ? (
          <PageState
            kind="empty"
            compact
            title="这个平台没有待处理的告警"
            description="只统计与本平台指标相关的严重/注意两档活跃告警；提示（info）不进这一栏，全部告警在告警与故障页。"
          />
        ) : (
          <ul className="flex flex-col gap-2">
            {shown.map((item) => (
              <li key={item.id}>
                <Link
                  to={item.href}
                  className="flex items-start gap-2 rounded-md border border-edge bg-surface-muted px-3 py-2 hover:border-accent"
                >
                  <Badge tone={item.tone === "bad" ? "danger" : "warning"}>
                    {workItemLabel(item.tone)}
                  </Badge>
                  <span className="min-w-0">
                    <span className="block text-xs font-medium text-fg">{item.title}</span>
                    <span className="block text-xs text-fg-muted">{item.detail}</span>
                  </span>
                </Link>
              </li>
            ))}
            {hidden > 0 ? (
              // 截掉了多少条要说出来：一栏「只有 4 条」的告警会让人以为就这些
              <li className="text-xs text-fg-muted">
                还有 {hidden} 条本平台的活跃告警没摆出来，
                <Link to="/alerts" className="mx-1 underline underline-offset-2">
                  去告警与故障页
                </Link>
                看全部。
              </li>
            ) : null}
          </ul>
        )}
      </ApiStateView>
    </Card>
  );
}

/** 「上游健康」——原型的右栏：上游渠道 / 订阅账号 / 连接状态三行。 */
function HealthCard({
  platform,
  channelBalance,
  channelStatus,
  demo,
  hasMetrics,
}: {
  platform: string;
  /** 渠道余额指标（sub2api.channels.balance）——订阅型上游账号下这条永远
   *  是空数组，仅在 channelStatus 也缺时兜底，或作为补充信息。 */
  channelBalance: MetricItem | undefined;
  /** 渠道状态指标（sub2api.channels.status）——「上游渠道」行的首选数据源。 */
  channelStatus: MetricItem | undefined;
  demo: boolean;
  hasMetrics: boolean;
}) {
  const accounts = useQuery({
    queryKey: [UPSTREAM_ACCOUNTS_QUERY],
    queryFn: ({ signal }) => listUpstreamAccounts({ signal }),
  });

  const balanceRows =
    channelBalance && channelBalance.freshness.state !== "uninitialized"
      ? readChannelRows(channelBalance.value)
      : [];
  const statusRows =
    channelStatus && channelStatus.freshness.state !== "uninitialized"
      ? readSub2ApiChannelStatusRows(channelStatus.value)
      : [];
  const rows: HealthRow[] = [
    // 渠道状态/余额指标只有 Sub2API 有；NewAPI 的渠道健康在左栏那张表里，
    // 这里不给它一行永远「未接入」的重复信息。优先用状态、缺了才落到余额，
    // 两者都没有才是「未接入」——见 sub2ApiChannelHealth 的判据说明
    ...(platform === "sub2api" ? [sub2ApiChannelHealth(statusRows, balanceRows)] : []),
    subscriptionHealth(accounts.data ?? [], platform),
    connectionHealth(demo, hasMetrics),
  ];

  return (
    <Card title="上游健康" hint="上游渠道 + 订阅账号">
      <div className="flex flex-col gap-2">
        {rows.map((row) => (
          <div key={row.label} className="flex flex-wrap items-center gap-2 text-xs">
            <span className="w-20 shrink-0 font-medium text-fg">{row.label}</span>
            {row.text === null ? (
              <Badge tone="neutral" title={row.hint}>
                未接入
              </Badge>
            ) : (
              <Badge tone={badgeTone(row.tone)} title={row.hint}>
                {row.text}
              </Badge>
            )}
            <span className="min-w-0 flex-1 truncate text-fg-muted" title={row.hint}>
              {row.hint}
            </span>
            <Link
              to={`/platforms/${platform}${row.href}`}
              className="shrink-0 text-accent underline underline-offset-2"
            >
              查看 ›
            </Link>
          </div>
        ))}
      </div>
    </Card>
  );
}

function badgeTone(tone: HealthRow["tone"]): "success" | "warning" | "danger" | "neutral" {
  switch (tone) {
    case "ok":
      return "success";
    case "warn":
      return "warning";
    case "bad":
      return "danger";
    default:
      return "neutral";
  }
}

/** NewAPI 供应商映射表查不到这个渠道类型时的原因——渠道目录契约的 vendor
 *  字段本身就是 null（不是没接，是查表落空），XM-CHAN-WIRE0 的 handoff 已经
 *  记录过这条：上游新增渠道类型后需要人工同步 chanfields 的静态映射表。 */
const NEWAPI_VENDOR_NULL_REASON =
  "这个渠道的类型不在 NewAPI 供应商映射表内（渠道目录契约 vendor 字段查表查不到，需要人工同步映射表）";

/** 「分组」列没有任何数据源：渠道目录契约（XM-CHAN-FIELDS0）的 14 个扩展
 *  字段里没有分组维度。NewAPI 上游确实有原生 Group 字段（model/channel.go
 *  的 group 列，路由用），但今天没有任何连接器/契约把它读出来——摆位置显示
 *  未接入，不编数字（宪法 12 条）。 */
const NEWAPI_GROUP_NULL_REASON =
  "NewAPI 渠道原生有分组字段（用于路由），但渠道目录契约（XM-CHAN-FIELDS0）没有采集这一维度，暂无数据源";

/** 「状态」列没有匹配到 newapi.channels.status 指标记录时的原因——覆盖两种
 *  情况（整条指标没采到 / 这一条渠道单独没匹配上），不细分是为了不过度
 *  声称精度：两种情况给运营的下一步动作是一样的（等下一轮采集）。 */
const NEWAPI_STATUS_NULL_REASON = "该渠道在 newapi.channels.status 指标里没有匹配记录，或该指标尚未成功采集";

/** 渠道目录 Query（`GET /api/v1/platforms/newapi/channels`）的新鲜度契约。
 *  目录本身不是指标观测，没有现成的 FreshnessContract 可用，这里从
 *  inventory.observedAt/complete 构造一份；阈值沿用全站 1800 秒的惯例
 *  （financeOverview.ts 的 aggregateFreshness 同一约定）。 */
function channelCatalogFreshness(page: PlatformChannelPage, now: number): FreshnessContract {
  if (!page.inventory.observedAt) {
    return {
      state: "uninitialized",
      staleness_seconds: null,
      threshold_seconds: 1800,
      is_partial: false,
      observed_at: null,
      last_success: null,
      last_error_code: "",
    };
  }
  const seconds = Math.max(0, Math.round((now - Date.parse(page.inventory.observedAt)) / 1000));
  const partial = !page.inventory.complete || page.inventory.coveragePartial;
  return {
    state: partial ? "partial" : seconds >= 1800 ? "stale" : "fresh",
    staleness_seconds: seconds,
    threshold_seconds: 1800,
    is_partial: partial,
    observed_at: page.inventory.observedAt,
    last_success: page.inventory.observedAt,
    last_error_code: "",
  };
}

/** NewAPI 的「渠道健康」表（原型左栏）。
 *
 *  XM-NEWAPI-OVERVIEW0：行的主数据源从 newapi.channels.status 指标换成真实
 *  渠道目录（`listPlatformChannels`，XM-CHAN-FIELDS0/WIRE0 已经交付真实的
 *  vendor / today.successRate 两个字段）——渠道/上游/成功率三列因此从「摆
 *  位置不给数字」升级成真数据，并且每行链接到渠道详情页（原型
 *  `data-go="newapi/upstream/detail/<id>"`，`channelDetailPath` 与
 *  ManagedChannelTable/ChannelDetailPage 同一个路由，点进去是同一个页面）。
 *
 *  「状态」列刻意继续用 newapi.channels.status 指标的 enabled/error_rate_ppm,
 *  不改用目录的 row.status 字符串——那是 XM-CHAN-MERGE0 记录在案的刻意取舍
 *  （sub2api/newapi 的 status 取值集合不同、都不是健康语义，贸然映射等于猜,
 *  见该片 handoff 的 risks 一节），本片认同这个判断、不重新决定它。两个数据
 *  源共用同一条底层 observation（`newapi.channels.status`，见
 *  internal/platform/httpapi/channel_bindings.go 的 findChannelsObservation
 *  注释：目录 Query 与指标端点读的是同一个 observation），按
 *  channel_id / channelRef.externalChannelId 关联，两边 id 同源，不是猜的。
 *
 *  只在**恰好一个已登记且 active 的 service** 时启用目录数据源（与
 *  ChannelTable.tsx 的 usesChannelRefGrain 同一个判据，由 NewApiOverview
 *  算好通过 serviceId 传进来）；没有 serviceId 时落回纯指标口径的旧实现
 *  （LegacyNewApiChannelHealthCard，行为逐字不变）——多实例/未登记场景没有
 *  serviceId 可用，猜一个是错的。 */
function NewApiChannelHealthCard({
  item,
  serviceId,
}: {
  item: MetricItem | undefined;
  serviceId: string | undefined;
}) {
  if (!serviceId) return <LegacyNewApiChannelHealthCard item={item} />;
  return <RealNewApiChannelHealthCard item={item} serviceId={serviceId} />;
}

function RealNewApiChannelHealthCard({
  item,
  serviceId,
}: {
  item: MetricItem | undefined;
  serviceId: string;
}) {
  const query = useQuery({
    queryKey: ["platform-channels", "newapi", serviceId],
    queryFn: ({ signal }) => listPlatformChannels("newapi", serviceId, { signal }),
  });

  // 「状态」列的匹配表：与目录共用同一条底层 observation（见上方文档注释）,
  // 按渠道 id 关联，指标未采集/未初始化时留空表，所有行落到「未接入」分支。
  const statusByChannelId = new Map<string, NewApiChannelRow>();
  if (item && item.freshness.state !== "uninitialized") {
    for (const row of readNewApiChannelRows(item.value)) statusByChannelId.set(row.channelId, row);
  }

  const page = query.data;
  const freshness = page ? channelCatalogFreshness(page, Date.now()) : null;

  return (
    <Card title="渠道健康" hint={page ? `${page.items.length} 条 NewAPI 渠道` : undefined}>
      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()} compact>
        {page && page.items.length === 0 ? (
          <PageState
            kind="empty"
            compact
            title="NewAPI 渠道目录为空"
            description="当前 service 的渠道目录为空或尚未成功采集；这不等于上游没有渠道。"
          />
        ) : page && freshness ? (
          <>
            <div className="relative max-w-full overflow-x-auto rounded-md border border-edge">
              <table className="w-full border-collapse">
                <caption className="sr-only">
                  NewAPI 渠道健康：渠道、上游、分组、成功率与状态，点击渠道名进入详情页
                </caption>
                <thead className="border-b border-edge bg-surface-muted">
                  <tr>
                    {["渠道", "上游", "分组", "成功率", "状态"].map((h) => (
                      <th
                        key={h}
                        scope="col"
                        className="px-2 py-1 text-left text-xs font-medium whitespace-nowrap text-fg-muted"
                      >
                        {h}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {page.items.map((row) => (
                    <NewApiChannelHealthRow
                      key={platformChannelRowKey(row)}
                      row={row}
                      status={statusByChannelId.get(row.channelRef.externalChannelId)}
                    />
                  ))}
                </tbody>
              </table>
            </div>
            <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-fg-muted">
              <FreshnessBadge freshness={freshness} />
              <FreshnessNote freshness={freshness} />
              <span>来源 {page.inventory.source || "未声明"}</span>
              <span>· 覆盖 {page.items.length} 条渠道目录</span>
            </div>
            <p className="text-xs text-fg-muted">{NEWAPI_GROUP_NULL_REASON}</p>
          </>
        ) : null}
      </ApiStateView>
    </Card>
  );
}

/** 渠道健康表的一行：渠道名链到详情页，上游/成功率来自目录，分组恒未接入,
 *  状态来自按 id 关联到的指标行（可能没有匹配，见 NEWAPI_STATUS_NULL_REASON）。 */
function NewApiChannelHealthRow({
  row,
  status,
}: {
  row: PlatformChannelRow;
  status: NewApiChannelRow | undefined;
}) {
  const successRate =
    row.today === null
      ? { text: null, reason: channelFieldNullReason("today", "newapi") }
      : row.today.successRate === null
        ? { text: null, reason: "该渠道今日统计已返回请求数与成本，但没有返回成功率" }
        : { text: `${(row.today.successRate * 100).toFixed(1)}%`, reason: null };

  return (
    <tr className="border-b border-edge last:border-b-0">
      <td className="px-2 py-1 text-xs text-fg">
        <Link
          to={channelDetailPath("newapi", row.channelRef.externalChannelId)}
          className="block font-medium text-accent underline underline-offset-2"
        >
          {row.name || row.channelRef.externalChannelId || "—"}
        </Link>
        {status?.type ? <span className="block text-fg-muted">类型 {status.type}</span> : null}
      </td>
      <td className="px-2 py-1 text-xs text-fg">
        {row.vendor ? row.vendor : <HealthUnavailableCell reason={NEWAPI_VENDOR_NULL_REASON} />}
      </td>
      <td className="px-2 py-1 text-xs">
        <HealthUnavailableCell reason={NEWAPI_GROUP_NULL_REASON} />
      </td>
      <td className="px-2 py-1 text-xs text-fg">
        {successRate.text ?? <HealthUnavailableCell reason={successRate.reason ?? ""} />}
      </td>
      <td className="px-2 py-1 text-xs text-fg">
        {status ? (
          <div className="flex flex-col items-start gap-1">
            <Badge tone={status.enabled === true ? "success" : status.enabled === false ? "neutral" : "warning"}>
              {status.enabled === true ? "启用" : status.enabled === false ? "停用" : "未知"}
            </Badge>
            <span className="text-fg-muted">
              错误率 {status.errorRatePPM === null ? "未接入" : formatErrorRatePPM(status.errorRatePPM)}
            </span>
          </div>
        ) : (
          <HealthUnavailableCell reason={NEWAPI_STATUS_NULL_REASON} />
        )}
      </td>
    </tr>
  );
}

/** 没有 serviceId（0 个或多个已登记 service）时的旧实现：纯指标口径，
 *  上游 / 分组 / 成功率三列摆位置但不给数字。多实例/未登记场景没有真实
 *  渠道目录可读，猜一个 serviceId 是错的，因此原样保留这条兜底路径。 */
function LegacyNewApiChannelHealthCard({ item }: { item: MetricItem | undefined }) {
  if (!item || item.freshness.state === "uninitialized") {
    return (
      <Card title="渠道健康">
        <PageState
          kind="empty"
          compact
          title="还没有渠道状态观测"
          description={`该环境下还没有 ${NEWAPI_CHANNELS_METRIC_KEY} 的成功观测；NewAPI 采集任务跑起来后会出现在这里。`}
        />
      </Card>
    );
  }

  const rows = readNewApiChannelRows(item.value);
  if (rows.length === 0) {
    return (
      <Card title="渠道健康">
        <PageState
          kind="empty"
          compact
          title="渠道状态没有返回逐渠道记录"
          description="NewAPI 渠道状态指标已观测，但没有可展示的渠道行；不会把空数组解释成全部正常。"
        />
        <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-fg-muted">
          <FreshnessBadge freshness={item.freshness} />
          <FreshnessNote freshness={item.freshness} />
          <span>来源 {item.source || "未声明"}</span>
        </div>
      </Card>
    );
  }

  return (
    <Card title="渠道健康" hint={`${rows.length} 条 NewAPI 渠道`}>
      <div className="relative max-w-full overflow-x-auto rounded-md border border-edge">
        <table className="w-full border-collapse">
          <caption className="sr-only">
            NewAPI 渠道启停与错误率；原型列为渠道、上游、分组、成功率、状态
          </caption>
          <thead className="border-b border-edge bg-surface-muted">
            <tr>
              {["渠道", "上游", "分组", "成功率", "状态"].map((h) => (
                <th
                  key={h}
                  scope="col"
                  className="px-2 py-1 text-left text-xs font-medium whitespace-nowrap text-fg-muted"
                >
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.channelId} className="border-b border-edge last:border-b-0">
                <td className="px-2 py-1 text-xs text-fg">
                  <span className="block">{row.name || row.channelId || "—"}</span>
                  <span className="block text-fg-muted">类型 {row.type || "未返回"}</span>
                </td>
                <td className="px-2 py-1 text-xs">
                  <HealthUnavailableCell reason="没有 serviceId 可用（0 个或多个已登记 service），无法读取真实渠道目录来解析渠道 ↔ 上游映射" />
                </td>
                <td className="px-2 py-1 text-xs">
                  <HealthUnavailableCell reason="上游分组字段未接入" />
                </td>
                <td className="px-2 py-1 text-xs">
                  <HealthUnavailableCell reason="没有 serviceId 可用（0 个或多个已登记 service），无法读取真实渠道目录来解析平台成功率" />
                </td>
                <td className="px-2 py-1 text-xs text-fg">
                  <div className="flex flex-col items-start gap-1">
                    <Badge tone={row.enabled === true ? "success" : row.enabled === false ? "neutral" : "warning"}>
                      {row.enabled === true ? "启用" : row.enabled === false ? "停用" : "未知"}
                    </Badge>
                    <span className="text-fg-muted">
                      错误率 {row.errorRatePPM === null ? "未接入" : formatErrorRatePPM(row.errorRatePPM)}
                    </span>
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-fg-muted">
        <FreshnessBadge freshness={item.freshness} />
        <FreshnessNote freshness={item.freshness} />
        <span>来源 {item.source || "未声明"}</span>
        <span>· 覆盖 {rows.length} 条逐渠道状态</span>
      </div>
      {/* 原型这张表的上游 / 分组 / 成功率列保留位置，但这条兜底路径没有
          serviceId、读不到真实渠道目录；每个单元格显式写「未接入」，避免
          空白被误读。恰好一个已登记且 active 的 service 时会走
          RealNewApiChannelHealthCard，那条路径上游/成功率是真数据。 */}
      <p className="text-xs text-fg-muted">
        上游、分组与成功率三列暂未接入：这个环境没有恰好一个已登记且运行中的 NewAPI
        service，无法读取真实渠道目录；分组字段本身也还没有任何数据源。当前可用的是启停与错误率。
      </p>
    </Card>
  );
}

function HealthUnavailableCell({ reason }: { reason: string }) {
  return (
    <span className="flex flex-col text-fg-muted" title={reason}>
      <span>—</span>
      <span className="text-[11px]">未接入</span>
    </span>
  );
}

/** 底部大图。有指标就画七天，没有就把位置留着并说清缺什么。
 *
 *  两条数据管线共用这一张卡：NewAPI 的「近 7 日请求量」走 MetricSparkline
 *  （按 metric-history 接口分次采集的历史快照，一路复用至今）；Sub2API 的
 *  「近 7 日调用量」走 trend_7d——**同一条指标观测里内嵌的逐日数组**，没有
 *  历史快照可拉。`samples` 传了就用它直接画（不再渲染 MetricSparkline，
 *  避免同一张卡对同一个指标发起两份不必要的历史请求）；不传则维持
 *  MetricSparkline 的旧路径，NewAPI 那张卡因此零改动。 */
function TrendCard({
  title,
  item,
  pendingNote,
  samples,
}: {
  title: string;
  item: MetricItem | undefined;
  pendingNote: string;
  samples?: SparkSample[];
}) {
  return (
    <Card
      title={title}
      hint={item ? `来源 ${item.source || "未声明"} · 水位 ${item.watermark || "—"}` : undefined}
    >
      {item ? (
        <>
          <div className="mb-2 flex flex-wrap items-center gap-2 text-xs text-fg-muted">
            <FreshnessBadge freshness={item.freshness} />
            <FreshnessNote freshness={item.freshness} />
            <span>指标 {item.metric_key}</span>
          </div>
          {samples ? (
            <Sparkline samples={samples} label={`${title}（近 7 个自然日）`} box={BIG_CHART_BOX} />
          ) : (
            <MetricSparkline item={item} hours={SEVEN_DAYS_HOURS} box={BIG_CHART_BOX} />
          )}
          <p className="mt-2 text-xs text-fg-muted">
            {samples
              ? "近 7 个自然日（UTC）；缺数据的日子折线在此断开，不拿上一天的读数顶替。"
              : "历史窗口 168 小时（近 7 日）；趋势完整性与来源切换说明随历史接口返回。"}
          </p>
        </>
      ) : (
        <PageState kind="unavailable" compact title="未接入" description={pendingNote} />
      )}
    </Card>
  );
}

/** 成本线那一行（XM-0037d 的四张卡）。
 *
 *  单独一行并写明口径：上面「今日成本」是 Sub2API 自己面板报的数，
 *  这一行是平台自建的成本台账（按上游账号与倍率算出来的供给成本）。
 *  **两者不是同一个口径，不该相减**——把它们并成一格才是真正会骗人的做法。 */
function CostLineRow({ systemType, label }: { systemType: string; label: string }) {
  return (
    <section className="flex flex-col gap-2">
      <h3 className="text-sm font-medium text-fg">
        成本台账口径
        <span className="ml-2 text-xs font-normal text-fg-muted">
          平台自建的成本核算（XM-0037），与上面那格「今日成本」口径不同，不要相减
        </span>
      </h3>
      <FinanceSummaryCards systemType={systemType} label={label} />
    </section>
  );
}

/** 原型没有的格去了哪。
 *
 *  写出来而不是默默删掉：这一页上曾经有五张指标卡，人再来看时会问
 *  「用户数那张呢」。说清它去了哪一页，比让人以为功能没了强。 */
function DispositionNote({ platform }: { platform: string }) {
  return (
    <p className="rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted">
      原型的概览没有用户数与用户余额两格：它们在
      <Link to={`/platforms/${platform}?tab=users`} className="mx-1 underline underline-offset-2">
        用户管理
      </Link>
      的顶部（同一份指标，口径不变）；渠道状态与余额明细在
      <Link to={`/platforms/${platform}?tab=upstream`} className="mx-1 underline underline-offset-2">
        渠道管理
      </Link>
      ，本页「上游渠道 x / y 可用」优先按渠道状态统计，只有状态观测也缺时才落回余额口径。
    </p>
  );
}
