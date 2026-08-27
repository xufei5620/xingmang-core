import { describe, expect, it } from "vitest";
import {
  currencyExponent,
  formatCount,
  formatMinorUnits,
  groupDigits,
  INVALID_VALUE_TEXT,
  toIntegerValue,
  formatErrorRatePPM,
} from "./money";

describe("formatMinorUnits：整数最小单位 → 展示", () => {
  it("两位小数币种按 100 分拆，且千分位正确", () => {
    expect(formatMinorUnits(123456, "CNY")).toBe("¥1,234.56");
    expect(formatMinorUnits(0, "CNY")).toBe("¥0.00");
    expect(formatMinorUnits(5, "CNY")).toBe("¥0.05");
    expect(formatMinorUnits(99, "CNY")).toBe("¥0.99");
    expect(formatMinorUnits(100, "CNY")).toBe("¥1.00");
    expect(formatMinorUnits(123456789012, "CNY")).toBe("¥1,234,567,890.12");
  });

  it("负数把符号放在币种符号之前", () => {
    expect(formatMinorUnits(-50, "CNY")).toBe("-¥0.50");
    expect(formatMinorUnits(-123456, "USD")).toBe("-$1,234.56");
  });

  it("零小数位币种不加小数点", () => {
    expect(formatMinorUnits(1234, "JPY")).toBe("JP¥1,234");
    expect(formatMinorUnits(0, "KRW")).toBe("₩0");
  });

  it("未知/空币种 fail closed：不猜小数位，原样给出最小单位数值（Codex #8）", () => {
    // 以前这里断言 "XYZ 1.00"，把「默认两位」这个猜测固化成了契约。
    // 可 BHD/KWD 是三位、JPY/KRW 是零位，猜错的结果是金额显示错 10~100 倍，
    // 而且错得非常像真的——所以现在明说单位未知，把原始值端出来
    expect(formatMinorUnits(100, "XYZ")).toBe("XYZ 100（最小单位，金额单位未知）");
    expect(formatMinorUnits(100, "")).toBe("100（最小单位，金额单位未知）");
    expect(formatMinorUnits(-1234567, "XYZ")).toBe("-XYZ 1,234,567（最小单位，金额单位未知）");
  });

  it("币种代码大小写不敏感", () => {
    expect(formatMinorUnits(123456, "cny")).toBe("¥1,234.56");
  });

  it("超出安全整数范围或非整数一律显式报异常，不显示算错的金额", () => {
    expect(formatMinorUnits(1.5, "CNY")).toBe(INVALID_VALUE_TEXT);
    expect(formatMinorUnits(Number.MAX_SAFE_INTEGER + 2, "CNY")).toBe(INVALID_VALUE_TEXT);
    expect(formatMinorUnits(undefined, "CNY")).toBe(INVALID_VALUE_TEXT);
    expect(formatMinorUnits(null, "CNY")).toBe(INVALID_VALUE_TEXT);
    expect(formatMinorUnits("abc", "CNY")).toBe(INVALID_VALUE_TEXT);
  });

  it("BigInt 与数字串直接可用，超大额不丢精度", () => {
    expect(formatMinorUnits(90071992547409919n, "CNY")).toBe("¥900,719,925,474,099.19");
    expect(formatMinorUnits("90071992547409919", "CNY")).toBe("¥900,719,925,474,099.19");
  });
});

describe("formatCount", () => {
  it("加千分位", () => {
    expect(formatCount(0)).toBe("0");
    expect(formatCount(999)).toBe("999");
    expect(formatCount(1000)).toBe("1,000");
    expect(formatCount(1234567)).toBe("1,234,567");
    expect(formatCount(-1234)).toBe("-1,234");
  });
  it("非整数报异常", () => {
    expect(formatCount(3.14)).toBe(INVALID_VALUE_TEXT);
  });
});

describe("底层工具", () => {
  it("groupDigits 三位一组", () => {
    expect(groupDigits("1")).toBe("1");
    expect(groupDigits("1234")).toBe("1,234");
    expect(groupDigits("1234567")).toBe("1,234,567");
  });
  it("currencyExponent 认不出就返回 null，不兜底成两位", () => {
    expect(currencyExponent("CNY")).toBe(2);
    expect(currencyExponent("JPY")).toBe(0);
    expect(currencyExponent("ZZZ")).toBeNull();
    expect(currencyExponent("")).toBeNull();
  });
  it("toIntegerValue 只接受安全整数", () => {
    expect(toIntegerValue(42)).toBe(42n);
    expect(toIntegerValue("42")).toBe(42n);
    expect(toIntegerValue(4.2)).toBeNull();
    expect(toIntegerValue({})).toBeNull();
  });
});

describe("错误率（ppm → 百分比）", () => {
  it("按两位小数展示，1% = 10000 ppm", () => {
    expect(formatErrorRatePPM(0)).toBe("0.00%");
    expect(formatErrorRatePPM(1_200)).toBe("0.12%");
    expect(formatErrorRatePPM(10_000)).toBe("1.00%");
    expect(formatErrorRatePPM(187_500)).toBe("18.75%");
    expect(formatErrorRatePPM(1_000_000)).toBe("100.00%");
  });

  it("小到 1 ppm 也不被抹成 0——ppm 的分辨率正是为此选的", () => {
    // 100 ppm = 0.01%，一个健康网关的常见量级。用百分之整数表达时它与 0
    // 无法区分，那正是契约层选 ppm 的理由。
    expect(formatErrorRatePPM(100)).toBe("0.01%");
    // 低于两位小数能表达的部分被截掉，但这不影响「它不是 0」这个判断——
    // 1 ppm 显示成 0.00% 是展示精度的下限，不是数据被抹平
    expect(formatErrorRatePPM(1)).toBe("0.00%");
  });

  it("截断而不是四舍五入：错误率不能显示得比实际低", () => {
    // 49999 ppm = 4.9999%，进位会显示成 5.00%，看着刚好压在 5% 的判据上
    expect(formatErrorRatePPM(49_999)).toBe("4.99%");
    expect(formatErrorRatePPM(50_000)).toBe("5.00%");
  });

  it("非整数与非法值给「数值异常」，不悄悄显示一个算错的比率", () => {
    expect(formatErrorRatePPM(1.5)).toBe(INVALID_VALUE_TEXT);
    expect(formatErrorRatePPM("abc")).toBe(INVALID_VALUE_TEXT);
    expect(formatErrorRatePPM(null)).toBe(INVALID_VALUE_TEXT);
    expect(formatErrorRatePPM(undefined)).toBe(INVALID_VALUE_TEXT);
    // 超过安全整数范围的 JSON 数字已经丢了精度，不能拿来显示
    expect(formatErrorRatePPM(Number.MAX_SAFE_INTEGER + 2)).toBe(INVALID_VALUE_TEXT);
  });

  it("负值不被吞掉：它是上游给错了，该看得出来", () => {
    expect(formatErrorRatePPM(-1_200)).toBe("-0.12%");
  });
});
