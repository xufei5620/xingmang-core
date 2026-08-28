import { useQuery } from "@tanstack/react-query";
import { FreshnessBadge, FreshnessNote, PageState, StatTile } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import type { ReactNode } from "react";
import { Link } from "react-router";
import { ACTIVE_ALERT_STATUSES, listAlerts } from "../api/alerts";
import { listUpstreamAccounts, UPSTREAM_ACCOUNTS_QUERY } from "../api/finance";
import { listMetrics, type MetricItem } from "../api/platform";
import { appDemoDataConfig, shouldShowDemoBanner } from "../lib/demoData";
import {
  channelHealth,
  connectionHealth,
  subscriptionHealth,
  limitWorkItems,
  toWorkItems,
  workItemLabel,
  type HealthRow,
} from "../lib/overview";
import {
  CHANNEL_BALANCE_METRIC_KEY,
  NEWAPI_CHANNELS_METRIC_KEY,
  presentMetric,
  readChannelRows,
  readNewApiChannelRows,
} from "../lib/metrics";
import { formatErrorRatePPM } from "../lib/money";
import { platformOfMetricKey } from "../lib/platforms";
import { ApiStateView } from "./ApiStateView";
import { FinanceSummaryCards } from "./FinanceSummaryCards";
import { MetricSparkline } from "./MetricSparkline";

/** 概览页的大图窗口：168 小时正好七天，也是后端 `maxHistoryHours` 的上限。 */
const SEVEN_DAYS_HOURS = 168;

/** 底部大图的画布。与卡片里的迷你图是**同一个组件换个盒子**——
 *  另写一个图表组件的话，两处的失败样本与部分数据画法迟早会漂开。 */
const BIG_CHART_BOX = { width: 960, height: 180, padding: 8 };

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
}: {
  serviceType: string;
  label: string;
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
            <NewApiOverview byKey={byKey} label={label} demo={demo} hasMetrics={mine.length > 0} />
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
  const channels = byKey.get(CHANNEL_BALANCE_METRIC_KEY);

  return (
    <>
      {/* 四张统计卡，顺序逐字照原型 */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <PendingTile
          label="今日调用量"
          note="调用量属于请求审计那条线（XM-0039 reqlog 只读网关）：今天只有逐条请求记录，没有按业务日聚合的指标"
        />
        <PendingTile
          label="成功率（24h）"
          note="Sub2API 侧还没有成功率指标。NewAPI 的渠道状态里有逐渠道错误率，但那是另一个平台、另一个口径，不能顶替"
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
          channels={channels}
          demo={demo}
          hasMetrics={hasMetrics}
        />
      </div>

      {/* 底部大图 */}
      <TrendCard
        title="近 7 日调用量"
        item={undefined}
        pendingNote="同上：调用量还没有按日聚合的指标。已接的充值与成本趋势在上面两张卡的迷你折线里"
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
}: {
  byKey: Map<string, MetricItem>;
  label: string;
  demo: boolean;
  hasMetrics: boolean;
}) {
  const users = byKey.get("newapi.users.total");
  const channels = byKey.get(NEWAPI_CHANNELS_METRIC_KEY);

  return (
    <>
      {/* 原型这四格是 用户总数 / 今日我方计费 / 今日上游成本 / 今日毛利。
          后三格是成本线的口径，成本卡那一行给的就是它们——这里不再复制一份
          数字，避免同一个金额在一页上有两处、两处还可能不同步 */}
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <MetricTile
          label="用户总数"
          item={users}
          note="含今日活跃数"
          missingNote="没有采到 newapi.users.total"
        />
        <PendingTile
          label="今日请求量"
          note="同 Sub2API：请求量属于 reqlog 那条线，没有按日聚合的指标"
        />
        <PendingTile
          label="成功率（24h）"
          note="渠道状态里有逐渠道错误率（下方「渠道健康」），但没有平台级的成功率口径"
        />
        <PendingTile
          label="今日订阅"
          note="newapi.subscription.daily 已在采集范围内，但这个环境还没有观测"
        />
      </div>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
        <NewApiChannelHealthCard item={channels} />
        <HealthCard platform="newapi" channels={undefined} demo={demo} hasMetrics={hasMetrics} />
      </div>

      <TrendCard
        title="近 7 日请求量"
        item={undefined}
        pendingNote="请求量没有按日聚合的指标；用户数与充值的趋势可在各自卡片里看"
      />

      <CostLineRow systemType="newapi" label={label} />
      <DispositionNote platform="newapi" />
    </>
  );
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
}: {
  label: string;
  item: MetricItem | undefined;
  note: string;
  missingNote: string;
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
          <MetricSparkline item={item} hours={SEVEN_DAYS_HOURS} />
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
  channels,
  demo,
  hasMetrics,
}: {
  platform: string;
  channels: MetricItem | undefined;
  demo: boolean;
  hasMetrics: boolean;
}) {
  const accounts = useQuery({
    queryKey: [UPSTREAM_ACCOUNTS_QUERY],
    queryFn: ({ signal }) => listUpstreamAccounts({ signal }),
  });

  const channelRows =
    channels && channels.freshness.state !== "uninitialized"
      ? readChannelRows(channels.value)
      : [];
  const rows: HealthRow[] = [
    // 渠道余额指标只有 Sub2API 有；NewAPI 的渠道健康在左栏那张表里，
    // 这里不给它一行永远「未接入」的重复信息
    ...(platform === "sub2api" ? [channelHealth(channelRows)] : []),
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

/** NewAPI 的「渠道健康」表（原型左栏）。 */
function NewApiChannelHealthCard({ item }: { item: MetricItem | undefined }) {
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
  return (
    <Card title="渠道健康" hint={`${rows.length} 条 NewAPI 渠道`}>
      <div className="relative max-w-full overflow-x-auto rounded-md border border-edge">
        <table className="w-full border-collapse">
          <caption className="sr-only">NewAPI 渠道启停与错误率</caption>
          <thead className="border-b border-edge bg-surface-muted">
            <tr>
              {["渠道", "类型", "错误率", "状态"].map((h) => (
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
                <td className="px-2 py-1 text-xs text-fg">{row.name || row.channelId || "—"}</td>
                <td className="px-2 py-1 text-xs text-fg-muted">{row.type || "—"}</td>
                <td className="px-2 py-1 text-xs tabular-nums text-fg">
                  {/* 走 formatErrorRatePPM 而不是 `Number(ppm)/10000`：
                      契约层用 ppm 整数就是为了不碰浮点，显示层再丢回 float
                      等于把纪律守到最后一米又松手（lib/money 同一条） */}
                  {row.errorRatePPM === null ? "—" : formatErrorRatePPM(row.errorRatePPM)}
                </td>
                <td className="px-2 py-1 text-xs">
                  <Badge tone={row.enabled === true ? "success" : row.enabled === false ? "neutral" : "warning"}>
                    {row.enabled === true ? "启用" : row.enabled === false ? "停用" : "未知"}
                  </Badge>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {/* 原型这张表还有「上游」「分组」「成功率」三列。前两列要 XM-0037d 的
          渠道汇总按平台渠道 id 对得上才填得出（见 lib/channelEconomics），
          成功率没有数据源——不画空列冒充已接 */}
      <p className="text-xs text-fg-muted">
        原型这张表还有上游、分组与成功率三列：前两列要等「平台渠道 ↔ 上游账号」的对应关系，
        成功率没有数据源，都不先画空列。
      </p>
    </Card>
  );
}

/** 底部大图。有指标就画七天，没有就把位置留着并说清缺什么。 */
function TrendCard({
  title,
  item,
  pendingNote,
}: {
  title: string;
  item: MetricItem | undefined;
  pendingNote: string;
}) {
  return (
    <Card title={title}>
      {item ? (
        <MetricSparkline item={item} hours={SEVEN_DAYS_HOURS} box={BIG_CHART_BOX} />
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
      的顶部（同一份指标，口径不变）；渠道余额明细在
      <Link to={`/platforms/${platform}?tab=upstream`} className="mx-1 underline underline-offset-2">
        渠道管理
      </Link>
      ，本页只用它算「上游渠道 x / y 可用」。
    </p>
  );
}
