import {
  serviceUnitDefinitions,
  type ServiceUnitCodeWire,
} from "./eligibility-wire.generated";

// XM-INV-UNIT-DISPLAY. 用户端「按平台计算的开票资格」卡片里，切点前旧余额与
// 非现金额度以前直接渲染后端记账用的原始刻度，长成
// `30,179,629,498 SUB2_BALANCE_1E8`。那是给账本对账用的数，不是上游用户在自己
// 账号页上看到的余额。这里按契约把它换算成 `301.80`，原始数字与单位码退到 title
// 提示里，供出问题时逐位核对。
//
// 三条纪律：
//   1. 只换刻度，不换口径。换算后仍是「非现金 / 切点前」的源侧余额，不是人民币，
//      所以调用方不加 ¥ 也不加 $，并且必须把 note（来源口径）一起显示出来。
//   2. 不碰浮点。service_units 最长 78 位，Number 在 2^53 以上就开始丢位，而这里
//      是「不许丢位」的地方，所以全程 BigInt 整数运算。
//   3. 认不出来的照实说，不抛错。后端比这份 bundle 新一个版本是常态（RC106 的降级
//      原则），未知单位码退回「原始数字 + 单位码 +（未识别单位）」，页面照样出。

export interface ServiceUnitValue {
  serviceUnits: string;
  // 宽化成 string：契约里的两个码是已知集，但运行时可能收到这份 bundle 还不认识的
  // 码。窄联合会让「未识别」这条路在类型上不可达，也就等于没有这条路。
  unitCode: string | null;
}

export interface ServiceUnitDefinition {
  readonly code: ServiceUnitCodeWire;
  readonly divisor: string;
  readonly decimals: number;
  readonly displayLabel: string;
}

export interface ServiceUnitView {
  /** 主显示。换算成功时是 `301.80`，否则是原始数字（未识别时带上单位码）。 */
  amount: string;
  /** 副标签：换算成功时是来源口径，否则是说明为什么没换算的中文。 */
  note: string;
  /** 悬停提示：原始数字 + 单位码，供与上游账号页逐位核对。 */
  title: string;
  /** 是否真的按契约换算过。调用方据此决定要不要显示「折合」提示。 */
  converted: boolean;
}

// 现状文案，逐字保留：unitCode 为 null 说明这个来源还没有建立单位合同，
// 那与「换算不出来」是两件事，不能合并成一句。
export const unitContractPendingNote = "（单位合同待建立）";
export const unrecognisedUnitNote = "（未识别单位）";
export const unconvertibleAmountNote = "（数值异常，未换算）";

const wholeNumberPattern = /^[0-9]+$/;

export function serviceUnitDefinition(
  code: string | null,
): ServiceUnitDefinition | undefined {
  if (!code) return undefined;
  return serviceUnitDefinitions.find((unit) => unit.code === code);
}

function groupThousands(digits: string) {
  return digits.replace(/\B(?=(\d{3})+(?!\d))/g, ",");
}

/**
 * 把原始服务单位按契约除数换算成上游显示的余额数，全程 BigInt。
 *
 * 小数取舍用「四舍五入」（remainder*2 >= divisor 进一位），不是截断。派工单里
 * 建议截断以免显示得比账本多，但同一份派工单给出的两个已核实数据点都是四舍五入
 * 的结果：99,948,771,408 / 1e8 = 999.48771408，上游显示 999.49（截断会得 999.48）；
 * 30,179,629,498 / 1e8 = 301.79629498，派工单自己写的期望是 301.80（截断会得 301.79）。
 * 这两格的目的就是「让用户看到跟上游一样的数」，差一分钱会重新制造它要消除的困惑。
 * 而「显示得比账本多」在这里没有风险：这两格都标着**不可开票**，谁也支不走，
 * 它是对账信息不是可用额度。真正的可开票金额走 money()，那条路不经过这里。
 *
 * 换不出来时返回 null，绝不返回一个「差不多」的数。
 */
export function convertServiceUnits(
  serviceUnits: string,
  unit: ServiceUnitDefinition,
): string | null {
  if (!wholeNumberPattern.test(serviceUnits)) return null;
  if (!wholeNumberPattern.test(unit.divisor)) return null;
  if (!Number.isInteger(unit.decimals) || unit.decimals < 0 || unit.decimals > 8) {
    return null;
  }
  const divisor = BigInt(unit.divisor);
  if (divisor <= 0n) return null;
  const scale = 10n ** BigInt(unit.decimals);
  const numerator = BigInt(serviceUnits) * scale;
  const quotient = numerator / divisor;
  const remainder = numerator % divisor;
  const rounded = remainder * 2n >= divisor ? quotient + 1n : quotient;
  if (unit.decimals === 0) return groupThousands(rounded.toString());
  // padStart 处理进位后仍不足一位整数的情况（1 单位 / 1e8 -> 0.00）。
  const digits = rounded.toString().padStart(unit.decimals + 1, "0");
  const whole = digits.slice(0, digits.length - unit.decimals);
  const fraction = digits.slice(digits.length - unit.decimals);
  return `${groupThousands(whole)}.${fraction}`;
}

export function serviceUnitView(value: ServiceUnitValue): ServiceUnitView {
  const raw = wholeNumberPattern.test(value.serviceUnits)
    ? groupThousands(value.serviceUnits)
    : value.serviceUnits;
  if (!value.unitCode) {
    return {
      amount: raw,
      note: unitContractPendingNote,
      title: raw,
      converted: false,
    };
  }
  const title = `${raw} ${value.unitCode}`;
  const unit = serviceUnitDefinition(value.unitCode);
  if (!unit) {
    return { amount: title, note: unrecognisedUnitNote, title, converted: false };
  }
  const converted = convertServiceUnits(value.serviceUnits, unit);
  if (converted === null) {
    // 单位码认得，数字本身不合法。跟「未识别单位」分开说，否则排查的人会去查
    // 契约，而问题其实在这一行数据上。
    return { amount: title, note: unconvertibleAmountNote, title, converted: false };
  }
  return { amount: converted, note: unit.displayLabel, title, converted: true };
}
