import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  FINANCE_READ_PERMISSION,
  SUBSCRIPTION_MANAGE_PERMISSION,
  type UpstreamAccountItem,
} from "./api/finance";
import { ChannelTable } from "./components/ChannelTable";
import { FinanceSummaryCards } from "./components/FinanceSummaryCards";
import { UpstreamAccountDetail } from "./components/UpstreamAccountDetail";
import { ChannelDetailPage } from "./pages/ChannelDetailPage";
import { UpstreamDetailPage } from "./pages/UpstreamDetailPage";

/** 上游汇总（`/finance/upstreams/summary`）**只有一份缓存**——跨组件回归线。
 *
 *  ## 这条线拦的是什么
 *
 *  这份数据在仓库里一度有**两种 queryKey**：`[UPSTREAM_SUMMARY_QUERY]`
 *  （= `["finance-upstream-summary"]`）与散写的 `["finance", "upstreams", "summary"]`。
 *  react-query 的失效是**前缀匹配**，而这两个数组互不为前缀，于是谁都作废不了谁：
 *  在上游详情页登记一笔订阅批次，摊销当场变，但平台概览资金卡 / 渠道管理表 /
 *  渠道详情页照旧显示改之前的余额、可用天数与成本。
 *
 *  这个 bug 有三个性质，正好让它躲过了当时所有的用例：
 *
 *  1. 没刷新的那一半**看起来完全正常**——不是空白、不是错误态、不是「未接入」,
 *     而是一个陈旧但形状完美的读数，与正确值唯一的区别就是数值；
 *  2. **同屏内不自相矛盾**——共用同一份缓存的那几处要陈旧一起陈旧，
 *     所以不会出现「同页两个数打架」这种显眼症状，只有跨页切换才撞得见；
 *  3. **组件级用例抓不到**——每个组件的测试只装配自己那一侧，断言「写完调了
 *     invalidateQueries」，两侧都过，因为各自作废的确实是自己读的那个 key。
 *
 *  所以拦它的用例必须是**跨组件**的：同一个 `QueryClient` 下同时挂读者与写者,
 *  断言的是**读者屏幕上的数变了**，而不是「写者调了某个函数」。
 *
 *  ## 变异验证（2026-09-08 逐条跑过，四次变异 + 两组对照）
 *
 *  变异都是**改条件**（把某一处的 key 换成另一种写法），不是删代码；每次只动
 *  一处，跑完立刻还原。记录的是「红在哪一行」——红在正向锚点上的那次不算数。
 *
 *  1. `FinanceSummaryCards` 的读 key → `["finance", "upstreams", "summary"]`：
 *     用例一红在**目标断言**（卡上一直是旧的「12 天」，等不到「4 天」），
 *     用例二红在**目标断言**（缓存里 2 份）；两条的正向锚点都还是绿的。
 *  2. `UpstreamAccountDetail` 作废的 key → 同一个字面量：用例一红在同一行
 *     （写者这一侧分叉，屏幕上的症状一模一样），用例二**绿**——它不含写路径,
 *     这正是两条用例分工的证据。
 *  3. `UpstreamDetailPage` 的读 key → 同一个字面量：用例二红在**目标断言**
 *     （缓存里 2 份），用例一绿。
 *     （这一条第一次做的时候红在锚点 `findByText("OpenAI A")` 上——「找到多个」,
 *     目标断言压根没跑到，等于什么都没证明；锚点因此一律改成 `findAllBy*`。）
 *  4. 对照组甲：只改常量本身的值（八处一起跟着变，仍然彼此一致）→ 两条都绿,
 *     说明它们断的是「统一」，不是「恰好等于某个字面量」。
 *  5. 对照组乙：改一条**无关**的 key（渠道汇总）→ 两条都绿，说明它们不是
 *     「任何 key 一动就红」的哨兵。 */

const SUMMARY_URL = "/finance/upstreams/summary";

function fakeResponse(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

const ACTIVE_SERVICE = {
  id: "svc-1",
  service_type: "sub2api",
  instance_id: "sub2api-a",
  environment: "development",
  endpoint: "",
  owner: "",
  status: "active",
  source_watermark: "",
  observed_at: null,
  stale_seconds: null,
};

function money(minor: string, currency = "CNY", scale = 6) {
  return { amount_minor: minor, currency, scale };
}

const COVERAGE = {
  row_count: 1,
  revenue_known_rows: 1,
  cost_known_rows: 1,
  account_grain_rows: 0,
  mixed_currency: false,
  complete: true,
};

const OBSERVED = {
  cost_observed_at: "2026-08-28T02:00:00Z",
  revenue_observed_at: "2026-08-28T03:00:00Z",
  updated_at: null,
  source: "finance.profit_window",
};

/** 登记簿里的那一条。**订阅型**：写路径（登记批次）挂在这种账号上。 */
function subscriptionAccount(over: Partial<UpstreamAccountItem> = {}): UpstreamAccountItem {
  return {
    id: "up-1",
    system_type: "sub2api",
    access_method: "subscription_account",
    base_url: "",
    credential_ref: "secret://xm/upstream/a",
    recharge_ratio: "",
    recharge_cost_rate: "",
    currency: "USD",
    business_day_tz: "+08:00",
    platform_id: "sub2api",
    status: "active",
    environment: "development",
    metered: false,
    token_mappings: [],
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-28T09:00:00Z",
    upstream_name: "Relay 甲",
    upstream_contact: "",
    upstream_group: "gpt-main",
    group_rate: "",
    ...over,
  };
}

/** 上游汇总的一行。`days` 就是屏幕上「可用天数（最紧）」那一格的值。 */
function upstreamSummary(days: number) {
  return {
    id: "up-1",
    name: "relay-a",
    supplier_key: "sub2api|https://relay-a.example.com",
    system_type: "sub2api",
    access_method: "subscription_account",
    base_url: "",
    recharge_cost_rate: "",
    credential_ref: "secret://xm/upstream/a",
    status: "active",
    token_count: 2,
    usage_revenue: money("8000000"),
    supply_cost: money("5000000"),
    gross_profit: money("3000000"),
    coverage: COVERAGE,
    observed: OBSERVED,
    runway: {
      days,
      level: "warning",
      reason: "",
      window_days: 7,
      covered_days: 7,
      daily_average: null,
      balance: money("123450000"),
      balance_observed_at: "2026-08-28T08:30:00Z",
    },
  };
}

function channelSummary() {
  return {
    id: "up-1",
    name: "relay-a",
    system_type: "sub2api",
    access_method: "subscription_account",
    metered: false,
    base_url: "",
    platform_id: "sub2api",
    credential_ref: "secret://xm/upstream/a",
    recharge_ratio: "",
    recharge_cost_rate: "",
    business_day_tz: "+08:00",
    status: "active",
    token_count: 2,
    usage_revenue: money("8000000"),
    supply_cost: money("5000000"),
    gross_profit: money("3000000"),
    gross_margin: "0.375000",
    coverage: COVERAGE,
    observed: OBSERVED,
    runway: {
      days: null,
      level: "healthy",
      reason: "not_applicable",
      window_days: 7,
      covered_days: 7,
      daily_average: null,
      balance: null,
      balance_observed_at: null,
    },
  };
}

function boundChannelRow() {
  return {
    channel_ref: { service_id: "svc-1", external_channel_id: "channel-a" },
    name: "OpenAI A",
    binding: {
      id: "bind-1",
      upstream_account_id: "up-1",
      valid_from: "2026-08-28T00:00:00Z",
      reason: "人工确认",
    },
    candidate: {
      state: "bound",
      evidence_status: "sufficient",
      upstream_account_ids: ["up-1"],
      reason_codes: [],
      platform_assignment_missing: false,
      inventory_unknown: false,
    },
    economics: null,
    economics_state: "binding_pending_economics",
    conflicts: [],
    health: { state: "observed" },
    models: { count: 4 },
    assurance: null,
    runway: { days: null },
    observed: { source: "sub2api-a", observed_at: "2026-08-29T01:00:00Z", is_stale: false },
  };
}

function channelPage() {
  const items = [boundChannelRow()];
  return {
    service: {
      id: "svc-1",
      service_type: "sub2api",
      instance_id: "sub2api-a",
      environment: "development",
    },
    inventory: {
      state: "ok",
      source: "sub2api-a",
      observed_at: "2026-08-29T01:00:00Z",
      complete: true,
      truncated: false,
      reported_count: items.length,
      fetched_count: items.length,
      coverage_partial: false,
      evidence: "reported_count",
    },
    from: "2026-08-29",
    to: "2026-08-29",
    items,
    runway_coverage: { total: 1, known: 1, reasons: {} },
    next_cursor: null,
  };
}

/** 上游汇总端点会被**数次数**，并且写操作之后换一份新数值下发。
 *
 *  「写之后换数」是这条线的判据来源：卡上的数从 `before` 变成 `after`，
 *  唯一的可能就是那个 query 真的重新取了一次数——而它重新取数的唯一触发
 *  是写者作废了**它读的那个 key**。 */
function stubApi({ before = 12, after = 4 }: { before?: number; after?: number } = {}) {
  const state = { summaryCalls: 0, writes: 0 };
  const fetchMock = vi.fn((input: unknown) => {
    const url = String(input);
    if (url.includes("/actions/")) {
      state.writes += 1;
      return Promise.resolve(fakeResponse({ action_run_id: "run-batch-1" }));
    }
    if (url.includes(SUMMARY_URL)) {
      state.summaryCalls += 1;
      // 第一次给 before，写完之后的每一次都给 after
      const days = state.writes > 0 ? after : before;
      return Promise.resolve(
        fakeResponse({
          items: [upstreamSummary(days)],
          from: "2026-08-28",
          to: "2026-08-28",
          runway_coverage: { total: 1, known: 1, reasons: {} },
          runway_thresholds: { critical_days: 5, warning_days: 10, serious_days: 20 },
        }),
      );
    }
    if (url.includes("/finance/channels/summary")) {
      return Promise.resolve(
        fakeResponse({ items: [channelSummary()], from: "2026-08-28", to: "2026-08-28" }),
      );
    }
    if (url.includes("/finance/upstream-accounts")) {
      return Promise.resolve(fakeResponse({ items: [subscriptionAccount()] }));
    }
    if (url.includes("/finance/subscription-batches")) {
      return Promise.resolve(
        fakeResponse({ items: [], truncated: false, limit: 200, as_of: "2026-08-28" }),
      );
    }
    if (url.includes("/finance/proxy-assets")) {
      return Promise.resolve(fakeResponse({ items: [], truncated: false, limit: 200 }));
    }
    if (url.includes("/platforms/sub2api/channels")) {
      return Promise.resolve(fakeResponse(channelPage()));
    }
    if (url.includes("/api/v1/services")) {
      return Promise.resolve(fakeResponse({ items: [ACTIVE_SERVICE] }));
    }
    return Promise.resolve(fakeResponse({ items: [] }));
  });
  vi.stubGlobal("fetch", fetchMock);
  return state;
}

function newClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } });
}

/** 缓存里存着「上游汇总」的条目，**按响应形状认，不按 key 认**。
 *
 *  `runwayThresholds` 只有 `/finance/upstreams/summary` 的响应带（渠道汇总
 *  只有 items/from/to，渠道目录带的是 runwayCoverage）。用 key 去找会把
 *  「key 是否统一」这件待证的事当成前提，永远数出 1 来。 */
function upstreamSummaryEntries(client: QueryClient) {
  return client
    .getQueryCache()
    .getAll()
    .filter((query) => {
      const data = query.state.data;
      return typeof data === "object" && data !== null && "runwayThresholds" in data;
    })
    .map((query) => query.queryKey);
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("上游汇总缓存：跨组件的一份", () => {
  it("在上游账号上记一笔批次，**同屏另一个组件**的可用天数当场跟着变", async () => {
    const api = stubApi({ before: 12, after: 4 });
    const onDone = vi.fn();
    const queryClient = newClient();
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          {/* 写者：上游详情页里的那一块（登记 / 终止 / 退款都从这里发） */}
          <UpstreamAccountDetail
            account={subscriptionAccount()}
            onDone={onDone}
            scopes={[FINANCE_READ_PERMISSION, SUBSCRIPTION_MANAGE_PERMISSION]}
          />
          {/* 读者：平台概览与跨平台财务页上的资金卡，与写者分属两棵组件树,
              共同点只有一个——同一个 QueryClient */}
          <FinanceSummaryCards systemType="sub2api" label="Sub2API" />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    // 正向锚点。**旧实现下这一行照样绿**：分叉影响的是「写完之后」，不是初次取数
    expect(await screen.findByText("12 天")).toBeTruthy();
    expect(api.summaryCalls).toBe(1);

    // —— 触发一次真实的写：登记一笔订阅批次 ——
    const trigger = await screen.findByRole("button", { name: "登记/续费新增批次" });
    await vi.waitFor(() => expect((trigger as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(trigger);
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(screen.getByLabelText(/实际支付/), { target: { value: "29.99" } });
    fireEvent.change(screen.getByLabelText(/开始日期/), { target: { value: "2026-08-01" } });
    fireEvent.change(screen.getByLabelText(/到期日期/), { target: { value: "2026-08-31" } });
    fireEvent.change(screen.getByLabelText(/账号数量/), { target: { value: "1" } });
    fireEvent.click(dialog.querySelector('button[type="submit"]')!);

    // 第二个正向锚点：写确实成功了（否则下面那条断言红在别的地方，什么都没证明）
    await vi.waitFor(() =>
      expect(onDone).toHaveBeenCalledWith({ title: "订阅批次已登记", runId: "run-batch-1" }),
    );

    // —— 目标断言 ——
    // 卡上的数从 12 天变成 4 天。两侧 key 一旦分叉，这里会**一直**是「12 天」:
    // 不是错误态、不是空白，就是一个安安静静的旧数字
    expect(await screen.findByText("4 天")).toBeTruthy();
    expect(screen.queryByText("12 天")).toBeNull();
    expect(api.summaryCalls).toBe(2);
  });

  it("一屏上的多个读者在缓存里**只留一份**上游汇总，不是各存各的", async () => {
    // 上一条用例锚的是「写者 ↔ 一个读者」这一对；仓库里读这份数据的地方有六处,
    // 任何一处自己另起一个 key，症状都是同一个。这一条把其中四处放到同一屏上,
    // 直接去数缓存里有几份这个数据——分叉的那一处会多留下一份。
    //
    // **判据不取「发了几次请求」**：staleTime 默认是 0，后挂上来的观察者
    // （ChannelDetailPage 要等 /services 回来才挂）本来就会再取一次数，
    // 那个次数反映的是挂载时序，不是 key 是否一致。缓存条目数没有这个噪声。
    //
    // 认条目不靠 key 本身（那会变成拿 key 校验 key，恒真）：认的是**这个端点
    // 独有的响应形状** runwayThresholds——渠道汇总与渠道目录都不带这个字段。
    const api = stubApi();
    const queryClient = newClient();
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={["/platforms/sub2api/suppliers/up-1"]}>
          <Routes>
            <Route
              path="/platforms/:serviceType/suppliers/:upstreamId"
              element={<UpstreamDetailPage />}
            />
          </Routes>
        </MemoryRouter>
        <MemoryRouter initialEntries={["/platforms/sub2api/upstream/detail/channel-a"]}>
          <Routes>
            <Route
              path="/platforms/:serviceType/upstream/detail/:channelId"
              element={<ChannelDetailPage />}
            />
          </Routes>
        </MemoryRouter>
        <MemoryRouter>
          {/* serviceId + active ⇒ ChannelTable 内部挂 ManagedChannelTable,
              两个组件各有一处读 */}
          <ChannelTable platform="sub2api" lead="Sub2API" serviceId="svc-1" serviceStatus="active" />
          <FinanceSummaryCards systemType="sub2api" label="Sub2API" />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    // 正向锚点：四棵树都真的挂起来并取到了数。少了这几行，「缓存里只有一份」
    // 会在某个组件根本没渲染时恒真
    // 一律用 findAllBy*：这几段文字在多棵树上都会出现（渠道名同时是详情页的
    // 一个字段和上游详情页渠道清单里的一个链接），findBy* 会因为「找到多个」
    // 而红在锚点上，把真正要证的那条断言挡在后面
    expect((await screen.findAllByText("Relay 甲")).length).toBeGreaterThan(0);
    expect((await screen.findAllByText("OpenAI A")).length).toBeGreaterThan(0);
    expect((await screen.findAllByText("12 天")).length).toBeGreaterThan(0);
    await vi.waitFor(() => expect(queryClient.isFetching()).toBe(0));
    // 端点确实被读到了（不是所有读者都挂在错误态上空转）
    expect(api.summaryCalls).toBeGreaterThan(0);

    // 目标断言：四个组件、六处读，缓存里只有一份上游汇总
    expect(upstreamSummaryEntries(queryClient)).toHaveLength(1);
  });
});
