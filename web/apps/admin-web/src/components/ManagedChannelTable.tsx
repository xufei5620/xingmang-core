import { useQuery, useQueryClient } from "@tanstack/react-query";
import { DataTableV2, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge, cx } from "@xingmang/ui-primitives";
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
 *  ## 三轮裁定叠加
 *
 *  04:40 裁定把这张表从 XM-C-MAP0 的映射工作台改回原型渠道表；同日 07:20/
 *  07:25 两条口径一致的补充裁定（先在 ACCEPTANCE-LOG 里发现、随后 team-lead
 *  发来完整规格，含逐字段 JSON 名）把"上游管理降级为页内区块"整体推翻——
 *  单表、新增 ID 列、按行区分「订阅账号」与「上游渠道」、登记簿字段并入行与
 *  详情页。这一版是三轮叠加后的最终状态：
 *
 *  - `平台 / 类型` 列显示**真实供应商名**（`row.vendor` 优先，没有则回退到
 *    绑定账号的 `upstream_name` join）+ 类型徽章（`row.kind` 优先，没有则用
 *    `accountRowType` 从 access_method 派生）——不是原型的假 AI 供应商分类
 *  - `倍率 / 上游倍率` 同样"新字段优先，查不到就退回登记簿 join"：
 *    `row.rateMultiplier`/`row.upstreamMultiplier` 优先于 `group_rate`/
 *    `recharge_ratio`。这两个字段今天**有真实数据**（登记簿 join），
 *    不是从头到尾的未接入——与下面 8 个纯新增字段的诚实策略不同，这里是
 *    "有真数据就先用，新契约来了自动切换到更权威的来源"，不是无中生有
 *  - `容量/并发`、`调度`、`今日统计`、`用量窗口`、`最近使用`（必需列）与
 *    `代理`、`创建时间`、`过期时间`（可选列）这 8 个字段今天在
 *    `GET /api/v1/platforms/{p}/channels` 里恒为 null——已经把类型定好、
 *    按 XM-CHAN-FIELDS0 裁定给定的 JSON 名解析（`api/platformChannels.ts` 的
 *    `PlatformChannelFieldsExtension`），这里只管渲染：字段非 null 就显示
 *    真值，null 就显式未接入并说明原因。等 chanfields 交付，这张表**不需要
 *    再改代码**，后端一开始下发真值格子就自动"亮起来"
 *  - `调度` 列即使字段到位也保持只读：渲染一个禁用态的开关控件 + 优先级,
 *    tooltip 固定文案「调度开关待 XM-SCHED0 Action」——写操作是另一个切片
 *  - `状态` 列**没有**接 `row.status`：那是 chanfields 还没定形的枚举，
 *    盲目塞进既有的健康/需关注徽章体系等于猜它的取值，宪法 12 条不允许。
 *    类型已经加到 `PlatformChannelRow` 上备用，渲染逻辑维持现状（绑定 +
 *    观测新鲜度），是刻意的、记录在案的取舍，不是漏做
 *  - 筛选新增「平台 / 来源」（按真实供应商名动态生成选项，回到 04:40 那一版
 *    的做法，与「类型」是两个独立维度）；视图改成 全部/订阅账号/上游渠道/
 *    需关注（「未映射」不再单独占一个视图，但仍是「类型」筛选里的一个选项,
 *    否则未映射的行会无处可筛）
 *
 *  顶部四格仍由父组件 `ChannelTable` 统一渲染，这里只画表本身。 */
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

  const columns = channelRefColumns({ platform, accountsById, summariesById });
  const allColumnIds = columns.map((c) => c.id);

  return (
    <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
      <DataTableV2
        caption={`${label} 渠道管理：一行一个账号 / 渠道，ID、类型、状态、余额与毛利；登记簿字段并入本表与详情页`}
        columns={columns}
        rows={rows}
        rowKey={(row) => `${row.channelRef.serviceId}:${row.channelRef.externalChannelId}`}
        searchable
        stickyFirstColumn
        defaultDensity="compact"
        filters={channelRefFilters(rows, accountsById)}
        toolbarExtra={addUpstreamButton}
        views={[
          { name: "全部", state: { query: "", filters: {}, sort: null, visibleColumns: allColumnIds, density: "compact" } },
          {
            name: "订阅账号",
            state: { query: "", filters: { platformType: "订阅账号" }, sort: null, visibleColumns: allColumnIds, density: "compact" },
          },
          {
            name: "上游渠道",
            state: { query: "", filters: { platformType: "上游渠道" }, sort: null, visibleColumns: allColumnIds, density: "compact" },
          },
          {
            name: "需关注",
            state: { query: "", filters: { status: "需关注" }, sort: null, visibleColumns: allColumnIds, density: "compact" },
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

/** 「平台 / 来源」筛选：动态取当前页面上出现过的真实供应商名（`row.vendor`
 *  优先，没有则回退登记簿 join 的 `upstream_name`），外加「未映射」——不是
 *  原型的静态假供应商列表（宪法 12 条）。「类型」是固定三态，`accountRowType`
 *  已经把它收成了封闭集合，多出的「未映射」选项是为了让未绑定的行也能被
 *  筛出来，不然「类型」筛选覆盖不了所有行。「状态」沿用既有的健康 / 需关注
 *  两态。 */
function channelRefFilters(
  rows: readonly PlatformChannelRow[],
  accountsById: ReadonlyMap<string, UpstreamAccountItem>,
): { columnId: string; label: string; options: readonly string[] }[] {
  const vendors = new Set<string>();
  let hasUnmapped = false;
  for (const row of rows) {
    if (row.binding) {
      const account = accountsById.get(row.binding.upstreamAccountId);
      vendors.add(row.vendor ?? account?.upstream_name ?? row.binding.upstreamAccountId);
    } else {
      hasUnmapped = true;
    }
  }
  const vendorOptions = [...vendors].sort((a, b) => a.localeCompare(b, "zh-CN"));
  if (hasUnmapped) vendorOptions.push("未映射");

  return [
    { columnId: "upstreamContact", label: "平台 / 来源", options: vendorOptions },
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

function pendingBadge(hint: string) {
  return (
    <span className="text-xs text-fg-muted" title={hint}>
      未接入
    </span>
  );
}

/** 未绑定 → "未映射"；绑定了但登记簿查不到这个账号 id → 保守按"上游渠道"处理
 *  （不是"订阅账号"，因为订阅账号的成本口径完全不同，不能猜）；`row.kind`
 *  有值时优先用它（chanfields 交付后更权威的来源），否则按绑定账号的
 *  access_method 走 `accountRowType`。列的 `value` 与单元格渲染共用这同一个
 *  函数，两者不会读出不一致的类型。 */
function typeLabelFor(row: PlatformChannelRow, account: UpstreamAccountItem | undefined): "订阅账号" | "上游渠道" | "未映射" {
  if (!row.binding) return "未映射";
  if (row.kind === "subscription") return "订阅账号";
  if (row.kind === "upstream") return "上游渠道";
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
      cell: (row) => <span className="block min-w-24 select-all break-all font-mono text-xs">{row.channelRef.externalChannelId}</span>,
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
      cell: (row) => <PlatformTypeCell row={row} account={boundAccount(row)} />,
      headerTitle: "供应商名（真实登记簿数据，不是假的 AI 供应商分类）+ 类型徽章：类型区分订阅账号与上游渠道",
    },
    {
      id: "capacity",
      header: "容量 / 并发",
      cell: (row) =>
        row.capacity ? (
          <span className="text-xs">
            {row.capacity.used} / {row.capacity.limit}
          </span>
        ) : (
          pendingBadge(fieldsPendingHint("容量 / 并发"))
        ),
      headerTitle: fieldsPendingHint("容量 / 并发"),
    },
    {
      id: "status",
      header: "状态",
      value: (row) => statusText(row),
      cell: (row) => <StatusCell row={row} />,
      headerTitle: "未映射到上游账号，或最近一次观测已过期时标「需关注」；不是请求成功率意义上的健康。这一格没有接 chanfields 的 status 字段——那是尚未定形的枚举，盲目映射到健康/需关注体系等于猜取值",
    },
    {
      id: "scheduling",
      header: "调度",
      cell: (row) => <SchedulingCell row={row} />,
      headerTitle: fieldsPendingHint("调度", "即使字段到位，开关 / 优先级这类写操作也另立 XM-SCHED0；本轮任何时候都只做只读展示"),
    },
    {
      id: "todayStats",
      header: "今日统计",
      cell: (row) => <TodayStatsCell row={row} />,
      headerTitle: fieldsPendingHint("今日统计（请求数 · 成功率 · 消耗）"),
    },
    {
      id: "usageWindow",
      header: "用量窗口",
      cell: (row) => <UsageWindowCell row={row} account={boundAccount(row)} />,
      headerTitle: "订阅账号显示用量窗口占比与重置时间；上游渠道没有这个概念，显示不适用；" + fieldsPendingHint("用量窗口"),
    },
    {
      id: "rate",
      header: "倍率 / 上游倍率",
      value: (row) => row.rateMultiplier ?? boundAccount(row)?.group_rate ?? "",
      cell: (row) => <RateCell row={row} account={boundAccount(row)} />,
      headerTitle: "倍率只展示，不并入成本折算（§10.2）。新契约字段（rate_multiplier/upstream_multiplier）到位前，先用登记簿 join 的 group_rate/recharge_ratio",
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
    {
      id: "lastUsed",
      header: "最近使用",
      value: (row) => row.lastUsedAt ?? "",
      cell: (row) => (row.lastUsedAt ? <span className="text-xs">{row.lastUsedAt}</span> : pendingBadge(fieldsPendingHint("最近使用"))),
      headerTitle: fieldsPendingHint("最近使用"),
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
    {
      id: "proxy",
      header: "代理",
      defaultHidden: true,
      cell: (row) => (row.proxy ? <span className="text-xs">{row.proxy}</span> : pendingBadge(fieldsPendingHint("代理"))),
      headerTitle: fieldsPendingHint("代理"),
    },
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
    {
      id: "createdAt",
      header: "创建时间",
      defaultHidden: true,
      value: (row) => row.createdAt ?? "",
      cell: (row) => (row.createdAt ? <span className="text-xs">{row.createdAt}</span> : pendingBadge(fieldsPendingHint("创建时间"))),
      headerTitle: fieldsPendingHint("创建时间"),
    },
    {
      id: "expiresAt",
      header: "过期时间",
      defaultHidden: true,
      value: (row) => row.expiresAt ?? "",
      cell: (row) => (row.expiresAt ? <span className="text-xs">{row.expiresAt}</span> : pendingBadge(fieldsPendingHint("过期时间"))),
      headerTitle: fieldsPendingHint("过期时间"),
    },
    {
      id: "upstreamContact",
      header: "上游名称 / 联系人",
      defaultHidden: true,
      value: (row) => (row.binding ? row.vendor ?? boundAccount(row)?.upstream_name ?? "" : "未映射"),
      cell: (row) => {
        const account = boundAccount(row);
        const vendor = row.vendor ?? account?.upstream_name;
        if (!account && !vendor) {
          return <span className="text-xs text-fg-muted">未接入</span>;
        }
        return (
          <div className="min-w-28 text-xs">
            <span className="font-medium">{vendor || "未命名上游"}</span>
            {account?.upstream_contact ? <p className="text-fg-muted">{account.upstream_contact}</p> : null}
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

function PlatformTypeCell({ row, account }: { row: PlatformChannelRow; account: UpstreamAccountItem | undefined }) {
  if (!row.binding) {
    return (
      <div className="min-w-28">
        <Badge tone={row.candidate.state === "conflict" ? "danger" : "neutral"}>未映射</Badge>
        {row.candidate.upstreamAccountIds.length > 0 ? (
          <p className="mt-1 text-xs text-fg-muted">候选 {row.candidate.upstreamAccountIds.length} 个，详情页确认</p>
        ) : null}
      </div>
    );
  }
  const vendor = row.vendor ?? account?.upstream_name;
  const type = typeLabelFor(row, account);
  return (
    <div className="min-w-28">
      <span className="block text-xs font-medium">{vendor || "未接入"}</span>
      <Badge tone="neutral">{type}</Badge>
      {!account ? (
        <p className="mt-1 text-xs text-fg-muted" title="已绑定但登记簿里找不到这个上游账号 id">
          账号 {row.binding.upstreamAccountId}
        </p>
      ) : null}
    </div>
  );
}

const SCHEDULING_WRITE_HINT = "调度开关待 XM-SCHED0 Action";

/** 禁用态的开关外观——ui-primitives 今天没有现成的 Toggle/Switch 组件，这里
 *  用既有的 token 化工具类（边框/背景/圆角都取自设计令牌，没有硬编码颜色）
 *  画一个纯展示用的假开关，不是新增一个可复用组件（只在这一格用，不值得
 *  为它去走 Storybook-first 流程）。`aria-disabled` + 不挂 onClick/tabIndex,
 *  读屏器会正确报成"已禁用的开关"，不会被当成可操作控件。 */
function DisabledToggle({ checked }: { checked: boolean }) {
  return (
    <span
      role="switch"
      aria-checked={checked}
      aria-disabled="true"
      className={cx(
        "inline-flex h-4 w-7 shrink-0 items-center rounded-full border border-edge-strong transition-colors",
        checked ? "bg-accent/40" : "bg-surface-muted",
      )}
    >
      <span
        className={cx(
          "block h-3 w-3 rounded-full bg-surface shadow-sm transition-transform",
          checked ? "translate-x-3.5" : "translate-x-0.5",
        )}
      />
    </span>
  );
}

function SchedulingCell({ row }: { row: PlatformChannelRow }) {
  if (!row.scheduling) {
    return (
      <div className="flex items-center gap-1.5" title={fieldsPendingHint("调度") + "；" + SCHEDULING_WRITE_HINT}>
        <DisabledToggle checked={false} />
        <span className="text-xs text-fg-muted">未接入</span>
      </div>
    );
  }
  return (
    <div className="flex items-center gap-1.5" title={SCHEDULING_WRITE_HINT}>
      <DisabledToggle checked={row.scheduling.enabled} />
      <span className="text-xs text-fg-muted">优先级 {row.scheduling.priority}</span>
    </div>
  );
}

function TodayStatsCell({ row }: { row: PlatformChannelRow }) {
  if (!row.today) return pendingBadge(fieldsPendingHint("今日统计（请求数 · 成功率 · 消耗）"));
  const cost = formatScaledMinorUnits(row.today.costMinor, row.today.currency, row.today.scale);
  return (
    <span className="text-xs">
      {row.today.requests} 次 · {row.today.successRate.toFixed(1)}% · {cost}
    </span>
  );
}

function UsageWindowCell({ row, account }: { row: PlatformChannelRow; account: UpstreamAccountItem | undefined }) {
  const type = typeLabelFor(row, account);
  if (type === "上游渠道") {
    return (
      <span className="text-xs text-fg-muted" title="上游渠道没有用量窗口这个概念">
        不适用
      </span>
    );
  }
  if (!row.usageWindow) return pendingBadge(fieldsPendingHint("用量窗口"));
  const pct = Math.round(row.usageWindow.usedRatio * 100);
  return (
    <span className="text-xs">
      {pct}%{row.usageWindow.resetsAt ? ` · 重置于 ${row.usageWindow.resetsAt}` : ""}
    </span>
  );
}

function RateCell({ row, account }: { row: PlatformChannelRow; account: UpstreamAccountItem | undefined }) {
  const rate = row.rateMultiplier ?? account?.group_rate;
  const upstreamRate = row.upstreamMultiplier ?? account?.recharge_ratio;
  if (!rate && !upstreamRate) {
    return (
      <span className="text-xs text-fg-muted" title={row.binding ? "这个上游账号没有登记倍率" : "渠道还没有绑定上游账号"}>
        未接入
      </span>
    );
  }
  return (
    <span className="text-xs">
      {rate ? `${rate}×` : "—"}
      {upstreamRate ? ` / ${upstreamRate}×` : ""}
    </span>
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
