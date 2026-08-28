import { Link } from "react-router";
import type { DataTableColumn } from "@xingmang/ui-admin";
import { Badge } from "@xingmang/ui-primitives";
import type { ChannelSummary, RunwayThresholds } from "../api/finance";
import { formatScaledMinorUnits } from "../lib/money";
import { RUNWAY_TONE, runwayNote, runwayReasonText } from "../lib/runway";
import { MarginText, MoneyCell, moneyValue, PendingCell } from "./ChannelMoneyCells";

/** 渠道管理表的列（原型 `V["s2/upstream"]` / `V["newapi/upstream"]` 的表头）。
 *
 *  两个平台共用一份列定义。原型给它们画了几乎相同的表，差别只有主列的名字
 *  与 Sub2API 多一列「成功率」；把差异做成参数而不是两份文件，
 *  是因为经营口径不该因为看的是哪个平台而不同——
 *  这条理由与 XM-0048 让两个平台共用经营列时写的是同一条。
 *
 *  ⚠️ **一行 = 一个上游账号**，不是被管平台自己的一条渠道（见 `channelTable.ts`）。
 *  原型的行粒度是后者，但两边的 id 互不认识，按 id join 一条都对不上。
 *  改粒度换来的是这张表上的钱是真的；代价写在 `ChannelScopeNote` 里。 */

export type ChannelPlatform = "sub2api" | "newapi";

/** 原型里有、我们今天给不出的两列。
 *
 *  **保留列而不是删掉**，是产品的要求（逐格对齐原型），也确实有用：
 *  一个空着并写明「什么上线才有」的格子，比一列凭空消失更能说明进度。
 *  但**绝不能显示 0 或「正常」**——那会被读成已经接上了。 */
const MODELS_PENDING = "可用模型清单要渠道保障（M1.5）上线后才有：今天没有任何一个数据源在回答「这条上游支持哪些模型、验证过几个」";
const SUCCESS_RATE_PENDING = "24h 成功率要渠道保障（M1.5）上线后才有：请求成功率今天不在成本线的采集范围里";

export interface ChannelColumnOptions {
  platform: ChannelPlatform;
  /** 可用天数的三档阈值，跟着上游汇总端点一起下来。给不出时不画档位说明。 */
  thresholds: RunwayThresholds | null;
}

export function channelTableColumns({
  platform,
  thresholds,
}: ChannelColumnOptions): DataTableColumn<ChannelSummary>[] {
  const columns: DataTableColumn<ChannelSummary>[] = [
    {
      id: "account",
      header: platform === "sub2api" ? "Sub2API 账号" : "NewAPI 渠道",
      primary: true,
      value: (row) => row.name || row.id,
      cell: (row) => (
        <>
          <span className="font-medium">{row.name || "—"}</span>
          {/* id 是 UUID，整串铺开会把首列撑成四行；截断 + title 保留全值。
              **不省略成 8 位就完事**——运维要用它去 grep 日志，
              悬停能拿到完整的那一串才算数 */}
          <span
            className="block max-w-[14ch] truncate font-mono text-xs text-fg-muted"
            title={row.id}
          >
            {row.id}
          </span>
        </>
      ),
      headerTitle: "一行 = 一个上游账号（finance.upstream_account）",
    },
    {
      id: "platform",
      header: "平台 / 来源",
      value: (row) => `${row.platformId || "未归属"} ${row.systemType}`,
      cell: (row) => <SourceCell row={row} />,
    },
    {
      id: "group",
      header: "上游分组",
      // 排序按倍率：没配的排在一起，正好是「待配置」那一堆
      value: (row) => row.groupRate ?? "",
      cell: (row) => <GroupCell row={row} />,
      headerTitle: "分组倍率只展示，不并入成本折算（§10.2）",
    },
    {
      id: "models",
      // value 缺席 = 这一列不可排序也不参与搜索。给了 value 就等于说它可排序，
      // 而按一列全是「未接入」的东西排序只会让人以为排序坏了
      header: "可用模型",
      cell: () => <PendingCell reason={MODELS_PENDING} label="未接入 · M1.5" />,
      headerTitle: MODELS_PENDING,
    },
    {
      id: "supplyCost",
      header: "供给成本",
      numeric: true,
      value: (row) => moneyValue(row.supplyCost),
      cell: (row) => <MoneyCell money={row.supplyCost} note={costBasisNote(row)} />,
      headerTitle: "上游 / 订阅 / 代理折算到这个账号的现金成本",
    },
    {
      id: "revenue",
      header: "我方计费消耗",
      numeric: true,
      value: (row) => moneyValue(row.usageRevenue),
      cell: (row) => <MoneyCell money={row.usageRevenue} note="用户计费额" />,
      headerTitle: "这个账号名下令牌在窗口内产生的我方计费额",
    },
    {
      id: "balance",
      header: platform === "sub2api" ? "余额 / 有效期" : "余额状态",
      value: (row) => (row.runway.days === null ? null : row.runway.days),
      cell: (row) => <BalanceCell row={row} thresholds={thresholds} />,
      headerTitle: "余额 ÷ 近 7 个完整业务日的日均消耗（§10.4）",
    },
    {
      id: "grossProfit",
      header: "毛利",
      numeric: true,
      value: (row) => moneyValue(row.grossProfit),
      cell: (row) => (
        <>
          <MoneyCell money={row.grossProfit} />
          <MarginText margin={row.grossMargin} />
        </>
      ),
      headerTitle: "我方计费消耗 − 供给成本；副行是后端算好的毛利率",
    },
  ];

  // 原型只在 Sub2API 的表上画了成功率
  if (platform === "sub2api") {
    columns.push({
      id: "successRate",
      header: "成功率",
      cell: () => <PendingCell reason={SUCCESS_RATE_PENDING} label="未接入 · M1.5" />,
      headerTitle: SUCCESS_RATE_PENDING,
    });
  }

  columns.push(
    {
      id: "status",
      header: "状态",
      value: (row) => statusText(row),
      cell: (row) => <StatusCell row={row} />,
      headerTitle: "登记簿状态 + 可用天数档位。不是请求成功率意义上的「健康」",
    },
    {
      id: "detail",
      header: "详情",
      // 无 value：这一列只有一个链接，不该参与排序与搜索
      cell: (row) => (
        <Link
          to={`/platforms/${platform}?tab=suppliers`}
          className="text-xs underline underline-offset-2"
          title={`在上游管理里查看 ${row.name || row.id} 的登记与令牌映射`}
        >
          上游管理
        </Link>
      ),
      headerTitle: "登记详情、令牌映射与倍率修改都在上游管理页",
    },
  );

  return columns;
}

/** 「平台 / 来源」。
 *
 *  `platformId` 空串 = 未归属（后端四桶的第三桶），**显示出来而不是留白**：
 *  一个没配归属的账号，它的成本谁也不认领，这正是要被看见的状态。 */
function SourceCell({ row }: { row: ChannelSummary }) {
  return (
    <div>
      {row.platformId ? (
        <Badge tone="neutral">{row.platformId}</Badge>
      ) : (
        <Badge tone="warning" title="这个上游账号还没配平台归属，它的成本不会计入任何平台">
          未归属
        </Badge>
      )}
      <span className="block text-xs text-fg-muted">{sourceText(row)}</span>
    </div>
  );
}

function sourceText(row: ChannelSummary): string {
  const host = hostOf(row.baseUrl);
  return host ? `${row.systemType} · ${host}` : row.systemType;
}

function hostOf(baseUrl: string): string {
  if (!baseUrl) return "";
  try {
    return new URL(baseUrl).host;
  } catch {
    // 认不出来就原样显示：编一个 host 比显示一段怪地址更糟
    return baseUrl;
  }
}

/** 「上游分组」。
 *
 *  ⚠️ 原型这一列的主行是**分组名 + 倍率**（`gpt-main · 0.85×`），
 *  而登记簿里**没有分组名这个字段**——XM-0049 只补了 `group_rate` 的存储，
 *  没有分组名，也没有原型 `UPSTREAM_META.groups` 那种「一个上游下多个分组」的结构。
 *  所以这里只出倍率，不编一个名字：一个假的分组名会被当成真的去对账。
 *
 *  倍率本身**只展示不参与任何计算**（§10.2）：后端一次都没乘过它，
 *  前端再乘一遍就成了那条禁令说的「重复乘算」本身。 */
function GroupCell({ row }: { row: ChannelSummary }) {
  return (
    <div>
      <span className="font-medium">
        {row.groupRate ? (
          `${row.groupRate}×`
        ) : row.metered ? (
          <span className="text-fg-muted" title="这个上游账号没有配分组倍率。没配就是没配，不是 1">
            未配置倍率
          </span>
        ) : (
          "订阅型"
        )}
      </span>
      <span className="block text-xs text-fg-muted">{groupSubText(row)}</span>
    </div>
  );
}

function groupSubText(row: ChannelSummary): string {
  const cred = row.credentialRef ? "凭据已配" : "无凭据";
  if (!row.metered) return `${cred} · 订阅摊销口径`;
  // 充值成本率是**展示投影**（1 / recharge_ratio），不拿它反算成本
  return `${cred} · 成本率 ${row.rechargeCostRate}`;
}

/** 供给成本副行：这笔成本是怎么来的。
 *
 *  三种接入方式的成本口径完全不同，不写出来的话，一个订阅型账号的
 *  日摊金额会被当成「今天充值花了这么多」。 */
function costBasisNote(row: ChannelSummary): string {
  switch (row.accessMethod) {
    case "upstream_key":
      return "额度消耗 ÷ 充值倍率";
    case "subscription_account":
      return "订阅 / 代理日摊";
    case "official_api":
      return "官方直连 · 口径待定";
    default:
      return "口径未知";
  }
}

/** 「余额 / 有效期」。
 *
 *  三条纪律都在这一格里：
 *  ① 天数算不出来时**说清是哪一种给不出**，不写「暂无数据」；
 *  ② 有天数时必须同时显示余额的观测时刻（§10.4：不在过期数据上显示伪精确天数）；
 *  ③ 一个上游账号名下有多个令牌时，这份余额是**共享**的——
 *     §12.2 的口径就是为这件事写的，把每行的余额加起来会把同一笔钱数很多遍。 */
function BalanceCell({
  row,
  thresholds,
}: {
  row: ChannelSummary;
  thresholds: RunwayThresholds | null;
}) {
  const { runway } = row;
  const shared = row.tokenCount > 1;

  if (runway.days === null) {
    const reason = runwayReasonText(runway);
    return (
      <div>
        <span className="text-fg-muted">—</span>
        <span className="block text-xs text-fg-muted">{reason ?? "可用天数给不出"}</span>
      </div>
    );
  }

  const tone = RUNWAY_TONE[runway.level] ?? "neutral";
  return (
    <div>
      <span className="font-medium">
        {runway.balance
          ? formatScaledMinorUnits(
              runway.balance.amountMinor,
              runway.balance.currency,
              runway.balance.scale,
            )
          : "—"}
      </span>
      <span className="ml-2">
        <Badge tone={tone} title={thresholds ? runwayNote(runway, thresholds) : undefined}>
          约 {runway.days} 天
        </Badge>
      </span>
      <span className="block text-xs text-fg-muted">
        {shared ? `${row.tokenCount} 个令牌共享此余额 · ` : ""}
        {runway.balanceObservedAt ? `余额观测 ${runway.balanceObservedAt}` : "余额观测时刻未知"}
      </span>
    </div>
  );
}

/** 「状态」。
 *
 *  原型这一格是「健康 / 需关注」，判据是请求成功率——**那个我们今天没有**。
 *  所以这里换成两个我们真有的东西：登记簿状态（启用 / 停用）与可用天数档位。
 *  换判据必须说出来（表头 title 里写了），否则「健康」二字会被当成
 *  「请求都成功」，而它其实只是「余额还够」。 */
function statusText(row: ChannelSummary): string {
  if (row.status !== "active") return `已停用（${row.status}）`;
  if (row.runway.level === "critical" || row.runway.level === "warning") return "需关注";
  if (row.runway.days === null) return "余额未知";
  return "正常";
}

function StatusCell({ row }: { row: ChannelSummary }) {
  const text = statusText(row);
  if (row.status !== "active") {
    return (
      <Badge tone="neutral" title="登记簿里这个账号不是 active">
        {text}
      </Badge>
    );
  }
  if (text === "需关注") {
    return (
      <Badge tone={RUNWAY_TONE[row.runway.level] ?? "warning"} title="可用天数已进入告警档">
        需关注
      </Badge>
    );
  }
  if (text === "余额未知") {
    return (
      <Badge tone="neutral" title={runwayReasonText(row.runway) ?? "可用天数给不出"}>
        余额未知
      </Badge>
    );
  }
  return (
    <Badge tone="success" title="登记簿状态为 active，且可用天数不在告警档">
      正常
    </Badge>
  );
}
