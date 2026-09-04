import { describe, expect, it } from "vitest";
import {
  cardStatusLabel,
  cardStatusTone,
  isCardLocked,
  transactionStatusLabel,
  transactionTypeLabel,
} from "./cardStatus";

describe("卡片状态显示", () => {
  // 权威枚举来自官方 OpenAPI（contracts/connectors/infini/openapi/card.yaml）
  // 的 /list 参数描述，共六个取值。
  it("六个权威取值都有中文", () => {
    for (const [value, label] of [
      ["init", "初始化"],
      ["pending", "处理中"],
      ["active", "已激活"],
      ["suspend", "已锁定"],
      ["pending_delete", "删除中"],
      ["deleted", "已删除"],
    ]) {
      expect(cardStatusLabel(value!)).toBe(label);
    }
  });

  // 上游加新状态时，这是最该被人看见的时刻——不能静默归到某个已知分类。
  it("没见过的取值原样显示并标记为未知", () => {
    const got = cardStatusLabel("brand_new_state");
    expect(got).toContain("brand_new_state");
    expect(got).toContain("未知");
    expect(cardStatusTone("brand_new_state")).toBe("neutral");
  });

  // 锁定态是 suspend（2026-09-05 对真实卡实测），不是曾经猜的 frozen。
  it("只有 suspend 算锁定", () => {
    expect(isCardLocked("suspend")).toBe(true);
    expect(isCardLocked("active")).toBe(false);
    expect(isCardLocked("frozen")).toBe(false);
  });
});

describe("交易类型与状态显示", () => {
  // 上游两处大小写不同：REST 回 Consume/Completed，webhook 回
  // consume/completed。按字面匹配必有一条路悄悄匹配不上。
  it("大小写不敏感", () => {
    expect(transactionTypeLabel("Consume")).toBe("消费");
    expect(transactionTypeLabel("consume")).toBe("消费");
    expect(transactionStatusLabel("Completed")).toBe("已完成");
    expect(transactionStatusLabel("completed")).toBe("已完成");
  });

  it("覆盖 webhook 文档列出的类型与状态", () => {
    expect(transactionTypeLabel("reversal")).toBe("冲正");
    expect(transactionTypeLabel("refund")).toBe("退款");
    expect(transactionStatusLabel("authorized")).toBe("授权中");
    expect(transactionStatusLabel("failed")).toBe("失败");
  });

  it("没见过的取值原样显示，空值显示占位", () => {
    expect(transactionTypeLabel("teleport")).toBe("teleport");
    expect(transactionTypeLabel("")).toBe("—");
    expect(transactionStatusLabel("")).toBe("—");
  });
});
