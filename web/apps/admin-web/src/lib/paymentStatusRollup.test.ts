import { describe, expect, it } from "vitest";

import { rollupPaymentStatuses } from "./paymentStatusRollup";

function stat(count: number, minor: string | null, currency = "CNY") {
  return { count, amount: { minor_units: minor, currency } };
}

describe("按归一化分桶汇总 stats_by_status（XM-PAY-STATUS-ROLLUP）", () => {
  it("把多个原始状态归进同一个桶，笔数与金额都相加，并保留原始状态", () => {
    const rollup = rollupPaymentStatuses({
      PAID: stat(3, "30000"),
      SUCCESS: stat(2, "20000"),
      PENDING: stat(1, "5000"),
    });
    const paid = rollup.buckets.find((bucket) => bucket.bucket === "成功到账");
    expect(paid?.count).toBe(5);
    expect(paid?.minorUnits).toBe(50000n);
    // 归一化只为好读，不掩盖上游实际说了什么。
    expect(paid?.rawStatuses).toEqual(["PAID", "SUCCESS"]);
    expect(rollup.totalCount).toBe(6);
  });

  it("桶的顺序固定，与订单表的状态筛选一致，未知永远排最后", () => {
    const rollup = rollupPaymentStatuses({
      REFUNDED: stat(1, "100"),
      SOMETHING_NEW: stat(1, "200"),
      PENDING: stat(1, "300"),
      PAID: stat(1, "400"),
      FAILED: stat(1, "500"),
    });
    expect(rollup.buckets.map((bucket) => bucket.bucket)).toEqual([
      "成功到账",
      "待处理",
      "失败",
      "退款与冲正",
      "未知",
    ]);
  });

  // 上游加了新状态而我们悄悄丢掉，比显示一个陌生的桶名危险得多。
  it("归不进已知桶的状态计入未知行并单独报出来，不被丢弃", () => {
    const rollup = rollupPaymentStatuses({
      CHARGEBACK: stat(2, "700"),
      PAID: stat(1, "100"),
    });
    expect(rollup.unknownStatuses).toEqual(["CHARGEBACK"]);
    expect(rollup.buckets.find((bucket) => bucket.bucket === "未知")?.count).toBe(2);
    expect(rollup.totalCount).toBe(3);
  });

  // 一个桶里没有任何一条给出金额，与这个桶合计为零，是两件完全不同的事。
  it("整桶都没有金额时合计是「未知」而不是 0", () => {
    const rollup = rollupPaymentStatuses({ PENDING: stat(4, null) });
    const pending = rollup.buckets.find((bucket) => bucket.bucket === "待处理");
    expect(pending?.count).toBe(4);
    expect(pending?.minorUnits).toBeNull();
  });

  it("桶里只有部分条目给出金额时，合计的是给出的那几条", () => {
    const rollup = rollupPaymentStatuses({
      EXPIRED: stat(2, null),
      FAILED: stat(3, "900"),
    });
    const failed = rollup.buckets.find((bucket) => bucket.bucket === "失败");
    expect(failed?.count).toBe(5);
    expect(failed?.minorUnits).toBe(900n);
  });

  // 读不懂的金额算成 0 会让合计静悄悄变小。
  it("金额解析不出来时按未知处理，不当成 0", () => {
    const rollup = rollupPaymentStatuses({
      PAID: stat(1, "not-a-number"),
      SUCCESS: stat(1, "100"),
    });
    expect(rollup.buckets.find((bucket) => bucket.bucket === "成功到账")?.minorUnits).toBe(100n);
  });

  it("金额是大整数时不经过 number，不丢精度", () => {
    const rollup = rollupPaymentStatuses({
      PAID: stat(1, "9007199254740993"),
      SUCCESS: stat(1, "1"),
    });
    expect(rollup.buckets.find((bucket) => bucket.bucket === "成功到账")?.minorUnits).toBe(
      9007199254740994n,
    );
  });

  it("空汇总不报错，返回零行零笔", () => {
    const rollup = rollupPaymentStatuses({});
    expect(rollup.buckets).toEqual([]);
    expect(rollup.totalCount).toBe(0);
    expect(rollup.unknownStatuses).toEqual([]);
  });
});
