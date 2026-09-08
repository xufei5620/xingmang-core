import { useQuery } from "@tanstack/react-query";
import type { ReactElement } from "react";

import { Badge } from "@xingmang/ui-primitives";
import {
  MetricCard,
  StatTile,
  formatUtcTimestamp,
  type FreshnessContract,
} from "@xingmang/ui-admin";

import {
  listChannelSummaries,
  listUpstreamSummaries,
  UPSTREAM_SUMMARY_QUERY,
  type ChannelSummary,
  type Money,
  type Runway,
  type RunwayThresholds,
  type UpstreamSummary,
} from "../api/finance";
import { formatScaledMinorUnits } from "../lib/money";
import { RUNWAY_REASON_TEXT, RUNWAY_TONE, runwayNote } from "../lib/runway";
import { sumMoneyValues } from "../lib/financeOverview";
import { ApiStateView } from "./ApiStateView";

/** 平台概览页上的成本三卡（XM-0037d，设计稿 §8.5 + UI 交接 §10.3/§10.4/§10.5）。
 *
 *  「今日毛利」「今日供给成本」「可用天数」，外加一张诚实占位的「贡献利润」。
 *
 *  贯穿这四张卡的一条规矩：**给不出就说给不出，并说清为什么**。
 *  这不是保守，是因为这几个数会被拿去做决定——「今天毛利 0」会让人去查定价，
 *  而真相可能只是「今天的成本还没采到」。两者在页面上必须长得不一样
 *  （宪法 12 条、后端 §5.1）。 */

/** 与后端 finance 指标同一个阈值（`ProfitStalenessThresholdSeconds`）。
 *
 *  采集默认 5 分钟一轮，半小时没有新数据说明入账链路有问题，
 *  而不是「今天没有毛利」。 */
const FINANCE_STALENESS_THRESHOLD_SECONDS = 1800;

export interface FinanceSummaryCardsProps {
  /** 只显示这个上游系统的渠道（`sub2api` / `newapi`）。 */
  systemType: string;
  label: string;
}

/** 把窗口的观测元数据折成一份新鲜度契约。
 *
 *  台账没有独立的 ops 指标行——它的新鲜度就是「最后一次写入离现在多久」。
 *  合成一份而不是不显示：一个没有新鲜度徽章的金额就是裸数字（规格 §9.1）。
 *
 *  `complete === false` 落 `partial` 而不是 `fresh`：覆盖不全的窗口哪怕刚刷新过，
 *  它的合计也是偏低的，那正是 partial 这个状态存在的意义。 */
function summaryFreshness(
  updatedAt: string | null,
  complete: boolean,
  now: number,
): FreshnessContract {
  const base = {
    threshold_seconds: FINANCE_STALENESS_THRESHOLD_SECONDS,
    is_partial: !complete,
    observed_at: updatedAt,
    last_success: updatedAt,
    last_error_code: "",
  };
  if (!updatedAt) {
    // 从未写入 ≠ 数值是 0（宪法 12 条）。徽章说「未初始化」，
    // 值的位置也会显示「未接入」而不是一个金额。
    return { ...base, state: "uninitialized", staleness_seconds: null, observed_at: null };
  }
  const parsed = Date.parse(updatedAt);
  if (Number.isNaN(parsed)) {
    return { ...base, state: "uninitialized", staleness_seconds: null, observed_at: null };
  }
  const staleness = Math.max(0, Math.round((now - parsed) / 1000));
  const state = !complete
    ? "partial"
    : staleness >= FINANCE_STALENESS_THRESHOLD_SECONDS
      ? "stale"
      : "fresh";
  return { ...base, state, staleness_seconds: staleness };
}

/** 一组渠道的金额合计。
 *
 *  **任一条给不出、或币种不一致，整个合计就给不出**——与后端
 *  `ProfitWindow.RevenueMinor()` 是同一条纪律，只是搬到了跨渠道那一层：
 *  少一条渠道的成本，合计出来的毛利会偏高，而它长得和完整的一模一样。 */
interface MoneyTotal {
  minor: bigint | null;
  currency: string;
  scale: number;
}

function totalOf(values: (Money | null)[]): MoneyTotal {
  const total = sumMoneyValues(values);
  return total ? { minor: BigInt(total.amountMinor), currency: total.currency, scale: total.scale } : { minor: null, currency: "", scale: Number.NaN };
}

function totalText(total: MoneyTotal): { value: string; unavailable: boolean } {
  if (total.minor === null) return { value: "—", unavailable: true };
  return {
    value: formatScaledMinorUnits(total.minor.toString(), total.currency, total.scale),
    unavailable: false,
  };
}

/** 覆盖率说明：合计代表了几条渠道、其中几条是账号级聚合。
 *
 *  金额与覆盖率必须一起给（宪法 12 条）。「3 条渠道里有 1 条今天没入账」
 *  与「3 条都入了账」在数字上看不出差别，只有这一行说得出来。 */
function coverageNote(channels: ChannelSummary[]): string {
  if (channels.length === 0) return "本平台还没有登记任何渠道";
  const complete = channels.filter((c) => c.coverage.complete).length;
  const aggregated = channels.reduce((n, c) => n + c.coverage.accountGrainRows, 0);
  const parts = [`${complete}/${channels.length} 条渠道数据完整`];
  if (aggregated > 0) {
    // 账号级聚合行没有独立的令牌下钻，值得单独说一句
    parts.push(`${aggregated} 行为账号级聚合`);
  }
  return parts.join(" · ");
}

/** 从一组上游里挑出**最紧的**那条可用天数。
 *
 *  取最小值而不是平均：可用天数是预警，一条快见底的上游不该被另外几条
 *  充裕的平均掉——那正好把最该被看见的那条藏起来了。 */
function tightestRunway(upstreams: UpstreamSummary[]): UpstreamSummary | null {
  let tightest: UpstreamSummary | null = null;
  for (const item of upstreams) {
    if (item.runway.days === null) continue;
    if (tightest === null || item.runway.days < (tightest.runway.days ?? Number.MAX_SAFE_INTEGER)) {
      tightest = item;
    }
  }
  return tightest;
}

/** FinanceSummaryCards 渲染一个平台的成本三卡 + 贡献利润占位。 */
export function FinanceSummaryCards({
  systemType,
  label,
}: FinanceSummaryCardsProps): ReactElement {
  const channelQuery = useQuery({
    queryKey: ["finance", "channels", "summary"],
    queryFn: ({ signal }) => listChannelSummaries({ signal }),
  });
  const upstreamQuery = useQuery({
    queryKey: [UPSTREAM_SUMMARY_QUERY],
    queryFn: ({ signal }) => listUpstreamSummaries({ signal }),
  });

  const now = Date.now();
  const channels = (channelQuery.data?.items ?? []).filter(
    (item) => item.systemType === systemType,
  );
  const upstreams = (upstreamQuery.data?.items ?? []).filter(
    (item) => item.systemType === systemType,
  );

  const revenue = totalOf(channels.map((c) => c.usageRevenue));
  const cost = totalOf(channels.map((c) => c.supplyCost));
  const profit = totalOf(channels.map((c) => c.grossProfit));
  const allComplete = channels.length > 0 && channels.every((c) => c.coverage.complete);
  // 观测时刻取一组渠道里**最旧**的那个：聚合值的新鲜度由最不新鲜的成员决定。
  const oldestUpdatedAt = channels
    .map((c) => c.observed.updatedAt)
    .filter((v): v is string => Boolean(v))
    .sort()[0] ?? null;
  const freshness = summaryFreshness(oldestUpdatedAt, allComplete, now);

  const tightest = tightestRunway(upstreams);
  const coverage = upstreamQuery.data?.runwayCoverage;
  const thresholds = upstreamQuery.data?.runwayThresholds ?? {
    criticalDays: 0,
    warningDays: 0,
    seriousDays: 0,
  };
  // 覆盖率必须显式呈现（§12 拍板要求「标注覆盖率边界」）：余额采集还没接通时
  // 这个比值是 0/N，不说出来看板上就只是一排「—」，看起来像坏了。
  const runwayCoverageNote =
    coverage && coverage.total > 0
      ? `余额覆盖 ${coverage.known}/${coverage.total} 个上游`
      : "本平台没有需要可用天数的上游";

  const revenueText = totalText(revenue);
  const costText = totalText(cost);
  const profitText = totalText(profit);

  return (
    <ApiStateView
      isPending={channelQuery.isPending || upstreamQuery.isPending}
      error={channelQuery.error ?? upstreamQuery.error}
      onRetry={() => {
        void channelQuery.refetch();
        void upstreamQuery.refetch();
      }}
    >
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
        <MetricCard
          label={`${label} 今日毛利`}
          value={profitText.value}
          unavailable={profitText.unavailable}
          secondary={marginText(channels)}
          freshness={freshness}
          source={channels[0]?.observed.source ?? ""}
          link={<span className="text-xs text-fg-muted">{coverageNote(channels)}</span>}
        />
        <MetricCard
          label={`${label} 今日供给成本`}
          value={costText.value}
          unavailable={costText.unavailable}
          secondary={
            revenueText.unavailable ? "使用计费收入：—" : `使用计费收入 ${revenueText.value}`
          }
          freshness={freshness}
          source={channels[0]?.observed.source ?? ""}
          link={
            <span className="text-xs text-fg-muted">
              {channels.length > 0
                ? `${channels.length} 条渠道 · ${channels.reduce((n, c) => n + c.tokenCount, 0)} 把令牌`
                : "—"}
            </span>
          }
        />
        {renderRunwayCard(upstreams, tightest, thresholds, runwayCoverageNote)}
        <StatTile
          label="贡献利润"
          value="—"
          unavailable
          note="毛利再减支付手续费与可归属基础设施/代理成本（§10.5）。两项数据源待 M3 支付接入，接上前不显示占位数字"
          status={<Badge tone="neutral">未接入</Badge>}
        />
      </div>
    </ApiStateView>
  );
}

/** 毛利率的副标题。**渠道多于一条时不给合计毛利率**：
 *  几条渠道的毛利率不能平均，也不能把分子分母各自相加再除——
 *  后者才是对的，但它需要两侧都覆盖满，而那个条件已经由合计金额表达了。
 *  与其在副标题里再算一遍，不如只在单渠道时给出后端算好的那个。 */
function marginText(channels: ChannelSummary[]): string {
  if (channels.length !== 1) return "毛利率见渠道明细";
  const margin = channels[0]?.grossMargin;
  if (!margin) return "毛利率：—（没有收入时不显示）";
  return `毛利率 ${formatPercent(margin)}`;
}

/** 定点十进制字符串 → 百分比展示。
 *
 *  **不经过一次浮点**：后端刻意用定点字符串传比率（宪法 13 条），
 *  显示层再 `parseFloat` 等于把纪律守到最后一米又松手。
 *  纯字符串搬小数点：`0.687500` → `68.75%`。 */
export function formatPercent(fixed: string): string {
  const match = /^(-?)(\d+)(?:\.(\d+))?$/.exec(fixed.trim());
  if (!match) return "—";
  const [, sign, whole, fraction = ""] = match;
  const digits = `${whole}${fraction}`;
  // 乘 100 = 小数点右移两位
  const point = (whole?.length ?? 0) + 2;
  const padded = digits.padEnd(Math.max(point + 2, digits.length), "0");
  const intPart = padded.slice(0, point).replace(/^0+(?=\d)/, "");
  const fracPart = padded.slice(point, point + 2).padEnd(2, "0");
  return `${sign}${intPart}.${fracPart}%`;
}

function renderRunwayCard(
  upstreams: UpstreamSummary[],
  tightest: UpstreamSummary | null,
  thresholds: RunwayThresholds,
  coverageNoteText: string,
): ReactElement {
  if (!tightest || tightest.runway.days === null) {
    // 一个没有解释的「—」会被读成 bug，然后有人就去把它「修」成 0 了
    // （§10.4 + 宪法 12 条）。所以这里必须说得出**为什么**算不出来。
    const reason = dominantRunwayReason(upstreams);
    return (
      <StatTile
        label="可用天数（最紧）"
        value="—"
        unavailable
        note={`${runwayReasonText(reason)} · ${coverageNoteText}`}
        status={<Badge tone="neutral">未接入</Badge>}
      />
    );
  }
  const runway = tightest.runway;
  const tone = RUNWAY_TONE[runway.level] ?? "neutral";
  return (
    <StatTile
      label="可用天数（最紧）"
      value={`${runway.days} 天`}
      note={`${tightest.name} · ${runwayNote(runway, thresholds)} · ${coverageNoteText}`}
      status={<Badge tone={tone}>{runway.level}</Badge>}
      link={
        <span className="text-xs text-fg-muted">
          {/* §10.4 硬要求：必须显示观测时间 */}
          余额观测 {formatUtcTimestamp(runway.balanceObservedAt)}
        </span>
      }
    />
  );
}

/** 给不出天数时的说明文案（导出供测试与其他页面复用）。 */
export function runwayReasonText(reason: string): string {
  return RUNWAY_REASON_TEXT[reason] ?? "可用天数暂不可用";
}

/** 一组上游里**出现最多**的那个「算不出来」的原因。
 *
 *  取众数而不是第一条：一批上游里若两条是「订阅型不适用」、五条是
 *  「余额还没接通」，卡上该说的显然是后者——那是一件要去做的事，
 *  前者只是一个事实。并列时按 RUNWAY_REASON_TEXT 的键序取第一个，
 *  让同一份数据每次都给出同一句话。 */
function dominantRunwayReason(upstreams: UpstreamSummary[]): string {
  const counts = new Map<string, number>();
  for (const item of upstreams) {
    if (item.runway.days !== null) continue;
    const reason = item.runway.reason || "";
    counts.set(reason, (counts.get(reason) ?? 0) + 1);
  }
  let best = "";
  let bestCount = 0;
  for (const reason of Object.keys(RUNWAY_REASON_TEXT)) {
    const count = counts.get(reason) ?? 0;
    if (count > bestCount) {
      best = reason;
      bestCount = count;
    }
  }
  return best;
}
