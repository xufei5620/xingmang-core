/** 金额、计数与比率的展示格式化。
 *
 *  宪法 13 条：金额禁止 Float，货币金额一律用整数最小单位。所以这里全程
 *  BigInt 整除 + 取余拼字符串，**没有一次除法是浮点的**——`123456 / 100` 这种
 *  写法在别的地方也许无害，在金额上就是错的开始。
 *
 *  比率（错误率，ppm）走同一条纪律、同一套整除取余的手法，所以放在同一个
 *  文件里：把「显示用的除法一律整数」这件事集中在一处，比按「这是钱还是比率」
 *  分成两个文件更不容易漏。 */

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

/** ppm（百万分之一）整数 → 百分比展示字符串，两位小数。
 *
 *  例：`1200` → `"0.12%"`，`187500` → `"18.75%"`，`0` → `"0.00%"`。
 *
 *  **全程整数运算，不经过一次浮点。** 写成 `(ppm / 10000).toFixed(2)` 看着更短，
 *  但那正是契约层用 ppm 整数要避免的东西（见 connectors/newapi 的
 *  ErrorRatePPM 注释）：数据在类型上守住了不用浮点，显示层再把它丢回 float
 *  等于把纪律守到最后一米又松手。整数位取 `ppm / 10000`，两位小数取
 *  `ppm % 10000 / 100` 再左补零。
 *
 *  取值不是合法整数时返回「数值异常」，与金额同一条处理：宁可显眼地说不对，
 *  也不能悄悄显示一个算错的比率。 */
export function formatErrorRatePPM(value: unknown): string {
  const ppm = toIntegerValue(value);
  if (ppm === null) return INVALID_VALUE_TEXT;

  const negative = ppm < 0n;
  const abs = negative ? -ppm : ppm;
  const whole = abs / 10_000n;
  // 截断而不是四舍五入：错误率显示成比实际**低**的值是危险的方向，
  // 而进位只会在 x.xx5 上把它抬高。截断至少不会让 4.999% 显示成 5.00%
  // 从而看着刚好压在阈值上。
  const fraction = (abs % 10_000n) / 100n;
  const body = `${groupDigits(whole.toString())}.${fraction.toString().padStart(2, "0")}%`;
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

/** 把「任意标度的整数最小单位」按币种的最小单位格式化（XM-0037d）。
 *
 *  成本核算这条线的金额是 **scale-6 微单位**（`money.MicroScale`，设计稿 §2.4），
 *  而 `formatMinorUnits` 按的是**币种自己的**最小单位小数位（USD/CNY 是 2）。
 *  把前者直接喂给后者，$29.99 会显示成 $299,900.00——**差一万倍，且不报错**。
 *
 *  所以后端在每个金额上都带了 `scale`，前端据此降标度，而不是把 6 硬编码在
 *  某个格式化函数里：那个 6 一旦与后端漂开，所有金额都会静静地错着。
 *
 *  **全程 BigInt，一次浮点都不经过**（宪法 13 条）。降标度用半进
 *  （away from zero），与后端 `money.Rescale` 是同一条舍入规则——
 *  两边用不同的舍入方式，同一笔钱在页面上和在台账里会差一分。
 *
 *  scale 不是合法非负整数、或币种未登记时，退回 `formatMinorUnits` 的既有行为
 *  （「数值异常」/「金额单位未知」）：宁可显眼地说不对，也不能悄悄显示一个算错的数。 */
export function formatScaledMinorUnits(
  minorUnits: unknown,
  currency: string,
  scale: unknown,
): string {
  const value = toIntegerValue(minorUnits);
  if (value === null) return INVALID_VALUE_TEXT;

  const from = toIntegerValue(scale);
  if (from === null || from < 0n || from > 18n) return INVALID_VALUE_TEXT;

  const to = currencyExponent((currency || "").toUpperCase());
  // 币种未登记时不猜小数位：交给 formatMinorUnits 去说「金额单位未知」，
  // 原样把整数给出来，而不是先按一个猜出来的标度换算一遍。
  if (to === null) return formatMinorUnits(value, currency);

  const target = BigInt(to);
  if (from === target) return formatMinorUnits(value, currency);
  if (from < target) {
    return formatMinorUnits(value * 10n ** (target - from), currency);
  }

  const divisor = 10n ** (from - target);
  const negative = value < 0n;
  const abs = negative ? -value : value;
  // +divisor/2 就是整数域里的半进（away from zero）
  const scaled = (abs + divisor / 2n) / divisor;
  return formatMinorUnits(negative ? -scaled : scaled, currency);
}
