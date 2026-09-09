import { describe, expect, it } from "vitest";

import { presentPaymentBucket, rollupPaymentStatuses } from "./paymentStatusRollup";

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

describe("跨币种 fail closed", () => {
  // 与「资金概览」的 aggregateChannelMoney（currency-mismatch → money=null）
  // 同一条纪律：不同币种不做隐式换算，也不给一个看起来可用的合计。
  it("同一个桶里出现两种币种时不给合计，并说明原因", () => {
    const rollup = rollupPaymentStatuses({
      PAID: stat(3, "30000", "CNY"),
      SUCCESS: stat(2, "20000", "USD"),
    });
    const paid = rollup.buckets.find((bucket) => bucket.bucket === "成功到账");
    expect(paid?.minorUnits).toBeNull();
    expect(paid?.amountGap).toBe("currency-mismatch");
    expect(paid?.currencies).toEqual(["CNY", "USD"]);
    // 笔数不受币种影响：契约里 Count 本来就不区分币种，跨币种也照计。
    expect(paid?.count).toBe(5);
    expect(paid?.currency).toBe("");
  });

  // 对照组：币种一致时必须照常相加。把 fail closed 的判据改松（>1 改成 >2）
  // 时这条不该变红——它证明上一条的红不是「任何改动都会红」。
  it("币种一致时照常相加，不被 fail closed 误伤", () => {
    const rollup = rollupPaymentStatuses({
      PAID: stat(3, "30000", "USD"),
      SUCCESS: stat(2, "20000", "USD"),
    });
    const paid = rollup.buckets.find((bucket) => bucket.bucket === "成功到账");
    expect(paid?.minorUnits).toBe(50000n);
    expect(paid?.amountGap).toBe("");
    expect(paid?.currencies).toEqual(["USD"]);
  });

  // 「上游没说币种」不等于「和已知币种是同一种」——把两者相加等于替上游
  // 认定了币种。
  it("币种空串算一个独立取值，与已知币种混在一起时同样 fail closed", () => {
    const rollup = rollupPaymentStatuses({
      PAID: stat(1, "100", "CNY"),
      SUCCESS: stat(1, "100", ""),
    });
    expect(rollup.buckets.find((bucket) => bucket.bucket === "成功到账")?.amountGap).toBe(
      "currency-mismatch",
    );
  });

  // 金额缺席的那一条不贡献金额，因此它自称什么币种都不该触发 fail closed。
  it("只有金额缺席的条目带着另一种币种时不算跨币种", () => {
    const rollup = rollupPaymentStatuses({
      FAILED: stat(3, "900", "USD"),
      EXPIRED: stat(2, null, "CNY"),
    });
    const failed = rollup.buckets.find((bucket) => bucket.bucket === "失败");
    expect(failed?.minorUnits).toBe(900n);
    expect(failed?.currencies).toEqual(["USD"]);
  });

  it("整份汇总的币种唯一时给出币种，不唯一时留空", () => {
    expect(rollupPaymentStatuses({ PAID: stat(1, "1", "USD"), PENDING: stat(1, "2", "USD") }).currency).toBe("USD");
    expect(rollupPaymentStatuses({ PAID: stat(1, "1", "USD"), PENDING: stat(1, "2", "CNY") }).currency).toBe("");
    expect(rollupPaymentStatuses({}).currency).toBe("");
  });
});

describe("presentPaymentBucket：「最近事件」四行的取值口径", () => {
  it("桶缺席且汇总完整时是**确认过的零**，不是未接入", () => {
    const rollup = rollupPaymentStatuses({ PAID: stat(1, "100", "USD") });
    const cell = presentPaymentBucket(rollup, "退款与冲正", false);
    expect(cell.coverage).toBe("confirmed-zero");
    expect(cell.count).toBe(0);
    expect(cell.minorUnits).toBe(0n);
    expect(cell.currency).toBe("USD");
    expect(cell.amountNote).toBe("");
  });

  // 覆盖不全时键缺席说不清是「真的零」还是「被排除掉了」——这时断言 0
  // 正是宪法 12 条要防的那种「看起来有结论」。
  it("桶缺席但汇总不完整时连笔数都不给，且说明为什么", () => {
    const rollup = rollupPaymentStatuses({ PAID: stat(1, "100", "USD") });
    const cell = presentPaymentBucket(rollup, "退款与冲正", true);
    expect(cell.coverage).toBe("unknown");
    expect(cell.count).toBeNull();
    expect(cell.minorUnits).toBeNull();
    expect(cell.amountNote).toBe("覆盖不全：这个分桶在本次汇总里没有出现，无法确认它真的是零");
  });

  it("桶存在且汇总完整时金额与笔数都照实给出", () => {
    const rollup = rollupPaymentStatuses({ PAID: stat(42, "418000", "USD") });
    const cell = presentPaymentBucket(rollup, "成功到账", false);
    expect(cell).toEqual({
      bucket: "成功到账",
      count: 42,
      minorUnits: 418000n,
      currency: "USD",
      coverage: "known",
      amountNote: "",
    });
  });

  // 上游排除了非合约币种/未识别渠道的金额：笔数仍然完整，合计只会偏低，
  // 方向是确定的，所以给出数字 + 说明，而不是整行抹成「—」。
  it("汇总不完整时给出金额但标注覆盖不全", () => {
    const rollup = rollupPaymentStatuses({ PAID: stat(42, "418000", "USD") });
    const cell = presentPaymentBucket(rollup, "成功到账", true);
    expect(cell.coverage).toBe("partial");
    expect(cell.minorUnits).toBe(418000n);
    expect(cell.amountNote).toBe(
      "覆盖不全：上游有未计入金额的订单（非合约币种或未识别渠道），笔数仍完整，合计只会偏低",
    );
  });

  it("桶内币种不一致时不给金额，笔数照给", () => {
    const rollup = rollupPaymentStatuses({ PAID: stat(3, "1", "CNY"), SUCCESS: stat(2, "1", "USD") });
    const cell = presentPaymentBucket(rollup, "成功到账", false);
    expect(cell.minorUnits).toBeNull();
    expect(cell.count).toBe(5);
    expect(cell.amountNote).toBe("币种不一致，合计给不出；不做隐式换算");
  });

  it("桶内没有任何一条给出金额时说「没给」，不说 0", () => {
    const rollup = rollupPaymentStatuses({ PENDING: stat(4, null, "USD") });
    const cell = presentPaymentBucket(rollup, "待处理", false);
    expect(cell.minorUnits).toBeNull();
    expect(cell.count).toBe(4);
    expect(cell.amountNote).toBe("上游没有给出这个分桶的金额（不是 0）");
  });
});
