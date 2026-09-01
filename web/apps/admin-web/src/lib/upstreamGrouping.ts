import type { UpstreamAccountItem, UpstreamSummary } from "../api/finance";
import { coveredCount, oldestActualTimestamp, sumMoney, type MoneyTotal } from "./upstreamTotals";

/** 上游管理区块（原型 `V["s2/suppliers"]`）行粒度的纠正：供应商，不是账号。
 *
 *  ADMIN-IA §8.6 #1 记录的缺口——「当前行粒度仍是一个上游账号，不是供应商
 *  实体」——今天登记簿仍然没有一张独立的 supplier 表，所以这里不新增一层
 *  存储，而是在展示层按**已有字段**把账号归并成供应商分组：
 *
 *  1. `upstream_name` 非空 → 按名称归并（同一个上游多次登记的账号，名字通常一致）；
 *  2. 名称为空 → 退回按 `base_url` 的 host 归并（同一域名大概率是同一个上游）；
 *  3. 两者都取不到 → 各自成组，用账号 id 兜底，不编一个假名字。
 *
 *  这是**展示层的归并**，不是真相源：真正的供应商实体仍是后续切片的工作
 *  （见 `docs/architecture/ADMIN-IA.md` §8.6 #1），这里只保证同一片区的账号
 *  不会被拆散到很多行里、也不会把不同上游误并成一行。 */

export type SupplierGroupKind = "name" | "host" | "account";

export interface RateSpread {
  kind: "none" | "single" | "mixed";
  /** kind === "single" 时的那一个值；否则为 null。 */
  rate: string | null;
  /** 参与判定的计量型账号所用币种；混合币种也计入 mixed。 */
  currency: string | null;
  /** 出现过的全部不同取值，用于 title 展开。 */
  distinct: readonly string[];
}

export interface UpstreamSupplierGroup {
  /** 分组键；同一批输入里唯一。 */
  key: string;
  groupedBy: SupplierGroupKind;
  /** 展示名：groupedBy==="name" 用登记的上游名称；="host" 用网址 host；
   *  ="account" 时账号自己也没有可用名称，退回账号 id 短串。 */
  name: string;
  accounts: readonly UpstreamAccountItem[];
  /** 组内出现过的不同网址（非空），保留原始输入顺序去重。 */
  baseUrls: readonly string[];
  /** 组内出现过的不同 platform_id（非空）。 */
  platforms: readonly string[];
  /** platform_id 为空的账号数——未配对的必须仍然可见（ADMIN-IA §8.6 #3）。 */
  unpairedCount: number;
  keyAccountCount: number;
  subscriptionAccountCount: number;
  officialAccountCount: number;
  otherAccountCount: number;
  configuredCredentialCount: number;
  /** 组内出现过的不同联系人（非空）。 */
  contacts: readonly string[];
  /** 组内出现过的不同接入分组名（非空）。 */
  groupNames: readonly string[];
  rechargeRate: RateSpread;
  balanceTotal: MoneyTotal;
  balanceCovered: number;
  oldestBalanceObservedAt: string | null;
  costTotal: MoneyTotal;
  costCovered: number;
  profitTotal: MoneyTotal;
  profitCovered: number;
  /** 供成本/毛利证据用：参与合计的账号里，成本观测最旧的那个时刻。 */
  oldestCostObservedAt: string | null;
  oldestProfitObservedAt: string | null;
  disabledCount: number;
  /** 可用天数已进入告警档（critical/warning）的账号数。 */
  attentionCount: number;
  /** 汇总端点完全没有覆盖到的账号数（既不在 summaries 里）。 */
  uncoveredSummaryCount: number;
}

function hostOf(url: string): string {
  if (!url) return "";
  try {
    return new URL(url).host;
  } catch {
    return "";
  }
}

function dedupe(values: readonly string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const v of values) {
    if (!v || seen.has(v)) continue;
    seen.add(v);
    out.push(v);
  }
  return out;
}

/** 一个账号归到哪个分组键。导出是为了让调用方（例如"这条账号属于哪组"的
 *  单条判断）不必重新实现一遍同一条规则。 */
export function supplierGroupKeyFor(
  account: UpstreamAccountItem,
): { key: string; groupedBy: SupplierGroupKind; label: string } {
  const name = account.upstream_name.trim();
  if (name) return { key: `name:${name}`, groupedBy: "name", label: name };
  const host = hostOf(account.base_url);
  if (host) return { key: `host:${host}`, groupedBy: "host", label: host };
  return { key: `account:${account.id}`, groupedBy: "account", label: account.id };
}

function rateSpread(accounts: readonly UpstreamAccountItem[]): RateSpread {
  const metered = accounts.filter((a) => a.metered && a.recharge_cost_rate.trim());
  if (metered.length === 0) return { kind: "none", rate: null, currency: null, distinct: [] };
  const distinct = dedupe(metered.map((a) => `${a.currency || "USD"} ${a.recharge_cost_rate}`));
  if (distinct.length === 1) {
    const first = metered[0]!;
    return {
      kind: "single",
      rate: first.recharge_cost_rate,
      currency: first.currency || "USD",
      distinct,
    };
  }
  return { kind: "mixed", rate: null, currency: null, distinct };
}

/** 把登记簿的账号列表（已按平台过滤）按供应商归并，并联上汇总端点的钱。
 *
 *  `summariesById` 只需要覆盖入参账号即可；查不到的账号视为「这个窗口汇总
 *  还没有这一条」，计入 `uncoveredSummaryCount`，不参与任何金额合计
 *  （与 `sumMoney` 对 `null` 金额的处理是同一条规矩：跳过而不是当 0）。 */
export function groupUpstreamAccounts(
  accounts: readonly UpstreamAccountItem[],
  summariesById: ReadonlyMap<string, UpstreamSummary>,
): UpstreamSupplierGroup[] {
  const order: string[] = [];
  const buckets = new Map<string, { groupedBy: SupplierGroupKind; label: string; accounts: UpstreamAccountItem[] }>();

  for (const account of accounts) {
    const { key, groupedBy, label } = supplierGroupKeyFor(account);
    let bucket = buckets.get(key);
    if (!bucket) {
      bucket = { groupedBy, label, accounts: [] };
      buckets.set(key, bucket);
      order.push(key);
    }
    bucket.accounts.push(account);
  }

  return order.map((key) => {
    const bucket = buckets.get(key)!;
    const members = bucket.accounts;
    const summaries = members.map((a) => summariesById.get(a.id));

    const balances = summaries.map((s) => s?.runway.balance ?? null);
    const costs = summaries.map((s) => s?.supplyCost ?? null);
    const profits = summaries.map((s) => s?.grossProfit ?? null);

    let keyAccountCount = 0;
    let subscriptionAccountCount = 0;
    let officialAccountCount = 0;
    let otherAccountCount = 0;
    let disabledCount = 0;
    let attentionCount = 0;
    let uncoveredSummaryCount = 0;
    let configuredCredentialCount = 0;

    members.forEach((account, index) => {
      switch (account.access_method) {
        case "upstream_key":
          keyAccountCount += 1;
          break;
        case "subscription_account":
          subscriptionAccountCount += 1;
          break;
        case "official_api":
          officialAccountCount += 1;
          break;
        default:
          otherAccountCount += 1;
      }
      if (account.status !== "active") disabledCount += 1;
      if (account.credential_ref) configuredCredentialCount += 1;
      const summary = summaries[index];
      if (!summary) {
        uncoveredSummaryCount += 1;
      } else if (summary.runway.level === "critical" || summary.runway.level === "warning") {
        attentionCount += 1;
      }
    });

    return {
      key,
      groupedBy: bucket.groupedBy,
      name: bucket.label,
      accounts: members,
      baseUrls: dedupe(members.map((a) => a.base_url)),
      platforms: dedupe(members.map((a) => a.platform_id)),
      unpairedCount: members.filter((a) => a.platform_id === "").length,
      keyAccountCount,
      subscriptionAccountCount,
      officialAccountCount,
      otherAccountCount,
      configuredCredentialCount,
      contacts: dedupe(members.map((a) => a.upstream_contact)),
      groupNames: dedupe(members.map((a) => a.upstream_group)),
      rechargeRate: rateSpread(members),
      balanceTotal: sumMoney(balances),
      balanceCovered: coveredCount(balances),
      oldestBalanceObservedAt: oldestActualTimestamp(summaries.map((s) => s?.runway.balanceObservedAt)),
      costTotal: sumMoney(costs),
      costCovered: coveredCount(costs),
      profitTotal: sumMoney(profits),
      profitCovered: coveredCount(profits),
      oldestCostObservedAt: oldestActualTimestamp(
        summaries.filter((s) => Boolean(s?.supplyCost)).map((s) => s?.observed.costObservedAt),
      ),
      oldestProfitObservedAt: oldestActualTimestamp(
        summaries
          .filter((s) => Boolean(s?.grossProfit))
          .flatMap((s) => [s?.observed.costObservedAt, s?.observed.revenueObservedAt]),
      ),
      disabledCount,
      attentionCount,
      uncoveredSummaryCount,
    };
  });
}
