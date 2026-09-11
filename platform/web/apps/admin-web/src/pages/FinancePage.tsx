import { useQuery, type UseQueryResult } from "@tanstack/react-query";
import {
  formatUtcTimestamp,
  navItemByPath,
  navLabel,
  PageHeader,
  PageState,
  StatTile,
} from "@xingmang/ui-admin";
import { Badge, Tabs } from "@xingmang/ui-primitives";
import { Link, useSearchParams } from "react-router";

import {
  listChannelSummaries,
  listUpstreamSummaries,
  platformHasRefunds,
  UPSTREAM_SUMMARY_QUERY,
  type Money,
  type RunwayCoverage,
} from "../api/finance";
import { listMetrics, type MetricItem } from "../api/platform";
import { blueprintForPath, BlueprintTabView } from "../blueprints";
import { ApiStateView } from "../components/ApiStateView";
import { FinanceSummaryCards } from "../components/FinanceSummaryCards";
import { InvoiceConsolePanel } from "../components/InvoiceConsolePanel";
import {
  BucketCard,
  metricByKey,
  metricKeyFor,
  type PaymentsPlatform,
} from "../components/PaymentSummaryCards";
import { businessTodayDateOnly } from "../components/Sub2ApiOrdersPanel";
import {
  costCollectionNote,
  costCollectionState,
  crossPlatformBucketTotal,
  crossPlatformTotalNote,
  runwayAttentionEmptyReason,
  runwayAttentionItems,
  type CrossPlatformTotal,
  type PlatformBucketFact,
} from "../lib/financeGlobal";
import { readPaymentsDailySummary, type PaymentsDailySummary } from "../lib/metrics";
import { formatMinorUnits, formatScaledMinorUnits } from "../lib/money";
import { RUNWAY_TONE } from "../lib/runway";

/** 跨平台财务页（XM-FINANCE-GLOBAL0）。
 *
 *  六个子页签里今天有三格是真的：
 *  - **财务总览**：两平台 `payments.daily` 指标 + `/finance/{channels,upstreams}/summary`；
 *  - **开票集成**：嵌入开票系统自己的控制台（CR-0005 平台线 g，从 PlaceholderPage
 *    的 `governanceSubTabOverride` 搬过来的同一条路径，平台侧不读也不显示任何开票数字）；
 *  - **财务配置**：ADR-006 的金额语义与 ADR-003 的写入解锁条件——它们不是数据，
 *    是**已冻结的决定**，所以可以写死并注明出处。
 *
 *  其余三格（支付通道 / 财务对账 / 异常与冻结）后端根本不存在：`connectors/payment/`
 *  是空目录、finance schema 里没有对账批次表与异常表、路由表里没有对应端点。
 *  它们继续渲染蓝图（列头照原型、数字一个不显示）并在上面压一句「在等什么」——
 *  蓝图带着逐字列头，比一句「尚未接入」信息量大得多。
 *
 *  贯穿全页的三条纪律：跨币种不相加（fail closed）；「还没采到」与「今天是 0」
 *  长得不一样；每一屏都说清自己只覆盖到哪儿。 */

const FINANCE_SUB_TABS = (navItemByPath("/finance")?.item.subTabs ?? []).map(
  (tab) => [tab.id, tab.label] as const,
);

/** 默认子页 = 接了真实跨平台数据的那一格。 */
const FINANCE_DEFAULT_SUB = "overview";

const FINANCE_BLUEPRINT_PAGE = blueprintForPath("/finance");

const PLATFORMS: readonly { readonly key: PaymentsPlatform; readonly label: string }[] = [
  { key: "sub2api", label: "Sub2API" },
  { key: "newapi", label: "NewAPI" },
];

export function FinancePage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const rawSub = searchParams.get("sub");
  const activeSub = rawSub === null || rawSub.trim() === "" ? FINANCE_DEFAULT_SUB : rawSub;
  const known = FINANCE_SUB_TABS.some(([value]) => value === activeSub);

  // 认不出来的 ?sub= **不静默回落**到第一格：一个悄悄换了内容的地址，
  // 会让贴链接的人以为对方看到的是自己那一屏（同 OpsPage）。
  if (!known) {
    return (
      <section>
        <PageHeader
          title={navLabel("/finance")}
          description="跨平台财务的子页按里程碑逐步接入；未知地址不会静默回落到财务总览。"
        />
        <PageState
          kind="unavailable"
          title={`「${rawSub}」子页尚未接入`}
          description="请从已定义的跨平台财务子页中选择。"
          action={
            <Link
              to={`/finance?sub=${FINANCE_DEFAULT_SUB}`}
              className="text-sm font-medium text-accent hover:underline"
            >
              返回财务总览
            </Link>
          }
        />
      </section>
    );
  }

  const selectSub = (value: string) => {
    const next = new URLSearchParams(searchParams);
    next.set("sub", value);
    // 换子页签用 replace：连点六格不该在浏览器里堆六条历史
    setSearchParams(next, { replace: true });
  };

  return <FinanceBody activeSub={activeSub} onSelectSub={selectSub} />;
}

/** 一个平台的当日支付汇总（指标 + 解析结果）。 */
interface PlatformDaily {
  key: PaymentsPlatform;
  label: string;
  metric: MetricItem | undefined;
  summary: PaymentsDailySummary | null;
}

/** 页身。与 `FinancePage` 分开只为一件事：未知 `?sub=` 那一支**不发请求**——
 *  查询挂在这里，认不出地址时这个组件根本不挂载。 */
function FinanceBody({
  activeSub,
  onSelectSub,
}: {
  activeSub: string;
  onSelectSub: (value: string) => void;
}) {
  // 业务日固定为「今天」（Asia/Shanghai）：payments.daily 是单日粒度指标，
  // 这一页做的是「今天两个平台一共进来多少钱」，不提供区间选择——
  // 周 / 月合计要按天聚合，给一个能选区间却只会显示单日值的控件更糟。
  const businessDay = businessTodayDateOnly();
  const metricsQuery = useQuery({
    // 与 PaymentSummaryCards / 其它消费 /api/v1/metrics 的组件同一个 queryKey，
    // 于是同一屏上只发一次请求
    queryKey: ["metrics"],
    queryFn: ({ signal }) => listMetrics({ signal }),
  });

  const items = metricsQuery.data ?? [];
  const daily: PlatformDaily[] = PLATFORMS.map((platform) => {
    const metric = metricByKey(items, metricKeyFor(platform.key));
    return {
      key: platform.key,
      label: platform.label,
      metric,
      summary: metric ? readPaymentsDailySummary(metric.value) : null,
    };
  });

  return (
    <section>
      <PageHeader
        title={navLabel("/finance")}
        description="按平台保留原始资金事实，这一页只做聚合（ADR-006 统一体验）。财务总览、开票集成与财务配置已接真实内容；支付通道 / 财务对账 / 异常与冻结的后端还不存在，保持诚实占位。"
        onRefresh={() => void metricsQuery.refetch()}
        refreshing={metricsQuery.isFetching}
        lastRefreshedAt={metricsQuery.dataUpdatedAt || undefined}
      />
      <div className="flex flex-col gap-3">
        <HeadlineTiles query={metricsQuery} daily={daily} businessDay={businessDay} />
        <Tabs
          value={activeSub}
          onValueChange={onSelectSub}
          items={FINANCE_SUB_TABS.map(([value, label]) => ({
            value,
            label,
            content: subTabContent(value, label, daily, businessDay, metricsQuery),
          }))}
        />
      </div>
    </section>
  );
}

function subTabContent(
  tabId: string,
  label: string,
  daily: PlatformDaily[],
  businessDay: string,
  metricsQuery: UseQueryResult<MetricItem[]>,
) {
  if (tabId === "overview") {
    return <FinanceOverviewTab daily={daily} businessDay={businessDay} metricsQuery={metricsQuery} />;
  }
  // CR-0010：原生开票管理复用同一个 Router，全局视角仅开放配置与同步状态。
  if (tabId === "invoicing") return <InvoiceConsolePanel mode="global" />;
  if (tabId === "settings") return <FinanceSettingsTab />;
  return <FinancePendingTab tabId={tabId} label={label} />;
}

// ============================================================================
// 页顶四张统计格
// ============================================================================

function HeadlineTiles({
  query,
  daily,
  businessDay,
}: {
  query: UseQueryResult<MetricItem[]>;
  daily: PlatformDaily[];
  businessDay: string;
}) {
  const cash = crossPlatformBucketTotal(
    daily.map((entry) => bucketFact(entry, "succeeded")),
    businessDay,
  );
  const refunds = crossPlatformBucketTotal(
    daily.map((entry) => bucketFact(entry, "refunded")),
    businessDay,
  );

  return (
    <ApiStateView
      isPending={query.isPending}
      error={query.error}
      onRetry={() => void query.refetch()}
      compact
    >
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <TotalTile
          label="现金到账"
          hint="真实支付现金，不含赠额：两平台 succeeded 桶合计"
          total={cash}
          businessDay={businessDay}
        />
        <StatTile
          label="对账差异"
          value="—"
          unavailable
          note="平台金额与支付金额的差额合计。对账域尚未建立：finance schema 里没有对账批次表，路由表里也没有对账端点，差异合计给不出。对账批次随 M3 支付接入上线。"
          status={<Badge tone="neutral">未接入</Badge>}
        />
        <TotalTile
          label="退款 / 冻结"
          hint="已退款现金：Sub2API 的 refunded 桶。「冻结」不在这个数里——冻结在开票系统侧，平台不复制"
          total={refunds}
          businessDay={businessDay}
        />
        <StatTile
          label="开票数据延迟"
          value="—"
          unavailable
          note="按 CR-0005，平台侧不展示开票数字、也不新增到开票系统的 HTTP 或数据库通道；开票的水位与源健康在「开票集成」页签内由开票系统自己显示。这一格是刻意不接，不是漏做。"
          status={<Badge tone="neutral">刻意不接</Badge>}
        />
      </div>
    </ApiStateView>
  );
}

/** 一个平台在某个分桶上的当日事实。判断留给 `crossPlatformBucketTotal`，
 *  这里只做「指标 → 事实」的搬运。 */
function bucketFact(
  entry: PlatformDaily,
  bucket: "succeeded" | "pending" | "failed" | "refunded",
): PlatformBucketFact {
  // NewAPI 没有退款概念（上游没有 refund 字段/状态/函数），不是「还没做」——
  // 所以它不该让退款合计给不出（api/finance.ts 的 platformHasRefunds）
  const notApplicable = bucket === "refunded" && !platformHasRefunds(entry.key);
  const amount = entry.summary?.byStatus[bucket];
  return {
    label: entry.label,
    ...(notApplicable ? { notApplicable: true } : {}),
    available: Boolean(
      entry.metric && entry.summary && entry.metric.freshness.state !== "uninitialized",
    ),
    day: entry.summary?.day ?? null,
    currency: entry.summary?.currency ?? "",
    amountMinor: amount?.amountMinor ?? null,
    // 桶键在场但金额不是合法整数：坏数据，与「桶键缺席」不能混
    ...(amount && amount.amountMinor === null ? { invalidAmount: true } : {}),
    isPartial: entry.metric?.freshness.is_partial ?? false,
  };
}

function TotalTile({
  label,
  hint,
  total,
  businessDay,
}: {
  label: string;
  hint: string;
  total: CrossPlatformTotal;
  businessDay: string;
}) {
  const note = `${hint} · ${crossPlatformTotalNote(total, businessDay)}`;
  if (total.kind !== "total") {
    return (
      <StatTile
        label={label}
        value="—"
        unavailable
        note={note}
        status={
          <Badge tone={total.kind === "mixed-currency" ? "warning" : "neutral"}>
            {total.kind === "mixed-currency" ? "币种不一致" : "合计给不出"}
          </Badge>
        }
      />
    );
  }
  return (
    <StatTile
      label={label}
      value={formatMinorUnits(total.amountMinor, total.currency)}
      note={note}
      {...(total.partial ? { status: <Badge tone="warning">覆盖不全</Badge> } : {})}
    />
  );
}

// ============================================================================
// 财务总览
// ============================================================================

function FinanceOverviewTab({
  daily,
  businessDay,
  metricsQuery,
}: {
  daily: PlatformDaily[];
  businessDay: string;
  metricsQuery: UseQueryResult<MetricItem[]>;
}) {
  return (
    <div className="flex flex-col gap-3">
      <p className="rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted">
        这一屏的资金数字只覆盖今天这一个业务日（{businessDay}，Asia/Shanghai）：
        payments.daily 是单日粒度指标，周 / 月合计要按天聚合，本页不提供，别把这几格当成月累计。
        成本 / 毛利 / 可用天数默认同样只取今天。逐笔订单与各平台的原始资金事实在各自的「支付与财务」页，
        这一页只做聚合（ADR-006）。
      </p>
      <PlatformFundsCard daily={daily} businessDay={businessDay} metricsQuery={metricsQuery} />
      {PLATFORMS.map((platform) => (
        <CostSection key={platform.key} systemType={platform.key} label={platform.label} />
      ))}
      <AttentionCard />
    </div>
  );
}

/** 「平台资金构成」卡（蓝图键：Sub2API / NewAPI / 退款 / 差异）。
 *
 *  四个金额格直接用 `PaymentSummaryCards` 的 `BucketCard`，不另写一套判断：
 *  单日粒度判据、覆盖不全时不断言零、NewAPI 退款「不适用」这三条纪律都在它里面，
 *  在这里重抄一遍迟早会与平台页的同名卡说出两种话。 */
function PlatformFundsCard({
  daily,
  businessDay,
  metricsQuery,
}: {
  daily: PlatformDaily[];
  businessDay: string;
  metricsQuery: UseQueryResult<MetricItem[]>;
}) {
  const range = { from: businessDay, to: businessDay };
  return (
    <section className="rounded-lg border border-edge bg-surface p-4">
      <header className="flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="text-sm font-medium text-fg">平台资金构成</h3>
        <p className="text-xs text-fg-muted">按平台拆分现金与退款；差异属于对账域</p>
      </header>
      <ApiStateView
        isPending={metricsQuery.isPending}
        error={metricsQuery.error}
        onRetry={() => void metricsQuery.refetch()}
        compact
      >
        <div className="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {daily.map((entry) => (
            <BucketCard
              key={`succeeded-${entry.key}`}
              bucket="succeeded"
              label={`${entry.label} 成功到账`}
              platform={entry.key}
              metric={entry.metric}
              summary={entry.summary}
              range={range}
            />
          ))}
          {daily.map((entry) => (
            <BucketCard
              key={`refunded-${entry.key}`}
              bucket="refunded"
              label={`${entry.label} 退款与冲正`}
              platform={entry.key}
              metric={entry.metric}
              summary={entry.summary}
              range={range}
            />
          ))}
          <StatTile
            label="差异"
            value="—"
            unavailable
            note="对账域尚未建立（M3 支付接入）：没有对账批次表、也没有对账端点，平台金额与支付金额的差额合计给不出。"
            status={<Badge tone="neutral">未接入</Badge>}
          />
        </div>
      </ApiStateView>
      <p className="mt-3 text-xs text-fg-muted">
        蓝图这张卡还提到「赠额」：payments.daily 只按支付状态分桶（succeeded / pending / failed /
        refunded），不区分现金与赠额，平台今天拿不到赠额口径，所以这里没有那一行——
        金额语义的定义见「财务配置」。
      </p>
    </section>
  );
}

/** 一个平台的成本三卡 + 一句「这个 0 是什么意思」。
 *
 *  卡片本身直接挂 `FinanceSummaryCards`（它已经有「给不出就说给不出并说清为什么」
 *  的空态）；这里补的是它给不出的那一层信息：**成本侧今天到底有没有采到数**。
 *  生产上线时 `XM_FINANCE_COLLECT_ENABLED` 是关的（docs/runbooks/GO-LIVE-CHECKLIST.md），
 *  「今天毛利 0」与「成本一行都没写进来」在数字上完全一样，而这两件事的下一步
 *  差得很远（一个去查定价，一个去查采集）。判据不去猜环境变量，只看端点自己
 *  回报的覆盖率——见 lib/financeGlobal.ts 的 costCollectionState。 */
function CostSection({ systemType, label }: { systemType: PaymentsPlatform; label: string }) {
  const channelQuery = useQuery({
    // 与 FinanceSummaryCards 同一个 queryKey：同一屏上只发一次请求
    queryKey: ["finance", "channels", "summary"],
    queryFn: ({ signal }) => listChannelSummaries({ signal }),
  });
  const channels = (channelQuery.data?.items ?? []).filter(
    (channel) => channel.systemType === systemType,
  );

  return (
    <section className="flex flex-col gap-2 rounded-lg border border-edge bg-surface p-4">
      <header className="flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="text-sm font-medium text-fg">{label} 成本与毛利</h3>
        <p className="text-xs text-fg-muted">来源：/finance/channels/summary、/finance/upstreams/summary</p>
      </header>
      <FinanceSummaryCards systemType={systemType} label={label} />
      {channelQuery.data ? (
        <p className="text-xs text-fg-muted">{costCollectionNote(costCollectionState(channels))}</p>
      ) : null}
    </section>
  );
}

/** 「需要处理」卡（蓝图列：事项 / 来源 / 金额或影响 / 状态）。
 *
 *  今天只收一类事项——可用天数告警档位。原型里的另外三类（对账差异、开票契约
 *  状态、待审批合并）一个数据源都没有，拿别的数据凑行数会让这张卡看起来是全的。 */
function AttentionCard() {
  const upstreamQuery = useQuery({
    // 与同屏的 FinanceSummaryCards 共用一份缓存，但 key 走 api/finance 导出的
    // 常量而不是字面量数组：这份数据的 key 曾经分叉成两种写法，前缀匹配的失效
    // 谁都碰不到谁（原委见常量处；护栏是 src/upstreamSummaryCache.test.tsx）
    queryKey: [UPSTREAM_SUMMARY_QUERY],
    queryFn: ({ signal }) => listUpstreamSummaries({ signal }),
  });
  const upstreams = upstreamQuery.data?.items ?? [];
  const coverage: RunwayCoverage | undefined = upstreamQuery.data?.runwayCoverage;
  const rows = runwayAttentionItems(upstreams);
  const hasInput = upstreams.length > 0 && (coverage?.known ?? 0) > 0;

  return (
    <section className="flex flex-col gap-2 rounded-lg border border-edge bg-surface p-4">
      <header className="flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="text-sm font-medium text-fg">需要处理</h3>
        <p className="text-xs text-fg-muted">今天只有可用天数告警档位一类有真实来源</p>
      </header>
      <ApiStateView
        isPending={upstreamQuery.isPending}
        error={upstreamQuery.error}
        onRetry={() => void upstreamQuery.refetch()}
        compact
      >
        {rows.length === 0 ? (
          <PageState
            kind={hasInput ? "empty" : "unavailable"}
            title={hasInput ? "今天没有触发可用天数档位的上游" : "可用天数这条线还没有输入"}
            description={runwayAttentionEmptyReason(upstreams, coverage)}
            compact
          />
        ) : (
          <div className="overflow-x-auto rounded-lg border border-edge">
            <table className="w-full min-w-[640px] text-left text-xs">
              <caption className="sr-only">需要处理的事项：事项、来源、金额或影响、状态</caption>
              <thead className="bg-surface-muted text-fg-muted">
                <tr>
                  <th className="px-3 py-2 font-medium">事项</th>
                  <th className="px-3 py-2 font-medium">来源</th>
                  <th className="px-3 py-2 font-medium">金额 / 影响</th>
                  <th className="px-3 py-2 font-medium">状态</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((row) => (
                  <tr key={row.id} className="border-t border-edge">
                    <td className="px-3 py-2 text-fg">
                      上游「{row.name}」可用天数 {row.days} 天
                    </td>
                    <td className="px-3 py-2 font-mono text-fg-muted">
                      /finance/upstreams/summary · {row.systemType}
                    </td>
                    <td className="px-3 py-2 text-fg-muted">
                      余额 {moneyText(row.balance)} · 日均消耗 {moneyText(row.dailyAverage)} · 余额观测{" "}
                      {formatUtcTimestamp(row.balanceObservedAt)}
                    </td>
                    <td className="px-3 py-2">
                      <Badge tone={RUNWAY_TONE[row.level] ?? "neutral"}>{row.level}</Badge>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </ApiStateView>
      <p className="text-xs text-fg-muted">
        这张卡今天只收一类事项：可用天数告警档位（/finance/upstreams/summary 的 runway 与
        runway_thresholds）。原型里的另外三类——对账差异、开票契约状态、待审批合并——
        一个数据源都没有（对账域不存在；开票按 CR-0005 刻意不接；审批中心已启用但这张卡还没读它，
        而且 /api/v1/approvals 不支持按 action_id 前缀筛选，「只要财务相关的那些单」今天得整条队列拉回来自己过滤），
        所以这一屏不等于「今天全部要处理的事」。
      </p>
    </section>
  );
}

/** 金额展示。**给不出就说「—」**：一个悄悄换算过或按 0 兜底的金额，
 *  比一个空位危险得多（宪法 13 条，同 lib/money 的 fail closed）。 */
function moneyText(money: Money | null): string {
  if (!money) return "—";
  return formatScaledMinorUnits(money.amountMinor, money.currency, money.scale);
}

// ============================================================================
// 财务配置：两张卡都不是「数据」，是已冻结的决定
// ============================================================================

/** ADR-006 的核心规则，逐字引自
 *  docs/adr/ADR-006-财务体验统一领域不合并.md。 */
const ADR006_CORE_RULE =
  "只有实际真实充值且已经消费的现金金额可开票；赠送、返利、兑换码、管理员赠额和其他非现金额度不可开票。";

/** 蓝图那张卡的四个键。
 *
 *  **只写 ADR-006 真的说了的话**：它冻结的是术语表（七个词）与上面那条核心规则，
 *  并没有逐字段给出计入时点、含不含手续费这类口径——那些还在 contracts/ 里空着。
 *  在这里替它补一个定义，就是在页面上造一条没人批准过的规则。 */
const AMOUNT_SEMANTICS: readonly { readonly key: string; readonly body: string }[] = [
  {
    key: "cash_paid",
    body: "ADR-006 把它列入统一财务语义的术语表（cash_paid / bonus_granted / cash_consumed / bonus_consumed / cash_refunded / invoice_eligible / invoice_allocated 七个词）。逐字段口径（计入时点、含不含支付手续费）还没有落进 contracts/，原始支付订单仍以来源系统为真相源，所以这里不替它下定义。",
  },
  {
    key: "bonus_granted",
    body: "同属那张术语表。按核心规则，赠送、返利、兑换码、管理员赠额属于非现金额度，一律不可开票——这一条是冻结的，逐字段口径同样还没有落进契约。",
  },
  {
    key: "cash_refunded",
    body: "同属那张术语表。退款只有 Sub2API 有这个概念：NewAPI 上游没有 refund 字段、状态与函数，那是「平台没有这个概念」而不是「还没做」，所以本页的退款格对 NewAPI 显示「不适用」。",
  },
  {
    key: "invoice_eligible",
    body: "由核心规则定义，且 ADR-006 明写「开票资格算法只在开票系统实现」「平台不得复制资格算法」。所以这个值不会在平台侧算出来，本页也不去算它。",
  },
];

interface WriteFeatureLock {
  readonly feature: string;
  readonly conditions: readonly string[];
}

/** 三个写入功能的解锁条件。逐条写实，不写「敬请期待」。 */
const WRITE_FEATURE_LOCKS: readonly WriteFeatureLock[] = [
  {
    feature: "退款",
    conditions: [
      "等 M3 支付接入：connectors/payment/ 今天是空目录（只有 .gitkeep），平台没有任何支付 Connector",
      "等一个退款 Action 被注册：平台上已注册的 finance Action 全在上游成本侧，客户支付侧一个都没有。审批中心（XM-0030）已启用，L4 现在会落成审批单而不是被拒绝执行——所以内核不再是这件事的阻塞点",
    ],
  },
  {
    feature: "补单",
    conditions: [
      "等 M3 支付接入：补单要先有支付通道与逐笔订单的写路径",
      "等一个补单 Action 被注册：同退款，客户支付侧的写入口一个都没有注册",
    ],
  },
  {
    feature: "对账纠正",
    conditions: [
      "等对账域建立：finance schema 里没有对账批次表，路由表里也没有对账端点，没有可纠正的对象",
      "等一个对账纠正 Action 被注册：同上，客户支付侧的写入口一个都没有注册",
    ],
  },
];

function FinanceSettingsTab() {
  return (
    <div className="flex flex-col gap-3">
      <p className="rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted">
        这一格里没有查询结果：两张卡的内容是已冻结的决定（ADR-006 的金额语义、ADR-003 的风险等级与解锁条件），
        所以写死并注明出处。它们没有说的口径，这里也不替它们补。
      </p>

      <section className="rounded-lg border border-edge bg-surface p-4">
        <header className="flex flex-wrap items-baseline justify-between gap-2">
          <h3 className="text-sm font-medium text-fg">金额语义</h3>
          <p className="text-xs text-fg-muted">四个金额字段各自的定义</p>
        </header>
        <blockquote className="mt-3 border-l-2 border-accent bg-surface-muted px-3 py-2 text-xs text-fg">
          核心规则（ADR-006 原文）：{ADR006_CORE_RULE}
        </blockquote>
        <dl className="mt-3 flex flex-col gap-3">
          {AMOUNT_SEMANTICS.map((entry) => (
            <div key={entry.key} className="flex flex-col gap-1">
              <dt className="font-mono text-xs font-medium text-fg">{entry.key}</dt>
              <dd className="text-xs text-fg-muted">{entry.body}</dd>
            </div>
          ))}
        </dl>
        <p className="mt-3 text-xs text-fg-muted">
          依据 docs/adr/ADR-006-财务体验统一领域不合并.md。
        </p>
      </section>

      <section className="rounded-lg border border-edge bg-surface p-4">
        <header className="flex flex-wrap items-baseline justify-between gap-2">
          <h3 className="text-sm font-medium text-fg">写入功能启用条件</h3>
          <p className="text-xs text-fg-muted">锁定项在条件满足前不出现执行入口</p>
        </header>
        <div className="mt-3 overflow-x-auto rounded-lg border border-edge">
          <table className="w-full min-w-[560px] text-left text-xs">
            <caption className="sr-only">写入功能、当前状态与解锁条件</caption>
            <thead className="bg-surface-muted text-fg-muted">
              <tr>
                <th className="px-3 py-2 font-medium">功能</th>
                <th className="px-3 py-2 font-medium">状态</th>
                <th className="px-3 py-2 font-medium">解锁条件</th>
              </tr>
            </thead>
            <tbody>
              {WRITE_FEATURE_LOCKS.map((lock) => (
                <tr key={lock.feature} className="border-t border-edge">
                  <td className="px-3 py-2 text-fg">{lock.feature}</td>
                  <td className="px-3 py-2">
                    <Badge tone="neutral">锁定</Badge>
                  </td>
                  <td className="px-3 py-2 text-fg-muted">
                    <ul className="flex list-disc flex-col gap-1 pl-4">
                      {lock.conditions.map((condition) => (
                        <li key={condition}>{condition}</li>
                      ))}
                    </ul>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <p className="mt-3 text-xs text-fg-muted">
          锁定项不渲染任何执行入口——灰掉一个按钮仍然是在暗示「快了」，而这三件事今天连对象都没有。
          平台上已注册的 finance Action 全部是上游成本侧（上游账号 / 充值倍率 / 令牌映射 / 订阅批次 /
          代理资产 / 渠道绑定），客户支付侧的退款、补单与对账纠正一个都没有。依据
          docs/adr/ADR-003-Action唯一写入口.md；前端隐藏本来也不构成安全控制，真正的裁决在内核。
        </p>
      </section>
    </div>
  );
}

// ============================================================================
// 三个后端还不存在的子页签
// ============================================================================

/** 「在等什么」逐格写死。比蓝图自带的 `tab.source` 更具体一层：
 *  后者说的是「将来由什么填」，这里补的是「今天为什么填不了」。 */
const PENDING_TAB_COPY: Readonly<Record<string, string>> = {
  channels:
    "支付通道的健康、费率与结算随 M3 支付接入上线。今天平台没有支付 Connector（connectors/payment/ 里只有 .gitkeep），逐笔订单里虽然有支付方式字段，但没有按通道的成功率与结算口径——不拿它凑一张假的通道表。",
  reconciliation:
    "对账批次比对平台金额与支付金额；差异不自动抹平，逐条要求解释。今天平台没有对账域：finance schema 里没有对账批次表，路由表里也没有对账端点，随 M3 上线。",
  exceptions:
    "退款冻结、订单状态不一致等异常的协同处理。今天既没有异常来源（无异常表、无端点），执行入口也没有：退款与补单按 ADR-003 属 L3/L4，而平台已注册的 finance Action 全在上游成本侧，客户支付侧一个都没有。审批中心（XM-0030）已启用，L3/L4 现在会落成审批单——等的不是它，是那几个 Action 本身。",
};

/** 后端不存在的三格：先压一句「今天为什么填不了」，再照原样渲染蓝图。
 *
 *  **不换成一句「尚未接入」**：蓝图带着原型逐字列头（支付通道那张表 8 列、
 *  对账批次 10 列），那是这一格将来的规格，比一句话信息量大得多；
 *  而且它页尾本来就会打印 `tab.source` 那句「将来由什么填」。 */
function FinancePendingTab({ tabId, label }: { tabId: string; label: string }) {
  const spec = FINANCE_BLUEPRINT_PAGE?.tabs.find((tab) => tab.id === tabId);
  const description = PENDING_TAB_COPY[tabId] ?? "该子页尚未接入。";
  return (
    <section className="flex flex-col gap-3">
      <PageState kind="unavailable" title={`「${label}」尚未接入`} description={description} />
      {spec ? <BlueprintTabView tab={spec} /> : null}
    </section>
  );
}
