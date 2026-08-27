import { describe, expect, it, vi } from "vitest";
import type { ApiClient } from "./client";
import type { PlatformApiConfig } from "./config";
import { listChannelSummaries, listUpstreamSummaries } from "./finance";

function fakeClient(body: unknown): ApiClient {
  return {
    get: vi.fn().mockResolvedValue(body),
    post: vi.fn().mockResolvedValue(body),
  } as unknown as ApiClient;
}

const config: PlatformApiConfig = {
  baseUrl: "",
  principalId: "staff_alice",
  principalType: "HUMAN",
  scopes: ["finance.read"],
  environment: "development",
};

describe("listChannelSummaries：null 是「给不出」，不是 0", () => {
  it("三个金额与毛利率的 null 原样保留", async () => {
    const page = await listChannelSummaries(
      {},
      fakeClient({
        items: [
          {
            id: "c1",
            system_type: "sub2api",
            usage_revenue: null,
            supply_cost: null,
            gross_profit: null,
            gross_margin: null,
          },
        ],
        from: "2026-08-28",
        to: "2026-08-28",
      }),
      config,
    );
    const item = page.items[0]!;
    // 折成 0 会让「今天还没入账」看起来像「今天没赚钱」
    expect(item.usageRevenue).toBeNull();
    expect(item.supplyCost).toBeNull();
    expect(item.grossProfit).toBeNull();
    expect(item.grossMargin).toBeNull();
  });

  it("金额带 scale 一起映射——前端不硬编码 6", async () => {
    const page = await listChannelSummaries(
      {},
      fakeClient({
        items: [
          {
            id: "c1",
            supply_cost: { amount_minor: "29990000", currency: "USD", scale: 6 },
          },
        ],
      }),
      config,
    );
    expect(page.items[0]!.supplyCost).toEqual({
      amountMinor: "29990000",
      currency: "USD",
      scale: 6,
    });
  });

  it("后端漏了 scale 时不补一个默认值——补 6 正是这个字段要避免的事", async () => {
    const page = await listChannelSummaries(
      {},
      fakeClient({
        items: [{ id: "c1", supply_cost: { amount_minor: "1", currency: "USD" } }],
      }),
      config,
    );
    expect(Number.isNaN(page.items[0]!.supplyCost!.scale)).toBe(true);
  });

  it("覆盖率缺省按「不完整」处理，页面因此显示提示而不是一个笃定的数", async () => {
    const page = await listChannelSummaries(
      {},
      fakeClient({ items: [{ id: "c1" }] }),
      config,
    );
    expect(page.items[0]!.coverage.complete).toBe(false);
  });

  it("items 为 null 时按空数组处理，页面不会炸", async () => {
    const page = await listChannelSummaries({}, fakeClient({ items: null }), config);
    expect(page.items).toEqual([]);
  });

  it("不传区间时不往 URL 里塞 from/to（后端据此只取今天）", async () => {
    const client = fakeClient({ items: [] });
    await listChannelSummaries({}, client, config);
    expect(client.get).toHaveBeenCalledWith("/api/v1/finance/channels/summary", {
      searchParams: { environment: "development", from: undefined, to: undefined },
    });
  });
});

describe("listUpstreamSummaries：可用天数与覆盖率", () => {
  it("days 为 null 时 reason 一并带出——没有解释的空位会被读成 bug", async () => {
    const page = await listUpstreamSummaries(
      {},
      fakeClient({
        items: [
          {
            id: "u1",
            supplier_key: "sub2api|https://a.test",
            runway: {
              days: null,
              reason: "no_balance",
              window_days: 7,
              covered_days: 0,
              balance: null,
              balance_observed_at: null,
            },
          },
        ],
        runway_coverage: { total: 1, known: 0, reasons: { no_balance: 1 } },
        runway_thresholds: { critical_days: 5, warning_days: 10, serious_days: 20 },
      }),
      config,
    );
    const item = page.items[0]!;
    expect(item.runway.days).toBeNull();
    expect(item.runway.reason).toBe("no_balance");
    expect(item.supplierKey).toBe("sub2api|https://a.test");
    // 覆盖率与阈值原样带出：前端不硬编码那三个数
    expect(page.runwayCoverage).toEqual({ total: 1, known: 0, reasons: { no_balance: 1 } });
    expect(page.runwayThresholds).toEqual({
      criticalDays: 5,
      warningDays: 10,
      seriousDays: 20,
    });
  });

  it("算得出天数时余额与观测时刻一并带出（§10.4 要求显示观测时间）", async () => {
    const page = await listUpstreamSummaries(
      {},
      fakeClient({
        items: [
          {
            id: "u1",
            runway: {
              days: 105,
              level: "healthy",
              reason: "",
              window_days: 7,
              covered_days: 7,
              daily_average: { amount_minor: "4000000", currency: "USD", scale: 6 },
              balance: { amount_minor: "420000000", currency: "USD", scale: 6 },
              balance_observed_at: "2026-08-28T05:59:00Z",
            },
          },
        ],
      }),
      config,
    );
    const runway = page.items[0]!.runway;
    expect(runway.days).toBe(105);
    expect(runway.level).toBe("healthy");
    expect(runway.balance?.amountMinor).toBe("420000000");
    expect(runway.balanceObservedAt).toBe("2026-08-28T05:59:00Z");
    expect(runway.dailyAverage?.scale).toBe(6);
  });
});
