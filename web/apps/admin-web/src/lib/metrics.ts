import type { MetricItem } from "../api/platform";
import { formatCount, formatMinorUnits, toIntegerValue } from "./money";

/** 指标在卡片上的呈现结果。 */
export interface MetricPresentation {
  /** 友好名。未登记的 metric_key 直接用键名，不编一个好听的假名字。 */
  label: string;
  /** 主数字（或「未初始化」这类占位文案）。 */
  primary: string;
  /** 补充说明，例如业务日、单数、币种。 */
  secondary?: string;
  /** true 表示这里没有可信数值，卡片应弱化显示，不要看着像个正常读数。 */
  unavailable: boolean;
}

/** metric_key → 友好名。键取自 connectors/sub2api/contract.go 的指标常量。 */
const METRIC_LABELS: Record<string, string> = {
  "sub2api.users.total": "Sub2API 用户数",
  "sub2api.users.balance": "Sub2API 用户余额",
  "sub2api.revenue.daily": "Sub2API 日收入",
  "sub2api.cost.daily": "Sub2API 日成本",
  "sub2api.channels.balance": "Sub2API 渠道余额",
};

/** 没有可信数值时主位显示的占位符。 */
const NO_VALUE = "—";

function readString(value: Record<string, unknown>, key: string): string | undefined {
  const v = value[key];
  return typeof v === "string" && v.length > 0 ? v : undefined;
}

function currencyOf(value: Record<string, unknown>): string {
  return readString(value, "currency") ?? "";
}

interface ValueRender {
  primary: string;
  secondary?: string;
  unavailable?: boolean;
}

type Renderer = (value: Record<string, unknown>) => ValueRender;

function joinParts(parts: Array<string | undefined>): string | undefined {
  const kept = parts.filter((p): p is string => Boolean(p));
  return kept.length > 0 ? kept.join(" · ") : undefined;
}

/** 渠道余额是个聚合指标，值里是一个数组，单独处理。 */
function renderChannelBalance(value: Record<string, unknown>): ValueRender {
  const raw = value["channels"];
  const channels = Array.isArray(raw) ? (raw as Array<Record<string, unknown>>) : [];
  const count = toIntegerValue(value["channel_count"]) ?? BigInt(channels.length);

  let invalidTokens = 0;
  const currencies = new Set<string>();
  let total = 0n;
  let summable = channels.length > 0;
  for (const c of channels) {
    if (c["token_valid"] === false) invalidTokens += 1;
    currencies.add(currencyOf(c));
    const balance = toIntegerValue(c["balance_minor_units"]);
    if (balance === null) summable = false;
    else total += balance;
  }

  // 币种不一致时不给合计：把不同币种的最小单位加在一起是纯粹的错数
  const totalText =
    summable && currencies.size === 1
      ? `合计 ${formatMinorUnits(total, [...currencies][0] ?? "")}`
      : undefined;
  const tokenText = invalidTokens > 0 ? `${invalidTokens} 个渠道令牌失效` : undefined;

  return {
    primary: `${formatCount(count)} 个渠道`,
    secondary: joinParts([totalText, tokenText]),
  };
}

const RENDERERS: Record<string, Renderer> = {
  "sub2api.users.total": (v) => ({
    primary: formatCount(v["total_users"]),
    secondary: joinParts([`活跃 ${formatCount(v["active_users"])}`]),
  }),
  "sub2api.users.balance": (v) => ({
    primary: formatMinorUnits(v["balance_minor_units"], currencyOf(v)),
    secondary: joinParts([`透支 ${formatMinorUnits(v["overdraft_minor_units"], currencyOf(v))}`]),
  }),
  "sub2api.revenue.daily": (v) => ({
    primary: formatMinorUnits(v["amount_minor_units"], currencyOf(v)),
    secondary: joinParts([
      readString(v, "day") ? `业务日 ${readString(v, "day")}` : undefined,
      v["order_count"] === undefined ? undefined : `${formatCount(v["order_count"])} 单`,
    ]),
  }),
  "sub2api.cost.daily": (v) => ({
    primary: formatMinorUnits(v["amount_minor_units"], currencyOf(v)),
    secondary: joinParts([readString(v, "day") ? `业务日 ${readString(v, "day")}` : undefined]),
  }),
  "sub2api.channels.balance": renderChannelBalance,
};

/** 未登记指标的兜底渲染：认得出金额就按金额显示，认得出整数就按计数显示，
 *  都认不出就明说「值形状未知」并列出字段名——比瞎猜一个数字诚实。 */
function renderUnknown(value: Record<string, unknown>): ValueRender {
  const currency = currencyOf(value);
  for (const [key, raw] of Object.entries(value)) {
    if (key.endsWith("_minor_units") && toIntegerValue(raw) !== null) {
      return { primary: formatMinorUnits(raw, currency) };
    }
  }
  for (const [key, raw] of Object.entries(value)) {
    if (key === "currency") continue;
    if (toIntegerValue(raw) !== null) return { primary: formatCount(raw), secondary: key };
  }
  const keys = Object.keys(value);
  return {
    primary: NO_VALUE,
    secondary: keys.length > 0 ? `值形状未知：${keys.join("、")}` : "值为空",
    unavailable: true,
  };
}

/** 指标 → 卡片呈现内容。
 *
 *  第一条分支就是宪法 12 条：从未采集过就绝不显示数字。后端此时 value 里可能
 *  仍带着 0，直接渲染就会变成「余额 ¥0.00」这种理直气壮的假数据。 */
export function presentMetric(item: MetricItem): MetricPresentation {
  const label = METRIC_LABELS[item.metric_key] ?? item.metric_key;

  if (item.freshness.state === "uninitialized") {
    return { label, primary: "未初始化", secondary: "从未成功采集", unavailable: true };
  }

  const value = item.value ?? {};
  const render = (RENDERERS[item.metric_key] ?? renderUnknown)(value);
  return {
    label,
    primary: render.primary,
    ...(render.secondary === undefined ? {} : { secondary: render.secondary }),
    unavailable: render.unavailable ?? false,
  };
}

/** 指标友好名（表头、筛选器等只要名字的地方）。 */
export function metricLabel(metricKey: string): string {
  return METRIC_LABELS[metricKey] ?? metricKey;
}
