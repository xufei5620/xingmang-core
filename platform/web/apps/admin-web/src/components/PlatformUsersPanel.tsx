import { useInfiniteQuery } from "@tanstack/react-query";
import {
  DataTableV2,
  FreshnessBadge,
  FreshnessNote,
  PageState,
  PeriodControls,
  StatTile,
  formatUtcTimestamp,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { Link, useSearchParams } from "react-router";
import { FeatureNotMountedError } from "../api/client";
import {
  describeMaskedEmail,
  describeUserStatus,
  encodePlatformUserIdSegment,
  listPlatformUsers,
  type AmountBody,
  type CountBody,
  type PeriodGranularity,
  type PlatformUserItem,
  type PlatformUserPage,
  type PlatformUserSort,
  type PlatformUserStatus,
} from "../api/users";
import { appDemoDataConfig, shouldShowDemoBanner } from "../lib/demoData";
import { formatMinorUnits, toIntegerValue } from "../lib/money";
import { describeCoverage, parseBusinessDay, parseGranularity } from "../lib/period";
import { ApiStateView } from "./ApiStateView";

/** 一页拉多少条。与后端 `platformusers.DefaultLimit` 一致。 */
const PAGE_SIZE = 50;

/** 金额的展示：缺席显示「—」，已知的 0 显示「¥0.00」。
 *
 *  两者不能压成一个：一个「上游没给充值额」的用户显示成 ¥0.00,
 *  会被当成「这个月一分钱没充」——而事实是我们不知道。 */
function amountText(a: AmountBody | undefined): string {
  if (a === undefined || a.minor_units === null) return "—";
  return formatMinorUnits(toIntegerValue(a.minor_units), a.currency);
}

/** 排序值：缺席的一律给 null，由 DataTableV2 排到最后。 */
function amountSortValue(a: AmountBody | undefined): bigint | null {
  return a === undefined || a.minor_units === null ? null : toIntegerValue(a.minor_units);
}

/** 计数的展示。null = 上游没给，不是 0。 */
function countText(c: CountBody | undefined): string {
  return c === undefined || c.value === null ? "—" : String(c.value);
}

/** 金额单元格：缺席时弱化，避免「—」看着像一个读数。 */
function amountCell(a: AmountBody | undefined) {
  return (
    <span className={a === undefined || a.minor_units === null ? "text-fg-muted" : undefined}>
      {amountText(a)}
    </span>
  );
}

function amountColumn(
  id: string,
  header: string,
  pick: (u: PlatformUserItem) => AmountBody,
): DataTableColumn<PlatformUserItem> {
  return {
    id,
    header,
    numeric: true,
    // 排序用最小单位的整数，不用格式化后的字符串
    value: (u) => amountSortValue(pick(u)),
    cell: (u) => amountCell(pick(u)),
  };
}

/** 原型逐格的列。两个平台的差异只在中间几列（见 PLATFORM_VIEWS）。 */
const COLUMN_USER: DataTableColumn<PlatformUserItem> = {
  id: "user",
  header: "用户",
  primary: true,
  value: (u) => `${u.username} ${u.id} ${u.token_prefix}`,
  cell: (u) => (
    <>
      <span className="font-medium">{u.username || u.id}</span>
      <p className="font-mono text-xs text-fg-muted">
        {u.id}
        {u.token_prefix ? ` · ${u.token_prefix}` : null}
      </p>
    </>
  ),
};

const COLUMN_EMAIL: DataTableColumn<PlatformUserItem> = {
  id: "email",
  // 原型的表头就是「邮箱」。脱敏这件事说在下面那句说明里，不占表头
  header: "邮箱",
  headerTitle: "邮箱已在服务端打码，平台不持有明文",
  value: (u) => u.email_masked,
  cell: (u) => {
    const shown = describeMaskedEmail(u.email_masked);
    return (
      <span className={u.email_masked ? "font-mono text-xs" : "text-fg-muted"} title={shown.hint}>
        {shown.text}
      </span>
    );
  },
};

const COLUMN_STATUS: DataTableColumn<PlatformUserItem> = {
  id: "status",
  header: "状态",
  value: (u) => describeUserStatus(u.status).label,
  cell: (u) => {
    const shown = describeUserStatus(u.status);
    return (
      <Badge tone={shown.tone} title={shown.hint}>
        {shown.label}
      </Badge>
    );
  },
};

const COLUMN_LAST_ACTIVE: DataTableColumn<PlatformUserItem> = {
  id: "lastActive",
  header: "最后活跃",
  // 从未活跃排最后，不当成「很久以前」
  value: (u) => u.last_active_at,
  cell: (u) =>
    u.last_active_at === null ? (
      <span className="text-fg-muted" title="上游没有记录过这个账号的活跃时间">
        从未活跃
      </span>
    ) : (
      <span className="text-xs tabular-nums">{formatUtcTimestamp(u.last_active_at)}</span>
    ),
};

/** 行尾的详情深链（原型 `chevtd`）。
 *
 *  只让箭头这一格成为语义 Link，不给 `tr` 加 onClick：键盘与读屏会遇到一个
 *  有明确名称的导航目标，复制链接也得到可分享的完整详情地址。用户 ID 始终按
 *  不透明字符串编码成**一个** URL 段，斜杠与查询符号不能改变路由结构。 */
function detailColumn(platform: string): DataTableColumn<PlatformUserItem> {
  return {
    id: "detail",
    header: "",
    headerTitle: "打开用户详情",
    cell: (user) => {
      const label = user.username || user.id;
      const to = `/platforms/${encodeURIComponent(platform)}/users/${encodePlatformUserIdSegment(user.id)}`;
      return (
        <Link
          to={to}
          aria-label={`查看 ${label} 的用户详情`}
          title="打开完整用户详情页"
          className="inline-flex size-9 items-center justify-center rounded-md text-lg text-accent hover:bg-accent-soft focus-visible:outline-2 focus-visible:outline-accent"
        >
          <span aria-hidden="true">›</span>
        </Link>
      );
    },
  };
}

/** 每个平台照自己的原型来。
 *
 *  两页在原型里**不是同一张表**：Sub2API 那页有「区间充值」列与「所有用户
 *  总余额」格，NewAPI 那页没有充值、第四格换成「需关注」。做成一份「并集」
 *  的话，NewAPI 页会多出一列永远是「—」的充值——那看起来像上游坏了，
 *  而事实是它的原型压根没要这一列。 */
interface PlatformUsersView {
  /** 顶部说明（原型 `phead` 的副标题）。 */
  intro: string;
  columns: DataTableColumn<PlatformUserItem>[];
  /** 顶部四格。`items` 是**目前已加载的全部页**（不只是最后一页），
   *  给需要诚实标注范围的格（如「需关注」）用。 */
  tiles: (page: PlatformUserPage, items: PlatformUserItem[]) => React.ReactNode;
}

const COLUMN_PERIOD_RECHARGE = amountColumn("recharge", "区间充值", (u) => u.period_recharge);
const COLUMN_PERIOD_CONSUMED = amountColumn("consumed", "区间消费", (u) => u.period_consumed);
const COLUMN_LAST_30D = amountColumn("last30d", "近30天消费", (u) => u.last_30d_consumed);
const COLUMN_BALANCE = amountColumn("balance", "可用余额", (u) => u.balance);
const COLUMN_BALANCE_PLAIN = amountColumn("balance", "余额", (u) => u.balance);

/** 区间合计格。**带覆盖率**，这是这一页最要紧的一处诚实。
 *
 *  合计只加得动「上游给得出流水」的那些用户。覆盖不全时它是一个**下界**，
 *  副行必须说出来——否则它和一个真的合计长得一模一样（宪法 12 条）。 */
function PeriodTotalTile({
  label,
  amount,
  page,
}: {
  label: string;
  amount: AmountBody;
  page: PlatformUserPage;
}) {
  const totals = page.period_totals;
  const unavailable = amount.minor_units === null;
  return (
    <StatTile
      label={label}
      value={amountText(amount)}
      unavailable={unavailable}
      note={
        unavailable
          ? "本区间没有任何用户的流水是上游给得出的"
          : describeCoverage(totals.covered_users, totals.total_users, totals.complete)
      }
      status={
        unavailable || totals.complete ? undefined : <Badge tone="warning">合计不全</Badge>
      }
    />
  );
}

const SUB2API_VIEW: PlatformUsersView = {
  intro: "先看全体用户资金与消费，再进入单个用户查看消费、充值、开票与 API Key 明细。",
  columns: [
    COLUMN_USER,
    COLUMN_EMAIL,
    COLUMN_BALANCE,
    COLUMN_PERIOD_RECHARGE,
    COLUMN_PERIOD_CONSUMED,
    COLUMN_LAST_30D,
    COLUMN_STATUS,
    COLUMN_LAST_ACTIVE,
  ],
  tiles: (page) => (
    <>
      <StatTile
        label="用户总数"
        value={countText(page.total_count)}
        unavailable={page.total_count.value === null}
        // 原型的副行就是「今日活跃 311」
        note={
          page.active_today.value === null
            ? "上游没给今日活跃数"
            : `今日活跃 ${page.active_today.value}`
        }
      />
      <StatTile
        label="所有用户总余额"
        value={amountText(page.total_balance)}
        unavailable={page.total_balance.minor_units === null}
        note="含可用余额，不含上游余额"
      />
      <PeriodTotalTile label="区间充值" amount={page.period_totals.recharge} page={page} />
      <PeriodTotalTile label="区间消费（计费额）" amount={page.period_totals.consumed} page={page} />
    </>
  ),
};

const NEWAPI_VIEW: PlatformUsersView = {
  intro: "NewAPI 终端用户；可按指定日期、日、周、月查看余额与消费。",
  columns: [
    COLUMN_USER,
    COLUMN_EMAIL,
    COLUMN_BALANCE_PLAIN,
    COLUMN_PERIOD_CONSUMED,
    COLUMN_LAST_30D,
    COLUMN_STATUS,
    COLUMN_LAST_ACTIVE,
  ],
  tiles: (page, items) => (
    <>
      <StatTile
        label="用户总数"
        value={countText(page.total_count)}
        unavailable={page.total_count.value === null}
        note={
          page.active_today.value === null
            ? "上游没给今日活跃数"
            : `今日活跃 ${page.active_today.value}`
        }
      />
      <StatTile
        label="总余额"
        value={amountText(page.total_balance)}
        unavailable={page.total_balance.minor_units === null}
        note="含可用余额，不含上游余额"
      />
      <PeriodTotalTile label="区间消费" amount={page.period_totals.consumed} page={page} />
      <NeedsAttentionTile items={items} />
    </>
  ),
};

/** 「需关注」= 目前已加载的行里状态不是「正常」的用户数（NewAPI 原型第四格）。
 *
 *  副行必须说清它数的是**已加载的行**：服务端只按状态筛，没有给「全库有多少条
 *  需关注」这个数；点了几次「加载更多」，这个范围就跟着变，不能钉死在第一页
 *  50 条上——那样翻了页之后这句话就会开始撒谎。写成一个不带范围的「2」，
 *  会被读成全平台只有两个问题账号。 */
function NeedsAttentionTile({ items }: { items: PlatformUserItem[] }) {
  const count = items.filter((u) => u.status !== "active").length;
  return (
    <StatTile
      label="需关注"
      value={String(count)}
      note={`已加载 ${items.length} 条里状态不是「正常」的（上游未提供全库计数）`}
    />
  );
}

function viewFor(platform: string): PlatformUsersView {
  return platform === "newapi" ? NEWAPI_VIEW : SUB2API_VIEW;
}

/** 用户管理页签（交接文档 §9.3、原型 `V["s2/users"]` 与 `V["newapi/users"]`）。
 *
 *  ## 区间状态放 URL
 *
 *  筛选条件进 SearchParams 是本仓的既定纪律（交接文档 §8）：一次筛出来的
 *  结果要能贴给同事。日期与粒度都在 URL 里，游标不在。
 *
 *  ## 「今天」不由前端决定
 *
 *  日期为空时**不传** day，交给服务端按 CST +08:00 解释（宪法 14 条）。
 *  前端拿浏览器本地日期去填，在 UTC-5 的机器上会填成账面上的昨天。
 *
 *  金额全程整数：后端给的是十进制字符串，这里用 BigInt 解析（宪法 13 条）。 */
export function PlatformUsersPanel({ platform }: { platform: string }) {
  const [searchParams, setSearchParams] = useSearchParams();
  const day = parseBusinessDay(searchParams.get("day"));
  const granularity = parseGranularity(searchParams.get("granularity"));
  const sort = (searchParams.get("sort") ?? "balance_desc") as PlatformUserSort;
  // 服务端筛选（XM-USERS-HONEST0）：q / status 进 URL、随请求下发、进查询键。
  // 后端本来就接受这两个参数（listPlatformUsers 已拼进 searchParams），此前
  // 面板只筛已加载的那一页，让 next_cursor、总数和统计全部失真。
  const q = (searchParams.get("q") ?? "").trim();
  const status = parseUserStatusFilter(searchParams.get("status"));
  const view = viewFor(platform);

  const setParam = (key: string, value: string) => {
    const next = new URLSearchParams(searchParams);
    if (value.trim() === "") next.delete(key);
    else next.set(key, value);
    setSearchParams(next, { replace: true });
  };

  const query = useInfiniteQuery({
    queryKey: ["platform-users", platform, { sort, day, granularity, q, status }],
    queryFn: ({ pageParam, signal }) =>
      listPlatformUsers(platform, {
        signal,
        sort,
        ...(q ? { q } : {}),
        ...(status ? { status } : {}),
        limit: PAGE_SIZE,
        // 空串不传：让服务端解释「今天」
        ...(day ? { day } : {}),
        granularity,
        ...(pageParam ? { cursor: pageParam } : {}),
      }),
    initialPageParam: "",
    getNextPageParam: (lastPage) => lastPage.next_cursor || undefined,
    // 与 main.tsx 的全局默认一致地显式声明一遍：翻回这个页签命中缓存直接
    // 展示已加载的数据，不在新鲜期内重新刷屏；多页缓存条目更容易被将来的
    // 改动无意间改掉，所以这里不依赖隐式继承。
    staleTime: 15_000,
  });
  const pages = query.data?.pages ?? [];
  // 顶部四格、来源、新鲜度都是「全体用户」口径的聚合（后端注释：TotalBalance/
  // ActiveToday 本来就要求全量，不随游标变化），取最后一次成功响应即可；
  // 逐行表格才需要把已加载的每一页拼起来。
  const page = pages.length > 0 ? pages[pages.length - 1] : undefined;
  const items = pages.flatMap((p) => p.items);
  // 未挂载时下面渲染的是「未接入」空状态，不是样本表格：这句 warnbar 明说
  // 「下面的逐用户流水来自样本数据源」，在未接入场景下继续显示等于把「没接」
  // 说成「接了但是假的」——两者对运营是完全不同的下一步
  const notMounted = query.error instanceof FeatureNotMountedError;
  // 「来自样本数据源」这半句只有在数据源真是演示源时才成立：真实模式下三列
  // 显示「—」正是契约边界本身，不是样本。判据与详情页同一个（宪法 12 条）。
  const demo = page !== undefined && shouldShowDemoBanner([page.data_source], appDemoDataConfig);

  return (
    <section className="flex flex-col gap-3">
      {/* 原型逐字的那句 warnbar。它说的是**真实上游契约**的边界：
          当前 fake 样本能供出逐用户流水，真实 v1 端点还不能 */}
      {notMounted ? null : (
        <p
          role="status"
          className="rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg"
        >
          当前只读契约 v1 仅提供用户总数与总余额；逐用户今日充值、今日消费和消费明细是目标界面，
          接真实数据前需扩展 read contract v2。
          {demo ? (
            <>
              <strong> 下面的逐用户流水来自样本数据源</strong>
              ，接上真实上游后这几列会退回「—」，直到 v2 契约落地。
            </>
          ) : (
            <> 真实上游下这几列显示「—」是契约边界，不是数据缺失。</>
          )}
        </p>
      )}

      <div className="flex items-start justify-between gap-3">
        <p className="text-xs text-fg-muted">
          {view.intro} 邮箱已在服务端打码，平台不持有明文。
        </p>
        {page ? <FreshnessBadge freshness={page.freshness} /> : null}
      </div>

      <PeriodControls
        day={day}
        granularity={granularity}
        period={page?.period}
        onDayChange={(next) => setParam("day", next)}
        onGranularityChange={(next: PeriodGranularity) => setParam("granularity", next)}
      />

      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        {page === undefined ? null : (
          <div className="flex flex-col gap-3">
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
              {view.tiles(page, items)}
            </div>

            <div className="rounded-lg border border-edge bg-surface p-3">
              <FreshnessNote freshness={page.freshness} />
              <p className="text-xs text-fg-muted">
                来源 {page.data_source || "—"} · 已加载 {items.length} 条
              </p>
            </div>

            <div className="flex flex-wrap items-end gap-3">
              <UserSortPicker value={sort} onChange={(next) => setParam("sort", next)} />
              {/* 服务端筛选控件放在表格外层：DataTableV2 空表时只渲染 emptyState，
                  筛出零条的人必须还能看到并清掉自己的筛选条件 */}
              <UserServerFilters
                q={q}
                status={status}
                onQChange={(next) => setParam("q", next)}
                onStatusChange={(next) => setParam("status", next)}
              />
            </div>

            <DataTableV2
              caption="终端用户：余额、区间充值与消费、状态与最后活跃"
              columns={[...view.columns, detailColumn(platform)]}
              rows={items}
              rowKey={(u) => u.id}
              emptyState={
                <PageState
                  kind="empty"
                  title="这个平台还没有用户"
                  description="上游用户清单里一条都没有；也可能这条链路刚接上，还没同步过。"
                />
              }
              footerExtra={
                query.hasNextPage ? (
                  <button
                    type="button"
                    onClick={() => void query.fetchNextPage()}
                    disabled={query.isFetchingNextPage}
                    className="min-h-9 rounded-md border border-edge-strong px-3 py-1 text-xs font-medium text-accent hover:bg-accent-soft disabled:cursor-not-allowed disabled:opacity-50"
                  >
                    {query.isFetchingNextPage ? "加载中…" : "加载更多"}
                  </button>
                ) : null
              }
            />
          </div>
        )}
      </ApiStateView>
    </section>
  );
}

/** URL 里的 status 只认契约值；别的值当没写（不把整页搞崩，也不静默转发）。 */
function parseUserStatusFilter(raw: string | null): PlatformUserStatus | "" {
  return raw === "active" || raw === "limited" || raw === "disabled" || raw === "unknown" ? raw : "";
}

/** 服务端筛选：搜索词与账号状态。
 *
 *  与 DataTableV2 自带的表内搜索**二选一**——表内搜索只筛已加载的那一页，
 *  两个搜索框并存时人会以为自己搜的是全部用户，所以这里关掉了表内搜索。
 *  搜索词在回车 / 失焦时才写进 URL：每敲一个字就触发一次上游全量翻页扫描
 *  不可接受（upstream.go 记录了不转发 search 的理由）。 */
function UserServerFilters({
  q,
  status,
  onQChange,
  onStatusChange,
}: {
  q: string;
  status: PlatformUserStatus | "";
  onQChange: (next: string) => void;
  onStatusChange: (next: PlatformUserStatus | "") => void;
}) {
  const statusOptions: { value: PlatformUserStatus | ""; label: string }[] = [
    { value: "", label: "全部状态" },
    { value: "active", label: describeUserStatus("active").label },
    { value: "limited", label: describeUserStatus("limited").label },
    { value: "disabled", label: describeUserStatus("disabled").label },
    { value: "unknown", label: describeUserStatus("unknown").label },
  ];
  return (
    <>
      <label className="flex items-center gap-2 text-xs text-fg-muted">
        <span>账号状态（服务端筛选）</span>
        <select
          aria-label="账号状态（服务端筛选）"
          value={status}
          onChange={(event) => onStatusChange(parseUserStatusFilter(event.target.value))}
          className="rounded-md border border-edge-strong bg-surface px-2 py-1 text-xs text-fg hover:border-accent focus:outline-2 focus:outline-accent"
        >
          {statusOptions.map((o) => (
            <option key={o.value || "all"} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
      </label>
      <label className="flex items-center gap-2 text-xs text-fg-muted">
        <span>搜索（服务端）</span>
        <input
          type="search"
          aria-label="搜索用户（服务端筛选）"
          placeholder="用户名 / 用户 ID / 令牌前缀"
          defaultValue={q}
          key={q}
          onBlur={(event) => {
            if (event.target.value.trim() !== q) onQChange(event.target.value);
          }}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault();
              onQChange((event.target as HTMLInputElement).value);
            }
          }}
          className="h-8 w-56 rounded-md border border-edge-strong bg-surface px-2 text-xs text-fg outline-none placeholder:text-fg-muted focus-visible:outline-2 focus-visible:outline-accent"
        />
      </label>
    </>
  );
}

/** 服务端排序选择器。
 *
 *  与表内排序**分开**：表内排序只排当前这一页，服务端排序决定的是「哪 50 条
 *  进到这一页来」。两者叫同一个名字会让人以为点了表头就是全局排序。 */
function UserSortPicker({
  value,
  onChange,
}: {
  value: PlatformUserSort;
  onChange: (next: PlatformUserSort) => void;
}) {
  const options: { value: PlatformUserSort; label: string }[] = [
    { value: "balance_desc", label: "余额从高到低" },
    { value: "consumed_desc", label: "区间消费从高到低" },
    { value: "last_active_desc", label: "最近活跃优先" },
    { value: "username_asc", label: "按用户名" },
  ];
  return (
    <label className="flex items-center gap-2 text-xs text-fg-muted">
      <span>服务端排序（决定哪些用户进入本页）</span>
      <select
        value={value}
        onChange={(event) => onChange(event.target.value as PlatformUserSort)}
        className="rounded-md border border-edge-strong bg-surface px-2 py-1 text-xs text-fg hover:border-accent focus:outline-2 focus:outline-accent"
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </label>
  );
}
