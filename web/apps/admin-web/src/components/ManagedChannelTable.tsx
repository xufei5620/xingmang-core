import { useQuery } from "@tanstack/react-query";
import { DataTableV2, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { Link } from "react-router";
import { listPlatformChannels, type PlatformChannelRow } from "../api/platformChannels";
import { listUpstreamAccounts, listUpstreamSummaries, type UpstreamAccountItem, type UpstreamSummary } from "../api/finance";
import { formatScaledMinorUnits } from "../lib/money";
import { RUNWAY_TONE, runwayReasonText } from "../lib/runway";
import { channelDetailPath } from "../pages/ChannelDetailPage";
import { ApiStateView } from "./ApiStateView";

/** Sub2API / NewAPI 渠道管理表：一行 = 一个平台自己的账号 / Key（原型
 *  `V["s2/upstream"]` / `V["newapi/upstream"]`），只在**恰好一个已登记且
 *  active 的 service** 时启用（见 `ChannelTable.tsx`）。
 *
 *  2026-09-02 产品负责人裁定（ACCEPTANCE-LOG）纠正了这张表之前的样子：
 *  XM-C-MAP0 把它做成了映射工作台（KPI 是"目录渠道/已确认映射/待处理冲突/
 *  目录完整性"，筛选是"映射状态"，列是"上游映射/健康观测/经营核算"），
 *  验收线当时只审了数据契约、没有对照原型 UI。这一片把列、筛选、视图、
 *  密度与吸附列改回原型逐格对齐；候选/冲突/孤儿这些映射工作台专属状态,
 *  连同确认/解绑映射的操作，一并挪到 `pages/ChannelDetailPage.tsx` 的
 *  「上游映射」卡片——这里的「平台 / 来源」列只显示结果（已绑定上游的名字,
 *  或「未映射」徽章），不再就地展开候选与冲突。
 *
 *  顶部四格由父组件 `ChannelTable` 统一渲染（两个粒度共用同一份汇总数据),
 *  这里只画表本身。 */
export function ManagedChannelTable({
  platform,
  serviceId,
}: {
  platform: "sub2api" | "newapi";
  serviceId: string;
}) {
  const label = platform === "sub2api" ? "Sub2API" : "NewAPI";
  const query = useQuery({
    queryKey: ["platform-channels", platform, serviceId],
    queryFn: ({ signal }) => listPlatformChannels(platform, serviceId, { signal }),
  });
  // 与「上游管理」区块和 ChannelTable 的阈值查询共用同一个 query key,
  // react-query 按 key 去重，不会多打一次请求
  const accountsQuery = useQuery({
    queryKey: ["finance-upstream-accounts"],
    queryFn: ({ signal }) => listUpstreamAccounts({ signal }),
  });
  const summaryQuery = useQuery({
    queryKey: ["finance", "upstreams", "summary"],
    queryFn: ({ signal }) => listUpstreamSummaries({ signal }),
  });

  const rows = query.data?.items ?? [];
  const accountsById = new Map<string, UpstreamAccountItem>();
  for (const a of accountsQuery.data ?? []) accountsById.set(a.id, a);
  const summariesById = new Map<string, UpstreamSummary>();
  for (const s of summaryQuery.data?.items ?? []) summariesById.set(s.id, s);

  return (
    <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
      <DataTableV2
        caption={`${label} 渠道管理：账号 / Key、上游映射、成本、我方计费与毛利`}
        columns={channelRefColumns({ platform, accountsById, summariesById })}
        rows={rows}
        rowKey={(row) => `${row.channelRef.serviceId}:${row.channelRef.externalChannelId}`}
        searchable
        stickyFirstColumn
        defaultDensity="compact"
        filters={channelRefFilters(rows, accountsById)}
        views={[
          {
            name: "全部",
            state: { query: "", filters: {}, sort: null, visibleColumns: allColumnIds(platform), density: "compact" },
          },
          {
            name: "未映射",
            state: {
              query: "",
              filters: { platform: "未映射" },
              sort: null,
              visibleColumns: allColumnIds(platform),
              density: "compact",
            },
          },
          {
            name: "需关注",
            state: {
              query: "",
              filters: { status: "需关注" },
              sort: null,
              visibleColumns: allColumnIds(platform),
              density: "compact",
            },
          },
        ]}
        emptyState={
          <div className="rounded-lg border border-edge bg-surface p-8 text-center">
            <p className="text-sm font-medium text-fg">{label} 还没有渠道目录</p>
            <p className="mt-2 text-xs text-fg-muted">
              当前 service 的渠道目录为空或尚未成功采集；这不等于上游没有渠道。
            </p>
          </div>
        }
      />
    </ApiStateView>
  );
}

function allColumnIds(platform: "sub2api" | "newapi"): string[] {
  return channelRefColumns({
    platform,
    accountsById: new Map(),
    summariesById: new Map(),
  }).map((c) => c.id);
}

/** 「平台 / 来源」筛选的选项：动态取当前页面上出现过的绑定上游名，外加
 *  「未映射」。原型这一格的选项是静态样例厂商名（OpenAI/Anthropic/…），
 *  我们的登记簿不按 AI 供应商分类，编一套假选项会让筛选看起来能筛出结果、
 *  实际上一个都筛不中——所以改成按真实能筛的维度给选项（宪法 12 条）。 */
function channelRefFilters(
  rows: readonly PlatformChannelRow[],
  accountsById: ReadonlyMap<string, UpstreamAccountItem>,
): { columnId: string; label: string; options: readonly string[] }[] {
  const names = new Set<string>();
  let hasUnmapped = false;
  for (const row of rows) {
    if (row.binding) {
      const account = accountsById.get(row.binding.upstreamAccountId);
      names.add(account?.upstream_name || row.binding.upstreamAccountId);
    } else {
      hasUnmapped = true;
    }
  }
  const options = [...names].sort((a, b) => a.localeCompare(b, "zh-CN"));
  if (hasUnmapped) options.push("未映射");
  return [
    { columnId: "platform", label: "平台 / 来源", options },
    { columnId: "status", label: "状态", options: ["健康", "需关注"] },
  ];
}

const MODELS_PENDING = "可用模型清单要渠道保障（M1.5）上线后才有：今天没有任何一个数据源在回答「这条渠道支持哪些模型、验证过几个」";
const SUCCESS_RATE_PENDING = "24h 成功率要渠道保障（M1.5）上线后才有：请求成功率今天不在渠道目录的采集范围里";

function channelRefColumns({
  platform,
  accountsById,
  summariesById,
}: {
  platform: "sub2api" | "newapi";
  accountsById: ReadonlyMap<string, UpstreamAccountItem>;
  summariesById: ReadonlyMap<string, UpstreamSummary>;
}): DataTableColumn<PlatformChannelRow>[] {
  const boundAccount = (row: PlatformChannelRow) =>
    row.binding ? accountsById.get(row.binding.upstreamAccountId) : undefined;

  const columns: DataTableColumn<PlatformChannelRow>[] = [
    {
      id: "channel",
      header: platform === "sub2api" ? "Sub2API 账号" : "NewAPI 渠道",
      primary: true,
      value: (row) => `${row.name} ${row.channelRef.externalChannelId}`,
      cell: (row) => (
        <Link
          to={channelDetailPath(platform, row.channelRef.externalChannelId)}
          className="block min-w-44 text-fg hover:text-accent"
        >
          <strong className="font-medium">{row.name || "未命名渠道"}</strong>
          <span className="block font-mono text-xs text-fg-muted">{row.channelRef.externalChannelId}</span>
        </Link>
      ),
      headerTitle: "一行 = 一个平台自己的账号 / Key（channel_ref）",
    },
    {
      id: "platform",
      header: "平台 / 来源",
      value: (row) => {
        const account = boundAccount(row);
        return row.binding ? account?.upstream_name || row.binding.upstreamAccountId : "未映射";
      },
      cell: (row) => <SourceCell row={row} account={boundAccount(row)} />,
    },
    {
      id: "group",
      header: "上游分组",
      value: (row) => boundAccount(row)?.upstream_group ?? "",
      cell: (row) => {
        const account = boundAccount(row);
        const group = account?.upstream_group;
        if (!group) {
          return (
            <span className="text-xs text-fg-muted" title={row.binding ? "这个上游账号没有登记接入分组" : "渠道还没有绑定上游账号"}>
              未接入
            </span>
          );
        }
        // 原型这一格是「分组名 · 倍率」（如 gpt-main · 0.85×）；倍率只展示,
        // 不参与任何金额计算（§10.2，与 ChannelTableColumns 的同名字段同一条纪律）
        return (
          <span className="text-xs">
            {group}
            {account?.group_rate ? <span className="text-fg-muted"> · {account.group_rate}×</span> : null}
          </span>
        );
      },
      headerTitle: "分组倍率只展示，不并入成本折算（§10.2）",
    },
    {
      id: "models",
      header: "可用模型",
      cell: (row) =>
        typeof row.models?.count === "number" ? (
          <span className="text-xs">{row.models.count} 个模型（仅数量）</span>
        ) : (
          <span className="text-xs text-fg-muted" title={MODELS_PENDING}>
            未接入 · M1.5
          </span>
        ),
      headerTitle: MODELS_PENDING,
    },
    {
      id: "supplyCost",
      header: "供给成本",
      cell: (row) => <EconomicsCell row={row} field="cost" />,
      headerTitle: "按渠道单独核算的供给成本；服务端经营字段尚未按渠道拆分前显示未接入",
    },
    {
      id: "revenue",
      header: "我方计费消耗",
      cell: (row) => <EconomicsCell row={row} field="revenue" />,
      headerTitle: "这个账号 / Key 在窗口内产生的我方计费额",
    },
    {
      id: "balance",
      header: platform === "sub2api" ? "余额 / 有效期" : "余额状态",
      value: (row) => {
        const summary = boundAccount(row) ? summariesById.get(boundAccount(row)!.id) : undefined;
        return summary?.runway.days ?? null;
      },
      cell: (row) => <BalanceCell row={row} account={boundAccount(row)} summary={boundAccount(row) ? summariesById.get(boundAccount(row)!.id) : undefined} />,
      headerTitle: "余额按绑定的上游账号共享；多个渠道共用同一个上游时这一格显示同一份余额，不是渠道各自的钱",
    },
    {
      id: "grossProfit",
      header: "毛利",
      cell: (row) => <EconomicsCell row={row} field="profit" />,
      headerTitle: "我方计费消耗 − 供给成本，按渠道单独核算",
    },
  ];

  if (platform === "sub2api") {
    columns.push({
      id: "successRate",
      header: "成功率",
      cell: () => (
        <span className="text-xs text-fg-muted" title={SUCCESS_RATE_PENDING}>
          未接入 · M1.5
        </span>
      ),
      headerTitle: SUCCESS_RATE_PENDING,
    });
  }

  columns.push(
    {
      id: "status",
      header: "状态",
      value: (row) => statusText(row),
      cell: (row) => <StatusCell row={row} />,
      headerTitle: "未映射到上游账号，或最近一次观测已过期时标「需关注」；不是请求成功率意义上的健康",
    },
    {
      id: "detail",
      header: "详情",
      cell: (row) => (
        <Link
          to={channelDetailPath(platform, row.channelRef.externalChannelId)}
          className="text-xs underline underline-offset-2"
        >
          详情
        </Link>
      ),
      headerTitle: "上游映射的确认 / 解绑与候选、冲突详情都在渠道详情页",
    },
  );

  return columns;
}

function SourceCell({ row, account }: { row: PlatformChannelRow; account: UpstreamAccountItem | undefined }) {
  if (!row.binding) {
    return (
      <div>
        <Badge tone={row.candidate.state === "conflict" ? "danger" : "neutral"}>未映射</Badge>
        {row.candidate.upstreamAccountIds.length > 0 ? (
          <p className="mt-1 text-xs text-fg-muted">候选 {row.candidate.upstreamAccountIds.length} 个，详情页确认</p>
        ) : null}
      </div>
    );
  }
  return (
    <div className="min-w-32">
      <span className="text-xs font-medium">{account?.upstream_name || "未接入"}</span>
      {!account ? (
        <p className="mt-1 text-xs text-fg-muted" title="已绑定但登记簿里找不到这个上游账号 id">
          账号 {row.binding.upstreamAccountId}
        </p>
      ) : null}
    </div>
  );
}

function EconomicsCell({ row, field }: { row: PlatformChannelRow; field: "cost" | "revenue" | "profit" }) {
  // 服务端目前不按渠道拆分经营字段（economics 恒为 null），economicsState
  // 说明具体原因；等后端补上这个字段，这里不用改——数据钩子已经接在
  // row.economics 上了（ADMIN-IA §8.6 #4 同一条纪律：留列位、留钩子，
  // 不编数字）。
  if (!row.economics) {
    return (
      <span className="text-xs text-fg-muted" title={row.economicsState || "服务端暂无可断言口径"}>
        未接入
      </span>
    );
  }
  const raw = row.economics[field === "cost" ? "supply_cost" : field === "revenue" ? "usage_revenue" : "gross_profit"];
  if (raw && typeof raw === "object" && "amount_minor" in raw) {
    const money = raw as { amount_minor: string; currency: string; scale: number };
    return <span className="text-xs">{formatScaledMinorUnits(money.amount_minor, money.currency, money.scale)}</span>;
  }
  return <span className="text-xs text-fg-muted">服务端已提供经营对象，字段形状按契约展开</span>;
}

function BalanceCell({
  account,
  summary,
}: {
  row: PlatformChannelRow;
  account: UpstreamAccountItem | undefined;
  summary: UpstreamSummary | undefined;
}) {
  if (!account) {
    return <span className="text-xs text-fg-muted">未接入</span>;
  }
  if (!summary) {
    return (
      <span className="text-xs text-fg-muted" title="上游汇总里还没有这个账号 id 的行">
        未接入
      </span>
    );
  }
  const { runway } = summary;
  if (runway.days === null) {
    return (
      <span className="text-xs text-fg-muted" title={runway.reason || "可用天数给不出"}>
        {runwayReasonText(runway) ?? "—"}
      </span>
    );
  }
  const tone = RUNWAY_TONE[runway.level] ?? "neutral";
  return (
    <div className="text-xs">
      <span className="font-medium">
        {runway.balance ? formatScaledMinorUnits(runway.balance.amountMinor, runway.balance.currency, runway.balance.scale) : "—"}
      </span>
      <span className="ml-1">
        <Badge tone={tone}>约 {runway.days} 天</Badge>
      </span>
      <p className="text-fg-muted">{runway.balanceObservedAt ? `余额观测 ${runway.balanceObservedAt}` : "余额观测时间未接入"}</p>
      <p className="text-fg-muted">上游账号共享余额，同一上游下的多个渠道不重复合计</p>
    </div>
  );
}

/** 原型这一格是"健康 / 需关注"，判据是请求成功率——那个我们今天没有。
 *  换成两个我们真有的信号：这条渠道有没有绑定上游、绑定之后的观测是不是
 *  新鲜的。换判据必须说出来（列头 title 里写了）。 */
function statusText(row: PlatformChannelRow): string {
  if (!row.binding) return "需关注";
  if (!row.health) return "未接入";
  return row.observed.isStale ? "需关注" : "健康";
}

function StatusCell({ row }: { row: PlatformChannelRow }) {
  const text = statusText(row);
  if (text === "未接入") {
    return (
      <Badge tone="neutral" title="这条渠道还没有健康观测记录">
        未接入
      </Badge>
    );
  }
  if (text === "需关注") {
    return (
      <Badge
        tone="warning"
        title={row.binding ? "最近一次观测已过期" : "还没有绑定上游账号，成本与毛利算不出来"}
      >
        需关注
      </Badge>
    );
  }
  return (
    <Badge tone="success" title="已绑定上游账号，且最近一次观测仍新鲜">
      健康
    </Badge>
  );
}
