/** 金额与计数的展示格式化。
 *
 *  宪法 13 条：金额禁止 Float，货币金额一律用整数最小单位。所以这里全程
 *  BigInt 整除 + 取余拼字符串，**没有一次除法是浮点的**——`123456 / 100` 这种
 *  写法在别的地方也许无害，在金额上就是错的开始。 */

/** 最小单位指数：1 个货币单位 = 10^exponent 个最小单位。 */
const CURRENCY_EXPONENT: Record<string, number> = {
  CNY: 2,
  USD: 2,
  EUR: 2,
  GBP: 2,
  HKD: 2,
  // 日元与韩元没有小数位，硬按 2 位除会把金额缩小 100 倍
  JPY: 0,
  KRW: 0,
};

/** 展示符号。CNY 与 JPY 共用 ¥，所以日元加国别前缀区分。 */
const CURRENCY_SYMBOL: Record<string, string> = {
  CNY: "¥",
  USD: "$",
  EUR: "€",
  GBP: "£",
  HKD: "HK$",
  JPY: "JP¥",
  KRW: "₩",
};

/** 值明显不是整数最小单位时的显示文案。
 *  宁可显眼地写「数值异常」，也不能悄悄显示一个算错的金额。 */
export const INVALID_VALUE_TEXT = "数值异常";

/** 认不出币种时的显示文案。
 *
 *  这里以前默认按 2 位小数猜（`?? 2`），碰上 JPY 之外任何零小数位或三小数位
 *  币种（BHD/KWD 是 3 位）就会把金额显示错 10~100 倍，而且错得非常像真的。
 *  猜不出就 fail closed：说清「单位未知」，把原始最小单位数值原样端出来，
 *  让人自己拿去对账，绝不替他做一个没同意的换算（宪法 13 条 / Codex #8）。 */
export const UNKNOWN_CURRENCY_TEXT = "金额单位未知";

/** 三位一组加千分位。自己写而不用 Intl：Intl 只吃 number，
 *  把 BigInt 转回 number 就把「禁止 Float」这条规矩绕过去了。 */
export function groupDigits(digits: string): string {
  const out: string[] = [];
  for (let i = digits.length; i > 0; i -= 3) {
    out.unshift(digits.slice(Math.max(0, i - 3), i));
  }
  return out.join(",");
}

/** 把 JSON 里的值收敛成 BigInt；不是安全整数就返回 null。
 *
 *  JSON 数字进 JS 一律是 float64，超过 2^53 就已经丢精度了——那种值不能拿来
 *  当金额显示，返回 null 让调用方走「数值异常」分支。 */
export function toIntegerValue(value: unknown): bigint | null {
  if (typeof value === "bigint") return value;
  if (typeof value === "number") {
    return Number.isSafeInteger(value) ? BigInt(value) : null;
  }
  if (typeof value === "string" && /^-?\d+$/.test(value)) return BigInt(value);
  return null;
}

/** 币种的最小单位指数；**认不出就返回 null**，调用方必须自己处理这一支。
 *  返回 2 兜底等于替所有未知币种编了一个小数位数。 */
export function currencyExponent(currency: string): number | null {
  return CURRENCY_EXPONENT[currency.toUpperCase()] ?? null;
}

/** 整数最小单位 → 展示字符串，例如 (123456, "CNY") → "¥1,234.56"。
 *
 *  未知/空币种不换算：显示原始最小单位数值并标注「金额单位未知」，
 *  例如 (100, "XYZ") → "XYZ 100（最小单位，金额单位未知）"。 */
export function formatMinorUnits(minorUnits: unknown, currency: string): string {
  const value = toIntegerValue(minorUnits);
  if (value === null) return INVALID_VALUE_TEXT;

  const code = (currency || "").toUpperCase();
  const exponent = currencyExponent(code);
  const negative = value < 0n;
  const abs = negative ? -value : value;

  if (exponent === null) {
    const raw = `${code ? `${code} ` : ""}${groupDigits(abs.toString())}（最小单位，${UNKNOWN_CURRENCY_TEXT}）`;
    return negative ? `-${raw}` : raw;
  }

  const divisor = 10n ** BigInt(exponent);
  const whole = abs / divisor;
  const fraction = abs % divisor;

  let text = groupDigits(whole.toString());
  if (exponent > 0) text += `.${fraction.toString().padStart(exponent, "0")}`;

  const symbol = CURRENCY_SYMBOL[code];
  const body = symbol ? `${symbol}${text}` : `${code} ${text}`;
  return negative ? `-${body}` : body;
}

/** 整数计数 → 带千分位的展示字符串（人数、单数、渠道数）。 */
export function formatCount(value: unknown): string {
  const n = toIntegerValue(value);
  if (n === null) return INVALID_VALUE_TEXT;
  const negative = n < 0n;
  const digits = groupDigits((negative ? -n : n).toString());
  return negative ? `-${digits}` : digits;
}
