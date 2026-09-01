import { useQuery, useQueryClient } from "@tanstack/react-query";
import { DataTableV2, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { Link } from "react-router";
import { listPlatformChannels, type PlatformChannelRow } from "../api/platformChannels";
import {
  accountRowType,
  listUpstreamAccounts,
  listUpstreamSummaries,
  type UpstreamAccountItem,
  type UpstreamSummary,
} from "../api/finance";
import { formatScaledMinorUnits } from "../lib/money";
import { RUNWAY_TONE, runwayReasonText } from "../lib/runway";
import { channelDetailPath } from "../pages/ChannelDetailPage";
import { ApiStateView } from "./ApiStateView";
import { UpstreamAccountDialog } from "./UpstreamAccountDialog";

/** Sub2API / NewAPI 渠道管理表：一行 = 一个平台自己的账号 / Key（原型
 *  `V["s2/upstream"]` / `V["newapi/upstream"]`），只在**恰好一个已登记且
 *  active 的 service** 时启用（见 `ChannelTable.tsx`）。
 *
 *  ## 两条裁定的叠加
 *
 *  2026-09-02 04:40 产品负责人裁定（ACCEPTANCE-LOG）先把这张表从 XM-C-MAP0
 *  做成的映射工作台改回原型的渠道表；同一天 07:20 又裁定补充，推翻了 04:40
 *  裁定里"上游管理降级为页内区块"的做法——**登记簿不再有独立区块，也不再有
 *  独立表**，改成登记簿字段直接并入这张表的行与 `pages/ChannelDetailPage.tsx`
 *  详情页。这一版是两条裁定叠加之后的最终状态：
 *
 *  - 新增 `id` 独立列（原来是 `channel` 列里的一段子文字）；`name` 单独一列
 *  - 新增 `平台 / 类型` 列：`accountRowType` 把绑定账号的三态 `access_method`
 *    收成"订阅账号 / 上游渠道"两档；未绑定显示"未映射"
 *  - 「成功率」列整体去掉——07:20 裁定的必需列清单里没有它，且它恒为
 *    "未接入 · M1.5"，属于渠道保障（XM-ASSURE0）范围，不属于这张表
 *  - 新增 5 个必需占位列（容量/并发、调度、今日统计、用量窗口、最近使用）+
 *    3 个可选占位列（代理、创建时间、过期时间）：这 8 个字段今天在
 *    `GET /api/v1/platforms/{p}/channels` 里完全不存在，字段本身要等并行
 *    切片 XM-CHAN-FIELDS0 扩展渠道目录契约后才有——**结构先落地，占位显式
 *    标未接入并说明原因**，等契约到位后再把这些格子接上真值，不是这一片
 *    要做的事（宪法 12 条：未接的字段必须显式标未接入，不能不出现，也不能
 *    编数字）。「调度」额外说明：即使字段到位，写操作（开关/优先级）也另立
 *    XM-SCHED0，本轮任何时候都只做只读展示
 *  - 供给成本/我方计费消耗/上游分组/可用模型/上游名称联系人/充值成本率六个
 *    原本必需的列降级为列管理里默认收起的可选列——数据没变，只是不再默认
 *    铺满屏幕；候选/冲突/孤儿这些映射工作台专属状态，连同确认/解绑映射的
 *    操作，仍然在 `pages/ChannelDetailPage.tsx` 的「上游映射」卡片,
 *    这一层从 04:40 裁定起就没变过
 *  - 「＋ 添加上游」从页内区块的入口改成这张表工具条上的按钮（`toolbarExtra`),
 *    空态下也放一份，避免"目录是空的 → 连添加上游的入口都找不到"
 *
 *  顶部四格仍由父组件 `ChannelTable` 统一渲染（两个粒度共用同一份汇总数据),
 *  这里只画表本身。 */
export function ManagedChannelTable({
  platform,
  serviceId,
}: {
  platform: "sub2api" | "newapi";
  serviceId: string;
}) {
  const label = platform === "sub2api" ? "Sub2API" : "NewAPI";
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: ["platform-channels", platform, serviceId],
    queryFn: ({ signal }) => listPlatformChannels(platform, serviceId, { signal }),
  });
  // 与渠道详情页的阈值/登记簿查询共用同一个 query key,
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

  const addUpstreamButton = (
    <UpstreamAccountDialog
      platform={platform}
      onDone={() => {
        // 登记簿写完之后重新查一次目录相关的三个 query：新登记的账号可能
        // 立刻变成某一行的候选（base_url 匹配），也可能只是静静躺在登记簿里
        // 等人工去详情页确认绑定——两种情况都需要这三个 query 里至少一个
        // 的数据变化才看得出来
        void queryClient.invalidateQueries({ queryKey: ["platform-channels", platform, serviceId] });
        void queryClient.invalidateQueries({ queryKey: ["finance-upstream-accounts"] });
        void queryClient.invalidateQueries({ queryKey: ["finance", "upstreams", "summary"] });
      }}
    />
  );

  return (
    <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
      <DataTableV2
        caption={`${label} 渠道管理：一行一个账号 / 渠道，ID、类型、状态、余额与毛利；登记簿字段并入本表与详情页`}
        columns={channelRefColumns({ platform, accountsById, summariesById })}
        rows={rows}
        rowKey={(row) => `${row.channelRef.serviceId}:${row.channelRef.externalChannelId}`}
        searchable
        stickyFirstColumn
        defaultDensity="compact"
        filters={channelRefFilters()}
        toolbarExtra={addUpstreamButton}
        views={[
          {
            name: "全部",
            state: { query: "", filters: {}, sort: null, visibleColumns: allColumnIds(platform), density: "compact" },
          },
          {
            name: "未映射",
            state: {
              query: "",
              filters: { platformType: "未映射" },
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
          <div className="flex flex-col items-start gap-3 rounded-lg border border-edge bg-surface p-8 text-center">
            <div className="w-full">
              <p className="text-sm font-medium text-fg">{label} 还没有渠道目录</p>
              <p className="mt-2 text-xs text-fg-muted">
                当前 service 的渠道目录为空或尚未成功采集；这不等于上游没有渠道。
                可以先用下面的入口登记一个上游账号，等待与目录匹配。
              </p>
            </div>
            {addUpstreamButton}
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

/** 「类型」筛选是固定三态，不用像旧版「平台 / 来源」那样现场扫描行数据生成
 *  选项——`accountRowType` 已经把它收成了封闭集合。「状态」筛选沿用既有的
 *  健康 / 需关注两态。 */
function channelRefFilters(): { columnId: string; label: string; options: readonly string[] }[] {
  return [
    { columnId: "platformType", label: "类型", options: ["订阅账号", "上游渠道", "未映射"] },
    { columnId: "status", label: "状态", options: ["健康", "需关注"] },
  ];
}

const MODELS_PENDING = "可用模型清单要渠道保障（M1.5）上线后才有：今天没有任何一个数据源在回答「这条渠道支持哪些模型、验证过几个」";

/** XM-CHAN-FIELDS0 扩展渠道目录契约之前，8 个新增字段（容量/并发、调度、
 *  今日统计、用量窗口、最近使用、代理、创建时间、过期时间）在
 *  `GET /api/v1/platforms/{p}/channels` 里都不存在——不是"查出来是空"，
 *  是这个字段今天压根没有。统一给一段可复用的未接入说明，避免 8 处各写各的。 */
function fieldsPendingHint(label: string, extra?: string): string {
  const base = `${label}要并行切片 XM-CHAN-FIELDS0 扩展渠道目录契约（GET /api/v1/platforms/{p}/channels）之后才有；今天这个字段不存在，不是查出来是空`;
  return extra ? `${base}。${extra}` : base;
}

function pendingFieldColumn(
  id: string,
  header: string,
  opts: { defaultHidden?: boolean; extraHint?: string } = {},
): DataTableColumn<PlatformChannelRow> {
  const hint = fieldsPendingHint(header, opts.extraHint);
  return {
    id,
    header,
    ...(opts.defaultHidden ? { defaultHidden: true } : {}),
    cell: () => (
      <span className="text-xs text-fg-muted" title={hint}>
        未接入
      </span>
    ),
    headerTitle: hint,
  };
}

/** 未绑定 → "未映射"；绑定了但登记簿查不到这个账号 id → 保守按"上游渠道"处理
 *  （不是"订阅账号"，因为订阅账号的成本口径完全不同，不能猜）；其余按绑定
 *  账号的 access_method 走 `accountRowType`。列的 `value` 与单元格渲染共用
 *  这同一个函数，两者不会读出不一致的类型。 */
function typeLabelFor(row: PlatformChannelRow, account: UpstreamAccountItem | undefined): "订阅账号" | "上游渠道" | "未映射" {
  if (!row.binding) return "未映射";
  return accountRowType(account?.access_method ?? "").label as "订阅账号" | "上游渠道";
}

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

  return [
    {
      id: "id",
      header: "ID",
      primary: true,
      value: (row) => row.channelRef.externalChannelId,
      cell: (row) => <span className="block min-w-24 break-all font-mono text-xs">{row.channelRef.externalChannelId}</span>,
      headerTitle: platform === "sub2api" ? "Sub2API 账号 ID" : "NewAPI 渠道 ID",
    },
    {
      id: "name",
      header: "名称",
      value: (row) => row.name,
      cell: (row) => (
        <Link
          to={channelDetailPath(platform, row.channelRef.externalChannelId)}
          className="block min-w-32 font-medium text-fg hover:text-accent"
        >
          {row.name || "未命名渠道"}
        </Link>
      ),
    },
    {
      id: "platformType",
      header: "平台 / 类型",
      value: (row) => typeLabelFor(row, boundAccount(row)),
      cell: (row) => <PlatformTypeCell platform={platform} row={row} account={boundAccount(row)} />,
      headerTitle: "类型区分订阅账号与上游渠道（2026-09-02 07:20 裁定补充），按绑定账号的接入方式派生",
    },
    pendingFieldColumn("capacity", "容量 / 并发"),
    {
      id: "status",
      header: "状态",
      value: (row) => statusText(row),
      cell: (row) => <StatusCell row={row} />,
      headerTitle: "未映射到上游账号，或最近一次观测已过期时标「需关注」；不是请求成功率意义上的健康",
    },
    pendingFieldColumn("scheduling", "调度", {
      extraHint: "即使字段到位，开关 / 优先级这类写操作也另立 XM-SCHED0；本轮任何时候都只做只读展示",
    }),
    pendingFieldColumn("todayStats", "今日统计"),
    pendingFieldColumn("usageWindow", "用量窗口"),
    {
      id: "rate",
      header: "倍率 / 上游倍率",
      value: (row) => boundAccount(row)?.group_rate ?? "",
      cell: (row) => {
        const account = boundAccount(row);
        if (!account?.group_rate) {
          return (
            <span className="text-xs text-fg-muted" title={row.binding ? "这个上游账号没有登记分组倍率" : "渠道还没有绑定上游账号"}>
              未接入
            </span>
          );
        }
        return <span className="text-xs">{account.group_rate}×</span>;
      },
      headerTitle: "分组倍率只展示，不并入成本折算（§10.2）",
    },
    {
      id: "balance",
      header: "余额 / 有效期",
      value: (row) => {
        const summary = boundAccount(row) ? summariesById.get(boundAccount(row)!.id) : undefined;
        return summary?.runway.days ?? null;
      },
      cell: (row) => (
        <BalanceCell row={row} account={boundAccount(row)} summary={boundAccount(row) ? summariesById.get(boundAccount(row)!.id) : undefined} />
      ),
      headerTitle: "余额按绑定的上游账号共享；多个渠道共用同一个上游时这一格显示同一份余额，不是渠道各自的钱",
    },
    {
      id: "grossProfit",
      header: "毛利",
      cell: (row) => <EconomicsCell row={row} field="profit" />,
      headerTitle: "我方计费消耗 − 供给成本，按渠道单独核算",
    },
    pendingFieldColumn("lastUsed", "最近使用"),
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
    pendingFieldColumn("proxy", "代理", { defaultHidden: true }),
    {
      id: "upstreamGroup",
      header: "上游分组",
      defaultHidden: true,
      value: (row) => boundAccount(row)?.upstream_group ?? "",
      cell: (row) => {
        const group = boundAccount(row)?.upstream_group;
        if (!group) {
          return (
            <span className="text-xs text-fg-muted" title={row.binding ? "这个上游账号没有登记接入分组" : "渠道还没有绑定上游账号"}>
              未接入
            </span>
          );
        }
        return <span className="text-xs">{group}</span>;
      },
    },
    {
      id: "models",
      header: "可用模型",
      defaultHidden: true,
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
      defaultHidden: true,
      cell: (row) => <EconomicsCell row={row} field="cost" />,
      headerTitle: "按渠道单独核算的供给成本；服务端经营字段尚未按渠道拆分前显示未接入",
    },
    {
      id: "revenue",
      header: "我方计费消耗",
      defaultHidden: true,
      cell: (row) => <EconomicsCell row={row} field="revenue" />,
      headerTitle: "这个账号 / Key 在窗口内产生的我方计费额",
    },
    pendingFieldColumn("createdAt", "创建时间", { defaultHidden: true }),
    pendingFieldColumn("expiresAt", "过期时间", { defaultHidden: true }),
    {
      id: "upstreamContact",
      header: "上游名称 / 联系人",
      defaultHidden: true,
      value: (row) => boundAccount(row)?.upstream_name ?? "",
      cell: (row) => {
        const account = boundAccount(row);
        if (!account) {
          return <span className="text-xs text-fg-muted">未接入</span>;
        }
        return (
          <div className="min-w-28 text-xs">
            <span className="font-medium">{account.upstream_name || "未命名上游"}</span>
            {account.upstream_contact ? <p className="text-fg-muted">{account.upstream_contact}</p> : null}
          </div>
        );
      },
    },
    {
      id: "rechargeCostRate",
      header: "充值成本率",
      defaultHidden: true,
      value: (row) => boundAccount(row)?.recharge_cost_rate ?? "",
      cell: (row) => {
        const rate = boundAccount(row)?.recharge_cost_rate;
        return rate ? <span className="text-xs">{rate}</span> : <span className="text-xs text-fg-muted">未接入</span>;
      },
    },
  ];
}

function PlatformTypeCell({
  platform,
  row,
  account,
}: {
  platform: "sub2api" | "newapi";
  row: PlatformChannelRow;
  account: UpstreamAccountItem | undefined;
}) {
  const platformLabel = platform === "sub2api" ? "Sub2API" : "NewAPI";
  if (!row.binding) {
    return (
      <div className="min-w-28">
        <Badge tone="info">{platformLabel}</Badge>
        <span className="ml-1">
          <Badge tone={row.candidate.state === "conflict" ? "danger" : "neutral"}>未映射</Badge>
        </span>
        {row.candidate.upstreamAccountIds.length > 0 ? (
          <p className="mt-1 text-xs text-fg-muted">候选 {row.candidate.upstreamAccountIds.length} 个，详情页确认</p>
        ) : null}
      </div>
    );
  }
  const type = typeLabelFor(row, account);
  return (
    <div className="min-w-28">
      <Badge tone="info">{platformLabel}</Badge>
      <span className="ml-1">
        <Badge tone="neutral">{type}</Badge>
      </span>
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
