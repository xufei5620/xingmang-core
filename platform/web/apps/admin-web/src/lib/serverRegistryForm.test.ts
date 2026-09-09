import { describe, expect, it } from "vitest";
import {
  isExpiringSoon,
  minorUnitsToDecimal,
  parseDecimalToMinorUnits,
} from "./serverRegistryForm";

describe("isExpiringSoon", () => {
  const now = new Date("2026-08-31T00:00:00Z");

  it("空串（未登记）永远不警示", () => {
    expect(isExpiringSoon("", 30, now)).toBe(false);
  });

  it("已过期算需要关注，不只是未来那一侧", () => {
    expect(isExpiringSoon("2026-08-20", 30, now)).toBe(true);
  });

  it("落在窗口内", () => {
    expect(isExpiringSoon("2026-09-10", 30, now)).toBe(true);
  });

  it("超出窗口不警示", () => {
    expect(isExpiringSoon("2026-10-15", 30, now)).toBe(false);
  });

  it("解析不出的日期不警示（不替坏数据背书）", () => {
    expect(isExpiringSoon("not-a-date", 30, now)).toBe(false);
  });
});

describe("parseDecimalToMinorUnits", () => {
  it("空输入 = 不登记金额", () => {
    const r = parseDecimalToMinorUnits("", "USD");
    expect(r.ok).toBe(true);
    expect(r.minor).toBeUndefined();
  });

  it("USD 两位小数：99.90 → 9990", () => {
    const r = parseDecimalToMinorUnits("99.90", "USD");
    expect(r).toEqual({ ok: true, minor: "9990" });
  });

  it("USD 整数：100 → 10000", () => {
    const r = parseDecimalToMinorUnits("100", "USD");
    expect(r).toEqual({ ok: true, minor: "10000" });
  });

  it("JPY 零位小数：1000 → 1000", () => {
    const r = parseDecimalToMinorUnits("1000", "JPY");
    expect(r).toEqual({ ok: true, minor: "1000" });
  });

  it("JPY 带小数点应被拒——JPY 没有小数位", () => {
    const r = parseDecimalToMinorUnits("1000.5", "JPY");
    expect(r.ok).toBe(false);
  });

  it("小数位超过币种标度应被拒", () => {
    const r = parseDecimalToMinorUnits("1.234", "USD");
    expect(r.ok).toBe(false);
  });

  it("负数应被拒——月付成本不能为负", () => {
    const r = parseDecimalToMinorUnits("-5", "USD");
    expect(r.ok).toBe(false);
  });

  it("非十进制文本应被拒", () => {
    const r = parseDecimalToMinorUnits("abc", "USD");
    expect(r.ok).toBe(false);
  });

  it("科学计数法应被拒（正则不认识 e）", () => {
    const r = parseDecimalToMinorUnits("1e4", "USD");
    expect(r.ok).toBe(false);
  });

  it("未登记币种应被拒，不猜标度", () => {
    const r = parseDecimalToMinorUnits("10", "XYZ");
    expect(r.ok).toBe(false);
  });

  it("0 是合法金额（免费）", () => {
    const r = parseDecimalToMinorUnits("0", "USD");
    expect(r).toEqual({ ok: true, minor: "0" });
  });
});

describe("minorUnitsToDecimal 与 parseDecimalToMinorUnits 互为逆运算", () => {
  it("USD", () => {
    expect(minorUnitsToDecimal("9990", "USD")).toBe("99.90");
    expect(parseDecimalToMinorUnits(minorUnitsToDecimal("9990", "USD"), "USD").minor).toBe("9990");
  });

  it("JPY（零位小数，原样返回）", () => {
    expect(minorUnitsToDecimal("1000", "JPY")).toBe("1000");
  });
});
