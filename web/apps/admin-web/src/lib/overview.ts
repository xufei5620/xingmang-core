import { describeFireCount, type AlertItem } from "../api/alerts";
import type { UpstreamAccountItem } from "../api/finance";
import type { ChannelRow, Sub2ApiChannelStatusRow } from "./metrics";

/** 「需要处理的事」里的一条。
 *
 *  原型只有两档（严重 / 注意），而告警有三档（critical / warning / info）。
 *  **info 不进这个列表**：它是「知道一下」，混进「需要处理的事」会让这一栏
 *  长期挂着一堆不需要处理的东西，然后整栏被忽略。 */
export interface WorkItem {
  id: string;
  tone: "bad" | "warn";
  /** 逐字用告警标题，不改写。 */
  title: string;
  /** 副行：规则与最近命中时刻。 */
  detail: string;
  /** 点进去看的地方。 */
  href: string;
}

/** 原型的两档 → 徽章文案。 */
export function workItemLabel(tone: WorkItem["tone"]): string {
  return tone === "bad" ? "严重" : "注意";
}

/** 活跃告警 → 需要处理的事。
 *
 *  按平台过滤走 `source_metric_key` 的平台前缀。**取不到前缀的告警不算进来**:
 *  它可能是全局规则（如控制面健康），挂到某个平台头上会让人去那个平台里
 *  找一件根本不在那里的事。
 *
 *  排序：严重在前，同档按最近命中倒序。原型把严重那条排在最上面,
 *  那不是排版偏好——一屏放不下时被截掉的应该是「注意」而不是「严重」。 */
export function toWorkItems(
  alerts: readonly AlertItem[],
  platform: string,
  platformOf: (metricKey: string) => string,
): WorkItem[] {
  const mine = alerts.filter((a) => {
    if (a.severity === "info") return false;
    return a.source_metric_key !== "" && platformOf(a.source_metric_key) === platform;
  });

  const items = mine.map((a) => ({
    id: a.id,
    tone: (a.severity === "critical" ? "bad" : "warn") as WorkItem["tone"],
    title: a.title,
    // 「命中 N 次」是同一句错话的第五份副本：fire_count 是每 60 秒重评一轮、
    // 条件仍成立就 +1 的轮数，措辞统一走 describeFireCount（见 api/alerts）。
    detail: `${a.rule_key} · 最近 ${a.last_seen_at}${a.fire_count > 1 ? ` · ${describeFireCount(a).combined}` : ""}`,
    href: "/alerts",
  }));

  return items.sort((x, y) => {
    if (x.tone !== y.tone) return x.tone === "bad" ? -1 : 1;
    return 0;
  });
}

/** 概览这一栏最多摆几条（原型摆了 4 条）。
 *
 *  **必须有上限**：告警是会堆的，一个环境攒到几十条时这一栏会把整页撑到
 *  几千像素高，右边那栏跟着拉出一大片空白，而概览的用途是「一眼看完」。
 *  截断本身不是隐瞒——被截掉多少条要显示出来，并给一条去全量列表的路。 */
export const WORK_ITEM_LIMIT = 4;

/** 截断结果：摆出来的那几条 + 还剩多少条。 */
export interface WorkItemPage {
  shown: WorkItem[];
  /** 没摆出来的条数；0 表示全摆下了。 */
  hidden: number;
}

export function limitWorkItems(
  items: readonly WorkItem[],
  limit: number = WORK_ITEM_LIMIT,
): WorkItemPage {
  return { shown: items.slice(0, limit), hidden: Math.max(items.length - limit, 0) };
}

/** 「上游健康」里的一行。 */
export interface HealthRow {
  label: string;
  tone: "ok" | "warn" | "bad" | "neutral";
  /** 徽章正文；null = 这一行还没有数据源。 */
  text: string | null;
  /** 悬停/副行说明。 */
  hint: string;
  /** 「查看 ›」去哪。 */
  href: string;
}

/** 上游渠道 x / y 可用。
 *
 *  分母是**这次观测里的渠道数**，不是登记簿里的账号数：两者是不同的东西
 *  （一个上游账号下可能有多条渠道），混用会得到一个永远对不上的比值。
 *
 *  `tokenValid === null`（上游没给这个字段）**算进分母不算进分子**：
 *  当成可用是给一条状态未知的渠道发绿灯，而这一栏存在的意义正是发现不可用。 */
export function channelHealth(rows: readonly ChannelRow[]): HealthRow {
  if (rows.length === 0) {
    return {
      label: "上游渠道",
      tone: "neutral",
      text: null,
      hint: "这次观测里没有渠道；采集任务跑起来后才有内容",
      href: "?tab=upstream",
    };
  }
  const usable = rows.filter((r) => r.tokenValid === true).length;
  const unknown = rows.filter((r) => r.tokenValid === null).length;
  const tone: HealthRow["tone"] = usable === rows.length ? "ok" : usable === 0 ? "bad" : "warn";
  return {
    label: "上游渠道",
    tone,
    text: `${usable} / ${rows.length} 可用`,
    hint:
      unknown > 0
        ? `其中 ${unknown} 条上游没给 token_valid，按不可用计——状态未知不等于可用`
        : "按渠道余额指标里的 token_valid 统计",
    href: "?tab=upstream",
  };
}

/** 「上游渠道」这一行的名字里最多列出的不可用渠道名个数。
 *  hint 是一行文字，列全了会把这一行撑爆；列出前几个足够运营认出是哪条。 */
const UNAVAILABLE_NAMES_SHOWN = 3;

/** 上游渠道 x / y 可用——按 Sub2API 渠道状态指标（sub2api.channels.status）
 *  统计，取代按渠道余额 token_valid 统计的旧口径（XM-OVERVIEW-UI）。
 *
 *  Sub2API 的上游账号是订阅型，没有钱包余额，channels.balance 永远是空
 *  数组——那条口径对这个平台早就不成立了，渠道健康要看的是这条 status
 *  指标。active 计入可用，**其余状态一律计入不可用**：不认识的状态不能
 *  默认当成可用，这一栏存在的意义正是发现不可用（与 channelHealth 对
 *  token_valid 未知的处理同一条纪律）。 */
export function channelStatusHealth(rows: readonly Sub2ApiChannelStatusRow[]): HealthRow {
  if (rows.length === 0) {
    return {
      label: "上游渠道",
      tone: "neutral",
      text: null,
      hint: "这次观测里没有渠道；采集任务跑起来后才有内容",
      href: "?tab=upstream",
    };
  }
  const usable = rows.filter((r) => r.status === "active").length;
  const unusable = rows.filter((r) => r.status !== "active");
  const tone: HealthRow["tone"] = usable === rows.length ? "ok" : usable === 0 ? "bad" : "warn";
  const names = unusable.slice(0, UNAVAILABLE_NAMES_SHOWN).map((r) => r.name || r.channelId || "未命名渠道");
  const namesText = unusable.length > UNAVAILABLE_NAMES_SHOWN ? `${names.join("、")} 等` : names.join("、");
  return {
    label: "上游渠道",
    tone,
    text: `${usable} / ${rows.length} 可用`,
    hint:
      unusable.length > 0
        ? `${unusable.length} 个渠道不可用：${namesText}`
        : "按渠道状态指标统计，全部渠道可用",
    href: "?tab=upstream",
  };
}

/** 「上游渠道」这一行的完整判据（XM-OVERVIEW-UI）：
 *  1. 优先用渠道状态指标计算 x / y——它是真实、逐渠道更新的数据源；
 *  2. 只有状态观测也缺（不存在、从未初始化，或没有逐渠道记录）时，才落回
 *     旧的渠道余额口径（channelHealth，按 token_valid 统计）；
 *  3. 两者都没有渠道时才是「未接入」；
 *  4. 状态可用、但余额观测**恰好也**返回了渠道时，不丢掉那条信息——
 *     订阅型账号本不该有余额，如果它还是给出来了，值得在 hint 里提一句，
 *     而不是悄悄吞掉。
 *
 *  这个顺序不是巧合：Sub2API 的余额指标已知永远为空，如果状态缺失就直接判
 *  「未接入」，会让一个仍然可以从余额口径拿到（哪怕是旧口径）信息的环境
 *  显示得比实际更差。 */
export function sub2ApiChannelHealth(
  statusRows: readonly Sub2ApiChannelStatusRow[],
  balanceRows: readonly ChannelRow[],
): HealthRow {
  if (statusRows.length > 0) {
    const row = channelStatusHealth(statusRows);
    return balanceRows.length > 0
      ? { ...row, hint: `${row.hint}（渠道余额观测另有 ${balanceRows.length} 条记录，供参考）` }
      : row;
  }
  if (balanceRows.length > 0) return channelHealth(balanceRows);
  return {
    label: "上游渠道",
    tone: "neutral",
    text: null,
    hint: "上游渠道状态与余额均未观测到；采集任务跑起来后才有内容",
    href: "?tab=upstream",
  };
}

/** 订阅账号的状态计数。
 *
 *  原型画的是「4 正常 · 1 冷却 · 1 疑似封禁」，而登记簿只有 active / disabled
 *  两个状态——**冷却与疑似封禁在平台侧根本不存在**。所以这里只报登记簿有的，
 *  并在 hint 里说清缺的那两档要等什么，不拿 disabled 去冒充「疑似封禁」。 */
export function subscriptionHealth(
  accounts: readonly UpstreamAccountItem[],
  platform: string,
): HealthRow {
  const mine = accounts.filter(
    (a) =>
      a.access_method === "subscription_account" &&
      (a.platform_id === platform || a.platform_id === ""),
  );
  if (mine.length === 0) {
    return {
      label: "订阅账号",
      tone: "neutral",
      text: null,
      hint: "登记簿里这个平台还没有订阅型上游账号",
      href: "?tab=suppliers",
    };
  }
  const active = mine.filter((a) => a.status === "active").length;
  const disabled = mine.length - active;
  return {
    label: "订阅账号",
    tone: disabled > 0 ? "warn" : "ok",
    text: disabled > 0 ? `${active} 正常 · ${disabled} 停用` : `${active} 正常`,
    hint: "登记簿只有启用/停用两个状态；原型画的「冷却」「疑似封禁」需要上游账号健康探针（M1.5）",
    href: "?tab=suppliers",
  };
}

/** 连接状态。
 *
 *  原型这一行写死「只读凭据未配置」。真实判据用**指标来源是不是演示实例**:
 *  那正是「凭据还没配、现在看到的是样例」的可观测形态（lib/demoData 同一条判据）。 */
export function connectionHealth(demo: boolean, hasMetrics: boolean): HealthRow {
  if (!hasMetrics) {
    return {
      label: "连接状态",
      tone: "bad",
      text: "未采集到任何指标",
      hint: "这个平台的采集任务还没有产出观测；先在「连接与凭据」里确认连接配置",
      href: "?tab=creds",
    };
  }
  return demo
    ? {
        label: "连接状态",
        tone: "bad",
        text: "只读凭据未配置",
        hint: "指标来自演示（Fake）连接器，不是真实运营数据",
        href: "?tab=creds",
      }
    : {
        label: "连接状态",
        tone: "ok",
        text: "只读连接已配置",
        hint: "指标来自真实实例",
        href: "?tab=creds",
      };
}
