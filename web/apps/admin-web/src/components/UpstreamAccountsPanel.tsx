import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  DataTableV2,
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
} from "../api/finance";
import { formatScaledMinorUnits } from "../lib/money";
import { coveredCount, describeMissingTotal, sumMoney } from "../lib/upstreamTotals";
import { ActionResultNote, type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";
import { RechargeRatioDialog } from "./RechargeRatioDialog";
import { UpstreamAccountDialog } from "./UpstreamAccountDialog";
import { UpstreamAccountDetail } from "./UpstreamAccountDetail";

/** 上游管理(交接文档 §9.6、原型 `V["s2/suppliers"]`)。
 *
 *  **按上游供应商/实例汇总，不等同于渠道管理**：渠道管理一行是一个账号/一把 Key,
 *  这里一行是一个**上游账号**（登记簿里的 upstream_account），展开后是它下面的
 *  令牌映射、订阅批次与代理资产。
 *
 *  数据源是 XM-0037a 建的成本登记簿(`GET /api/v1/finance/upstream-accounts`),
 *  写路径全部走 Action(宪法 2 条)——这一页是登记簿的 UI，不是第二份存储。
 *
 *  §9.6 要求的字段里有几样**后端今天给不出**(上游名称、联系人、上游分组、
 *  上游总余额、本期消耗/利润)。它们在页面上显示为「未接入」并写明被什么挡着,
 *  不编：一个填着假联系人的登记簿比没有联系人这一列更糟。 */
export function UpstreamAccountsPanel({ platform }: { platform: string }) {
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
  const costTotal = sumMoney(mine.map((s) => s?.supplyCost ?? null));
  const profitTotal = sumMoney(mine.map((s) => s?.grossProfit ?? null));
  const covered = coveredCount(mine.map((s) => s?.supplyCost ?? null));
  // 观测时刻取窗口里最旧的那个：金额可以显示，但不能不说它是什么时候的
  // （§9.1 数据新鲜度必须可见）
  const observedAt = oldestObserved(mine);

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
          凭据只显示引用与状态，平台永不持有明文。
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
              label="令牌映射"
              value={String(rows.reduce((n, a) => n + a.token_mappings.length, 0))}
              note="成本侧键 ↔ 收入侧键的对账映射"
            />
            {/* 合计不出来时显示「—」而不是 ¥0.00：
                「这个窗口还没有可用的汇总数」与「这期没花钱」是两件事 */}
            <MoneyTile
              label="本期我方消耗"
              total={costTotal}
              covered={covered}
              rowCount={rows.length}
              observedAt={observedAt}
              pending={summaryQuery.isPending}
              failed={Boolean(summaryQuery.error)}
            />
            <MoneyTile
              label="本期整体毛利"
              total={profitTotal}
              covered={covered}
              rowCount={rows.length}
              observedAt={observedAt}
              pending={summaryQuery.isPending}
              failed={Boolean(summaryQuery.error)}
            />
          </div>

          <MissingFieldsNote />

          <DataTableV2
            caption="上游账号登记簿：接入方式、充值倍率、凭据状态与令牌映射"
            columns={upstreamColumns(afterWrite, summaries)}
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

/** 窗口里最旧的观测时刻。
 *
 *  取**最旧**而不是最新：一格合计里只要有一条是三小时前的，这个数就只有
 *  三小时前那么新。取最新会让整格看起来比实际更可信（§9.1）。 */
function oldestObserved(summaries: readonly (UpstreamSummary | undefined)[]): string | null {
  let oldest: string | null = null;
  for (const s of summaries) {
    const at = s?.observed.costObservedAt;
    if (!at) continue;
    // RFC 3339 UTC 串按字典序比较就是按时间比较，不必解析成 Date
    if (oldest === null || at < oldest) oldest = at;
  }
  return oldest;
}

/** 一格金额合计。算得出来就显示，算不出来说清是哪一种「算不出来」。 */
function MoneyTile({
  label,
  total,
  covered,
  rowCount,
  observedAt,
  pending,
  failed,
}: {
  label: string;
  total: ReturnType<typeof sumMoney>;
  covered: number;
  rowCount: number;
  observedAt: string | null;
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
  return (
    <StatTile
      label={label}
      value={formatScaledMinorUnits(total.total.toString(), total.currency, total.scale)}
      note={`${coverageNote}${observedAt ? ` · 成本观测于 ${observedAt}` : ""}`}
      {...(covered < rowCount ? { status: <Badge tone="warning">覆盖不全</Badge> } : {})}
    />
  );
}

/** §9.6 要求、但后端今天给不出的那几样。
 *
 *  单独说出来而不是各列显示「—」：五样都缺的时候，五个「—」看着像数据没加载完;
 *  一句话把它们归到一起，并说明各自被什么挡着。 */
function MissingFieldsNote() {
  return (
    <p className="rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted">
      交接文档 §9.6 还要求上游名称、联系人、上游分组（含未接入分组）与上游总余额。
      登记簿（XM-0037a）今天没有前三个字段，要扩表结构；总余额与可用天数在
      XM-0037d 的上游汇总里已经有了，但那是**另一格**的内容（余额与可用天数），
      本片只接了本期消耗与毛利。在其余字段到位之前这一页不编——一个填着假联系人的登记簿，
      比没有联系人这一列更糟。
    </p>
  );
}

function upstreamColumns(
  onDone: (result: ActionResult) => void,
  summaries: Map<string, UpstreamSummary>,
): DataTableColumn<UpstreamAccountItem>[] {
  return [
    {
      id: "upstream",
      header: "上游 / 网址",
      primary: true,
      value: (a) => `${a.base_url} ${a.system_type} ${a.id}`,
      cell: (a) => (
        <>
          <span className="font-medium">{hostOf(a.base_url)}</span>
          <p className="font-mono text-xs break-all text-fg-muted">
            {a.base_url}
            {a.system_type ? ` · ${a.system_type}` : null}
          </p>
        </>
      ),
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
      id: "mappings",
      header: "令牌映射",
      numeric: true,
      value: (a) => a.token_mappings.length,
      cell: (a) => a.token_mappings.length,
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
      // 排序按供给成本。汇总还没到手时不给 value——按一列全是「—」的东西
      // 排序只会让人以为排序坏了
      ...(summaries.size > 0
        ? { value: (a: UpstreamAccountItem) => moneyValue(summaries.get(a.id)?.supplyCost) }
        : {}),
      cell: (a) => <PeriodCell summary={summaries.get(a.id)} />,
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

/** 本期消耗 / 毛利单元格。
 *
 *  金额的 `null` 是**「给不出」不是 0**（覆盖不全或币种混杂，XM-0037d 的纪律）,
 *  所以这里不折成 ¥0.00——那会被读成「这个上游这期没花钱」。 */
function PeriodCell({ summary }: { summary: UpstreamSummary | undefined }) {
  if (!summary) {
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

/** 从 base_url 里取主机名当显示名。
 *
 *  登记簿没有「上游名称」字段（§9.6 要求但表里没有），拿主机名当名字是**能从
 *  现有数据推出来**的最接近的东西；编一个好听的中文名会骗人。 */
function hostOf(baseURL: string): string {
  try {
    return new URL(baseURL).host;
  } catch {
    return baseURL || "—";
  }
}
