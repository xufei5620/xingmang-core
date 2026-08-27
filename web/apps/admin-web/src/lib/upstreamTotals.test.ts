import { describe, expect, it } from "vitest";
import type { Money } from "../api/finance";
import { coveredCount, describeMissingTotal, sumMoney } from "./upstreamTotals";

function micro(amountMinor: string, currency = "CNY", scale = 6): Money {
  return { amountMinor, currency, scale };
}

describe("金额合计", () => {
  it("同币种同标度才相加", () => {
    const total = sumMoney([micro("70000000"), micro("30000000")]);
    expect(total).toEqual({ kind: "ok", total: 100000000n, currency: "CNY", scale: 6 });
  });

  it("大额不丢精度——BigInt 全程", () => {
    const total = sumMoney([micro("9007199254740993"), micro("1")]);
    expect(total.kind === "ok" && total.total).toBe(9007199254740994n);
  });

  it("**币种不一致不给合计**：把不同币种的最小单位加起来是纯粹的错数", () => {
    expect(sumMoney([micro("100", "CNY"), micro("100", "USD")]).kind).toBe("mixed-currency");
  });

  it("**标度不一致也不给**：scale-6 与 scale-2 直接相加差一万倍", () => {
    expect(sumMoney([micro("100", "CNY", 6), micro("100", "CNY", 2)]).kind).toBe("mixed-scale");
  });

  it("null 跳过而不是当 0——它是「给不出」", () => {
    // 折成 0 会让合计看起来算全了，而事实是漏了一条
    const total = sumMoney([micro("70000000"), null, undefined]);
    expect(total).toEqual({ kind: "ok", total: 70000000n, currency: "CNY", scale: 6 });
    expect(coveredCount([micro("1"), null, undefined])).toBe(1);
  });

  it("一条都没有时说「空」，不返回 0", () => {
    // 0 会被读成「这期没花钱」，而事实是这个窗口还没有可用的汇总数
    expect(sumMoney([]).kind).toBe("empty");
    expect(sumMoney([null, null]).kind).toBe("empty");
  });

  it("金额形状不对时显眼地说不对，不悄悄跳过", () => {
    expect(sumMoney([micro("12.34")]).kind).toBe("malformed");
    expect(sumMoney([micro("100", "CNY", Number.NaN)]).kind).toBe("malformed");
  });

  it("四种算不出来各有各的说法——下一步不一样", () => {
    const kinds = ["empty", "mixed-currency", "mixed-scale", "malformed"] as const;
    const said = kinds.map((k) => describeMissingTotal(k));
    expect(new Set(said).size).toBe(kinds.length);
    for (const s of said) expect(s.length).toBeGreaterThan(0);
  });
});
