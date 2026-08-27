import { describe, expect, it } from "vitest";
import { formatMoneyItem } from "./UpstreamAccountDetail";

describe("登记簿金额的展示", () => {
  it("**按响应里的 scale 降标度**，不硬编码", () => {
    // 成本线是 scale-6 微单位，币种最小单位是 2 位。把前者当后者显示，
    // ¥1,450.00 会变成 ¥14,500,000.00 —— 差一万倍，且不报错
    expect(formatMoneyItem({ amount_minor: "1450000000", currency: "CNY", scale: 6 })).toBe(
      "¥1,450.00",
    );
    expect(formatMoneyItem({ amount_minor: "145000", currency: "CNY", scale: 2 })).toBe(
      "¥1,450.00",
    );
  });

  it("大额不丢精度——中途转 Number 就会", () => {
    // 2^53 + 1 个最小单位：Number 走一遍会变成 2^53
    expect(formatMoneyItem({ amount_minor: "9007199254740993", currency: "CNY", scale: 2 })).toBe(
      "¥90,071,992,547,409.93",
    );
  });

  it("币种符号交给公共表，不在这里各写一份", () => {
    expect(formatMoneyItem({ amount_minor: "150000", currency: "USD", scale: 2 })).toBe(
      "$1,500.00",
    );
    // 零小数位币种照 0 位算：硬按 2 位除会把日元缩小 100 倍
    expect(formatMoneyItem({ amount_minor: "1500", currency: "JPY", scale: 0 })).toBe("JP¥1,500");
  });

  it("负数保留符号", () => {
    expect(formatMoneyItem({ amount_minor: "-12345", currency: "CNY", scale: 2 })).toBe("-¥123.45");
  });

  it("**没有这个金额**给「—」：批次没算出摊销与摊销为零是两件事", () => {
    expect(formatMoneyItem(null)).toBe("—");
    expect(formatMoneyItem(undefined)).toBe("—");
    expect(formatMoneyItem({ amount_minor: "", currency: "CNY", scale: 2 })).toBe("—");
  });

  it("**值的形状不对**说「数值异常」，与「没有这个金额」分开", () => {
    // 压成同一个「—」会把「后端给了个算不了的值」这条要有人看一眼的信号盖掉；
    // 宁可显眼地说不对，也不能悄悄显示一个算错的金额
    expect(formatMoneyItem({ amount_minor: "12.34", currency: "CNY", scale: 2 })).toBe("数值异常");
    // scale 缺失时后端映射层会给 NaN，同样要显眼地说不对
    expect(formatMoneyItem({ amount_minor: "100", currency: "CNY", scale: Number.NaN })).toBe(
      "数值异常",
    );
  });
});
