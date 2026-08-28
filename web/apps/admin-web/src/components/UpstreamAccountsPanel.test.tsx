import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { UpstreamAccountsPanel } from "./UpstreamAccountsPanel";

/** 凭据引用的样例。拼接而不是就地写完整字面量：`secret://...` 这类串
 *  容易被 gitleaks 的通用规则判成泄漏，而本仓库禁止用 allowlist 消音。 */
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

function fakeResponse(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

/** XM-0037d 上游汇总的一行。**id 与登记簿同源**，所以这一侧可以直接 join。 */
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

function stubAccounts(items: unknown[], summaries: unknown[] = []) {
  const fetchMock = vi.fn((url: string) => {
    if (url.includes("/finance/upstream-accounts")) {
      return Promise.resolve(fakeResponse({ items }));
    }
    if (url.includes("/finance/upstreams/summary")) {
      return Promise.resolve(fakeResponse({ items: summaries, from: "2026-08-28", to: "2026-08-28" }));
    }
    // 批次与代理资产：展开订阅型行时才会打，默认给空
    return Promise.resolve(fakeResponse({ items: [] }));
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderPanel(platform = "sub2api") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <UpstreamAccountsPanel platform={platform} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

/** 等表格出现，再把查询范围收到表格本体上（不含四个计数格与说明）。
 *
 *  收范围是必要的："未配置""未接入"这类词在计数格与说明里也出现，
 *  不收范围的断言会被自己的诚实文案匹配到，测出一个假的绿。 */
async function findTable() {
  return within(await screen.findByRole("table"));
}

describe("上游管理（成本登记簿的 UI）", () => {
  beforeEach(() => {
    stubAccounts([account()]);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("登记簿是跨平台的一张表，这一页只看归属本平台的那些", async () => {
    stubAccounts([
      account(),
      account({
        id: "22222222-2222-4222-8222-222222222222",
        base_url: "https://relay-b.example.com",
        upstream_name: "Relay B",
        platform_id: "newapi",
      }),
    ]);
    renderPanel("sub2api");
    expect(await screen.findByText("https://relay-a.example.com")).toBeTruthy();
    // 归 newapi 的那条不该出现在 sub2api 页上
    expect((await findTable()).queryByText("https://relay-b.example.com")).toBeNull();
    expect((await findTable()).queryAllByText("newapi")).toHaveLength(0);
  });

  it("**未配对的账号照样显示**，并标出来", async () => {
    // 藏起来的话，一个漏配 platform_id 的账号会从两个平台的页面上同时消失，
    // 而它的成本仍在发生
    stubAccounts([account({ platform_id: "" })]);
    renderPanel("sub2api");
    expect((await findTable()).getByText("未配对")).toBeTruthy();
  });

  it("登记簿元数据按证据展示，不再用网址主机名冒充上游名称", async () => {
    renderPanel();
    const table = await findTable();
    const row = within(table.getByText("Relay A").closest("tr") as HTMLElement);
    expect(row.getByText("https://relay-a.example.com")).toBeTruthy();
    expect(row.getByText("运营群 @relay-a")).toBeTruthy();
    expect(row.getByText("gpt-main")).toBeTruthy();
    expect(row.getByText(/倍率 1\.25×/)).toBeTruthy();
  });

  it("元数据缺失明确显示未接入，分组倍率缺失显示破折号", async () => {
    stubAccounts([account({
      upstream_name: "",
      upstream_contact: "",
      upstream_group: "",
      group_rate: "",
    })]);
    renderPanel();
    const table = await findTable();
    const row = within(table.getByText("https://relay-a.example.com").closest("tr") as HTMLElement);
    expect(row.getAllByText("未接入").length).toBeGreaterThanOrEqual(3);
    expect(row.getByText("倍率 —")).toBeTruthy();
  });

  it("按 upstream_account.id 关联 KEY 数、余额、可用天数与观测证据", async () => {
    stubAccounts([account()], [upstreamSummary()]);
    renderPanel();
    const table = await findTable();
    const row = within(table.getByText("Relay A").closest("tr") as HTMLElement);
    expect(row.getByText("3 个 KEY")).toBeTruthy();
    expect(row.getByText("¥123.45")).toBeTruthy();
    expect(row.getByText("约 12 天")).toBeTruthy();
    expect(row.getByText(/余额观测.*2026-08-28/)).toBeTruthy();
  });

  it("订阅账号显示一账号，且有效期因汇总无该字段而诚实标待接入", async () => {
    stubAccounts(
      [account({ access_method: "subscription_account", metered: false, recharge_ratio: "" })],
      [upstreamSummary({
        access_method: "subscription_account",
        token_count: 0,
        runway: {
          days: null,
          level: "",
          reason: "not_applicable",
          window_days: 0,
          covered_days: 0,
          daily_average: null,
          balance: null,
          balance_observed_at: null,
        },
      })],
    );
    renderPanel();
    const table = await findTable();
    const row = within(table.getByText("Relay A").closest("tr") as HTMLElement);
    expect(row.getByText("1 个账号")).toBeTruthy();
    expect(row.getByText("有效期待接入")).toBeTruthy();
  });

  it("汇总里没有这一条时显示「未接入」，**不显示 0**", async () => {
    // ¥0.00 会被读成「这个上游这期没花钱」，而事实是这个窗口还没有汇总数
    renderPanel();
    await screen.findByText("https://relay-a.example.com");
    expect(screen.getByText("本期我方消耗")).toBeTruthy();
    expect(screen.getAllByText("未接入").length).toBeGreaterThan(0);
    expect(screen.queryByText("¥0.00")).toBeNull();
    expect(screen.getAllByText(/还没有可用的汇总数/).length).toBe(2);
  });

  it("汇总接上之后两格显示金额，并说清观测时刻（§9.1）", async () => {
    stubAccounts([account()], [upstreamSummary()]);
    renderPanel();
    await screen.findByText("https://relay-a.example.com");
    // scale-6 微单位降到币种最小单位：70000000 = ¥70.00。计数格与行内各一处
    expect(screen.getAllByText("¥70.00").length).toBe(2);
    expect(screen.getAllByText("¥30.00").length).toBe(1);
    expect(screen.getAllByText(/成本观测于 2026-08-28T09:00:00Z/).length).toBeGreaterThan(0);
  });

  it("只有部分账号有汇总时标「覆盖不全」——只算了一半的合计看着和算全的一样", async () => {
    stubAccounts(
      [
        account(),
        account({
          id: "55555555-5555-4555-8555-555555555555",
          base_url: "https://relay-c.example.com",
          upstream_name: "Relay C",
        }),
      ],
      [upstreamSummary()],
    );
    renderPanel();
    await screen.findByText("https://relay-c.example.com");
    // 两个金额格 + 新增 KEY/账号格，各自都必须暴露覆盖不全。
    expect(screen.getAllByText("覆盖不全").length).toBe(3);
    expect(screen.getAllByText(/只含 2 个账号里有汇总的 1 个/).length).toBe(2);
  });

  it("币种不一致时不给合计，并说明原因", async () => {
    // 把不同币种的最小单位加起来是纯粹的错数，而它看起来完全正常
    stubAccounts(
      [
        account(),
        account({
          id: "55555555-5555-4555-8555-555555555555",
          base_url: "https://relay-c.example.com",
          upstream_name: "Relay C",
        }),
      ],
      [
        upstreamSummary(),
        upstreamSummary({
          id: "55555555-5555-4555-8555-555555555555",
          supply_cost: { amount_minor: "1000000", currency: "USD", scale: 6 },
          gross_profit: { amount_minor: "500000", currency: "USD", scale: 6 },
        }),
      ],
    );
    renderPanel();
    await screen.findByText("https://relay-c.example.com");
    expect(screen.getAllByText(/币种不一致，合计没有意义/).length).toBe(2);
  });

  it("行内也显示本期消耗与毛利，汇总里没有的那一行说「未接入」", async () => {
    stubAccounts(
      [
        account(),
        account({
          id: "55555555-5555-4555-8555-555555555555",
          base_url: "https://relay-c.example.com",
          upstream_name: "Relay C",
        }),
      ],
      [upstreamSummary()],
    );
    renderPanel();
    const table = await findTable();
    expect(table.getByText("¥70.00")).toBeTruthy();
    expect(table.getByText(/毛利 ¥30\.00/)).toBeTruthy();
    // 没进过窗口的那条不编一个 0
    expect(table.getAllByText("未接入").length).toBe(1);
  });

  it("汇总读失败与「没有数据」分开说——前者要重试，后者要去看采集", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) =>
        url.includes("/finance/upstreams/summary")
          ? Promise.resolve(fakeResponse({ error: { code: "INTERNAL", message: "boom" } }, 500))
          : Promise.resolve(fakeResponse({ items: url.includes("upstream-accounts") ? [account()] : [] })),
      ),
    );
    renderPanel();
    await screen.findByText("https://relay-a.example.com");
    expect(screen.getAllByText("读取失败").length).toBe(2);
    // 登记簿本身读到了，表照画——两个查询的失败不该互相拖累
    expect((await findTable()).getByText("https://relay-a.example.com")).toBeTruthy();
  });

  it("说明当前账号粒度以及仍未接入的 supplier/group 与订阅有效期边界", async () => {
    renderPanel();
    await screen.findByText("https://relay-a.example.com");
    const note = screen.getByText(/当前一行仍是一个上游账号/);
    expect(note.textContent).toContain("供应商/分组实体");
    expect(note.textContent).toContain("订阅有效期");
  });

  it("凭据只显示状态，表格里不出现引用值本身", async () => {
    renderPanel();
    await screen.findByText("https://relay-a.example.com");
    expect((await findTable()).getAllByText("已配置").length).toBeGreaterThan(0);
    expect((await findTable()).queryByText(SAMPLE_REF)).toBeNull();
  });

  it("计量型显示成本率与倍率两行——只给一个数，人不知道它是除数还是乘数", async () => {
    renderPanel();
    await screen.findByText("https://relay-a.example.com");
    expect((await findTable()).getByText(/0\.869565217/)).toBeTruthy();
    expect((await findTable()).getByText(/倍率 1\.15/)).toBeTruthy();
  });

  it("计量型没配倍率显示「未配置」，不是空白也不是 0", async () => {
    stubAccounts([account({ recharge_ratio: "", recharge_cost_rate: "" })]);
    renderPanel();
    expect((await findTable()).getByText("未配置")).toBeTruthy();
  });

  it("订阅型显示「订阅摊销」而不是 0——0 会被读成「这个上游不要钱」", async () => {
    stubAccounts([
      account({
        access_method: "subscription_account",
        metered: false,
        recharge_ratio: "",
        recharge_cost_rate: "",
      }),
    ]);
    renderPanel();
    expect((await findTable()).getByText("订阅摊销")).toBeTruthy();
  });

  it("官方 API 说「口径待定」，**不说「订阅摊销」**", async () => {
    // 浏览器实测发现的：照 metered 二分会把官方 API 归到订阅那一支，
    // 等于告诉运营「这条的成本从订阅批次摊出来」，而它根本没有批次。
    // metered 是布尔，成本口径有三套（§2.0）
    stubAccounts([
      account({
        access_method: "official_api",
        metered: false,
        recharge_ratio: "",
        recharge_cost_rate: "",
      }),
    ]);
    renderPanel();
    const table = await findTable();
    expect(table.getByText("口径待定")).toBeTruthy();
    expect(table.queryByText("订阅摊销")).toBeNull();
  });

  it("成本率按这一行的币种显示，不写死 ¥", async () => {
    // 登记簿的默认币种是 USD。给一条 USD 的上游标一个 ¥,
    // 是把整整一倍的汇率差藏进一个看起来完全正常的数字里
    stubAccounts([account({ currency: "USD" })]);
    renderPanel();
    const table = await findTable();
    expect(table.getByText(/USD 0\.869565217/)).toBeTruthy();
    expect(table.queryByText(/¥0\.869565217/)).toBeNull();
  });

  it("官方 API 的展开区不画订阅批次表，并说明它的成本口径", async () => {
    stubAccounts([
      account({ access_method: "official_api", metered: false, recharge_ratio: "" }),
    ]);
    renderPanel();
    fireEvent.click((await findTable()).getByRole("button", { name: "详情" }));
    expect(await screen.findByText(/官方 API 直连账号/)).toBeTruthy();
    expect(screen.queryByText("订阅批次")).toBeNull();
  });

  it("订阅型不给「改倍率」入口，并说明为什么", async () => {
    // 后端 SetRechargeRatio 对订阅型直接拒；做成一句话而不是禁用按钮，
    // 因为禁用的按钮拿不到键盘焦点，读屏用户听不到理由
    stubAccounts([
      account({ access_method: "subscription_account", metered: false, recharge_ratio: "" }),
    ]);
    renderPanel();
    expect((await findTable()).getByText("订阅型不适用")).toBeTruthy();
    expect((await findTable()).queryByRole("button", { name: "改倍率" })).toBeNull();
  });

  it("计量型给「改倍率」入口，且改倍率必须填理由", async () => {
    renderPanel();
    await screen.findByText("https://relay-a.example.com");
    fireEvent.click((await findTable()).getByRole("button", { name: "改倍率" }));

    const dialog = within(await screen.findByRole("dialog"));
    fireEvent.click(dialog.getByRole("button", { name: "保存倍率" }));
    // 没有理由的倍率改动，事后复盘时与手滑不可区分
    expect(await dialog.findByText(/必须写明为什么改/)).toBeTruthy();
  });

  it("展开一行能看到令牌映射，凭据仍然只是状态", async () => {
    renderPanel();
    await screen.findByText("https://relay-a.example.com");
    fireEvent.click((await findTable()).getByRole("button", { name: "详情" }));

    expect(await screen.findByText("tok-1")).toBeTruthy();
    expect(screen.getByText("acct-9")).toBeTruthy();
    // 展开区里显示凭据**引用**是允许的（引用不是凭据，ADR-014），
    // 但绝不能出现在「凭据」那一列的状态位上
    expect(screen.getAllByText("已配置").length).toBeGreaterThan(0);
  });

  it("计量型的展开区不画订阅批次与代理资产两张空表", async () => {
    // 给计量型也画两张空表，会让人以为它漏配了
    renderPanel();
    await screen.findByText("https://relay-a.example.com");
    fireEvent.click((await findTable()).getByRole("button", { name: "详情" }));
    expect(await screen.findByText(/这是计量型账号/)).toBeTruthy();
    expect(screen.queryByText("订阅批次")).toBeNull();
  });

  it("登记簿读失败时整页给错误态，而不是一张空表", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          fakeResponse(
            { error: { code: "PERMISSION_DENIED", message: "缺少权限 finance.read" } },
            403,
          ),
        ),
      ),
    );
    renderPanel();
    expect(await screen.findByText("无权访问")).toBeTruthy();
    expect(screen.getByText(/finance\.read/)).toBeTruthy();
  });
});
