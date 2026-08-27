import { describe, expect, it } from "vitest";
import {
  currencyExponent,
  formatCount,
  formatMinorUnits,
  groupDigits,
  INVALID_VALUE_TEXT,
  toIntegerValue,
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
