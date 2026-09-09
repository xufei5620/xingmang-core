import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";

import { ChannelProfitView, type ChannelProfitPlatform } from "./ChannelProfitView";

function response(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

function renderProfit(platform: ChannelProfitPlatform) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={[`/platforms/${platform}?tab=finance&sub=profit&day=2026-08-28`]}>
      <QueryClientProvider client={queryClient}>
        <ChannelProfitView platform={platform} initialDate="2026-08-28" />
      </QueryClientProvider>
    </MemoryRouter>,
  );
}

/** 一行渠道汇总的原始形状（`GET /api/v1/finance/channels/summary` 的 item）。
 *
 *  金额是 **scale-6 微单位**：`200000000` 是 ¥200.00，不是 ¥2,000,000.00。
 *  夹具里两个平台的金额刻意两两不同——同一个数字出现在两边的话，
 *  「只显示本平台的行」这条断言即使过滤器坏掉也照样绿。 */
function channelRow(overrides: Record<string, unknown>) {
  return {
    access_method: "upstream_key",
    base_url: "https://upstream.example.test",
    platform_id: "prod",
    recharge_ratio: "1.0",
    recharge_cost_rate: "1.0",
    status: "active",
    coverage: { row_count: 1, revenue_known_rows: 1, cost_known_rows: 1, complete: true },
    observed: { source: "finance-collect", updated_at: "2026-08-28T10:00:00Z" },
    runway: {},
    ...overrides,
  };
}

const SUB2API_ROWS = [
  channelRow({
    id: "s2-ch-1",
    name: "Claude 转售渠道",
    system_type: "sub2api",
    access_method: "subscription_account",
    base_url: "https://claude.sub2api.example.test",
    group_rate: "1.5",
    usage_revenue: { amount_minor: "200000000", currency: "CNY", scale: 6 },
    supply_cost: { amount_minor: "150000000", currency: "CNY", scale: 6 },
    gross_profit: { amount_minor: "50000000", currency: "CNY", scale: 6 },
    gross_margin: "0.25",
  }),
  channelRow({
    id: "s2-ch-2",
    name: "Codex 转售渠道",
    system_type: "sub2api",
    base_url: "https://codex.sub2api.example.test",
    status: "disabled",
    usage_revenue: { amount_minor: "900000000", currency: "CNY", scale: 6 },
    supply_cost: null,
    gross_profit: null,
    gross_margin: null,
  }),
];

const NEWAPI_ROWS = [
  channelRow({
    id: "na-ch-1",
    name: "Gemini 主渠道",
    system_type: "newapi",
    base_url: "https://google.example.test",
    usage_revenue: { amount_minor: "100000000", currency: "CNY", scale: 6 },
    supply_cost: { amount_minor: "70000000", currency: "CNY", scale: 6 },
    gross_profit: { amount_minor: "30000000", currency: "CNY", scale: 6 },
    gross_margin: "0.3",
  }),
  channelRow({
    id: "na-ch-2",
    name: "GPT 备用渠道",
    system_type: "newapi",
    base_url: "https://openai.example.test",
    status: "retired",
    usage_revenue: { amount_minor: "400000000", currency: "CNY", scale: 6 },
    supply_cost: { amount_minor: "380000000", currency: "CNY", scale: 6 },
    gross_profit: { amount_minor: "20000000", currency: "CNY", scale: 6 },
    gross_margin: "0.05",
  }),
];

function mockSummary(items: readonly unknown[], overrides: Record<string, unknown> = {}) {
  return vi.fn((input: string) => {
    if (input.includes("/finance/channels/summary")) {
      return Promise.resolve(
        response(200, { items, from: "2026-08-28", to: "2026-08-28", ...overrides }),
      );
    }
    return Promise.resolve(response(404, { error: { code: "NOT_REGISTERED", message: "unknown" } }));
  });
}

/** 两个平台的期望值成对写在一起。
 *
 *  过滤器一旦失效，「本平台的行在」与「另一平台的行不在」会**同时**变红——
 *  单看任何一条都可能因为夹具巧合而恒真，成对才盯得住。 */
const EXPECTED: Record<
  ChannelProfitPlatform,
  { mine: string[]; myAmounts: string[]; theirs: string[]; theirAmounts: string[]; tableLabel: string }
> = {
  sub2api: {
    mine: ["Claude 转售渠道", "Codex 转售渠道"],
    myAmounts: ["¥200.00", "¥150.00", "¥50.00", "25.00%", "¥900.00"],
    theirs: ["Gemini 主渠道", "GPT 备用渠道"],
    theirAmounts: ["¥100.00", "¥70.00", "¥30.00", "30.00%", "¥400.00"],
    tableLabel: "Sub2API 渠道利润核算",
  },
  newapi: {
    mine: ["Gemini 主渠道", "GPT 备用渠道"],
    myAmounts: ["¥100.00", "¥70.00", "¥30.00", "30.00%", "¥400.00"],
    theirs: ["Claude 转售渠道", "Codex 转售渠道"],
    theirAmounts: ["¥200.00", "¥150.00", "¥50.00", "25.00%", "¥900.00"],
    tableLabel: "NewAPI 渠道利润核算",
  },
};

describe("渠道利润核算（两个平台共用一张表）", () => {
  afterEach(() => vi.unstubAllGlobals());

  // 同一份混合夹具喂给两个平台，断言互为镜像。这是本片的主变异靶子：
  // 删掉 `item.systemType === platform` 这一步，两条用例同时变红。
  for (const platform of ["sub2api", "newapi"] as const) {
    it(`${platform} 视图只显示本平台的行，另一平台的渠道与金额都不出现`, async () => {
      vi.stubGlobal("fetch", mockSummary([...SUB2API_ROWS, ...NEWAPI_ROWS]));
      renderProfit(platform);

      const expected = EXPECTED[platform];
      const table = await screen.findByRole("table", { name: expected.tableLabel });
      for (const name of expected.mine) {
        expect(within(table).getByText(name)).toBeTruthy();
      }
      for (const text of expected.myAmounts) {
        expect(within(table).getByText(text)).toBeTruthy();
      }
      for (const name of expected.theirs) {
        expect(within(table).queryByText(name)).toBeNull();
      }
      for (const text of expected.theirAmounts) {
        expect(within(table).queryByText(text)).toBeNull();
      }
      // 行数也钉住：过滤器若退化成「全都要」，上面的 queryByText 仍可能因为
      // 某个文案恰好不冲突而漏网，行数不会。
      expect(within(table).getAllByRole("row")).toHaveLength(expected.mine.length + 1);
    });
  }

  it("Sub2API 视图在只有 NewAPI 行时给 Sub2API 的空态，逐字不含 NewAPI", async () => {
    vi.stubGlobal("fetch", mockSummary(NEWAPI_ROWS));
    renderProfit("sub2api");

    expect(await screen.findByText("暂无 Sub2API 利润明细")).toBeTruthy();
    expect(
      screen.getByText(
        "finance/channels/summary 已读取，但当前统计区间没有可归属的 Sub2API 渠道；这不等于利润为 0。",
      ),
    ).toBeTruthy();
    expect(screen.queryByText("暂无 NewAPI 利润明细")).toBeNull();
    // 空态不是「利润为 0」——这一页任何时候都不该凭空长出一个笃定的零。
    expect(screen.queryByText("¥0.00")).toBeNull();
    expect(screen.getByText("暂无可展示记录；空态不代表金额为 0。")).toBeTruthy();
  });

  it("NewAPI 视图在只有 Sub2API 行时给 NewAPI 的空态（对称）", async () => {
    vi.stubGlobal("fetch", mockSummary(SUB2API_ROWS));
    renderProfit("newapi");

    expect(await screen.findByText("暂无 NewAPI 利润明细")).toBeTruthy();
    expect(
      screen.getByText(
        "finance/channels/summary 已读取，但当前统计区间没有可归属的 NewAPI 渠道；这不等于利润为 0。",
      ),
    ).toBeTruthy();
    expect(screen.queryByText("暂无 Sub2API 利润明细")).toBeNull();
  });

  it("说明与表头逐字带本平台名，列结构两边一致", async () => {
    vi.stubGlobal("fetch", mockSummary([...SUB2API_ROWS, ...NEWAPI_ROWS]));
    renderProfit("sub2api");

    // 说明段落在 ApiStateView 之外，加载中就已经在了——先等表格落定，
    // 否则下面的表头断言会在「加载中…」那一帧上跑。
    await screen.findByRole("table", { name: "Sub2API 渠道利润核算" });
    expect(
      screen.getByText(
        "按 Sub2API 渠道逐行核算我方计费、上游成本、毛利与毛利率；同一上游下的多个渠道不会在这里合并。",
      ),
    ).toBeTruthy();
    for (const header of ["渠道", "上游", "分组 / 倍率", "我方计费", "上游成本", "毛利", "毛利率", "状态"]) {
      expect(screen.getByRole("columnheader", { name: header })).toBeTruthy();
    }
    // 筛选在搜索之前（原型约定），两个平台同一个壳。
    const toolbar = screen.getByRole("toolbar", { name: "利润核算明细筛选与搜索" });
    const controls = [...toolbar.querySelectorAll("select, input[type=search]")];
    expect(controls.at(-1)?.getAttribute("type")).toBe("search");
  });

  it("演示数据横幅点名的是本平台，不会把人指到另一个平台的凭据页", async () => {
    // finance-collect-staging 是 DEFAULT_DEMO_SOURCES 里的 Fake 来源。
    const demoRows = SUB2API_ROWS.map((row) => ({
      ...row,
      observed: { source: "finance-collect-staging", updated_at: "2026-08-28T10:00:00Z" },
    }));
    vi.stubGlobal("fetch", mockSummary(demoRows));
    renderProfit("sub2api");

    expect(
      await screen.findByText(
        "当前展示的是演示数据（Fake 连接器），非真实运营数据；请在「连接与凭据」配置真实 Sub2API 实例。",
      ),
    ).toBeTruthy();
    expect(screen.queryByText(/配置真实 NewAPI 实例/)).toBeNull();
  });

  it("非演示来源不挂横幅（上一条的对照组）", async () => {
    vi.stubGlobal("fetch", mockSummary(SUB2API_ROWS));
    renderProfit("sub2api");

    expect(await screen.findByText("Claude 转售渠道")).toBeTruthy();
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("上游与状态筛选按本平台的行取值，选中后仍只剩本平台的行", async () => {
    vi.stubGlobal("fetch", mockSummary([...SUB2API_ROWS, ...NEWAPI_ROWS]));
    renderProfit("sub2api");

    await screen.findByText("Claude 转售渠道");
    const upstream = screen.getByRole("combobox", { name: "上游筛选" });
    // 下拉里只该有 Sub2API 两条上游 + 「全部」；NewAPI 的上游主机不该混进来。
    const options = [...upstream.querySelectorAll("option")].map((o) => o.textContent);
    expect(options).toEqual(["全部", "claude.sub2api.example.test", "codex.sub2api.example.test"]);

    fireEvent.change(upstream, { target: { value: "claude.sub2api.example.test" } });
    expect(await screen.findByText("Claude 转售渠道")).toBeTruthy();
    expect(screen.queryByText("Codex 转售渠道")).toBeNull();
    expect(screen.getByText("显示 1 / 2 条")).toBeTruthy();
  });

  it("金额缺席显示「—」而不是 0，毛利率缺席同理", async () => {
    vi.stubGlobal("fetch", mockSummary(SUB2API_ROWS));
    renderProfit("sub2api");

    const row = (await screen.findByText("Codex 转售渠道")).closest("tr") as HTMLElement;
    // 上游成本 / 毛利 / 毛利率 三格都缺席 → 三个「—」，一个 0 都不该有。
    expect(within(row).getAllByText("—")).toHaveLength(3);
    expect(within(row).queryByText("¥0.00")).toBeNull();
    expect(within(row).queryByText("0.00%")).toBeNull();
    // 没配分组倍率时说「未配置倍率」，不显示 1×。
    expect(within(row).getByText("未配置倍率")).toBeTruthy();
    expect(within(row).getByText("已停用")).toBeTruthy();
  });

  it("首次读取失败时错误文案带错误码，重试成功后恢复本平台空态", async () => {
    let attempts = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn((input: string) => {
        if (!input.includes("/finance/channels/summary")) return Promise.resolve(response(404, {}));
        attempts += 1;
        return attempts === 1
          ? Promise.resolve(
              response(503, { error: { code: "UPSTREAM_UNAVAILABLE", message: "暂时不可用" } }),
            )
          : Promise.resolve(response(200, { items: [], from: "2026-08-28", to: "2026-08-28" }));
      }),
    );
    renderProfit("sub2api");

    expect(await screen.findByText("加载失败")).toBeTruthy();
    expect(screen.getByText("暂时不可用（错误码 UPSTREAM_UNAVAILABLE）")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "重试" }));
    expect(await screen.findByText("暂无 Sub2API 利润明细")).toBeTruthy();
  });
});
