import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  formatUtcTimestamp,
  PageState,
  StatTile,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { useState } from "react";
import {
  describeAccessMethod,
  describeCredential,
  listUpstreamAccounts,
  listUpstreamSummaries,
  FINANCE_READ_PERMISSION,
  UPSTREAM_ACCOUNTS_QUERY,
  UPSTREAM_SUMMARY_QUERY,
  type UpstreamAccountItem,
  type UpstreamSummary,
  type UpstreamRegistryPlatform,
} from "../api/finance";
import { formatScaledMinorUnits } from "../lib/money";
import { RUNWAY_TONE, runwayReasonText } from "../lib/runway";
import { coveredCount, describeMissingTotal, sumMoney } from "../lib/upstreamTotals";
import { ActionResultNote, type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";
import { RechargeRatioDialog } from "./RechargeRatioDialog";
import { UpstreamAccountDialog } from "./UpstreamAccountDialog";
import { UpstreamAccountDetail } from "./UpstreamAccountDetail";
import { PersistentDataTable, platformSavedViewTableKey } from "./PersistentDataTable";

/** 上游管理(交接文档 §9.6、原型 `V["s2/suppliers"]`)。
 *
 *  **当前行粒度仍是一个上游账号，不是供应商实体**：展开后是它下面的
 *  令牌映射、订阅批次与代理资产。
 *
 *  数据源是 XM-0037a 建的成本登记簿(`GET /api/v1/finance/upstream-accounts`),
 *  写路径全部走 Action(宪法 2 条)——这一页是登记簿的 UI，不是第二份存储。
 *
 *  XM-C003 补齐上游名称、联系人、接入分组与 group_rate 的登记簿往返；
 *  KEY 数、余额与 runway 按同源账号 ID 接上 summary。未接入分组发现与订阅
 *  有效期仍没有聚合端点，继续明确显示未接入。 */
export function UpstreamAccountsPanel({ platform }: { platform: UpstreamRegistryPlatform }) {
  const queryClient = useQueryClient();
  const [result, setResult] = useState<ActionResult | null>(null);

  const query = useQuery({
    queryKey: [UPSTREAM_ACCOUNTS_QUERY],
    queryFn: ({ signal }) => listUpstreamAccounts({ signal }),
  });

  // 本期的钱来自 XM-0037d 的上游汇总。**这一侧可以直接 join**：两个端点的
  // 行 id 都是 finance.upstream_account 的主键（与渠道表的情形正好相反,
  // 那边一行是被管平台自己的渠道，对不上，见 lib/channelEconomics）。
  //
  // 单独一个 query 而不是并进上面那个：登记簿读不到时表还能画（结构是它自己的),
  // 汇总读不到时只是钱那几格显示不出来——两者的失败不该互相拖累
  const summaryQuery = useQuery({
    queryKey: [UPSTREAM_SUMMARY_QUERY],
    queryFn: ({ signal }) => listUpstreamSummaries({ signal }),
  });

  // 登记簿是**跨平台**的一张表，这一页只看归属本平台的那些。
  // platform_id 为空 = 未配对——它们同样要显示出来（§12 惯例：未接入不许隐藏）,
  // 否则一个漏配 platform_id 的账号会从两个平台的页面上同时消失
  const all = query.data ?? [];
  const rows = all.filter((a) => a.platform_id === platform || a.platform_id === "");
  const unpaired = rows.filter((a) => a.platform_id === "").length;

  const summaries = new Map<string, UpstreamSummary>();
  for (const s of summaryQuery.data?.items ?? []) summaries.set(s.id, s);
  const mine = rows.map((a) => summaries.get(a.id));
  const costs = mine.map((s) => s?.supplyCost ?? null);
  const profits = mine.map((s) => s?.grossProfit ?? null);
  const costTotal = sumMoney(costs);
  const profitTotal = sumMoney(profits);
  const costCovered = coveredCount(costs);
  const profitCovered = coveredCount(profits);
  const subscriptionCount = rows.filter((a) => a.access_method === "subscription_account").length;
  const keyCount = rows.reduce((total, account) => {
    if (account.access_method === "subscription_account") return total;
    return total + (summaries.get(account.id)?.tokenCount ?? 0);
  }, 0);
  const countCovered = rows.filter(
    (account) => account.access_method === "subscription_account" || summaries.has(account.id),
  ).length;
  const summaryState: SummaryLoadState = summaryQuery.isPending
    ? "pending"
    : summaryQuery.error
      ? "failed"
      : "ready";
  // 两张卡各自只让**参与自己合计的行**提供证据。毛利依赖成本和收入两侧，
  // 所以还要在参与毛利的行里取两侧实际时刻的最旧值。
  const costObservedAt = oldestCostObserved(mine);
  const profitObservedAt = oldestProfitObserved(mine);
  const costObservationIncomplete = mine.some(
    (summary) => Boolean(summary?.supplyCost) && !isValidTimestamp(summary?.observed.costObservedAt),
  );
  const profitObservationIncomplete = mine.some(
    (summary) =>
      Boolean(summary?.grossProfit) &&
      (!isValidTimestamp(summary?.observed.costObservedAt) ||
        !isValidTimestamp(summary?.observed.revenueObservedAt)),
  );

  // 写完不做乐观更新，重新查一次：这一页是登记簿的 UI，页面上的数必须是库里的数。
  // 令牌映射内嵌在账号行里，所以改映射同样要刷这一个 key
  const afterWrite = (written: ActionResult) => {
    setResult(written);
    void queryClient.invalidateQueries({ queryKey: [UPSTREAM_ACCOUNTS_QUERY] });
  };

  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <p className="max-w-3xl text-xs text-fg-muted">
          由平台手工登记不同上游，维护网址、凭据状态、接入平台与倍率，并汇总该上游下所有账号/渠道的整体利润。
          当前仍一行对应一个上游账号；凭据只显示状态，平台永不持有明文。
        </p>
        <UpstreamAccountDialog
          platform={platform}
          onDone={(runId) => afterWrite({ title: "上游账号已登记", runId })}
        />
      </div>

      {result ? <ActionResultNote result={result} onDismiss={() => setResult(null)} /> : null}

      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        <div className="flex flex-col gap-3">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <StatTile
              label="上游账号"
              value={String(rows.length)}
              note={unpaired > 0 ? `其中 ${unpaired} 个未配对平台` : "已登记并归属本平台"}
            />
            <StatTile
              label="KEY / 订阅账号"
              value={
                summaryState === "failed"
                  ? `— / ${subscriptionCount}`
                  : summaryState === "pending"
                    ? `… / ${subscriptionCount}`
                    : `${keyCount} / ${subscriptionCount}`
              }
              unavailable={summaryState === "failed"}
              note={
                summaryState === "failed"
                  ? "KEY 数汇总读取失败；订阅账号数来自登记簿"
                  : summaryState === "pending"
                    ? "正在读取 KEY 数；订阅账号数来自登记簿"
                    : countCovered < rows.length
                      ? `只覆盖 ${rows.length} 个账号中的 ${countCovered} 个`
                      : "KEY 数来自上游汇总；订阅账号数来自登记簿"
              }
              {...(countCovered < rows.length && summaryState === "ready"
                ? { status: <Badge tone="warning">覆盖不全</Badge> }
                : {})}
            />
            {/* 合计不出来时显示「—」而不是 ¥0.00：
                「这个窗口还没有可用的汇总数」与「这期没花钱」是两件事 */}
            <MoneyTile
              label="本期我方消耗"
              total={costTotal}
              covered={costCovered}
              rowCount={rows.length}
              observedAt={costObservedAt}
              observationLabel="成本观测于"
              observationIncomplete={costObservationIncomplete}
              pending={summaryQuery.isPending}
              failed={Boolean(summaryQuery.error)}
            />
            <MoneyTile
              label="本期整体毛利"
              total={profitTotal}
              covered={profitCovered}
              rowCount={rows.length}
              observedAt={profitObservedAt}
              observationLabel="成本/收入最旧观测于"
              observationIncomplete={profitObservationIncomplete}
              pending={summaryQuery.isPending}
              failed={Boolean(summaryQuery.error)}
            />
          </div>

          <ScopeNote />

          <PersistentDataTable
            tableKey={platformSavedViewTableKey(platform, "upstreams")}
            caption="上游账号登记簿：上游资料、接入分组、KEY 或账号、余额证据与经营汇总"
            columns={upstreamColumns(afterWrite, summaries, summaryState)}
            rows={rows}
            rowKey={(a) => a.id}
            searchable
            pageSize={20}
            filters={[
              { columnId: "access", label: "接入方式", options: ["上游中转", "官方 API", "订阅账号"] },
              { columnId: "status", label: "状态", options: ["active", "disabled"] },
            ]}
            renderExpanded={(account) => <UpstreamAccountDetail account={account} onDone={afterWrite} />}
            emptyState={
              <PageState
                kind="empty"
                title="还没有登记任何上游账号"
                description={`用右上角的「登记上游账号」登记第一个。需要 ${FINANCE_READ_PERMISSION} 才能看到这张表。`}
              />
            }
          />
        </div>
      </ApiStateView>
    </section>
  );
}

/** 按真实 instant 取最旧时间，并保留原始 RFC3339 作为页面证据。
 *
 *  不能直接比较字符串：`01:00-07:00` 实际比 `08:30+02:00` 更新，
 *  但词典序恰好相反。解析不出的时间不替任何金额背书。 */
function oldestActualTimestamp(timestamps: readonly (string | null)[]): string | null {
  let oldest: { raw: string; instant: number } | null = null;
  for (const raw of timestamps) {
    if (!raw) continue;
    const instant = Date.parse(raw);
    if (!Number.isFinite(instant)) continue;
    if (oldest === null || instant < oldest.instant) oldest = { raw, instant };
  }
  return oldest?.raw ?? null;
}

function isValidTimestamp(raw: string | null | undefined): boolean {
  return Boolean(raw) && Number.isFinite(Date.parse(raw ?? ""));
}

function oldestCostObserved(summaries: readonly (UpstreamSummary | undefined)[]): string | null {
  const timestamps: (string | null)[] = [];
  for (const s of summaries) {
    if (s?.supplyCost) timestamps.push(s.observed.costObservedAt);
  }
  return oldestActualTimestamp(timestamps);
}

function oldestProfitObserved(summaries: readonly (UpstreamSummary | undefined)[]): string | null {
  const timestamps: (string | null)[] = [];
  for (const s of summaries) {
    // 这一行没进毛利合计，就不能拿自己的时间替那笔部分和背书。
    if (!s?.grossProfit) continue;
    timestamps.push(s.observed.costObservedAt, s.observed.revenueObservedAt);
  }
  return oldestActualTimestamp(timestamps);
}

/** 一格金额合计。算得出来就显示，算不出来说清是哪一种「算不出来」。 */
function MoneyTile({
  label,
  total,
  covered,
  rowCount,
  observedAt,
  observationLabel,
  observationIncomplete,
  pending,
  failed,
}: {
  label: string;
  total: ReturnType<typeof sumMoney>;
  covered: number;
  rowCount: number;
  observedAt: string | null;
  observationLabel: string;
  observationIncomplete: boolean;
  pending: boolean;
  failed: boolean;
}) {
  if (pending) {
    return <StatTile label={label} value="…" unavailable note="正在读取上游汇总" />;
  }
  if (failed) {
    // 读失败与「没有数据」分开说：前者要重试或报障，后者要去看采集
    return (
      <StatTile
        label={label}
        value="—"
        unavailable
        note="上游汇总读取失败，这一格暂时给不出"
        status={<Badge tone="danger">读取失败</Badge>}
      />
    );
  }
  if (total.kind !== "ok") {
    return (
      <StatTile
        label={label}
        value="—"
        unavailable
        note={describeMissingTotal(total.kind)}
        status={<Badge tone="neutral">未接入</Badge>}
      />
    );
  }

  const coverageNote =
    covered < rowCount ? `只含 ${rowCount} 个账号里有汇总的 ${covered} 个` : `含全部 ${rowCount} 个账号`;
  const observationNote = observationIncomplete
    ? ` · 观测不完整${observedAt ? `；已知最旧观测于 ${observedAt}` : ""}`
    : observedAt
      ? ` · ${observationLabel} ${observedAt}`
      : "";
  return (
    <StatTile
      label={label}
      value={formatScaledMinorUnits(total.total.toString(), total.currency, total.scale)}
      note={`${coverageNote}${observationNote}`}
      {...(covered < rowCount ? { status: <Badge tone="warning">覆盖不全</Badge> } : {})}
    />
  );
}

/** 当前边界：补齐已有账号的元数据，但不把账号伪装成供应商实体。 */
function ScopeNote() {
  return (
    <p className="rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted">
      当前一行仍是一个上游账号，名称、联系人和已接入分组来自登记簿；KEY 数、余额与可用天数按账号
      ID 对应上游汇总。未接入分组发现需要后续供应商/分组实体，订阅有效期也尚未进入汇总，
      两者都不会用样例值代替。
    </p>
  );
}

type SummaryLoadState = "pending" | "failed" | "ready";

function upstreamColumns(
  onDone: (result: ActionResult) => void,
  summaries: Map<string, UpstreamSummary>,
  summaryState: SummaryLoadState,
): DataTableColumn<UpstreamAccountItem>[] {
  return [
    {
      id: "upstream",
      header: "上游名称 / 网址",
      primary: true,
      value: (a) => `${a.upstream_name} ${a.base_url} ${a.system_type} ${a.id}`,
      cell: (a) => (
        <>
          {a.upstream_name ? (
            <span className="font-medium">{a.upstream_name}</span>
          ) : (
            <Badge tone="neutral">未接入</Badge>
          )}
          <p className="font-mono text-xs break-all text-fg-muted">{a.base_url || "网址未接入"}</p>
          <p className="text-xs text-fg-muted">{a.system_type || "系统类型未知"}</p>
        </>
      ),
    },
    {
      id: "group",
      header: "接入分组 / 倍率",
      value: (a) => `${a.upstream_group} ${a.group_rate}`,
      cell: (a) => (
        <span className="text-xs">
          {a.upstream_group ? a.upstream_group : <Badge tone="neutral">未接入</Badge>}
          <span className="block tabular-nums text-fg-muted">
            倍率 {a.group_rate ? `${a.group_rate}×` : "—"}
          </span>
        </span>
      ),
      headerTitle: "分组与倍率来自登记簿；分组倍率仅展示，不参与金额计算",
    },
    {
      id: "supply-count",
      header: "KEY / 账号",
      value: (a) => {
        if (a.access_method === "subscription_account") return 1;
        return summaries.get(a.id)?.tokenCount ?? null;
      },
      cell: (a) => (
        <SupplyCountCell account={a} summary={summaries.get(a.id)} state={summaryState} />
      ),
      headerTitle: "KEY 数来自上游汇总；订阅账号数来自当前登记簿行",
    },
    {
      id: "runway",
      header: "总余额 / 可用期",
      value: (a) => moneyValue(summaries.get(a.id)?.runway.balance),
      cell: (a) => (
        <BalanceRunwayCell account={a} summary={summaries.get(a.id)} state={summaryState} />
      ),
      headerTitle: "余额、可用天数与观测时刻来自上游汇总；订阅有效期尚未进入该汇总",
    },
    {
      id: "access",
      header: "接入方式",
      value: (a) => describeAccessMethod(a.access_method).label,
      cell: (a) => {
        const shown = describeAccessMethod(a.access_method);
        return (
          <span title={shown.hint} className="text-xs">
            {shown.label}
          </span>
        );
      },
    },
    {
      id: "ratio",
      header: "充值成本率",
      // 排序用**投影值**而不是倍率本身：界面显示的是成本率，按倍率排会得到
      // 一个和眼睛看到的相反的顺序（倍率越大成本率越小）
      value: (a) => (a.recharge_cost_rate ? Number(a.recharge_cost_rate) : null),
      cell: (a) => <RatioCell account={a} />,
    },
    {
      id: "platform",
      header: "接入平台",
      value: (a) => a.platform_id,
      cell: (a) =>
        a.platform_id ? (
          <span className="text-xs">{a.platform_id}</span>
        ) : (
          // 未配对不许隐藏：台账的「未归属」那一桶，对应的就是这一列为空的账号
          <Badge tone="warning" title="没有配 platform_id，这个账号的成本归不到任何平台">
            未配对
          </Badge>
        ),
    },
    {
      id: "contact",
      header: "联系人",
      value: (a) => a.upstream_contact,
      cell: (a) =>
        a.upstream_contact ? (
          <span className="text-xs">{a.upstream_contact}</span>
        ) : (
          <Badge tone="neutral">未接入</Badge>
        ),
    },
    {
      id: "credential",
      header: "凭据",
      value: (a) => describeCredential(a.credential_ref).label,
      cell: (a) => {
        const shown = describeCredential(a.credential_ref);
        return (
          <Badge tone={shown.configured ? "success" : "neutral"} title={shown.hint}>
            {shown.label}
          </Badge>
        );
      },
    },
    {
      id: "status",
      header: "状态",
      value: (a) => a.status,
      cell: (a) => (
        <Badge tone={a.status === "active" ? "success" : "neutral"}>{a.status || "—"}</Badge>
      ),
    },
    {
      id: "margin",
      header: "本期消耗 / 毛利",
      numeric: true,
      // 能力必须从首屏起就是静态可排序；汇总尚未返回时 value=null，不能把
      // SavedView 里的 margin 排序误判成“列已移除”并永久清空。
      value: (a: UpstreamAccountItem) => moneyValue(summaries.get(a.id)?.supplyCost),
      cell: (a) => <PeriodCell summary={summaries.get(a.id)} state={summaryState} />,
      headerTitle: "本期供给成本 / 毛利，来自 XM-0037d 的上游汇总端点",
    },
    {
      id: "actions",
      header: "操作",
      cell: (a) => (
        <div className="flex flex-wrap gap-1">
          <UpstreamAccountDialog
            platform={a.platform_id}
            account={a}
            onDone={(runId) => onDone({ title: "上游账号已更新", runId })}
          />
          <RechargeRatioDialog
            account={a}
            onDone={(runId) => onDone({ title: "充值倍率已更新", runId })}
          />
        </div>
      ),
    },
  ];
}

/** 排序值取最小单位整数。用 bigint：成本线是 scale-6 微单位，
 *  一笔上万元的成本就已经是十位数，几笔加起来很快越过 2^53。 */
function moneyValue(money: { amountMinor: string } | null | undefined): bigint | null {
  if (!money || !/^-?\d+$/.test(money.amountMinor)) return null;
  return BigInt(money.amountMinor);
}

function SupplyCountCell({
  account,
  summary,
  state,
}: {
  account: UpstreamAccountItem;
  summary: UpstreamSummary | undefined;
  state: SummaryLoadState;
}) {
  if (account.access_method === "subscription_account") {
    return (
      <span className="text-xs tabular-nums">
        1 个账号
        <span className="block text-fg-muted">来自当前登记簿行</span>
      </span>
    );
  }
  if (state === "pending") return <span className="text-xs text-fg-muted">汇总读取中</span>;
  if (state === "failed") return <span className="text-xs text-danger">汇总读取失败</span>;
  if (!summary) {
    return (
      <Badge tone="warning" title="上游汇总没有与这个 upstream_account.id 对应的行">
        汇总未覆盖
      </Badge>
    );
  }
  return (
    <span className="text-xs tabular-nums">
      {summary.tokenCount} 个 KEY
      <span className="block text-fg-muted">按账号 ID 对应汇总</span>
    </span>
  );
}

function BalanceRunwayCell({
  account,
  summary,
  state,
}: {
  account: UpstreamAccountItem;
  summary: UpstreamSummary | undefined;
  state: SummaryLoadState;
}) {
  if (account.access_method === "subscription_account") {
    return (
      <span className="text-xs">
        <Badge tone="neutral">有效期待接入</Badge>
        <span className="block text-fg-muted">上游汇总尚无订阅到期字段</span>
      </span>
    );
  }
  if (state === "pending") return <span className="text-xs text-fg-muted">汇总读取中</span>;
  if (state === "failed") return <span className="text-xs text-danger">汇总读取失败</span>;
  if (!summary) {
    return (
      <Badge tone="warning" title="上游汇总没有与这个 upstream_account.id 对应的行">
        汇总未覆盖
      </Badge>
    );
  }

  const runway = summary.runway;
  const reason = runwayReasonText(runway);
  const coverage = `覆盖 ${runway.coveredDays}/${runway.windowDays} 天`;
  const observed = runway.balanceObservedAt
    ? `余额观测 ${formatUtcTimestamp(runway.balanceObservedAt)}`
    : "余额观测时间未接入";
  if (!runway.balance) {
    return (
      <span className="text-xs text-fg-muted" title={runway.reason || "汇总未给出原因"}>
        {reason ?? "—"}
        <span className="block">{coverage}</span>
        <span className="block">{observed}</span>
      </span>
    );
  }

  const tone = RUNWAY_TONE[runway.level] ?? "neutral";
  return (
    <span className="text-xs tabular-nums">
      {formatScaledMinorUnits(
        runway.balance.amountMinor,
        runway.balance.currency,
        runway.balance.scale,
      )}
      <span className="block text-fg-muted">
        {runway.days === null ? (reason ?? "可用天数未接入") : `约 ${runway.days} 天`}
      </span>
      <span className="block text-fg-muted">{coverage}</span>
      <span className="block text-fg-muted">{observed}</span>
      {runway.days === null ? null : <Badge tone={tone}>{runway.level || "已计算"}</Badge>}
    </span>
  );
}

/** 本期消耗 / 毛利单元格。
 *
 *  金额的 `null` 是**「给不出」不是 0**（覆盖不全或币种混杂，XM-0037d 的纪律）,
 *  所以这里不折成 ¥0.00——那会被读成「这个上游这期没花钱」。 */
function PeriodCell({
  summary,
  state,
}: {
  summary: UpstreamSummary | undefined;
  state: SummaryLoadState;
}) {
  if (!summary) {
    if (state === "pending") return <span className="text-xs text-fg-muted">汇总读取中</span>;
    if (state === "failed") return <span className="text-xs text-danger">汇总读取失败</span>;
    return (
      <span className="text-xs text-fg-muted" title="上游汇总里没有这一条：它可能刚登记，还没进过窗口">
        未接入
      </span>
    );
  }
  const cost = summary.supplyCost;
  const profit = summary.grossProfit;
  if (!cost && !profit) {
    return (
      <span className="text-xs text-fg-muted" title="这个业务日窗口里两侧都没有可用数据">
        —
      </span>
    );
  }
  return (
    <span className="text-xs tabular-nums">
      {cost ? formatScaledMinorUnits(cost.amountMinor, cost.currency, cost.scale) : "—"}
      <span className="block text-fg-muted">
        毛利 {profit ? formatScaledMinorUnits(profit.amountMinor, profit.currency, profit.scale) : "—"}
      </span>
    </span>
  );
}

/** 充值成本率单元格。
 *
 *  **三个分支，不是两个**。`metered` 是布尔，而成本口径有三套（§2.0）：
 *  计量型按实扣 ÷ 倍率、订阅型按批次摊销、官方 API 的 v1 口径待定。
 *  照着 `metered` 二分会让官方 API 显示成「订阅摊销」——那是在告诉运营
 *  「这条的成本从订阅批次摊出来」，而它根本没有批次。
 *
 *  这也正是「让后端算好 metered、前端不再按 access_method 判一次」的边界：
 *  **要不要按倍率算**问后端（metered），**这一格该说什么**看 access_method。 */
function RatioCell({ account }: { account: UpstreamAccountItem }) {
  if (account.metered) {
    if (!account.recharge_cost_rate) {
      return (
        <span className="text-xs text-fg-muted" title="计量型但没有配倍率，成本算不出来">
          未配置
        </span>
      );
    }
    return (
      <span
        className="text-xs tabular-nums"
        title={`规范存储的倍率（除数）= ${account.recharge_ratio}`}
      >
        {rateText(account.recharge_cost_rate, account.currency)}
        <span className="block text-fg-muted">倍率 {account.recharge_ratio}</span>
      </span>
    );
  }

  if (account.access_method === "subscription_account") {
    return (
      <span className="text-xs text-fg-muted" title="订阅型按批次摊销到每天，不适用充值倍率">
        订阅摊销
      </span>
    );
  }

  // 官方 API（或将来新增的非计量口径）：倍率可填可不填，填了就照实显示
  if (account.recharge_cost_rate) {
    return (
      <span
        className="text-xs tabular-nums"
        title={`登记了倍率（除数）= ${account.recharge_ratio}，但这条不走计量口径，成本不由它算`}
      >
        {rateText(account.recharge_cost_rate, account.currency)}
        <span className="block text-fg-muted">仅登记，不参与折算</span>
      </span>
    );
  }
  return (
    <span
      className="text-xs text-fg-muted"
      title="官方 API 直连的成本口径 v1 待定（设计稿 §2.0/§12 拍板：占位后置）"
    >
      口径待定
    </span>
  );
}

/** 成本率的展示文本。
 *
 *  币种从这一行取，不写死 ¥：登记簿的默认币种是 USD，给一条 USD 的上游
 *  标一个 ¥，是把汇率差整整一倍地藏进一个看起来完全正常的数字里。 */
function rateText(rate: string, currency: string): string {
  const code = (currency || "").toUpperCase();
  const amount = code === "CNY" ? `¥${rate}` : `${code || "?"} ${rate}`;
  return `${amount} / 额度`;
}
