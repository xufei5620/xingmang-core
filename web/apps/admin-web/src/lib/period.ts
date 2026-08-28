import type { PeriodBody, PeriodGranularity } from "../api/users";

/** 统计区间的展示口径（原型顶部的「统计区间」控件）。
 *
 *  ## 为什么这里只有展示，没有计算
 *
 *  「这一周是哪七天」由**服务端**算，前端只负责显示它回显的 `from`/`to`。
 *  前端自己再算一遍的话，两份实现在跨月那一周对不上的那天，没人说得清哪个
 *  是对的——而它们的产出都是一组合理的数字，界面上分辨不出来。
 *
 *  ## 为什么业务日不能用 `new Date()` 取
 *
 *  业务日按 CST +08:00 切（宪法 14 条）。浏览器本地时区在 UTC-5 时，
 *  `new Date().toISOString().slice(0, 10)` 会给出账面上的**昨天**，
 *  于是运营打开页面看到的「今天」是错的一天，而那一天的数字同样合理。
 *  所以「今天」一律不传，交给服务端解释（见 `listPlatformUsers`）。 */

/** 三个粒度按钮的文案。顺序即原型里的排列顺序。 */
export const GRANULARITY_OPTIONS: readonly { value: PeriodGranularity; label: string }[] = [
  { value: "day", label: "日" },
  { value: "week", label: "周" },
  { value: "month", label: "月" },
];

/** 粒度 → 「按日查看」这类后缀。 */
export function granularityLabel(g: PeriodGranularity): string {
  switch (g) {
    case "week":
      return "按周查看";
    case "month":
      return "按月查看";
    default:
      return "按日查看";
  }
}

/** 区间的一句话描述，对应原型的 `data-period-output`。
 *
 *  「日」只说那一天（`2026-08-27 · 按日查看`，与原型逐字一致）；
 *  「周」「月」把起止两天都说出来（`2026-08-24 ~ 2026-08-30 · 按周查看`）——
 *  原型只有日粒度的静态文案，但一个只写「按周查看」而不说是哪七天的标题，
 *  在跨月那一周会让人读错。 */
export function describePeriod(period: PeriodBody | undefined): string {
  if (period === undefined || period.day === "") return "—";
  const suffix = granularityLabel(period.granularity);
  if (period.granularity === "day" || period.from === period.to) {
    return `${period.day} · ${suffix}`;
  }
  if (period.from === "" || period.to === "") return `${period.day} · ${suffix}`;
  return `${period.from} ~ ${period.to} · ${suffix}`;
}

/** 校验粒度串。认不出的一律回落到 `day`。
 *
 *  这里**可以**静默回落，与服务端相反：粒度是从 URL 查询串来的，
 *  而 URL 是人能手改的。一个手抖改坏的 `?granularity=weekly` 让整页报错
 *  没有意义；服务端那一层仍然会拒绝拼错的值，纪律没有被放松。 */
export function parseGranularity(raw: string | null): PeriodGranularity {
  switch (raw) {
    case "week":
    case "month":
      return raw;
    default:
      return "day";
  }
}

/** 校验业务日串（严格 YYYY-MM-DD，且必须是真实存在的日期）。
 *
 *  认不出就返回空串 = 「用服务端的今天」。理由同 `parseGranularity`。
 *  严格是必要的：`2026-02-30` 能被 `new Date` 接受并悄悄变成 3 月 2 日，
 *  于是人选的日期和查出来的数对不上。 */
export function parseBusinessDay(raw: string | null): string {
  if (raw === null) return "";
  const text = raw.trim();
  if (!/^\d{4}-\d{2}-\d{2}$/.test(text)) return "";
  // 用 UTC 构造再比对回去：只有真实存在的日期才能原样往返。
  // 不用本地时区构造——那会让判定随浏览器所在时区改变。
  const parsed = new Date(`${text}T00:00:00Z`);
  if (Number.isNaN(parsed.getTime())) return "";
  return parsed.toISOString().slice(0, 10) === text ? text : "";
}

/** 覆盖率的一句话说明（区间合计的分母）。
 *
 *  合计只加得动「上游给得出流水」的那些用户。覆盖不全时这个数是**下界**，
 *  必须说出来——否则它和一个真的合计长得一模一样（宪法 12 条）。 */
export function describeCoverage(covered: number, total: number, complete: boolean): string {
  if (total <= 0) return "本区间没有符合条件的用户";
  if (complete) return `覆盖全部 ${total} 位用户`;
  return `只覆盖 ${covered}/${total} 位用户，实际金额不低于这个数`;
}
