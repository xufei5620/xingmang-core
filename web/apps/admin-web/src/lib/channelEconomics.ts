import type { ChannelSummary } from "../api/finance";

/** 按 id 建索引，供渠道表逐行查。
 *
 *  接受 `undefined` 并返回空索引，而不是让调用方各自判空:
 *  「没取到汇总」与「取到了但这一行不在里面」在**显示上是同一件事**（未接入）,
 *  在代码里也必须只有一条路径——两条路径迟早会有一条忘了标注。 */
export function indexChannelSummaries(
  summaries: readonly ChannelSummary[] | undefined,
): Map<string, ChannelSummary> {
  const index = new Map<string, ChannelSummary>();
  for (const s of summaries ?? []) index.set(s.id, s);
  return index;
}

/** 毛利率（定点十进制字符串）→ 百分比展示文本。
 *
 *  **后端算好、前端只格式化**（XM-0037d 的 `gross_margin`）。前端不自己
 *  用毛利除以收入再算一遍：两处算法一旦在舍入或分母口径上漂开,
 *  页面上的百分比与台账里的就对不上，而两边看起来都完全正常。
 *
 *  `null` = 后端说给不出（收入 ≤ 0 时没有毛利率），返回 null 由调用方
 *  显示「—」而不是 0%——0% 会被读成「做了生意但一分没赚」。
 *
 *  只做定点字符串 → 百分号的搬运，全程字符串，不经过一次浮点（宪法 13 条）:
 *  小数点右移两位就是百分数。 */
export function formatGrossMargin(raw: string | null): string | null {
  if (raw === null) return null;
  const text = raw.trim();
  if (!text) return null;

  const negative = text.startsWith("-");
  const body = negative ? text.slice(1) : text.startsWith("+") ? text.slice(1) : text;
  const dot = body.indexOf(".");
  const intPart = dot < 0 ? body : body.slice(0, dot);
  const fracPart = dot < 0 ? "" : body.slice(dot + 1);
  if (!/^\d*$/.test(intPart) || !/^\d*$/.test(fracPart) || intPart + fracPart === "") {
    // 认不出来就原样端出去 + 一个百分号是**编数据**：宁可显眼地说不对
    return null;
  }

  // 右移两位 = ×100。位数不够就补零，多出来的留作小数
  const digits = intPart + fracPart.padEnd(2, "0");
  const shiftedInt = digits.slice(0, intPart.length + 2).replace(/^0+(?=\d)/, "");
  const shiftedFrac = digits.slice(intPart.length + 2).replace(/0+$/, "");
  const shown = shiftedFrac ? `${shiftedInt}.${shiftedFrac}` : shiftedInt;
  return `${negative ? "-" : ""}${shown}%`;
}

/** 毛利率是不是负的。标红用，不参与计算。 */
export function isNegativeMargin(raw: string | null): boolean {
  return raw !== null && raw.trim().startsWith("-");
}

/** 渠道经营三列在**被管平台的渠道表**上还接不上的原因。
 *
 *  XM-0037d 的端点已经上线（`GET /api/v1/finance/channels/summary`),
 *  但它一行 = 一个**上游账号**（后端 §8.5：两个汇总端点同粒度），
 *  而渠道管理表一行 = 被管平台自己的一条渠道。两边的 id 互不认识:
 *  按 id join 不会报错，只会一条都匹配不上，毛利列全空还看着像「后端没数据」。
 *
 *  缺的是「平台渠道 ↔ 上游账号」的对应关系。登记簿今天只有
 *  「上游令牌 ↔ 我方账号」的令牌映射，那是另一个维度。
 *
 *  写成常量而不是在两个面板里各写一遍：两处文案漂开之后，
 *  Sub2API 说「等端点」而 NewAPI 说「暂无数据」，读的人会以为那是两件事。 */
export const ECONOMICS_PENDING_NOTE =
  "汇总端点按上游账号出行，与被管平台自己的渠道 id 对不上；缺「平台渠道 ↔ 上游账号」的对应关系";

/** 上游管理页用得上汇总的那一面：登记簿的行 id 就是汇总的行 id。
 *
 *  两边都来自 `finance.upstream_account` 的主键，所以**这一侧可以直接 join**——
 *  与上面渠道表的情形正好相反，值得写下来免得下一个人以为两处都接不上。 */
export const UPSTREAM_JOIN_NOTE =
  "上游管理页的行 id 与汇总端点同源（都是 finance.upstream_account 的主键），可直接对应";
