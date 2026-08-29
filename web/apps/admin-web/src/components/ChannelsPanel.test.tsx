import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ChannelSummary } from "../api/finance";
import { ChannelsPanel } from "./ChannelsPanel";
import { NewApiChannelsPanel } from "./NewApiChannelsPanel";

/** 渠道管理页（XM-0052 逐格对齐原型 `V["s2/upstream"]`）。
 *
 *  这一片把行粒度从「平台自己的渠道」改成了「上游账号」，于是表上的钱第一次
 *  是真的。用例分三组：口径声明说没说清、顶部四格算得对不对、
 *  以及**给不出的东西有没有伪装成给得出**（这一组最要紧）。 */

/** 线上原始形状（snake_case），走一遍 api/finance.ts 的映射层。 */
function rawChannel(over: Record<string, unknown> = {}) {
  return {
    id: "acc-1",
    name: "上游 A",
    system_type: "sub2api",
    access_method: "upstream_key",
    metered: true,
    base_url: "https://a.example.test",
    platform_id: "sub2api",
    credential_ref: "secret://xm/a",
    recharge_ratio: "1.5",
    recharge_cost_rate: "0.666667",
    business_day_tz: "+08:00",
    status: "active",
    token_count: 1,
    usage_revenue: { amount_minor: "100000000", currency: "CNY", scale: 6 },
    supply_cost: { amount_minor: "70000000", currency: "CNY", scale: 6 },
    gross_profit: { amount_minor: "30000000", currency: "CNY", scale: 6 },
    gross_margin: "0.3",
    coverage: {
      row_count: 1,
      revenue_known_rows: 1,
      cost_known_rows: 1,
      account_grain_rows: 0,
      mixed_currency: false,
      complete: true,
    },
    observed: { source: "test" },
    runway: {
      days: 30,
      level: "healthy",
      reason: "",
      window_days: 7,
      covered_days: 7,
      balance: { amount_minor: "3000000000", currency: "CNY", scale: 6 },
      balance_observed_at: "2026-08-28T02:00:00Z",
    },
    ...over,
  };
}

function fakeResponse(body: unknown, status = 200) {
  return Promise.resolve(
    new Response(JSON.stringify(body), {
      status,
      headers: { "content-type": "application/json" },
    }),
  );
}

/** 两个端点各回各的：渠道汇总给行，上游汇总只给阈值。 */
function stubApi(
  channels: unknown[],
  options: { thresholdStatus?: number; savedViews?: unknown[] } = {},
) {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/finance/channels/summary")) {
        return fakeResponse({ items: channels, from: "2026-08-28", to: "2026-08-28" });
      }
      if (url.includes("/finance/upstreams/summary")) {
        if (options.thresholdStatus && options.thresholdStatus >= 400) {
          return fakeResponse({ error: { code: "boom" } }, options.thresholdStatus);
        }
        return fakeResponse({
          items: [],
          from: "2026-08-28",
          to: "2026-08-28",
          runway_coverage: { total: 1, known: 1, reasons: {} },
          runway_thresholds: { critical_days: 5, warning_days: 10, serious_days: 20 },
        });
      }
      if (url.includes("/ui/saved-views")) {
        return fakeResponse({ items: options.savedViews ?? [] });
      }
      return fakeResponse({ items: [] });
    }),
  );
}

function renderPanel(node: React.ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>{node}</MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("口径声明（§12.2 / 原型 warnbar）", () => {
  it("Sub2API 照抄原型那句话，并给出去上游管理的入口", async () => {
    stubApi([rawChannel()]);
    renderPanel(<ChannelsPanel />);
    expect(
      await screen.findByText(/渠道管理只做单账号 \/ 单 Key 核算，不在这里汇总上游/),
    ).toBeTruthy();
    expect(screen.getByText(/请到顶部「上游管理」查看共享余额与整体利润/)).toBeTruthy();
    const link = screen.getByRole("link", { name: "上游管理" });
    expect(link.getAttribute("href")).toBe("/platforms/sub2api?tab=suppliers");
  });

  it("NewAPI 说的是共用上游目录但各自核算", async () => {
    stubApi([rawChannel({ platform_id: "newapi" })]);
    renderPanel(<NewApiChannelsPanel />);
    expect(await screen.findByText(/NewAPI 与 Sub2API 共用上游目录和充值成本率/)).toBeTruthy();
    expect(screen.getByRole("link", { name: "上游管理" }).getAttribute("href")).toBe(
      "/platforms/newapi?tab=suppliers",
    );
  });

  it("说清一行 = 一个上游账号，且余额可能是多个令牌共享的", async () => {
    stubApi([rawChannel()]);
    renderPanel(<ChannelsPanel />);
    expect(await screen.findByText(/把每行相加会把同一笔钱数很多遍/)).toBeTruthy();
  });
});

describe("本页只看归属本平台的账号", () => {
  it("别的平台的不显示，未配对的显示并标「未归属」", async () => {
    stubApi([
      rawChannel({ id: "mine", platform_id: "sub2api" }),
      rawChannel({ id: "theirs", platform_id: "newapi" }),
      rawChannel({ id: "orphan", platform_id: "" }),
    ]);
    renderPanel(<ChannelsPanel />);
    const table = await screen.findByRole("table");
    expect(within(table).getByText("mine")).toBeTruthy();
    expect(within(table).queryByText("theirs")).toBeNull();
    expect(within(table).getByText("orphan")).toBeTruthy();
    expect(within(table).getByText("未归属")).toBeTruthy();
  });
});

describe("顶部四格（原型逐格）", () => {
  it("第一格是 Key 账号 / 订阅账号的「7 / 3 式」计数", async () => {
    stubApi([
      rawChannel({ id: "a", access_method: "upstream_key", metered: true }),
      rawChannel({ id: "b", access_method: "upstream_key", metered: true }),
      rawChannel({ id: "c", access_method: "subscription_account", metered: false }),
    ]);
    renderPanel(<ChannelsPanel />);
    expect(await screen.findByText("Key 账号 / 订阅账号")).toBeTruthy();
    expect(screen.getByText("2 / 1")).toBeTruthy();
  });

  it("官方直连不并进任何一边，单独在说明里点名", async () => {
    stubApi([
      rawChannel({ id: "a", access_method: "upstream_key", metered: true }),
      rawChannel({ id: "b", access_method: "official_api", metered: false }),
    ]);
    renderPanel(<ChannelsPanel />);
    expect(await screen.findByText("1 / 0")).toBeTruthy();
    expect(screen.getByText(/官方直连 1/)).toBeTruthy();
  });

  // 这条是本文件最要紧的一条。
  it("「7 天内需补充」必须同时报出算不出天数的条数，否则 0 会被读成「都很充裕」", async () => {
    stubApi([
      rawChannel({ id: "a", runway: { days: null, level: "", reason: "no_balance" } }),
      rawChannel({ id: "b", runway: { days: null, level: "", reason: "no_balance" } }),
    ]);
    renderPanel(<ChannelsPanel />);
    expect(await screen.findByText("7 天内需补充")).toBeTruthy();
    expect(screen.getByText(/另有 2 个算不出可用天数/)).toBeTruthy();
    expect(screen.getByText("余额覆盖不全")).toBeTruthy();
  });

  it("毛利格带毛利率角标，且用的是 Σ毛利 ÷ Σ收入", async () => {
    stubApi([
      rawChannel({
        id: "a",
        usage_revenue: { amount_minor: "100000000", currency: "CNY", scale: 6 },
        gross_profit: { amount_minor: "30000000", currency: "CNY", scale: 6 },
      }),
      rawChannel({
        id: "b",
        usage_revenue: { amount_minor: "100000000", currency: "CNY", scale: 6 },
        gross_profit: { amount_minor: "10000000", currency: "CNY", scale: 6 },
      }),
    ]);
    renderPanel(<ChannelsPanel />);
    expect(await screen.findByText("今日毛利")).toBeTruthy();
    // scale-6 微单位 → ¥40.00，不是差一万倍的那个数
    expect(screen.getByText("¥40.00")).toBeTruthy();
    expect(screen.getByText("20%")).toBeTruthy();
  });

  it("覆盖不全时不给毛利率角标，只说覆盖了几条", async () => {
    stubApi([
      rawChannel({ id: "a" }),
      rawChannel({
        id: "b",
        usage_revenue: null,
        supply_cost: null,
        gross_profit: null,
        gross_margin: null,
      }),
    ]);
    renderPanel(<ChannelsPanel />);
    // 收入格与毛利格各说一遍覆盖率——两格都是合计，两格都得标
    expect((await screen.findAllByText(/只含 2 个账号里有汇总的 1 个/)).length).toBe(2);
    expect(screen.getAllByText("覆盖不全").length).toBe(2);

    // 逐行的 30% 仍在（那是后端给的，本来就该显示）；
    // 不该有的是**合计格上**的百分比——用 1 行的毛利除以 1 行的收入代表不了整页
    const profitTile = screen.getByText("今日毛利").closest("article");
    expect(profitTile).toBeTruthy();
    expect(profitTile?.textContent).not.toMatch(/%/);
  });
});

describe("给不出的东西不许伪装成给得出", () => {
  it("可用模型与成功率两列保留，但写明要 M1.5 才有", async () => {
    stubApi([rawChannel()]);
    renderPanel(<ChannelsPanel />);
    const table = await screen.findByRole("table");
    expect(within(table).getByText("可用模型")).toBeTruthy();
    expect(within(table).getByText("成功率")).toBeTruthy();
    expect(within(table).getAllByText("未接入 · M1.5").length).toBe(2);
  });

  it("NewAPI 的表按原型没有成功率列", async () => {
    stubApi([rawChannel({ platform_id: "newapi" })]);
    renderPanel(<NewApiChannelsPanel />);
    const table = await screen.findByRole("table");
    expect(within(table).queryByText("成功率")).toBeNull();
    expect(within(table).getByText("可用模型")).toBeTruthy();
  });

  it("金额给不出时显示「未接入」而不是 ¥0.00", async () => {
    stubApi([rawChannel({ supply_cost: null, gross_profit: null, gross_margin: null })]);
    renderPanel(<ChannelsPanel />);
    const table = await screen.findByRole("table");
    expect(within(table).getAllByText("未接入").length).toBeGreaterThan(0);
    expect(within(table).queryByText("¥0.00")).toBeNull();
  });

  it("没配分组倍率显示「未配置倍率」，不显示 1", async () => {
    stubApi([rawChannel()]); // group_rate 缺席 = 后端没配
    renderPanel(<ChannelsPanel />);
    const table = await screen.findByRole("table");
    expect(within(table).getByText("未配置倍率")).toBeTruthy();
    expect(within(table).queryByText("1.00×")).toBeNull();
  });

  it("配了分组倍率就原样显示，且不拿它乘任何金额", async () => {
    stubApi([rawChannel({ group_rate: "1.25" })]);
    renderPanel(<ChannelsPanel />);
    const table = await screen.findByRole("table");
    expect(within(table).getByText("1.25×")).toBeTruthy();
    // 供给成本仍是后端给的 ¥70.00，没有被 ×1.25 成 ¥87.50
    expect(within(table).getByText("¥70.00")).toBeTruthy();
  });

  it("可用天数算不出来时说清是哪一种，不写「暂无数据」", async () => {
    stubApi([rawChannel({ runway: { days: null, level: "", reason: "no_balance" } })]);
    renderPanel(<ChannelsPanel />);
    const table = await screen.findByRole("table");
    expect(within(table).getByText(/尚未读到上游余额（余额采集未接通）/)).toBeTruthy();
  });

  it("有天数时同时显示余额观测时刻（§10.4 不在过期数据上显示伪精确天数）", async () => {
    stubApi([rawChannel()]);
    renderPanel(<ChannelsPanel />);
    const table = await screen.findByRole("table");
    expect(within(table).getByText(/约 30 天/)).toBeTruthy();
    expect(within(table).getByText(/余额观测/)).toBeTruthy();
  });

  it("一个账号名下多个令牌时点出余额是共享的", async () => {
    stubApi([rawChannel({ token_count: 3 })]);
    renderPanel(<ChannelsPanel />);
    const table = await screen.findByRole("table");
    expect(within(table).getByText(/3 个令牌共享此余额/)).toBeTruthy();
  });

  it("状态列不谎称「健康」——它说的是登记状态 + 可用天数档", async () => {
    stubApi([
      rawChannel({ id: "warn", runway: { days: 3, level: "critical", reason: "" } }),
      rawChannel({ id: "off", status: "disabled" }),
    ]);
    renderPanel(<ChannelsPanel />);
    const table = await screen.findByRole("table");
    expect(within(table).getByText("需关注")).toBeTruthy();
    expect(within(table).getByText(/已停用/)).toBeTruthy();
  });
});

describe("阈值取不到不该把整张表拖垮", () => {
  it("上游汇总 500 时表照样渲染", async () => {
    stubApi([rawChannel()], { thresholdStatus: 500 });
    renderPanel(<ChannelsPanel />);
    const table = await screen.findByRole("table");
    expect(within(table).getByText("上游 A")).toBeTruthy();
  });
});

describe("渠道 SavedView 静态列能力", () => {
  it("grossProfit 在首屏即可恢复排序，不依赖某行金额是否为空", async () => {
    const ids = [
      "account", "platform", "group", "models", "supplyCost", "revenue",
      "balance", "grossProfit", "successRate", "status", "detail",
    ];
    stubApi(
      [rawChannel(), rawChannel({ id: "acc-2", gross_profit: null })],
      {
        savedViews: [{
          id: "view-profit",
          table_key: "platform.sub2api.channels",
          name: "毛利优先",
          state_version: 1,
          state: {
            schema_version: 1, query: "", filters: {},
            sort: { column_id: "grossProfit", direction: "desc" },
            columns: { known: ids, visible: ids }, density: "compact",
          },
          created_at: "2026-08-29T01:00:00Z",
          updated_at: "2026-08-29T01:00:00Z",
        }],
      },
    );
    renderPanel(<ChannelsPanel />);
    const option = await screen.findByRole("option", { name: "毛利优先" }) as HTMLOptionElement;
    fireEvent.change(screen.getByRole("combobox", { name: /视图/ }), {
      target: { value: option.value },
    });
    expect(screen.getByRole("columnheader", { name: /毛利/ }).getAttribute("aria-sort")).toBe("descending");
  });
});

describe("渠道汇总读失败与「没有数据」分开说", () => {
  it("端点 500 时给错误态，而不是一张空表", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => fakeResponse({ error: { code: "boom" } }, 500)),
    );
    renderPanel(<ChannelsPanel />);
    expect(await screen.findByRole("button", { name: /重试/ })).toBeTruthy();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("一条都没有时给空态，说清去哪儿登记", async () => {
    stubApi([]);
    renderPanel(<ChannelsPanel />);
    expect(await screen.findByText("还没有上游账号")).toBeTruthy();
  });
});

/** 类型层的哨兵：行类型就是 ChannelSummary 本身，不是另一个映射过的形状。 */
export type _RowIsChannelSummary = ChannelSummary;
