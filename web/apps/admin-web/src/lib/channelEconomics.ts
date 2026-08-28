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

/** 上游管理页用得上汇总的那一面：登记簿的行 id 就是汇总的行 id。
 *
 *  两边都来自 `finance.upstream_account` 的主键，所以**这一侧可以直接 join**——
 *  与上面渠道表的情形正好相反，值得写下来免得下一个人以为两处都接不上。 */
export const UPSTREAM_JOIN_NOTE =
  "上游管理页的行 id 与汇总端点同源（都是 finance.upstream_account 的主键），可直接对应";
