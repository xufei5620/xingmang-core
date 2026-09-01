import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  DataTableV2,
  formatUtcTimestamp,
  PageState,
  StatTile,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { useState } from "react";
import { Link } from "react-router";
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
import { coveredCount, describeMissingTotal, oldestActualTimestamp, sumMoney } from "../lib/upstreamTotals";
import { groupUpstreamAccounts, type UpstreamSupplierGroup } from "../lib/upstreamGrouping";
import { upstreamDetailPath } from "../pages/ChannelDetailPage";
import { ActionResultNote, type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";
import { RechargeRatioDialog } from "./RechargeRatioDialog";
import { UpstreamAccountDialog } from "./UpstreamAccountDialog";
import { UpstreamAccountDetail } from "./UpstreamAccountDetail";

/** 上游管理（原型 `V["s2/suppliers"]`，2026-09-02 起是渠道管理页内区块,
 *  不再是独立页签——见 `ChannelTable.tsx` 挂载它的那个 `id="upstream-management"`
 *  区块，以及 ADMIN-IA §8.8）。
 *
 *  ## 行粒度：供应商，不是账号（ADMIN-IA §8.6 #1 的纠正）
 *
 *  之前这张表一行是一个 `finance.upstream_account`；原型一行是一个上游
 *  **供应商**，账号/Key 是供应商下面的明细。登记簿今天没有独立的供应商表,
 *  所以这里在展示层按 `upstream_name`（缺省退回 `base_url` 的 host）把
 *  账号归并成组——见 `lib/upstreamGrouping.ts`。归并只影响这张表怎么画,
 *  不改写登记簿本身；同一个供应商下的账号仍然各自是独立的写入单元。
 *
 *  展开一个供应商组，看到的是它名下每个账号——原来那张表的列、单元格与
 *  写操作（改上游账号、改倍率、令牌映射、订阅批次）原样搬进这一层，
 *  不重新实现一遍。
 *
 *  数据源仍是 XM-0037a 的成本登记簿(`GET /api/v1/finance/upstream-accounts`),
 *  写路径全部走 Action（宪法 2 条）——这一页是登记簿的 UI，不是第二份存储。 */
export function UpstreamAccountsPanel({ platform }: { platform: UpstreamRegistryPlatform }) {
  const queryClient = useQueryClient();
  const [result, setResult] = useState<ActionResult | null>(null);

  const query = useQuery({
    queryKey: [UPSTREAM_ACCOUNTS_QUERY],
    queryFn: ({ signal }) => listUpstreamAccounts({ signal }),
  });
  const summaryQuery = useQuery({
    queryKey: [UPSTREAM_SUMMARY_QUERY],
    queryFn: ({ signal }) => listUpstreamSummaries({ signal }),
  });

  // 登记簿是**跨平台**的一张表，这一页只看归属本平台的那些。
  // platform_id 为空 = 未配对——它们同样要显示出来（§12 惯例：未接入不许隐藏）
  const all = query.data ?? [];
  const rows = all.filter((a) => a.platform_id === platform || a.platform_id === "");
  const unpaired = rows.filter((a) => a.platform_id === "").length;

  const summaries = new Map<string, UpstreamSummary>();
  for (const s of summaryQuery.data?.items ?? []) summaries.set(s.id, s);
  const groups = groupUpstreamAccounts(rows, summaries);

  const mine = rows.map((a) => summaries.get(a.id));
  const costs = mine.map((s) => s?.supplyCost ?? null);
  const profits = mine.map((s) => s?.grossProfit ?? null);
  const costTotal = sumMoney(costs);
  const profitTotal = sumMoney(profits);
  const costCovered = coveredCount(costs);
  const profitCovered = coveredCount(profits);
  const summaryState: SummaryLoadState = summaryQuery.isPending
    ? "pending"
    : summaryQuery.error
      ? "failed"
      : "ready";
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

  // 写完不做乐观更新，重新查一次：这一页是登记簿的 UI，页面上的数必须是库里的数
  const afterWrite = (written: ActionResult) => {
    setResult(written);
    void queryClient.invalidateQueries({ queryKey: [UPSTREAM_ACCOUNTS_QUERY] });
  };

  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <p className="max-w-3xl text-xs text-fg-muted">
          由平台手工登记不同上游，维护网址、凭据状态、接入平台与联系人，并汇总该上游下所有账号/渠道的整体利润。
          同名（或同域名）的账号在这张表上按供应商归并展示；凭据只显示状态，平台永不持有明文。
        </p>
        <UpstreamAccountDialog
          platform={platform}
          onDone={(runId) => afterWrite({ title: "上游账号已登记", runId })}
        />
      </div>

      {result ? <ActionResultNote result={result} onDismiss={() => setResult(null)} /> : null}

      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
        <div className="flex flex-col gap-3">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <StatTile
              label="上游实例"
              value={String(groups.length)}
              note={
                groups.length === rows.length
                  ? `共 ${rows.length} 个账号，逐个成组`
                  : `归并自 ${rows.length} 个账号${unpaired > 0 ? `，其中 ${unpaired} 个未配对` : ""}`
              }
            />
            <StatTile
              label="接入账号 / 渠道"
              value={String(rows.length)}
              note="已登记并归属本平台（或未配对）的账号数；平台侧实际渠道数以「渠道管理」表为准"
            />
            <MoneyTile
              label="本期我方计费消耗"
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

          <DataTableV2
            caption="上游供应商：网址、接入平台、账号/凭据、总余额与本期整体利润，展开看逐账号明细"
            columns={supplierColumns(platform)}
            rows={groups}
            rowKey={(g) => g.key}
            searchable
            defaultDensity="compact"
            renderExpanded={(group) => (
              <SupplierAccountsTable
                group={group}
                summaries={summaries}
                summaryState={summaryState}
                onDone={afterWrite}
              />
            )}
            emptyState={
              <PageState
                kind="empty"
                title="还没有登记任何上游账号"
                description={`用右上角的「＋ 添加上游」登记第一个。需要 ${FINANCE_READ_PERMISSION} 才能看到这张表。`}
              />
            }
          />
        </div>
      </ApiStateView>
    </section>
  );
}

/** 供应商组表的列。原型 12 列 `上游名称 / 网址 | 接入平台 | 上游账号 / 凭据 |
 *  Key / 账号 | 接入分组 | 总余额 / 有效期 | 充值成本率 | 本期我方消耗 |
 *  本期总利润 | 联系人 | 状态 | 详情`——逐格对齐，内容按我们真有的字段填,
 *  给不出的（比如原型那种"全部分组 / 已接入分组"目录，我们只有账号自带
 *  的分组名，没有供应商级分组目录）显式说明缺什么，不编数字。 */
function supplierColumns(platform: UpstreamRegistryPlatform): DataTableColumn<UpstreamSupplierGroup>[] {
  return [
    {
      id: "supplier",
      header: "上游名称 / 网址",
      primary: true,
      value: (g) => `${g.name} ${g.baseUrls.join(" ")}`,
      cell: (g) => (
        <div className="min-w-40">
          <span className="font-medium">{g.name}</span>
          <p className="font-mono text-xs break-all text-fg-muted">
            {g.baseUrls[0] ?? "网址未接入"}
            {g.baseUrls.length > 1 ? ` 等 ${g.baseUrls.length} 个网址` : ""}
          </p>
          {g.groupedBy !== "name" ? (
            <p className="text-xs text-fg-muted" title="这个供应商没有登记 upstream_name，按网址域名归并">
              {g.groupedBy === "host" ? "按网址归并" : "未登记名称，独立成组"}
            </p>
          ) : null}
        </div>
      ),
      headerTitle: "供应商归并键：优先用登记的上游名称，没有名称退回网址 host（展示层归并，不是独立的供应商实体）",
    },
    {
      id: "platforms",
      header: "接入平台",
      value: (g) => g.platforms.join(" "),
      cell: (g) => (
        <div>
          {g.platforms.length > 0 ? (
            g.platforms.map((p) => (
              <Badge key={p} tone="neutral" className="mr-1">
                {p}
              </Badge>
            ))
          ) : (
            <span className="text-xs text-fg-muted">—</span>
          )}
          {g.unpairedCount > 0 ? (
            <p className="mt-1 text-xs text-warning" title="这些账号没有配 platform_id，成本归不到任何平台">
              {g.unpairedCount} 个未配对
            </p>
          ) : null}
        </div>
      ),
    },
    {
      id: "credential",
      header: "上游账号 / 凭据",
      value: (g) => g.configuredCredentialCount,
      cell: (g) => (
        <span className="text-xs">
          {g.accounts.length} 个账号
          <span className="block text-fg-muted">
            {g.configuredCredentialCount}/{g.accounts.length} 已配置凭据
          </span>
        </span>
      ),
      headerTitle: "凭据只显示状态；完整密钥永不进入页面响应",
    },
    {
      id: "keyCount",
      header: "Key / 账号",
      value: (g) => g.keyAccountCount,
      cell: (g) => (
        <span className="text-xs tabular-nums">
          {g.keyAccountCount} Key · {g.subscriptionAccountCount} 账号
          {g.officialAccountCount > 0 ? ` · 官方直连 ${g.officialAccountCount}` : ""}
          {g.otherAccountCount > 0 ? ` · 未知 ${g.otherAccountCount}` : ""}
        </span>
      ),
    },
    {
      id: "groups",
      header: "接入分组",
      value: (g) => g.groupNames.join(" "),
      cell: (g) =>
        g.groupNames.length > 0 ? (
          <span className="text-xs">{g.groupNames.join("、")}</span>
        ) : (
          <span className="text-xs text-fg-muted" title="供应商级分组目录尚未接入（ADMIN-IA §8.6 #1）：这里只出账号自带的分组名">
            未接入
          </span>
        ),
    },
    {
      id: "balance",
      header: "总余额 / 有效期",
      value: (g) => moneyValue(g.balanceTotal),
      cell: (g) => <SupplierBalanceCell group={g} />,
      headerTitle: "组内账号余额合计；币种或标度不一致时不给合计",
    },
    {
      id: "ratio",
      header: "充值成本率",
      cell: (g) => <SupplierRateCell group={g} />,
    },
    {
      id: "cost",
      header: "本期我方消耗",
      numeric: true,
      value: (g) => moneyValue(g.costTotal),
      cell: (g) => <SupplierMoneyValueCell total={g.costTotal} covered={g.costCovered} rowCount={g.accounts.length} />,
    },
    {
      id: "profit",
      header: "本期总利润",
      numeric: true,
      value: (g) => moneyValue(g.profitTotal),
      cell: (g) => <SupplierMoneyValueCell total={g.profitTotal} covered={g.profitCovered} rowCount={g.accounts.length} />,
    },
    {
      id: "contact",
      header: "联系人",
      value: (g) => g.contacts.join(" "),
      cell: (g) =>
        g.contacts.length > 0 ? (
          <span className="text-xs">
            {g.contacts[0]}
            {g.contacts.length > 1 ? ` 等 ${g.contacts.length} 人` : ""}
          </span>
        ) : (
          <Badge tone="neutral">未接入</Badge>
        ),
    },
    {
      id: "status",
      header: "状态",
      value: (g) => supplierStatusText(g),
      cell: (g) => <SupplierStatusCell group={g} />,
    },
    {
      id: "detail",
      header: "详情",
      cell: (g) => {
        const primary = g.accounts[0];
        if (!primary) return <span className="text-xs text-fg-muted">—</span>;
        // 未配对账号（platform_id 为空）也要能打开详情：退回当前页面所在的平台,
        // 不能把空串拼进路径——那会漏掉 URL 里的平台段，变成一条 404 的死链
        // （/platforms/suppliers/<id>，认不出 suppliers 是哪个平台）
        return (
          <Link
            to={upstreamDetailPath(primary.platform_id || platform, primary.id)}
            className="text-xs underline underline-offset-2"
            title={g.accounts.length > 1 ? "打开该供应商下第一个账号的详情" : "打开这个账号的详情"}
          >
            详情
          </Link>
        );
      },
      headerTitle: "打开供应商下第一个账号的既有详情页；供应商级汇总详情页是后续切片的工作（ADMIN-IA §8.6 #1）",
    },
  ];
}

function supplierStatusText(g: UpstreamSupplierGroup): string {
  if (g.disabledCount === g.accounts.length) return "已停用";
  if (g.disabledCount > 0 || g.attentionCount > 0) return "需关注";
  if (g.uncoveredSummaryCount === g.accounts.length) return "未知";
  return "健康";
}

function SupplierStatusCell({ group }: { group: UpstreamSupplierGroup }) {
  const text = supplierStatusText(group);
  if (text === "已停用") {
    return <Badge tone="neutral">已停用</Badge>;
  }
  if (text === "需关注") {
    return (
      <Badge
        tone="warning"
        title={`${group.disabledCount > 0 ? `${group.disabledCount} 个账号已停用` : ""}${
          group.attentionCount > 0 ? ` ${group.attentionCount} 个账号可用天数进入告警档` : ""
        }`}
      >
        需关注
      </Badge>
    );
  }
  if (text === "未知") {
    return (
      <Badge tone="neutral" title="上游汇总还没有覆盖这个供应商下的任何账号">
        未知
      </Badge>
    );
  }
  return <Badge tone="success">健康</Badge>;
}

function SupplierRateCell({ group }: { group: UpstreamSupplierGroup }) {
  const spread = group.rechargeRate;
  if (spread.kind === "none") {
    return (
      <span className="text-xs text-fg-muted" title="这个供应商下没有计量型账号（订阅型不适用充值成本率）">
        不适用
      </span>
    );
  }
  if (spread.kind === "single") {
    const code = (spread.currency || "").toUpperCase();
    const amount = code === "CNY" ? `¥${spread.rate}` : `${code || "?"} ${spread.rate}`;
    return <span className="text-xs tabular-nums">{amount} / 额度</span>;
  }
  return (
    <span className="text-xs text-fg-muted" title={spread.distinct.join("、")}>
      {spread.distinct.length} 种费率，见展开
    </span>
  );
}

function SupplierBalanceCell({ group }: { group: UpstreamSupplierGroup }) {
  if (group.balanceTotal.kind !== "ok") {
    return (
      <span className="text-xs text-fg-muted" title={describeMissingTotal(group.balanceTotal.kind)}>
        —
      </span>
    );
  }
  return (
    <div className="text-xs">
      <span className="font-medium tabular-nums">
        {formatScaledMinorUnits(group.balanceTotal.total.toString(), group.balanceTotal.currency, group.balanceTotal.scale)}
      </span>
      {group.balanceCovered < group.accounts.length ? (
        <Badge tone="warning" className="ml-1">
          覆盖不全
        </Badge>
      ) : null}
      <p className="text-fg-muted">
        {group.oldestBalanceObservedAt ? `余额观测于 ${formatUtcTimestamp(group.oldestBalanceObservedAt)}` : "余额观测时间未接入"}
      </p>
    </div>
  );
}

function SupplierMoneyValueCell({
  total,
  covered,
  rowCount,
}: {
  total: ReturnType<typeof sumMoney>;
  covered: number;
  rowCount: number;
}) {
  if (total.kind !== "ok") {
    return (
      <span className="text-xs text-fg-muted" title={describeMissingTotal(total.kind)}>
        未接入
      </span>
    );
  }
  return (
    <span className="text-xs tabular-nums">
      {formatScaledMinorUnits(total.total.toString(), total.currency, total.scale)}
      {covered < rowCount ? (
        <span className="block text-fg-muted">覆盖 {covered}/{rowCount}</span>
      ) : null}
    </span>
  );
}

function moneyValue(total: ReturnType<typeof sumMoney>): bigint | null {
  return total.kind === "ok" ? total.total : null;
}

/** 展开一个供应商组：逐账号的既有登记簿表（列、写操作与展开区跟今天完全
 *  一样，只是现在嵌在供应商这一层下面）。 */
function SupplierAccountsTable({
  group,
  summaries,
  summaryState,
  onDone,
}: {
  group: UpstreamSupplierGroup;
  summaries: Map<string, UpstreamSummary>;
  summaryState: SummaryLoadState;
  onDone: (result: ActionResult) => void;
}) {
  return (
    <DataTableV2
      caption={`${group.name} 名下的上游账号：登记资料、KEY/账号、余额与经营汇总`}
      columns={upstreamColumns(onDone, summaries, summaryState)}
      rows={group.accounts}
      rowKey={(a) => a.id}
      defaultDensity="compact"
      renderExpanded={(account) => <UpstreamAccountDetail account={account} onDone={onDone} />}
      emptyState={<PageState kind="empty" compact title="这个供应商下没有账号" />}
    />
  );
}

/** 按真实 instant 取最旧时间，并保留原始 RFC3339 作为页面证据。
 *
 *  不能直接比较字符串：`01:00-07:00` 实际比 `08:30+02:00` 更新，
 *  但词典序恰好相反。解析不出的时间不替任何金额背书。 */
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
      供应商归并按上游名称（缺省退回网址域名）在展示层完成，登记簿本身仍以账号为写入单元；
      展开一个供应商能看到它名下每个账号的登记资料、KEY 数、余额与经营汇总。未接入分组目录、
      供应商级凭据与订阅有效期仍未进入这套汇总，两者都不会用样例值代替。
    </p>
  );
}

type SummaryLoadState = "pending" | "failed" | "ready";

/** 单个上游账号的列——从原来的旗舰版原样保留（供应商归并只改了外面那层怎么
 *  分组，账号自己的字段、写操作与展开内容一个都没变）。 */
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
      value: (a) => accountMoneyValue(summaries.get(a.id)?.runway.balance),
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
      value: (a: UpstreamAccountItem) => accountMoneyValue(summaries.get(a.id)?.supplyCost),
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

function accountMoneyValue(money: { amountMinor: string } | null | undefined): bigint | null {
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

/** 充值成本率单元格（账号级）。
 *
 *  三个分支，不是两个。`metered` 是布尔，而成本口径有三套（§2.0）：
 *  计量型按实扣 ÷ 倍率、订阅型按批次摊销、官方 API 的 v1 口径待定。 */
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
