import { useQuery } from "@tanstack/react-query";
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
import { useSearchParams } from "react-router";
import {
  describeMaskedEmail,
  describeUserStatus,
  listPlatformUsers,
  type AmountBody,
  type CountBody,
  type PeriodGranularity,
  type PlatformUserItem,
  type PlatformUserPage,
  type PlatformUserSort,
} from "../api/users";
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

/** 行尾的详情箭头（原型 `chevtd`）。
 *
 *  **它不可点，而且必须看得出不可点。** 用户详情页（消费、充值、开票、
 *  API Key 明细）还没做；画一个能点的箭头是一句会落空的承诺，点下去要么
 *  没反应、要么进一个空页面，两种都比没有箭头更糟。
 *
 *  这一列没有 `value`，于是它既不可排序也不进搜索——DataTableV2 里
 *  「没有 value」正是这个意思。 */
const COLUMN_DETAIL_CHEVRON: DataTableColumn<PlatformUserItem> = {
  id: "detail",
  header: "",
  headerTitle: "用户详情页待上线",
  cell: () => (
    <span className="text-fg-muted" title="用户详情页待上线">
      <span aria-hidden="true">›</span>
      <span className="sr-only">详情页待上线</span>
    </span>
  ),
};

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
  /** 顶部四格。 */
  tiles: (page: PlatformUserPage) => React.ReactNode;
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
    COLUMN_DETAIL_CHEVRON,
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
    COLUMN_DETAIL_CHEVRON,
  ],
  tiles: (page) => (
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
      <NeedsAttentionTile page={page} />
    </>
  ),
};

/** 「需关注」= 本页里状态不是「正常」的用户数（NewAPI 原型第四格）。
 *
 *  副行必须说清它数的是**本页**：服务端只按状态筛，没有给「全库有多少条
 *  需关注」这个数。写成一个不带范围的「2」，会被读成全平台只有两个问题账号。 */
function NeedsAttentionTile({ page }: { page: PlatformUserPage }) {
  const count = page.items.filter((u) => u.status !== "active").length;
  return (
    <StatTile
      label="需关注"
      value={String(count)}
      note={`本页 ${page.items.length} 条里状态不是「正常」的（上游未提供全库计数）`}
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
  const view = viewFor(platform);

  const setParam = (key: string, value: string) => {
    const next = new URLSearchParams(searchParams);
    if (value.trim() === "") next.delete(key);
    else next.set(key, value);
    setSearchParams(next, { replace: true });
  };

  const query = useQuery({
    queryKey: ["platform-users", platform, { sort, day, granularity }],
    queryFn: ({ signal }) =>
      listPlatformUsers(platform, {
        signal,
        sort,
        limit: PAGE_SIZE,
        // 空串不传：让服务端解释「今天」
        ...(day ? { day } : {}),
        granularity,
      }),
  });
  const page = query.data;

  return (
    <section className="flex flex-col gap-3">
      {/* 原型逐字的那句 warnbar。它说的是**真实上游契约**的边界：
          当前 fake 样本能供出逐用户流水，真实 v1 端点还不能 */}
      <p
        role="status"
        className="rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg"
      >
        当前只读契约 v1 仅提供用户总数与总余额；逐用户今日充值、今日消费和消费明细是目标界面，
        接真实数据前需扩展 read contract v2。
        <strong> 下面的逐用户流水来自样本数据源</strong>
        ，接上真实上游后这几列会退回「—」，直到 v2 契约落地。
      </p>

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
              {view.tiles(page)}
            </div>

            <div className="rounded-lg border border-edge bg-surface p-3">
              <FreshnessNote freshness={page.freshness} />
              <p className="text-xs text-fg-muted">
                来源 {page.data_source || "—"} · 本页 {page.items.length} 条
                {page.next_cursor ? " · 还有更多（排序在服务端，翻页随第 6 片补齐）" : null}
              </p>
            </div>

            <UserSortPicker value={sort} onChange={(next) => setParam("sort", next)} />

            <DataTableV2
              caption="终端用户：余额、区间充值与消费、状态与最后活跃"
              columns={view.columns}
              rows={page.items}
              rowKey={(u) => u.id}
              searchable
              filters={[
                { columnId: "status", label: "状态", options: ["正常", "注意", "停用", "未知"] },
              ]}
              emptyState={
                <PageState
                  kind="empty"
                  title="这个平台还没有用户"
                  description="上游用户清单里一条都没有；也可能这条链路刚接上，还没同步过。"
                />
              }
            />
          </div>
        )}
      </ApiStateView>
    </section>
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
