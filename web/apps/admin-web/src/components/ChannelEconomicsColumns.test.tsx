import { render, screen, within } from "@testing-library/react";
import { DataTableV2 } from "@xingmang/ui-admin";
import { describe, expect, it } from "vitest";
import type { ChannelSummary, Money } from "../api/finance";
import { indexChannelSummaries } from "../lib/channelEconomics";
import { channelEconomicsColumns } from "./ChannelEconomicsColumns";

interface Row {
  channelId: string;
  name: string;
}

/** 成本线的金额是 scale-6 微单位（`money.MicroScale`）。 */
function micro(amountMinor: string, currency = "CNY"): Money {
  return { amountMinor, currency, scale: 6 };
}

function summary(over: Partial<ChannelSummary> = {}): ChannelSummary {
  return {
    id: "ch-1",
    name: "OpenAI 中转·主",
    systemType: "sub2api",
    accessMethod: "upstream_key",
    metered: true,
    baseUrl: "https://relay-a.example.com",
    platformId: "sub2api",
    credentialRef: "",
    rechargeRatio: "1.15",
    rechargeCostRate: "0.869565217",
    businessDayTz: "+08:00",
    status: "active",
    tokenCount: 2,
    usageRevenue: micro("100000000"),
    supplyCost: micro("70000000"),
    grossProfit: micro("30000000"),
    grossMargin: "0.300000",
    coverage: {
      rowCount: 1,
      revenueKnownRows: 1,
      costKnownRows: 1,
      accountGrainRows: 0,
      mixedCurrency: false,
      complete: true,
    },
    observed: { costObservedAt: null, revenueObservedAt: null, updatedAt: null, source: "" },
    runway: {
      days: null,
      level: "",
      reason: "",
      windowDays: 0,
      coveredDays: 0,
      dailyAverage: null,
      balance: null,
      balanceObservedAt: null,
    },
    ...over,
  };
}

/** 把三列挂在一张最小的表上单独看。
 *
 *  不经过 ChannelsPanel：那里今天必然走「未接入」分支（汇总按上游账号出行,
 *  与被管平台的渠道 id 对不上），而对上之后要正确渲染的是这一条路径——
 *  没有这个用例，接上的那天才第一次运行到它。 */
function renderColumns(summaries: ChannelSummary[]) {
  const rows: Row[] = [{ channelId: "ch-1", name: "OpenAI 中转·主" }];
  render(
    <DataTableV2<Row>
      caption="经营三列"
      columns={[
        { id: "name", header: "渠道", primary: true, value: (r) => r.name, cell: (r) => r.name },
        ...channelEconomicsColumns<Row>((r) => r.channelId, indexChannelSummaries(summaries)),
      ]}
      rows={rows}
      rowKey={(r) => r.channelId}
      emptyState={<p>空</p>}
    />,
  );
  return within(screen.getByRole("table"));
}

describe("渠道经营三列（对上汇总之后的那条路径）", () => {
  it("**按 scale 降标度**：scale-6 微单位不能当成币种最小单位直接显示", () => {
    // 70000000 微单位 = ¥70.00。当成分显示就是 ¥700,000.00 —— 差一万倍，且不报错
    const table = renderColumns([summary()]);
    expect(table.getByText("¥70.00")).toBeTruthy();
    expect(table.getByText("¥30.00")).toBeTruthy();
    expect(table.queryByText("¥700,000.00")).toBeNull();
  });

  it("毛利率用后端给的那个数，不自己拿毛利除收入", () => {
    const table = renderColumns([summary()]);
    expect(table.getByText("30%")).toBeTruthy();
  });

  it("负毛利标红——0.3% 与 -20% 在一列数字里长得太像", () => {
    const table = renderColumns([
      summary({ grossProfit: micro("-20000000"), grossMargin: "-0.200000" }),
    ]);
    const cell = table.getByText("-20%");
    expect(cell.className).toContain("text-danger");
  });

  it("金额为 null 时显示「未接入」而不是 ¥0.00——null 是「给不出」不是 0", () => {
    const table = renderColumns([summary({ supplyCost: null })]);
    expect(table.queryByText("¥0.00")).toBeNull();
    expect(table.getAllByText("未接入").length).toBeGreaterThan(0);
  });

  it("毛利率为 null 时给「—」，不给 0%", () => {
    const table = renderColumns([summary({ grossMargin: null })]);
    expect(table.queryByText("0%")).toBeNull();
    expect(table.getByText("—")).toBeTruthy();
  });

  it("汇总里没有这条渠道时，三列一起显示「未接入」", () => {
    const table = renderColumns([summary({ id: "别的渠道" })]);
    expect(table.getAllByText("未接入")).toHaveLength(3);
  });
});
