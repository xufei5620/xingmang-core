import type {
  ChannelSummary,
  Money,
  RunwayCoverage,
  RunwayLevel,
  UpstreamSummary,
} from "../api/finance";

/** 跨平台财务页（XM-FINANCE-GLOBAL0）的判断逻辑。
 *
 *  这一页与各平台自己的「支付与财务」页的区别只有一条：它要把**两个平台**的
 *  钱放进同一个数位里。而一旦开始跨平台相加，就多出三种在单平台页上不存在
 *  的说谎方式，这个文件存在就是为了把它们堵死：
 *
 *  1. **少一个平台的合计**。一个只算了 Sub2API 的「现金到账」，和一个算了两家的
 *     长得一模一样，只是小一半。所以任一平台今天没有可用汇总时整个合计给不出
 *     （与 `financeOverview.sumMoneyValues` 跨渠道那层是同一条纪律，只是搬到了
 *     跨平台这一层）。
 *  2. **跨币种相加**。CNY 与 USD 的最小单位不能相加；后端在渠道那层已经用
 *     `coverage.mixed_currency` fail closed（`internal/platform/finance/summary.go`
 *     的 `ProfitWindow.MixedCurrency`），跨平台这层必须自己再守一次。
 *  3. **把「还没采到」显示成 0**。生产的成本采集当前是关的（`XM_FINANCE_COLLECT_ENABLED`），
 *     于是「今天毛利 0」与「今天的成本一行都没写进来」在数字上完全一样——
 *     宪法 12 条要求这两件事在页面上长得不一样，`costCollectionState` 就是那个判据。
 *
 *  放在 lib 而不是组件里：这三条都是**判断**，判断要能被单独钉住。 */

// ============================================================================
// 跨平台分桶合计（页顶「现金到账」「退款 / 冻结」两格）
// ============================================================================

/** 一个平台在某个归一化分桶上的当日事实。
 *
 *  形状刻意贴着 `lib/metrics.ts` 的 `PaymentsDailySummary` + `MetricItem.freshness`，
 *  但不直接吃它们：解析指标是 `readPaymentsDailySummary` 的事，这里只做判断，
 *  于是测试不必伪造一整份指标 JSON。 */
export interface PlatformBucketFact {
  /** 平台展示名。文案里要点名说「谁没有数」，所以带进来。 */
  label: string;
  /** 这个平台**没有这个概念**（NewAPI 没有退款，见 api/finance.ts 的
   *  `platformHasRefunds`）。它不是缺口，因此不阻塞合计，只在说明里点一句。 */
  notApplicable?: boolean;
  /** 指标在场且成功采集过（`metric` 存在且 `freshness.state !== "uninitialized"`）。 */
  available: boolean;
  /** 指标自己的业务日。与所选业务日不一致时这个平台不参与合计——
   *  指标是**单日**粒度，拿别的一天的值凑今天的合计就是编数
   *  （与 `PaymentSummaryCards.periodMatchesMetric` 同一条判据）。 */
  day: string | null;
  /** 本次汇总的合约币种；空串表示上游没给。 */
  currency: string;
  /** 该分桶的金额（最小单位）。**null = `by_status` 里这个桶的键缺席**，
   *  与「金额是 0」不是一回事：契约原话是「上游这一天没有落进某个桶的订单，
   *  那个桶的键就不出现」，所以 `isPartial=false` 时缺席是**确认过的零**。 */
  amountMinor: bigint | null;
  /** 桶键**在场**但金额不是合法的整数最小单位（`amount_minor_units` 形状不对）。
   *  与「桶键缺席」是两件事：后者在覆盖完整时是确认过的零，这个是坏数据，
   *  按 0 计入就是把一笔不知道多少的钱当成没有。 */
  invalidAmount?: boolean;
  /** 本次汇总覆盖不全（翻页到顶，或有非合约币种订单的金额被排除在外）。 */
  isPartial: boolean;
}

export type CrossPlatformTotal =
  | {
      kind: "total";
      amountMinor: bigint;
      currency: string;
      /** 真正加进这个数的平台。 */
      contributors: readonly string[];
      /** 没有这个概念、因此不算缺口的平台。 */
      notApplicable: readonly string[];
      /** 任一参与方覆盖不全——合计**偏低**，必须说出来。 */
      partial: boolean;
    }
  | {
      kind: "mixed-currency";
      entries: readonly { label: string; currency: string }[];
    }
  | { kind: "unavailable"; reason: string };

/** 把若干平台的同一个分桶合成一个数。**任何一条不确定，整个合计就给不出。**
 *
 *  判断顺序不是随意的：先确认「谁该参与」，再确认「币种能不能加」，最后才看
 *  金额本身。反过来的话，一个缺席平台的空币种会被当成「币种不一致」报出来，
 *  而那句话会把人引到一个不存在的问题上。 */
export function crossPlatformBucketTotal(
  facts: readonly PlatformBucketFact[],
  businessDay: string,
): CrossPlatformTotal {
  const notApplicable = facts.filter((fact) => fact.notApplicable).map((fact) => fact.label);
  const contributors = facts.filter((fact) => !fact.notApplicable);
  if (contributors.length === 0) {
    return { kind: "unavailable", reason: "没有平台参与这个合计" };
  }

  const missing = contributors.filter(
    (fact) => !fact.available || fact.day === null || fact.day !== businessDay,
  );
  if (missing.length > 0) {
    return {
      kind: "unavailable",
      reason: `${names(missing)} 今天（业务日 ${businessDay}）没有可用的支付日汇总；少一个平台的合计会偏低，且与完整的合计长得一模一样，所以整个合计给不出`,
    };
  }

  const currencies = [...new Set(contributors.map((fact) => fact.currency))];
  if (currencies.some((currency) => currency === "")) {
    return {
      kind: "unavailable",
      reason: `${names(contributors.filter((fact) => fact.currency === ""))} 没有声明合约币种，不做没有单位的相加`,
    };
  }
  if (currencies.length > 1) {
    return {
      kind: "mixed-currency",
      entries: contributors.map((fact) => ({ label: fact.label, currency: fact.currency })),
    };
  }

  const invalid = contributors.filter((fact) => fact.invalidAmount);
  if (invalid.length > 0) {
    return {
      kind: "unavailable",
      reason: `${names(invalid)} 的分桶金额不是合法的整数最小单位，合计给不出`,
    };
  }

  const unconfirmed = contributors.filter((fact) => fact.amountMinor === null && fact.isPartial);
  if (unconfirmed.length > 0) {
    return {
      kind: "unavailable",
      reason: `${names(unconfirmed)} 覆盖不全，说不清这个分桶真的是零还是被漏掉了，合计给不出`,
    };
  }

  let amountMinor = 0n;
  for (const fact of contributors) {
    // 到这里 null 只可能来自「非 partial 的桶键缺席」＝ 契约里确认过的零
    amountMinor += fact.amountMinor ?? 0n;
  }
  return {
    kind: "total",
    amountMinor,
    currency: currencies[0] ?? "",
    contributors: contributors.map((fact) => fact.label),
    notApplicable,
    partial: contributors.some((fact) => fact.isPartial),
  };
}

function names(facts: readonly PlatformBucketFact[]): string {
  return facts.map((fact) => fact.label).join(" / ");
}

/** 合计格底下那句「这个数是怎么来的 / 为什么给不出」。
 *
 *  一个没有解释的「—」会被读成 bug，然后有人就去把它「修」成 0 了
 *  （同 FinanceSummaryCards 的可用天数卡）。 */
export function crossPlatformTotalNote(total: CrossPlatformTotal, businessDay: string): string {
  if (total.kind === "mixed-currency") {
    const detail = total.entries.map((entry) => `${entry.label} ${entry.currency}`).join(" · ");
    return `币种不一致，合计给不出（${detail}）。不同币种的最小单位不能相加，平台不替人做一次没同意的换算。`;
  }
  if (total.kind === "unavailable") return total.reason;

  const parts = [`${total.contributors.join(" + ")} 合计 · 业务日 ${businessDay}`];
  if (total.notApplicable.length > 0) {
    parts.push(`${total.notApplicable.join(" / ")} 不适用（平台没有这个概念）`);
  }
  if (total.partial) {
    parts.push("覆盖不全：可能有非合约币种订单未计入金额，这个合计偏低");
  }
  return parts.join(" · ");
}

// ============================================================================
// 成本采集状态（「今天毛利 0」还是「成本还没采到」）
// ============================================================================

export type CostCollectionState =
  | { kind: "no-channels" }
  | { kind: "mixed-currency"; channels: number }
  | { kind: "not-collected"; channels: number; rows: number }
  | {
      kind: "partial";
      channels: number;
      completeChannels: number;
      costKnownRows: number;
      rows: number;
    }
  | { kind: "covered"; channels: number; rows: number };

/** 一个平台的成本侧今天处在哪种状态。
 *
 *  判据全部来自 `/finance/channels/summary` 自己回报的覆盖率
 *  （`coverage.row_count` / `cost_known_rows` / `complete` / `mixed_currency`），
 *  不去猜环境变量：前端读不到 `XM_FINANCE_COLLECT_ENABLED`，但采集没开时
 *  这几个字段的形状是确定的（有渠道、有台账行、成本侧 0 行有数）。 */
export function costCollectionState(channels: readonly ChannelSummary[]): CostCollectionState {
  if (channels.length === 0) return { kind: "no-channels" };
  if (channels.some((channel) => channel.coverage.mixedCurrency)) {
    return { kind: "mixed-currency", channels: channels.length };
  }
  const rows = channels.reduce((sum, channel) => sum + channel.coverage.rowCount, 0);
  const costKnownRows = channels.reduce((sum, channel) => sum + channel.coverage.costKnownRows, 0);
  if (costKnownRows === 0) return { kind: "not-collected", channels: channels.length, rows };
  const completeChannels = channels.filter((channel) => channel.coverage.complete).length;
  if (completeChannels < channels.length) {
    return {
      kind: "partial",
      channels: channels.length,
      completeChannels,
      costKnownRows,
      rows,
    };
  }
  return { kind: "covered", channels: channels.length, rows };
}

/** 成本三卡下面那一行。**每种状态都有自己的话**——统一一句「暂无数据」
 *  正好把「今天毛利就是 0」和「成本一行都没采到」抹成同一件事。 */
export function costCollectionNote(state: CostCollectionState): string {
  switch (state.kind) {
    case "no-channels":
      return "这个平台一条上游渠道都没有登记：成本与毛利这一侧今天没有任何行可算。这是「链路还没接」，不是「今天成本为 0」。";
    case "mixed-currency":
      return `${state.channels} 条渠道里有渠道命中多币种，后端已按纪律把它的金额置空（不同币种的最小单位不能相加），所以毛利合计给不出。`;
    case "not-collected":
      return `已登记 ${state.channels} 条渠道、共 ${state.rows} 行台账，但成本侧 0 行有数：成本还没采到，不是「今天成本为 0」。成本采集（XM_FINANCE_COLLECT_ENABLED）没开启时就是这个形状。`;
    case "partial":
      return `${state.channels} 条渠道里 ${state.completeChannels} 条数据完整、成本侧 ${state.costKnownRows}/${state.rows} 行有数：覆盖不全。后端在任一侧覆盖不全时把金额置空（少一行成本会让毛利凭空变大），所以上面的合计显示「—」而不是一个偏高的数。`;
    case "covered":
      return `${state.channels} 条渠道 · 成本侧 ${state.rows}/${state.rows} 行都已采到：这一屏的毛利是可断言的读数，显示 0 就是真的 0。`;
  }
}

// ============================================================================
// 「需要处理」卡：今天唯一有真源的事项是可用天数告警档位
// ============================================================================

/** 会进「需要处理」的可用天数档位。`healthy` 与空档位不进——
 *  前者是好消息，后者是「判不出来」，两样都不是待办。 */
const ATTENTION_LEVELS: readonly RunwayLevel[] = ["critical", "warning", "serious"];

export interface RunwayAttentionItem {
  id: string;
  name: string;
  systemType: string;
  days: number;
  level: RunwayLevel;
  balance: Money | null;
  dailyAverage: Money | null;
  balanceObservedAt: string | null;
}

/** 从上游摘要里挑出触发了可用天数档位的行，**最紧的排最前**。
 *
 *  只挑这一类：「需要处理」在原型里还有对账差异、契约状态、待审批合并三类，
 *  那三类今天都进不了这张卡（对账域不存在、开票按 CR-0005 刻意不接、审批中心
 *  已启用但这张卡还没读它，而 /api/v1/approvals 也不支持按 action_id 筛选,
 *  「只要财务相关的那些单」今天得整条队列拉回来自己过滤）。拿别的数据凑行数
 *  会让这张卡看起来是全的——逐字同 FinancePage 上那张卡的落款，两处不各写一份。 */
export function runwayAttentionItems(
  upstreams: readonly UpstreamSummary[],
): RunwayAttentionItem[] {
  return upstreams
    .filter(
      (upstream) =>
        upstream.runway.days !== null && ATTENTION_LEVELS.includes(upstream.runway.level),
    )
    .map((upstream) => ({
      id: upstream.id,
      name: upstream.name,
      systemType: upstream.systemType,
      days: upstream.runway.days ?? 0,
      level: upstream.runway.level,
      balance: upstream.runway.balance,
      dailyAverage: upstream.runway.dailyAverage,
      balanceObservedAt: upstream.runway.balanceObservedAt,
    }))
    .sort((a, b) => (a.days === b.days ? (a.id < b.id ? -1 : a.id > b.id ? 1 : 0) : a.days - b.days));
}

/** 一行都没有时该说什么。
 *
 *  三种「空」的下一步完全不同：没登记上游要去登记；登记了但余额一个都没读到
 *  要去查采集；读到了且都没触发档位才是真的「今天没事」——而即便是最后一种，
 *  也只对**覆盖到的那些**成立，所以那句话必须带上覆盖率。 */
export function runwayAttentionEmptyReason(
  upstreams: readonly UpstreamSummary[],
  coverage: RunwayCoverage | undefined,
): string {
  if (upstreams.length === 0) {
    return "一个上游账号都没有登记：可用天数这条线今天没有任何输入。这是「链路还没接」，不是「今天没有要处理的事」。";
  }
  const total = coverage?.total ?? upstreams.length;
  const known = coverage?.known ?? 0;
  if (known === 0) {
    return `${total} 个上游里 0 个读到了余额，可用天数判不出档位。这是「余额还没采到」，不是「今天没有要处理的事」。`;
  }
  return `余额覆盖 ${known}/${total} 个上游，其中没有一个落进可用天数告警档位。未覆盖的那些判不出档位，所以这一屏不等于「全部安全」。`;
}
