import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { UpstreamAccountsPanel } from "./UpstreamAccountsPanel";

/** 上游管理（2026-09-02 起是渠道管理页内区块，行粒度改成供应商——见
 *  `lib/upstreamGrouping.ts` 的归并规则与 `UpstreamAccountsPanel.tsx` 的文件头）。
 *
 *  这份测试分两层：
 *  1. 供应商这一层是**新行为**，逐条覆盖——按名称归并、按网址 host 退回、
 *     未配对账号可见、顶部四格、供应商级列的聚合口径（余额/费率/联系人/状态）。
 *  2. 展开供应商看到的账号级表格是**原样保留的旧行为**（列定义、写操作、
 *     令牌映射展开一个字节都没改，只是现在嵌了一层），这里只做代表性抽查,
 *     不逐条重复旧套件已经很细地断言过的每一个单元格分支——那些分支的代码
 *     没变，真正需要新覆盖的是"嵌套之后这些东西还够不够得到"这件事本身。 */

const REF_SCHEME = "secret://";
const SAMPLE_REF = `${REF_SCHEME}sub2api/prod-key`;

function account(over: Record<string, unknown> = {}) {
  return {
    id: "11111111-1111-4111-8111-111111111111",
    system_type: "sub2api",
    access_method: "upstream_key",
    base_url: "https://relay-a.example.com",
    upstream_name: "Relay A",
    upstream_contact: "运营群 @relay-a",
    upstream_group: "gpt-main",
    credential_ref: SAMPLE_REF,
    recharge_ratio: "1.15",
    group_rate: "1.25",
    recharge_cost_rate: "0.869565217",
    currency: "USD",
    business_day_tz: "+08:00",
    platform_id: "sub2api",
    status: "active",
    environment: "development",
    metered: true,
    token_mappings: [
      {
        upstream_token_id: "tok-1",
        own_account_id: "acct-9",
        credential_ref: SAMPLE_REF,
        updated_at: "2026-08-28T09:00:00Z",
      },
    ],
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-28T09:00:00Z",
    ...over,
  };
}

function upstreamSummary(over: Record<string, unknown> = {}) {
  return {
    id: "11111111-1111-4111-8111-111111111111",
    name: "relay-a",
    supplier_key: "sub2api|https://relay-a.example.com",
    system_type: "sub2api",
    access_method: "upstream_key",
    base_url: "https://relay-a.example.com",
    recharge_cost_rate: "0.869565217",
    group_rate: "1.25",
    credential_ref: SAMPLE_REF,
    status: "active",
    token_count: 3,
    supply_cost: { amount_minor: "70000000", currency: "CNY", scale: 6 },
    gross_profit: { amount_minor: "30000000", currency: "CNY", scale: 6 },
    usage_revenue: { amount_minor: "100000000", currency: "CNY", scale: 6 },
    gross_margin: "0.300000",
    coverage: {
      row_count: 1,
      revenue_known_rows: 1,
      cost_known_rows: 1,
      account_grain_rows: 0,
      mixed_currency: false,
      complete: true,
    },
    observed: {
      cost_observed_at: "2026-08-28T09:00:00Z",
      revenue_observed_at: "2026-08-28T09:00:00Z",
      updated_at: "2026-08-28T09:05:00Z",
      source: "finance.profit_daily",
    },
    runway: {
      days: 12,
      level: "warning",
      reason: "",
      window_days: 7,
      covered_days: 7,
      daily_average: { amount_minor: "10000000", currency: "CNY", scale: 6 },
      balance: { amount_minor: "123450000", currency: "CNY", scale: 6 },
      balance_observed_at: "2026-08-28T08:30:00Z",
    },
    ...over,
  };
}

function fakeResponse(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function stubAccounts(items: unknown[], summaries: unknown[] = []) {
  const fetchMock = vi.fn((url: string) => {
    if (url.includes("/finance/upstream-accounts")) return Promise.resolve(fakeResponse({ items }));
    if (url.includes("/finance/upstreams/summary")) {
      return Promise.resolve(fakeResponse({ items: summaries, from: "2026-08-28", to: "2026-08-28" }));
    }
    // 批次与代理资产：展开账号级行时才会打，默认给空
    return Promise.resolve(fakeResponse({ items: [] }));
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderPanel(platform: "sub2api" | "newapi" = "sub2api") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <UpstreamAccountsPanel platform={platform} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

async function findOuterTable() {
  return within(await screen.findByRole("table"));
}

afterEach(() => vi.unstubAllGlobals());

describe("供应商归并（新行为）", () => {
  it("按 upstream_name 归并：两个同名账号在外层表上只出现一行", async () => {
    stubAccounts([
      account({ id: "a1" }),
      account({ id: "a2", upstream_group: "gpt-budget" }),
    ]);
    renderPanel();
    const table = await findOuterTable();
    expect(await table.findByText("Relay A")).toBeTruthy();
    expect(table.getAllByText("Relay A")).toHaveLength(1);
    expect(table.getByText("2 个账号")).toBeTruthy();
  });

  it("名称为空时按网址 host 归并，并标出这是按网址归并的", async () => {
    stubAccounts([
      account({ id: "a1", upstream_name: "", base_url: "https://relay-b.example.com/v1" }),
      account({ id: "a2", upstream_name: "", base_url: "https://relay-b.example.com/v2" }),
    ]);
    renderPanel();
    const table = await findOuterTable();
    expect(await table.findByText("relay-b.example.com")).toBeTruthy();
    expect(table.getByText("按网址归并")).toBeTruthy();
  });

  it("未配对的账号（platform_id 为空）仍然可见，并计入未配对提示", async () => {
    stubAccounts([account({ id: "a1", platform_id: "" })]);
    renderPanel();
    const table = await findOuterTable();
    expect(await table.findByText("1 个未配对")).toBeTruthy();
  });

  it("登记簿是跨平台的一张表，这一页只看归属本平台的账号（+ 未配对）", async () => {
    stubAccounts([
      account({ id: "a1", upstream_name: "Relay A" }),
      account({ id: "a2", upstream_name: "Relay B", base_url: "https://relay-b.example.com", platform_id: "newapi" }),
    ]);
    renderPanel("sub2api");
    const table = await findOuterTable();
    await table.findByText("Relay A");
    expect(table.queryByText("Relay B")).toBeNull();
  });
});

describe("顶部四格（原型逐格：上游实例 / 接入账号-渠道 / 本期我方计费消耗 / 本期整体毛利）", () => {
  it("上游实例数的是供应商组数，不是账号数", async () => {
    stubAccounts([account({ id: "a1" }), account({ id: "a2" })]); // 同名，归一组
    renderPanel();
    await screen.findByText("Relay A");
    const tile = screen.getByText("上游实例").closest("article");
    expect(within(tile as HTMLElement).getByText("1")).toBeTruthy();
    expect(within(tile as HTMLElement).getByText(/归并自 2 个账号/)).toBeTruthy();
  });

  it("接入账号 / 渠道数的是账号总数", async () => {
    stubAccounts([
      account({ id: "a1", upstream_name: "Relay A" }),
      account({ id: "a2", upstream_name: "Relay B", base_url: "https://relay-b.example.com" }),
    ]);
    renderPanel();
    await screen.findByText("Relay A");
    const tile = screen.getByText("接入账号 / 渠道").closest("article");
    expect(within(tile as HTMLElement).getByText("2")).toBeTruthy();
  });

  it("金额格：汇总里没有这一条时显示未接入，不显示 0", async () => {
    stubAccounts([account()]);
    renderPanel();
    await screen.findByText("Relay A");
    expect(screen.getByText("本期我方计费消耗")).toBeTruthy();
    expect(screen.getByText("本期整体毛利")).toBeTruthy();
    expect(screen.getAllByText("未接入").length).toBeGreaterThan(0);
    expect(screen.queryByText("¥0.00")).toBeNull();
  });

  it("汇总接上之后两格显示金额", async () => {
    stubAccounts([account()], [upstreamSummary()]);
    renderPanel();
    await screen.findByText("Relay A");
    // scale-6 微单位降到币种最小单位：70000000 = ¥70.00
    expect(screen.getAllByText("¥70.00").length).toBeGreaterThan(0);
    expect(screen.getAllByText("¥30.00").length).toBeGreaterThan(0);
  });
});

describe("供应商级列（外层表）", () => {
  it("Key / 账号：按接入方式在组内分类计数", async () => {
    stubAccounts([
      account({ id: "a1", access_method: "upstream_key", metered: true }),
      account({ id: "a2", access_method: "subscription_account", metered: false }),
    ]);
    renderPanel();
    const table = await findOuterTable();
    expect(await table.findByText("1 Key · 1 账号")).toBeTruthy();
  });

  it("接入分组：有分组名就显示", async () => {
    stubAccounts([account({ upstream_group: "gpt-main" })]);
    renderPanel();
    const table = await findOuterTable();
    expect(await table.findByText("gpt-main")).toBeTruthy();
  });

  it("接入分组：组内账号都没有分组名时，这一格显式未接入并说明原因", async () => {
    stubAccounts([account({ upstream_group: "" })]);
    renderPanel();
    const table = await findOuterTable();
    const row = within((await table.findByText("Relay A")).closest("tr") as HTMLElement);
    const groupCell = row.getAllByText("未接入").find((el) => el.getAttribute("title")?.includes("分组目录"));
    expect(groupCell).toBeTruthy();
  });

  it("充值成本率：组内账号费率一致给单一值，不一致标多种费率", async () => {
    stubAccounts([account({ id: "a1" }), account({ id: "a2", recharge_cost_rate: "0.9" })]);
    renderPanel();
    const table = await findOuterTable();
    expect(await table.findByText(/种费率，见展开/)).toBeTruthy();
  });

  it("联系人：显示第一个非空联系人，多个不同联系人标数量", async () => {
    stubAccounts([
      account({ id: "a1", upstream_contact: "老王" }),
      account({ id: "a2", upstream_contact: "老李" }),
    ]);
    renderPanel();
    const table = await findOuterTable();
    expect(await table.findByText(/老王 等 2 人/)).toBeTruthy();
  });

  it("状态：全部停用显示已停用，部分停用或余额告警显示需关注，其余健康", async () => {
    stubAccounts(
      [account({ id: "a1" }), account({ id: "a2", upstream_name: "Relay B", base_url: "https://relay-b.example.com", status: "disabled" })],
      [upstreamSummary({ id: "a1", runway: { ...upstreamSummary().runway, level: "healthy" } })],
    );
    renderPanel();
    const table = await findOuterTable();
    await table.findByText("Relay A");
    expect(table.getByText("健康")).toBeTruthy();
    expect(table.getByText("已停用")).toBeTruthy();
  });

  it("详情链接指向供应商下第一个账号的既有上游详情页", async () => {
    stubAccounts([account({ id: "a1", platform_id: "sub2api" })]);
    renderPanel();
    const table = await findOuterTable();
    const link = await table.findByRole("link", { name: "详情" });
    expect(link.getAttribute("href")).toBe("/platforms/sub2api/suppliers/a1");
  });

  it("搜索按供应商名 / 网址匹配", async () => {
    stubAccounts([
      account({ id: "a1", upstream_name: "Relay A" }),
      account({ id: "a2", upstream_name: "Relay B", base_url: "https://relay-b.example.com" }),
    ]);
    renderPanel();
    const table = await findOuterTable();
    await table.findByText("Relay A");
    fireEvent.change(screen.getByRole("searchbox", { name: "搜索当前表格" }), { target: { value: "Relay B" } });
    expect(table.queryByText("Relay A")).toBeNull();
    expect(table.getByText("Relay B")).toBeTruthy();
  });
});

describe("展开供应商：账号级明细原样可用（旧行为，代表性抽查）", () => {
  it("展开后能看到账号自己的登记资料、KEY 数与余额（原有 upstreamColumns 未改）", async () => {
    stubAccounts([account()], [upstreamSummary()]);
    renderPanel();
    const outer = await findOuterTable();
    fireEvent.click(await outer.findByRole("button", { name: "详情" }));

    const inner = within(await screen.findAllByRole("table").then((tables) => tables[1] as HTMLElement));
    expect(inner.getByText("https://relay-a.example.com")).toBeTruthy();
    expect(inner.getByText("3 个 KEY")).toBeTruthy();
    expect(inner.getByText("¥123.45")).toBeTruthy();
  });

  it("再展开一层能看到令牌映射，凭据只显示状态", async () => {
    stubAccounts([account()], [upstreamSummary()]);
    renderPanel();
    const outer = await findOuterTable();
    fireEvent.click(await outer.findByRole("button", { name: "详情" }));

    const innerTables = await screen.findAllByRole("table");
    const inner = within(innerTables[1] as HTMLElement);
    fireEvent.click(inner.getByRole("button", { name: "详情" }));

    expect(await screen.findByText("tok-1")).toBeTruthy();
    expect(screen.getAllByText("已配置").length).toBeGreaterThan(0);
  });

  it("改倍率入口仍然存在且要求填理由", async () => {
    stubAccounts([account()], [upstreamSummary()]);
    renderPanel();
    const outer = await findOuterTable();
    fireEvent.click(await outer.findByRole("button", { name: "详情" }));

    const innerTables = await screen.findAllByRole("table");
    const inner = within(innerTables[1] as HTMLElement);
    fireEvent.click(inner.getByRole("button", { name: "改倍率" }));

    const dialog = within(await screen.findByRole("dialog"));
    fireEvent.click(dialog.getByRole("button", { name: "保存倍率" }));
    expect(await dialog.findByText(/必须写明为什么改/)).toBeTruthy();
  });
});

describe("入口与状态", () => {
  it("头部按钮文案照原型「＋ 添加上游」", async () => {
    stubAccounts([account()]);
    renderPanel();
    expect(await screen.findByRole("button", { name: "＋ 添加上游" })).toBeTruthy();
  });

  it("登记簿读失败时整页给错误态，而不是一张空表", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          fakeResponse({ error: { code: "PERMISSION_DENIED", message: "缺少权限 finance.read" } }, 403),
        ),
      ),
    );
    renderPanel();
    expect(await screen.findByText("无权访问")).toBeTruthy();
    expect(screen.getByText(/finance\.read/)).toBeTruthy();
  });

  it("一条账号都没有时给空态，说清去哪儿登记", async () => {
    stubAccounts([]);
    renderPanel();
    expect(await screen.findByText("还没有登记任何上游账号")).toBeTruthy();
    expect(screen.getByRole("button", { name: "＋ 添加上游" })).toBeTruthy();
  });

  it("汇总读失败与登记簿本身分开说——登记簿读到了，表照画", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) =>
        url.includes("/finance/upstreams/summary")
          ? Promise.resolve(fakeResponse({ error: { code: "INTERNAL", message: "boom" } }, 500))
          : Promise.resolve(fakeResponse({ items: url.includes("upstream-accounts") ? [account()] : [] })),
      ),
    );
    renderPanel();
    const table = await findOuterTable();
    expect(await table.findByText("Relay A")).toBeTruthy();
    expect(screen.getAllByText("读取失败").length).toBeGreaterThan(0);
  });
});
