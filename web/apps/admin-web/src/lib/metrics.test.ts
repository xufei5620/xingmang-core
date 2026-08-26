import { describe, expect, it } from "vitest";
import type { MetricItem } from "../api/platform";
import { metricLabel, presentMetric } from "./metrics";

function metric(over: Partial<MetricItem> = {}): MetricItem {
  return {
    metric_key: "sub2api.revenue.daily",
    source: "sub2api-prod",
    environment: "production",
    watermark: "wm-1",
    value: { day: "2026-08-25", amount_minor_units: 123456, currency: "CNY", order_count: 42 },
    freshness: {
      state: "fresh",
      staleness_seconds: 30,
      threshold_seconds: 1800,
      is_partial: false,
      observed_at: "2026-08-26T10:00:00Z",
      last_success: "2026-08-26T10:00:00Z",
      last_error_code: "",
    },
    ...over,
  };
}

describe("presentMetric：未初始化铁律", () => {
  it("未初始化显示「未初始化」，绝不显示 0（宪法 12 条）", () => {
    const shown = presentMetric(
      metric({
        // 后端此时 value 里仍可能带着 0，直接渲染就成了理直气壮的假数据
        value: { amount_minor_units: 0, currency: "CNY" },
        freshness: {
          state: "uninitialized",
          staleness_seconds: null,
          threshold_seconds: 1800,
          is_partial: false,
          observed_at: null,
          last_success: null,
          last_error_code: "",
        },
      }),
    );
    expect(shown.primary).toBe("未初始化");
    expect(shown.primary).not.toContain("0");
    expect(shown.unavailable).toBe(true);
  });

  it("同步失败时仍显示上一次成功的值（由徽章负责标红），不抹成空", () => {
    const shown = presentMetric(
      metric({
        freshness: {
          state: "failed",
          staleness_seconds: 9000,
          threshold_seconds: 1800,
          is_partial: false,
          observed_at: "2026-08-26T08:00:00Z",
          last_success: "2026-08-26T08:00:00Z",
          last_error_code: "upstream_timeout",
        },
      }),
    );
    expect(shown.primary).toBe("¥1,234.56");
    expect(shown.unavailable).toBe(false);
  });
});

describe("presentMetric：已登记指标", () => {
  it("日收入：金额 + 业务日 + 单数", () => {
    const shown = presentMetric(metric());
    expect(shown.label).toBe("Sub2API 日收入");
    expect(shown.primary).toBe("¥1,234.56");
    expect(shown.secondary).toBe("业务日 2026-08-25 · 42 单");
  });

  it("日成本：金额 + 业务日", () => {
    const shown = presentMetric(
      metric({
        metric_key: "sub2api.cost.daily",
        value: { day: "2026-08-25", amount_minor_units: 9900, currency: "CNY" },
      }),
    );
    expect(shown.label).toBe("Sub2API 日成本");
    expect(shown.primary).toBe("¥99.00");
    expect(shown.secondary).toBe("业务日 2026-08-25");
  });

  it("用户数：计数带千分位 + 活跃数", () => {
    const shown = presentMetric(
      metric({
        metric_key: "sub2api.users.total",
        value: { total_users: 12345, active_users: 6789 },
      }),
    );
    expect(shown.label).toBe("Sub2API 用户数");
    expect(shown.primary).toBe("12,345");
    expect(shown.secondary).toBe("活跃 6,789");
  });

  it("用户余额：金额 + 透支额", () => {
    const shown = presentMetric(
      metric({
        metric_key: "sub2api.users.balance",
        value: { balance_minor_units: 505050, overdraft_minor_units: -1200, currency: "CNY" },
      }),
    );
    expect(shown.primary).toBe("¥5,050.50");
    expect(shown.secondary).toBe("透支 -¥12.00");
  });

  it("渠道余额：渠道数 + 同币种合计 + 失效令牌数", () => {
    const shown = presentMetric(
      metric({
        metric_key: "sub2api.channels.balance",
        value: {
          channel_count: 3,
          channels: [
            { channel_id: "a", balance_minor_units: 10000, currency: "CNY", token_valid: true },
            { channel_id: "b", balance_minor_units: 2500, currency: "CNY", token_valid: false },
            { channel_id: "c", balance_minor_units: 1, currency: "CNY", token_valid: true },
          ],
        },
      }),
    );
    expect(shown.primary).toBe("3 个渠道");
    expect(shown.secondary).toBe("合计 ¥125.01 · 1 个渠道令牌失效");
  });

  it("渠道币种不一致时不给合计——把不同币种加起来就是错数", () => {
    const shown = presentMetric(
      metric({
        metric_key: "sub2api.channels.balance",
        value: {
          channel_count: 2,
          channels: [
            { balance_minor_units: 10000, currency: "CNY", token_valid: true },
            { balance_minor_units: 10000, currency: "USD", token_valid: true },
          ],
        },
      }),
    );
    expect(shown.primary).toBe("2 个渠道");
    expect(shown.secondary).toBeUndefined();
  });
});

describe("presentMetric：未登记指标兜底", () => {
  it("键名直接当标题，不编好听的假名字", () => {
    const shown = presentMetric(metric({ metric_key: "newapi.tokens.total", value: { total: 7 } }));
    expect(shown.label).toBe("newapi.tokens.total");
    expect(shown.primary).toBe("7");
  });

  it("认得出金额字段就按金额显示", () => {
    const shown = presentMetric(
      metric({ metric_key: "newapi.wallet", value: { amount_minor_units: 250, currency: "USD" } }),
    );
    expect(shown.primary).toBe("$2.50");
  });

  it("完全认不出的值形状明说未知，不瞎猜数字", () => {
    const shown = presentMetric(
      metric({ metric_key: "newapi.weird", value: { note: "hello", flag: true } }),
    );
    expect(shown.primary).toBe("—");
    expect(shown.secondary).toContain("值形状未知");
    expect(shown.unavailable).toBe(true);
  });
});

describe("metricLabel", () => {
  it("已登记的取友好名，未登记的原样返回", () => {
    expect(metricLabel("sub2api.users.total")).toBe("Sub2API 用户数");
    expect(metricLabel("x.y.z")).toBe("x.y.z");
  });
});
