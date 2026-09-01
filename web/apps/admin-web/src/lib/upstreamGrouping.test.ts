import { describe, expect, it } from "vitest";
import type { UpstreamAccountItem, UpstreamSummary } from "../api/finance";
import { groupUpstreamAccounts, supplierGroupKeyFor } from "./upstreamGrouping";

function account(over: Partial<UpstreamAccountItem> = {}): UpstreamAccountItem {
  return {
    id: "acc-1",
    system_type: "sub2api",
    access_method: "upstream_key",
    upstream_name: "Relay A",
    upstream_contact: "",
    upstream_group: "",
    base_url: "https://relay-a.example.com",
    credential_ref: "secret://xm/upstream/a",
    recharge_ratio: "1.15",
    group_rate: "",
    recharge_cost_rate: "0.869565217",
    currency: "USD",
    business_day_tz: "+08:00",
    platform_id: "sub2api",
    status: "active",
    environment: "development",
    metered: true,
    token_mappings: [],
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-01T00:00:00Z",
    ...over,
  };
}

function summary(over: Partial<UpstreamSummary> = {}): UpstreamSummary {
  return {
    id: "acc-1",
    name: "relay-a",
    supplierKey: "",
    systemType: "sub2api",
    accessMethod: "upstream_key",
    baseUrl: "https://relay-a.example.com",
    rechargeCostRate: "0.869565217",
    credentialRef: "secret://xm/upstream/a",
    status: "active",
    tokenCount: 1,
    usageRevenue: null,
    supplyCost: { amountMinor: "70000000", currency: "CNY", scale: 6 },
    grossProfit: { amountMinor: "30000000", currency: "CNY", scale: 6 },
    coverage: {
      rowCount: 1,
      revenueKnownRows: 1,
      costKnownRows: 1,
      accountGrainRows: 0,
      mixedCurrency: false,
      complete: true,
    },
    observed: {
      costObservedAt: "2026-08-28T09:00:00Z",
      revenueObservedAt: "2026-08-28T09:00:00Z",
      updatedAt: "2026-08-28T09:05:00Z",
      source: "finance.profit_daily",
    },
    runway: {
      days: 12,
      level: "warning",
      reason: "",
      windowDays: 7,
      coveredDays: 7,
      dailyAverage: null,
      balance: { amountMinor: "123450000", currency: "CNY", scale: 6 },
      balanceObservedAt: "2026-08-28T08:30:00Z",
    },
    ...over,
  };
}

describe("supplierGroupKeyFor：按名称，退回 host，再退回账号自身", () => {
  it("有 upstream_name 就按名称分组", () => {
    expect(supplierGroupKeyFor(account({ upstream_name: "Relay A" }))).toEqual({
      key: "name:Relay A",
      groupedBy: "name",
      label: "Relay A",
    });
  });

  it("名称为空、网址能解出 host 时按 host 分组", () => {
    expect(
      supplierGroupKeyFor(account({ upstream_name: "", base_url: "https://relay-a.example.com/v1" })),
    ).toEqual({ key: "host:relay-a.example.com", groupedBy: "host", label: "relay-a.example.com" });
  });

  it("名称与网址都没有时各自成组，用账号 id 兜底，不编一个假名字", () => {
    expect(supplierGroupKeyFor(account({ id: "acc-9", upstream_name: "", base_url: "" }))).toEqual({
      key: "account:acc-9",
      groupedBy: "account",
      label: "acc-9",
    });
  });

  it("网址解析不出合法 URL 时按账号自身兜底，不把整段怪字符串当 host", () => {
    expect(
      supplierGroupKeyFor(account({ id: "acc-7", upstream_name: "", base_url: "not a url" })),
    ).toEqual({ key: "account:acc-7", groupedBy: "account", label: "acc-7" });
  });
});

describe("groupUpstreamAccounts：归并与聚合", () => {
  it("同名的多个账号归成一组，组内账号顺序保留输入顺序", () => {
    const groups = groupUpstreamAccounts(
      [
        account({ id: "a1", upstream_name: "Relay A" }),
        account({ id: "a2", upstream_name: "Relay A" }),
        account({ id: "b1", upstream_name: "Relay B", base_url: "https://relay-b.example.com" }),
      ],
      new Map(),
    );
    expect(groups).toHaveLength(2);
    expect(groups[0]?.name).toBe("Relay A");
    expect(groups[0]?.accounts.map((a) => a.id)).toEqual(["a1", "a2"]);
    expect(groups[1]?.name).toBe("Relay B");
  });

  it("名称为空时按 host 归并，不同账号只要同一域名就并成一组", () => {
    const groups = groupUpstreamAccounts(
      [
        account({ id: "a1", upstream_name: "", base_url: "https://relay-a.example.com/v1" }),
        account({ id: "a2", upstream_name: "", base_url: "https://relay-a.example.com/v2" }),
      ],
      new Map(),
    );
    expect(groups).toHaveLength(1);
    expect(groups[0]?.groupedBy).toBe("host");
    expect(groups[0]?.name).toBe("relay-a.example.com");
    expect(groups[0]?.accounts).toHaveLength(2);
  });

  it("未配对的账号（platform_id 为空）照样出现在分组里并计数（ADMIN-IA §8.6 #3）", () => {
    const groups = groupUpstreamAccounts(
      [account({ id: "a1", platform_id: "" }), account({ id: "a2", platform_id: "sub2api" })],
      new Map(),
    );
    expect(groups).toHaveLength(1);
    expect(groups[0]?.accounts.map((a) => a.id)).toContain("a1");
    expect(groups[0]?.unpairedCount).toBe(1);
    expect(groups[0]?.platforms).toEqual(["sub2api"]);
  });

  it("接入方式三分：Key / 订阅 / 官方直连，互不覆盖", () => {
    const groups = groupUpstreamAccounts(
      [
        account({ id: "a1", upstream_name: "Relay A", access_method: "upstream_key", metered: true }),
        account({ id: "a2", upstream_name: "Relay B", access_method: "subscription_account", metered: false }),
        account({ id: "a3", upstream_name: "Relay C", access_method: "official_api", metered: false }),
      ],
      new Map(),
    );
    expect(groups).toHaveLength(3);
    const [key, sub, official] = groups;
    expect(key?.keyAccountCount).toBe(1);
    expect(sub?.subscriptionAccountCount).toBe(1);
    expect(official?.officialAccountCount).toBe(1);
  });

  it("金额合计跳过没有汇总的账号，不当成 0；覆盖率如实统计", () => {
    const groups = groupUpstreamAccounts(
      [account({ id: "a1" }), account({ id: "a2", upstream_name: "Relay A" })],
      new Map([["a1", summary({ id: "a1" })]]),
    );
    expect(groups).toHaveLength(1); // 两个账号同名 "Relay A" 归一组
    const g = groups[0]!;
    expect(g.costTotal).toEqual({ kind: "ok", total: 70000000n, currency: "CNY", scale: 6 });
    expect(g.costCovered).toBe(1);
    expect(g.uncoveredSummaryCount).toBe(1);
  });

  it("币种不一致时不给合计，与 sumMoney 同一条规矩", () => {
    const groups = groupUpstreamAccounts(
      [account({ id: "a1" }), account({ id: "a2" })],
      new Map([
        ["a1", summary({ id: "a1", supplyCost: { amountMinor: "1000000", currency: "CNY", scale: 6 } })],
        ["a2", summary({ id: "a2", supplyCost: { amountMinor: "1000000", currency: "USD", scale: 6 } })],
      ]),
    );
    expect(groups[0]?.costTotal).toEqual({ kind: "mixed-currency" });
  });

  it("充值成本率：同组账号费率一致时给单一值，不一致时标记 mixed", () => {
    const uniform = groupUpstreamAccounts(
      [
        account({ id: "a1", recharge_cost_rate: "0.87" }),
        account({ id: "a2", recharge_cost_rate: "0.87" }),
      ],
      new Map(),
    );
    expect(uniform[0]?.rechargeRate).toMatchObject({ kind: "single", rate: "0.87" });

    const mixed = groupUpstreamAccounts(
      [
        account({ id: "a1", recharge_cost_rate: "0.87" }),
        account({ id: "a2", recharge_cost_rate: "0.90" }),
      ],
      new Map(),
    );
    expect(mixed[0]?.rechargeRate.kind).toBe("mixed");
  });

  it("充值成本率：组内没有计量型账号时给 none，不编一个费率", () => {
    const groups = groupUpstreamAccounts(
      [account({ id: "a1", access_method: "subscription_account", metered: false, recharge_cost_rate: "" })],
      new Map(),
    );
    expect(groups[0]?.rechargeRate.kind).toBe("none");
  });

  it("联系人与接入分组去重后保留", () => {
    const groups = groupUpstreamAccounts(
      [
        account({ id: "a1", upstream_contact: "老王", upstream_group: "gpt-main" }),
        account({ id: "a2", upstream_contact: "老王", upstream_group: "gpt-budget" }),
      ],
      new Map(),
    );
    expect(groups[0]?.contacts).toEqual(["老王"]);
    expect(groups[0]?.groupNames).toEqual(["gpt-main", "gpt-budget"]);
  });

  it("停用账号计入 disabledCount，可用天数进入告警档的计入 attentionCount", () => {
    const groups = groupUpstreamAccounts(
      [
        account({ id: "a1", status: "disabled" }),
        account({ id: "a2" }),
      ],
      new Map([["a2", summary({ id: "a2", runway: { ...summary().runway, level: "critical" } })]]),
    );
    expect(groups[0]?.disabledCount).toBe(1);
    expect(groups[0]?.attentionCount).toBe(1);
  });

  it("最旧余额观测时刻按真实 instant 取值，不按字符串词典序", () => {
    const groups = groupUpstreamAccounts(
      [account({ id: "a1" }), account({ id: "a2" })],
      new Map([
        [
          "a1",
          summary({
            id: "a1",
            runway: { ...summary().runway, balanceObservedAt: "2026-08-28T01:00:00-07:00" }, // 08:00Z
          }),
        ],
        [
          "a2",
          summary({
            id: "a2",
            runway: { ...summary().runway, balanceObservedAt: "2026-08-28T08:30:00+02:00" }, // 06:30Z，更旧
          }),
        ],
      ]),
    );
    expect(groups[0]?.oldestBalanceObservedAt).toBe("2026-08-28T08:30:00+02:00");
  });

  it("空输入给空数组，不抛异常", () => {
    expect(groupUpstreamAccounts([], new Map())).toEqual([]);
  });
});
