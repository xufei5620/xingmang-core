import { useQuery, useQueryClient } from "@tanstack/react-query";
import { PageState, StatTile } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import {
  listChannelSummaries,
  listUpstreamSummaries,
  UPSTREAM_SUMMARY_QUERY,
  type ChannelSummary,
  type RunwayThresholds,
} from "../api/finance";
import { formatGrossMargin } from "../lib/channelEconomics";
import {
  aggregateMargin,
  channelTotals,
  countByAccessMethod,
  countNeedTopup,
  summariesForPlatform,
  TOPUP_WINDOW_DAYS,
  type AccessMethodCounts,
  type ChannelTotals,
} from "../lib/channelTable";
import { formatScaledMinorUnits } from "../lib/money";
import { describeMissingTotal } from "../lib/upstreamTotals";
import { ApiStateView } from "./ApiStateView";
import { ChannelScopeNote } from "./ChannelScopeNote";
import { channelTableColumns, type ChannelPlatform } from "./ChannelTableColumns";
import { PersistentDataTable, platformSavedViewTableKey } from "./PersistentDataTable";
import { ManagedChannelTable } from "./ManagedChannelTable";
import { UpstreamAccountDialog } from "./UpstreamAccountDialog";

/** 渠道管理表（两个平台共用）。原型 `V["s2/upstream"]` / `V["newapi/upstream"]`。
 *
 *  ## 行粒度：两条分支，各自的行代表不同的东西
 *
 *  **恰好一个已登记且 active 的 service** 时（生产环境的实际状态）走
 *  `ManagedChannelTable`：一行 = 被管平台自己的一条渠道（Sub2API 账号 /
 *  NewAPI 渠道），这正是原型的粒度。2026-09-02 07:20 产品负责人裁定补充
 *  （ACCEPTANCE-LOG）在此基础上把登记簿字段（上游分组、余额、充值成本率、
 *  联系人……）并入这张表的行与渠道详情页，不再单独有一张"上游管理"表或
 *  页内区块——旧版本（04:40 裁定）曾经把登记簿降级成这张表下方的页内区块,
 *  07:20 的补充裁定推翻了这个做法，本文件因此不再挂载任何登记簿相关的
 *  独立组件。
 *
 *  多实例或未登记时回落到账号粒度分支（`channelTable.ts` 驱动）：一行 = 一个
 *  `finance.upstream_account`，因为登记簿里没有「平台渠道 ↔ 上游账号」的对应
 *  关系（只有「上游令牌 ↔ 我方账号」的令牌映射，那是另一个维度）。XM-0048
 *  试过按 id join，结果是一条都对不上、经营列全空——看起来像「后端没数据」,
 *  实际是两套 id 互不认识。这条分支的行粒度**不受** 07:20 裁定影响：那条
 *  裁定明确说的是"一个 Sub2API 账号 / 一条 NewAPI 渠道"，即 ChannelRef 粒度,
 *  这个回落分支从来就不是那个粒度。`ChannelScopeNote` 的文案按哪条分支在渲染
 *  分别措辞，不会说错自己是哪个粒度。
 *
 *  ## 顶部四格
 *
 *  与原型逐格对应。第二格「N 天内需补充」必须同时报出「算不出天数」的条数——
 *  今天余额采集还没接通（§7 的覆盖率边界），只说「0 个需补充」会被读成
 *  「余额都很充裕」，而事实是我们不知道。 */
export function ChannelTable({
  platform,
  lead,
  serviceId,
  serviceStatus,
}: {
  platform: ChannelPlatform;
  lead: string;
  serviceId?: string;
  serviceStatus?: string;
}) {
  const usesChannelRefGrain = Boolean(serviceId && serviceStatus === "active");

  const summaryQuery = useQuery({
    queryKey: ["finance", "channels", "summary"],
    queryFn: ({ signal }) => listChannelSummaries({ signal }),
  });
  // 阈值只在上游汇总端点上下发（两个端点同源同粒度，XM-0049 一份解析）。
  // **单独一个 query**：档位说明取不到不该把整张表拖成错误态
  const thresholdQuery = useQuery({
    queryKey: [UPSTREAM_SUMMARY_QUERY],
    queryFn: ({ signal }) => listUpstreamSummaries({ signal }),
  });

  const rows = summariesForPlatform(summaryQuery.data?.items ?? [], platform);
  const thresholds = thresholdQuery.data?.runwayThresholds ?? null;
  const columns = channelTableColumns({ platform, thresholds });
  // 预置视图要给全 TableViewState 的五个字段。列清单从列定义现取，
  // 而不是手抄一份——手抄的那份会在下次加列时悄悄把新列藏起来
  const allColumnIds = columns.map((c) => c.id);

  const queryClient = useQueryClient();
  // 回落分支（账号粒度）自己的「＋ 添加上游」入口——ManagedChannelTable 分支
  // 已经在自己的工具条上放了一份，这里只补回落分支缺的那一份，不是重复挂载
  // 同一个组件两次
  const addUpstreamButton = (
    <UpstreamAccountDialog
      platform={platform}
      onDone={() => {
        void queryClient.invalidateQueries({ queryKey: ["finance", "channels", "summary"] });
        void queryClient.invalidateQueries({ queryKey: ["finance-upstream-accounts"] });
        void queryClient.invalidateQueries({ queryKey: [UPSTREAM_SUMMARY_QUERY] });
      }}
    />
  );

  // 只有一个已登记且 active 的 service 才切换到 ChannelRef 粒度；
  // 多实例或降级状态继续使用已验证的上游账号汇总，避免猜测归属。
  //
  // 口径声明 / 顶部四格不再因为分支而缺席——之前 ChannelRef 分支整段提前
  // return，生产环境（单实例 active，恰好走这条分支）因此看不到这两样东西,
  // 这是 ACCEPTANCE-LOG 04:40 裁定记录的"现状渠道管理页偏离原型"的一部分。
  // 07:20 的补充裁定进一步把登记簿（原「上游管理」）并入了行与详情页，
  // 不再有独立区块可挂，因此这里不再有第三样"缺席"的东西要补。
  const mainTable = usesChannelRefGrain ? (
    <ManagedChannelTable platform={platform} serviceId={serviceId!} />
  ) : (
    <PersistentDataTable
      tableKey={platformSavedViewTableKey(platform, "channels")}
      caption={`${platform === "sub2api" ? "Sub2API" : "NewAPI"} 逐上游账号的成本、我方计费消耗与毛利`}
      columns={columns}
      rows={rows}
      rowKey={(row) => row.id}
      pageSize={10}
      searchable
      stickyFirstColumn
      defaultDensity="compact"
      toolbarExtra={addUpstreamButton}
      filters={[
        { columnId: "platform", label: "平台 / 来源", options: PLATFORM_FILTERS },
        { columnId: "status", label: "状态", options: STATUS_FILTERS },
      ]}
      views={[
        {
          name: "需关注",
          state: {
            query: "",
            filters: { status: "需关注" },
            sort: null,
            visibleColumns: allColumnIds,
            density: "compact",
          },
        },
        {
          name: "未归属",
          state: {
            query: "",
            filters: { platform: "未归属" },
            sort: null,
            visibleColumns: allColumnIds,
            density: "compact",
          },
        },
      ]}
      emptyState={
        <div className="flex flex-col items-start gap-3">
          <PageState
            kind="empty"
            title="还没有上游账号"
            description="登记簿里这个环境下还没有归属本平台（或未配对）的上游账号；用下面的入口登记之后会出现在这里"
          />
          {addUpstreamButton}
        </div>
      }
    />
  );

  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-col gap-3">
        <p className="text-xs text-fg-muted">{lead}</p>
        <ChannelScopeNote platform={platform} grain={usesChannelRefGrain ? "channel" : "account"} />
        <ApiStateView
          isPending={summaryQuery.isPending}
          error={summaryQuery.error}
          onRetry={() => void summaryQuery.refetch()}
        >
          <div className="flex flex-col gap-3">
            <ChannelTiles platform={platform} rows={rows} />
            {mainTable}
          </div>
        </ApiStateView>
      </div>
    </section>
  );
}

const PLATFORM_FILTERS = ["sub2api", "newapi", "未归属"] as const;
const STATUS_FILTERS = ["正常", "需关注", "余额未知", "已停用"] as const;

/** 顶部四格。原型逐格对齐；两个粒度的渠道表共用同一份（都是同一份
 *  `finance.upstream_account` 汇总数据算出来的，跟表本身画的是哪个粒度
 *  无关——渠道详情、映射确认这类**逐渠道**的事才分粒度）。 */
export function ChannelTiles({
  platform,
  rows,
}: {
  platform: ChannelPlatform;
  rows: readonly ChannelSummary[];
}) {
  const counts = countByAccessMethod(rows);
  const topup = countNeedTopup(rows);
  const totals = channelTotals(rows);

  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
      {platform === "sub2api" ? (
        <StatTile
          label="Key 账号 / 订阅账号"
          value={`${counts.metered} / ${counts.subscription}`}
          note={countsNote(rows.length, counts)}
        />
      ) : (
        <StatTile
          label="NewAPI 渠道"
          value={String(rows.length)}
          note={countsNote(rows.length, counts)}
        />
      )}

      <TopupTile topup={topup} />
      <MoneyTile label="今日我方计费消耗" totals={totals} pick="revenue" />
      <MoneyTile label="今日毛利" totals={totals} pick="profit" />
    </div>
  );
}

/** 计数格的说明行。
 *
 *  **官方直连单独说**：把它并进 Key 会让人以为它有余额和倍率，
 *  并进订阅会让人以为它在摊销，而它的成本口径今天还没定。
 *  一个数都没有时也要说话——`StatTile` 的 note 是必填的，
 *  留白等于让读的人自己猜这一格的范围。 */
function countsNote(rowCount: number, counts: AccessMethodCounts): string {
  const parts = [`共 ${rowCount} 个上游账号`];
  if (counts.official > 0) parts.push(`官方直连 ${counts.official}`);
  if (counts.other > 0) parts.push(`未知接入方式 ${counts.other}`);
  return parts.join(" · ");
}

/** 「7 天内需补充」。
 *
 *  `unknown > 0` 时**必须显示**「另有 N 个算不出天数」并挂一个 warning 徽章：
 *  这与 XM-0049 告警规则 R5 是同一条判据（算不出天数的不响也不算健康）。
 *  只给一个 0，读的人会以为余额都很充裕。 */
function TopupTile({ topup }: { topup: ReturnType<typeof countNeedTopup> }) {
  const { count, unknown, applicable } = topup;
  if (applicable === 0) {
    return (
      <StatTile
        label={`${TOPUP_WINDOW_DAYS} 天内需补充`}
        value="—"
        unavailable
        note="本页没有计量型上游账号；订阅型没有余额这个概念"
        status={<Badge tone="neutral">不适用</Badge>}
      />
    );
  }
  return (
    <StatTile
      label={`${TOPUP_WINDOW_DAYS} 天内需补充`}
      value={String(count)}
      note={
        unknown > 0
          ? `${applicable} 个计量型账号里，另有 ${unknown} 个算不出可用天数（余额未读到），这 ${count} 个只是下界`
          : `${applicable} 个计量型账号的可用天数都算得出来`
      }
      {...(unknown > 0
        ? { status: <Badge tone="warning">余额覆盖不全</Badge> }
        : count > 0
          ? { status: <Badge tone="danger">需补充</Badge> }
          : {})}
    />
  );
}

/** 金额格。合计算不出来时说明为什么，覆盖不全时标出来。 */
function MoneyTile({
  label,
  totals,
  pick,
}: {
  label: string;
  totals: ChannelTotals;
  pick: "revenue" | "profit";
}) {
  const total = pick === "revenue" ? totals.revenue : totals.profit;
  const covered = pick === "revenue" ? totals.revenueCovered : totals.profitCovered;

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

  const partial = covered < totals.rowCount;
  const note = partial
    ? `只含 ${totals.rowCount} 个账号里有汇总的 ${covered} 个`
    : `含全部 ${totals.rowCount} 个账号`;

  // 毛利这一格带毛利率角标（原型），但**只在合计站得住的时候给**：
  // 覆盖不全时 aggregateMargin 返回 null，理由见 channelTable.ts
  const margin = pick === "profit" ? formatGrossMargin(aggregateMargin(totals)) : null;

  return (
    <StatTile
      label={label}
      value={formatScaledMinorUnits(total.total.toString(), total.currency, total.scale)}
      note={note}
      {...(margin
        ? { status: <Badge tone={total.total < 0n ? "danger" : "success"}>{margin}</Badge> }
        : partial
          ? { status: <Badge tone="warning">覆盖不全</Badge> }
          : {})}
    />
  );
}
