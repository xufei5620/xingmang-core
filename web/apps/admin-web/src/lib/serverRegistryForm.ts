/** 服务器登记簿表单的枚举选项、金额换算与到期日警示（XM-SERVER0）。
 *
 *  拍板「服务器只做记录」：本文件不含任何探测/采集逻辑，只是表单与展示层
 *  的纯函数集合，供 assets/suppliers/domains/service-notes 四个面板共用。 */

import { currencyExponent } from "./money";

export const ASSET_STATUS_OPTIONS = [
  { value: "active", label: "使用中（active）" },
  { value: "retired", label: "已退役（retired）" },
  { value: "planned", label: "规划中（planned）" },
];

export const BILLING_CYCLE_OPTIONS = [
  { value: "", label: "未登记" },
  { value: "monthly", label: "按月" },
  { value: "quarterly", label: "按季" },
  { value: "yearly", label: "按年" },
];

export const CERT_SOURCE_OPTIONS = [
  { value: "", label: "未登记" },
  { value: "acme", label: "ACME 自动续期" },
  { value: "managed", label: "供应商 / CDN 托管" },
  { value: "manual", label: "手工上传" },
];

export const SERVICE_KIND_OPTIONS = [
  { value: "container", label: "容器（container）" },
  { value: "systemd", label: "systemd 单元" },
  { value: "process", label: "裸进程（process）" },
];

/** 与后端 money.CurrencyScale 逐字对齐的已登记币种清单（两位小数 8 种 +
 *  零位小数 3 种）。前端不猜未登记币种的最小单位小数位（宪法 13 条）。 */
export const CURRENCY_OPTIONS = [
  { value: "", label: "—" },
  { value: "USD", label: "USD 美元" },
  { value: "CNY", label: "CNY 人民币" },
  { value: "EUR", label: "EUR 欧元" },
  { value: "GBP", label: "GBP 英镑" },
  { value: "HKD", label: "HKD 港币" },
  { value: "AUD", label: "AUD 澳元" },
  { value: "CAD", label: "CAD 加元" },
  { value: "SGD", label: "SGD 新加坡元" },
  { value: "JPY", label: "JPY 日元（无小数位）" },
  { value: "KRW", label: "KRW 韩元（无小数位）" },
  { value: "VND", label: "VND 越南盾（无小数位）" },
];

/** 30 天到期警示窗口——概览统计与表格到期日标色共用同一个数字，
 *  两处漂开的话「表格标红的行」与「概览数的那个数」就会对不上。 */
export const EXPIRY_WARNING_DAYS = 30;

/** 判断一个 "YYYY-MM-DD" 到期日是否落在警示窗口内（含已过期——已经逾期
 *  比「还有 30 天」更需要关注，不能只对着未来那一侧判断）。
 *  空串（未登记）永远不警示：「没登记到期日」与「登记了但很紧急」不是一回事。 */
export function isExpiringSoon(dateText: string, days: number = EXPIRY_WARNING_DAYS, now: Date = new Date()): boolean {
  if (!dateText) return false;
  const target = new Date(`${dateText}T00:00:00Z`);
  if (Number.isNaN(target.getTime())) return false;
  const threshold = new Date(now.getTime() + days * 24 * 60 * 60 * 1000);
  return target.getTime() <= threshold.getTime();
}

export interface MoneyParseResult {
  ok: boolean;
  /** 解析成功时的整数最小单位字符串；输入为空时为 undefined（= 不登记金额）。 */
  minor?: string;
  error?: string;
}

/** 把用户输入的十进制金额（如 "99.90"）按币种自然标度换算成整数最小单位
 *  字符串（USD "99.90" → "9990"，JPY "1000" → "1000"）。
 *
 *  与后端 server.optionalMinorParam 的纪律对应：后端只接受**纯整数**，
 *  标度换算必须在这一层做完——静默接受一个换算错的标度，会让金额差一到
 *  两个数量级且不报错（宪法 13 条）。 */
export function parseDecimalToMinorUnits(raw: string, currency: string): MoneyParseResult {
  const trimmed = raw.trim();
  if (trimmed === "") return { ok: true, minor: undefined };
  const exponent = currencyExponent(currency);
  if (exponent === null) {
    return { ok: false, error: `未登记币种「${currency || "（未选择）"}」，无法换算最小单位` };
  }
  const match = /^(-?)(\d+)(?:\.(\d+))?$/.exec(trimmed);
  if (!match) {
    return { ok: false, error: "必须是十进制数字，如 99.90" };
  }
  const [, sign, whole, fracRaw = ""] = match;
  if (sign === "-") {
    return { ok: false, error: "月付成本不能为负" };
  }
  if (fracRaw.length > exponent) {
    return { ok: false, error: exponent === 0 ? `${currency} 没有小数位` : `最多 ${exponent} 位小数` };
  }
  const frac = fracRaw.padEnd(exponent, "0");
  const digits = `${whole}${frac}`.replace(/^0+(?=\d)/, "") || "0";
  return { ok: true, minor: digits };
}

/** 把整数最小单位字符串换算回十进制展示文本，供编辑表单回填
 *  （与 parseDecimalToMinorUnits 互为逆运算）。 */
export function minorUnitsToDecimal(minor: string, currency: string): string {
  const exponent = currencyExponent(currency);
  if (exponent === null || exponent === 0) return minor;
  const negative = minor.startsWith("-");
  const digits = negative ? minor.slice(1) : minor;
  const padded = digits.padStart(exponent + 1, "0");
  const whole = padded.slice(0, padded.length - exponent);
  const frac = padded.slice(padded.length - exponent);
  return `${negative ? "-" : ""}${whole}.${frac}`;
}
