import { useQuery } from "@tanstack/react-query";
import {
  DataTableV2,
  FreshnessBadge,
  FreshnessNote,
  PageState,
  StatTile,
  formatUtcTimestamp,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import { useState } from "react";
import {
  describeMaskedEmail,
  describeUserStatus,
  listPlatformUsers,
  type AmountBody,
  type PlatformUserItem,
  type PlatformUserSort,
} from "../api/users";
import { formatMinorUnits, toIntegerValue } from "../lib/money";
import { ApiStateView } from "./ApiStateView";

/** 一页拉多少条。与后端 `platformusers.DefaultLimit` 一致。 */
const PAGE_SIZE = 50;

/** 金额的展示：缺席显示「—」，已知的 0 显示「¥0.00」。
 *
 *  两者不能压成一个：一个「上游没给充值额」的用户显示成 ¥0.00,
 *  会被当成「这个月一分钱没充」——而事实是我们不知道。 */
function amountText(a: AmountBody): string {
  if (a.minor_units === null) return "—";
  return formatMinorUnits(toIntegerValue(a.minor_units), a.currency);
}

/** 排序值：缺席的一律给 null，由 DataTableV2 排到最后。 */
function amountSortValue(a: AmountBody): bigint | null {
  return a.minor_units === null ? null : toIntegerValue(a.minor_units);
}

const USER_COLUMNS: DataTableColumn<PlatformUserItem>[] = [
  {
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
  },
  {
    id: "email",
    header: "联系方式",
    value: (u) => u.email_masked,
    cell: (u) => {
      const shown = describeMaskedEmail(u.email_masked);
      return (
        <span className={u.email_masked ? "font-mono text-xs" : "text-fg-muted"} title={shown.hint}>
          {shown.text}
        </span>
      );
    },
  },
  {
    id: "balance",
    header: "可用余额",
    numeric: true,
    // 排序用最小单位的整数，不用格式化后的字符串
    value: (u) => amountSortValue(u.balance),
    cell: (u) => amountText(u.balance),
  },
  {
    id: "recharge",
    header: "区间充值",
    numeric: true,
    value: (u) => amountSortValue(u.period_recharge),
    cell: (u) => (
      <span className={u.period_recharge.minor_units === null ? "text-fg-muted" : undefined}>
        {amountText(u.period_recharge)}
      </span>
    ),
  },
  {
    id: "consumed",
    header: "区间消费",
    numeric: true,
    value: (u) => amountSortValue(u.period_consumed),
    cell: (u) => (
      <span className={u.period_consumed.minor_units === null ? "text-fg-muted" : undefined}>
        {amountText(u.period_consumed)}
      </span>
    ),
  },
  {
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
  },
  {
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
  },
];

/** 用户管理页签(交接文档 §9.3、原型 `V["s2/users"]`)。
 *
 *  顶部三格 + 一张表。原型顶部是四格(用户总数 / 所有用户总余额 / 区间充值 /
 *  区间消费)，后两格要**逐用户流水**才算得出来，而 v1 只读契约给不出——
 *  所以它们显示「未接入」而不是 0（原型自己的 warnbar 就是说这件事）。
 *
 *  金额全程整数：后端给的是十进制字符串，这里用 BigInt 解析（宪法 13 条）。 */
export function PlatformUsersPanel({ platform }: { platform: string }) {
  const [sort, setSort] = useState<PlatformUserSort>("balance_desc");

  const query = useQuery({
    queryKey: ["platform-users", platform, { sort }],
    queryFn: ({ signal }) =>
      listPlatformUsers(platform, { signal, sort, limit: PAGE_SIZE }),
  });
  const page = query.data;

  return (
    <section className="flex flex-col gap-3">
      {/* 原型逐字的那句 warnbar：这一页的边界由**上游契约**决定，不是我们没做 */}
      <p
        role="status"
        className="rounded-md border border-warning bg-warning/15 px-3 py-2 text-xs text-fg"
      >
        当前只读契约 v1 仅提供用户总数与总余额；逐用户今日充值、今日消费和消费明细是目标界面，
        接真实数据前需扩展 read contract v2。表里标着「—」的那几列就是这个原因，
        <strong>不是这些用户没有充值</strong>。
      </p>

      <div className="flex items-start justify-between gap-3">
        <p className="text-xs text-fg-muted">
          先看全体用户资金与消费，再进入单个用户查看消费、充值、开票与 API Key 明细。
          邮箱已在服务端打码，平台不持有明文。
        </p>
        {page ? <FreshnessBadge freshness={page.freshness} /> : null}
      </div>

      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        {page === undefined ? null : (
          <div className="flex flex-col gap-3">
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
              <StatTile
                label="用户总数"
                value={page.total_count.value === null ? "—" : String(page.total_count.value)}
                unavailable={page.total_count.value === null}
                note={
                  page.total_count.value === null
                    ? "上游只给了这一页，没给总数"
                    : "符合当前筛选条件的用户数"
                }
              />
              <StatTile
                label="所有用户总余额"
                value={amountText(page.total_balance)}
                unavailable={page.total_balance.minor_units === null}
                note="含可用余额，不含上游余额"
              />
              {/* 这两格要逐用户流水才算得出来，而 v1 契约给不出。
                  显示 0 会被读成「这个区间没人充值」 */}
              <StatTile
                label="区间充值"
                value="—"
                unavailable
                note="需扩展 read contract v2 才能逐用户汇总"
                status={<Badge tone="neutral">未接入</Badge>}
              />
              <StatTile
                label="区间消费"
                value="—"
                unavailable
                note="需扩展 read contract v2 才能逐用户汇总"
                status={<Badge tone="neutral">未接入</Badge>}
              />
            </div>

            <div className="rounded-lg border border-edge bg-surface p-3">
              <FreshnessNote freshness={page.freshness} />
              <p className="text-xs text-fg-muted">
                来源 {page.data_source || "—"} · 本页 {page.items.length} 条
                {page.next_cursor ? " · 还有更多（排序在服务端，翻页随第 6 片补齐）" : null}
              </p>
            </div>

            <UserSortPicker value={sort} onChange={setSort} />

            <DataTableV2
              caption="终端用户：余额、区间充值与消费、状态与最后活跃"
              columns={USER_COLUMNS}
              rows={page.items}
              rowKey={(u) => u.id}
              searchable
              filters={[{ columnId: "status", label: "状态", options: ["正常", "受限", "停用", "未知"] }]}
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
