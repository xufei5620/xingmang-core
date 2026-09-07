import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { FinancePage } from "./FinancePage";

/** 页面读的是真实挂钟「今天」（`businessTodayDateOnly()` 内部用
 *  Asia/Shanghai 算业务日），所以指标 fixture 的 `day` 必须跟着算，
 *  否则单日粒度判据会把每一格都判成「暂无匹配业务日的支付日汇总」。 */
function businessTodayForTest(): string {
  const parts = new Intl.DateTimeFormat("en-US", {
    timeZone: "Asia/Shanghai",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(new Date());
  const values = Object.fromEntries(parts.map((part) => [part.type, part.value]));
  return `${values.year ?? "1970"}-${values.month ?? "01"}-${values.day ?? "01"}`;
}

const TODAY = businessTodayForTest();
const INVOICE_ORIGIN = "https://invoice.example.test";

const FRESH = {
  state: "fresh" as const,
  staleness_seconds: 5,
  threshold_seconds: 1800,
  is_partial: false,
  observed_at: "2026-09-07T03:04:05Z",
  last_success: "2026-09-07T03:04:05Z",
  last_error_code: "",
};

function paymentsMetric(
  key: string,
  value: Record<string, unknown>,
  freshness: typeof FRESH = FRESH,
) {
  return {
    metric_key: key,
    source: key.split(".")[0] ?? "",
    environment: "development",
    watermark: "",
    value,
    freshness,
  };
}

function dailyValue(overrides: Record<string, unknown> = {}) {
  return {
    day: TODAY,
    currency: "CNY",
    by_status: {
      succeeded: { count: 3, amount_minor_units: 12345 },
      refunded: { count: 1, amount_minor_units: 500 },
    },
    fee_minor_units: 100,
    net_minor_units: null,
    ...overrides,
  };
}

const MONEY = (amount: string) => ({ amount_minor: amount, currency: "CNY", scale: 6 });

function rawChannel(overrides: Record<string, unknown> = {}) {
  return {
    id: "ch-1",
    name: "渠道一",
    system_type: "sub2api",
    access_method: "api_key",
    metered: true,
    business_day_tz: "Asia/Shanghai",
    status: "enabled",
    token_count: 2,
    usage_revenue: MONEY("1000000"),
    supply_cost: MONEY("400000"),
    gross_profit: MONEY("600000"),
    gross_margin: "0.600000",
    coverage: {
      row_count: 2,
      revenue_known_rows: 2,
      cost_known_rows: 2,
      account_grain_rows: 0,
      mixed_currency: false,
      complete: true,
    },
    observed: { updated_at: "2026-09-07T03:00:00Z", source: "finance.profit_daily" },
    runway: { days: null, level: "", reason: "no_balance", window_days: 7, covered_days: 0 },
    ...overrides,
  };
}

function rawUpstream(overrides: Record<string, unknown> = {}) {
  return { ...rawChannel(), supplier_key: "supplier-1", ...overrides };
}

interface Fixtures {
  metrics?: unknown[];
  channels?: unknown[];
  upstreams?: unknown[];
  runwayCoverage?: { total: number; known: number };
}

function jsonResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

function renderFinance(initialEntry = "/finance", fixtures: Fixtures = {}) {
  const metrics = fixtures.metrics ?? [
    paymentsMetric("sub2api.payments.daily", dailyValue()),
    paymentsMetric(
      "newapi.payments.daily",
      dailyValue({ by_status: { succeeded: { count: 1, amount_minor_units: 500 } } }),
    ),
  ];
  const channels = fixtures.channels ?? [rawChannel()];
  const upstreams = fixtures.upstreams ?? [rawUpstream()];
  const coverage = fixtures.runwayCoverage ?? { total: 1, known: 0 };

  const fetchImpl = vi.fn(async (input: unknown) => {
    const url = String(input);
    if (url.includes("/api/v1/finance/channels/summary")) {
      return jsonResponse({ items: channels, from: TODAY, to: TODAY });
    }
    if (url.includes("/api/v1/finance/upstreams/summary")) {
      return jsonResponse({
        items: upstreams,
        from: TODAY,
        to: TODAY,
        runway_coverage: { ...coverage, reasons: {} },
        runway_thresholds: { critical_days: 3, warning_days: 7, serious_days: 14 },
      });
    }
    if (url.includes("/api/v1/metrics")) return jsonResponse({ items: metrics });
    throw new Error(`未预期的请求：${url}`);
  });
  vi.stubGlobal("fetch", fetchImpl);

  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <FinancePage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return fetchImpl;
}

afterEach(() => {
  vi.unstubAllGlobals();
  delete window.__XM_CONFIG__;
});

describe("FinancePage 骨架与子页签", () => {
  it("默认进入财务总览，六格页签都在，三条真实端点都被请求", async () => {
    const fetchImpl = renderFinance();

    for (const label of [
      "财务总览",
      "支付通道",
      "财务对账",
      "异常与冻结",
      "开票集成",
      "财务配置",
    ]) {
      expect(screen.getByRole("tab", { name: label })).toBeTruthy();
    }

    expect(await screen.findByText("平台资金构成")).toBeTruthy();
    await waitFor(() => {
      const urls = JSON.stringify(fetchImpl.mock.calls);
      expect(urls).toContain("/api/v1/metrics");
      expect(urls).toContain("/api/v1/finance/channels/summary");
      expect(urls).toContain("/api/v1/finance/upstreams/summary");
    });
  });

  it("未知子页不静默回落到财务总览，并给回默认格的链接", async () => {
    renderFinance("/finance?sub=not-a-real-tab");
    expect(await screen.findByText("「not-a-real-tab」子页尚未接入")).toBeTruthy();
    expect(screen.getByRole("link", { name: "返回财务总览" }).getAttribute("href")).toBe(
      "/finance?sub=overview",
    );
    // 回落的话「平台资金构成」会出现在这一屏上
    expect(screen.queryByText("平台资金构成")).toBeNull();
  });
});

describe("页顶统计格", () => {
  it("现金到账＝两平台 succeeded 桶合计，并说清这个数由谁组成", async () => {
    renderFinance();
    // 12345 + 500 = 12845 分
    const tile = (await screen.findByText("现金到账")).closest("article");
    expect(tile).not.toBeNull();
    expect(within(tile as HTMLElement).getByText("¥128.45")).toBeTruthy();
    expect(within(tile as HTMLElement).getByText(/Sub2API \+ NewAPI 合计/)).toBeTruthy();
  });

  it("跨币种 fail closed：不相加，显示「—」并说「币种不一致，合计给不出」", async () => {
    renderFinance("/finance", {
      metrics: [
        paymentsMetric("sub2api.payments.daily", dailyValue()),
        paymentsMetric(
          "newapi.payments.daily",
          dailyValue({
            currency: "USD",
            by_status: { succeeded: { count: 1, amount_minor_units: 500 } },
          }),
        ),
      ],
    });
    const tile = (await screen.findByText("现金到账")).closest("article") as HTMLElement;
    expect(within(tile).getByText("—")).toBeTruthy();
    expect(within(tile).getByText(/币种不一致，合计给不出/)).toBeTruthy();
    expect(within(tile).getByText(/Sub2API CNY/)).toBeTruthy();
  });

  it("一个平台今天没有支付日汇总时，整格给不出，而不是只显示另一个平台的数", async () => {
    renderFinance("/finance", {
      metrics: [paymentsMetric("sub2api.payments.daily", dailyValue())],
    });
    const tile = (await screen.findByText("现金到账")).closest("article") as HTMLElement;
    expect(within(tile).getByText("—")).toBeTruthy();
    expect(within(tile).getByText(/NewAPI/)).toBeTruthy();
    // 只算了 Sub2API 的那个数绝不能出现在这一格里
    expect(within(tile).queryByText("¥123.45")).toBeNull();
  });

  it("退款 / 冻结只取 Sub2API，NewAPI 记为「不适用」而不是缺口，并标注冻结不在这个数里", async () => {
    renderFinance();
    const tile = (await screen.findByText("退款 / 冻结")).closest("article") as HTMLElement;
    expect(within(tile).getByText("¥5.00")).toBeTruthy();
    expect(within(tile).getByText(/NewAPI 不适用/)).toBeTruthy();
    expect(within(tile).getByText(/冻结在开票系统侧，平台不复制/)).toBeTruthy();
  });

  it("对账差异与开票数据延迟保持「—」，并各自说清是没有源还是刻意不接", async () => {
    renderFinance();
    const diff = (await screen.findByText("对账差异")).closest("article") as HTMLElement;
    expect(within(diff).getByText(/没有对账批次表/)).toBeTruthy();

    const invoice = screen.getByText("开票数据延迟").closest("article") as HTMLElement;
    expect(within(invoice).getByText(/CR-0005/)).toBeTruthy();
    expect(within(invoice).getByText(/刻意不接，不是漏做/)).toBeTruthy();
  });
});

describe("财务总览：成本采集状态与需要处理", () => {
  it("成本侧一行都没采到时，明说是「成本还没采到」而不是「今天成本为 0」", async () => {
    renderFinance("/finance", {
      channels: [
        rawChannel({
          coverage: {
            row_count: 2,
            revenue_known_rows: 2,
            cost_known_rows: 0,
            account_grain_rows: 0,
            mixed_currency: false,
            complete: false,
          },
        }),
      ],
    });
    expect(
      await screen.findByText(/成本还没采到，不是「今天成本为 0」/),
    ).toBeTruthy();
  });

  it("成本侧全部采到时，明说这一屏的 0 是真的 0", async () => {
    renderFinance();
    expect(await screen.findByText(/显示 0 就是真的 0/)).toBeTruthy();
  });

  it("余额一个都没读到时，「需要处理」说的是链路没接，不是今天没事", async () => {
    renderFinance("/finance", { runwayCoverage: { total: 3, known: 0 } });
    expect(await screen.findByText("可用天数这条线还没有输入")).toBeTruthy();
    expect(screen.getByText(/余额还没采到/)).toBeTruthy();
  });

  it("有上游触发可用天数档位时逐行列出，最紧的排最前", async () => {
    renderFinance("/finance", {
      runwayCoverage: { total: 2, known: 2 },
      upstreams: [
        rawUpstream({
          id: "up-warn",
          name: "上游·松",
          runway: {
            days: 9,
            level: "warning",
            reason: "",
            window_days: 7,
            covered_days: 7,
            balance: MONEY("900000"),
            daily_average: MONEY("100000"),
            balance_observed_at: "2026-09-07T01:00:00Z",
          },
        }),
        rawUpstream({
          id: "up-crit",
          name: "上游·紧",
          runway: {
            days: 2,
            level: "critical",
            reason: "",
            window_days: 7,
            covered_days: 7,
            balance: MONEY("200000"),
            daily_average: MONEY("100000"),
            balance_observed_at: "2026-09-07T01:00:00Z",
          },
        }),
      ],
    });
    const table = (await screen.findByRole("table", {
      name: /需要处理的事项/,
    })) as HTMLTableElement;
    const cells = within(table)
      .getAllByRole("row")
      .slice(1)
      .map((row) => row.textContent ?? "");
    expect(cells[0]).toContain("上游「上游·紧」可用天数 2 天");
    expect(cells[1]).toContain("上游「上游·松」可用天数 9 天");
    // 覆盖边界必须说出来：这一卡今天只收可用天数一类事项
    expect(screen.getByText(/不等于「今天全部要处理的事」/)).toBeTruthy();
  });

  it("总览页说清这一屏只覆盖今天一个业务日，不冒充周 / 月合计", async () => {
    renderFinance();
    expect(await screen.findByText(new RegExp(`只覆盖今天这一个业务日（${TODAY}`))).toBeTruthy();
  });
});

describe("后端还不存在的三格：蓝图 + 一句「今天为什么填不了」", () => {
  it("支付通道：说清没有支付 Connector，并保留原型逐字列头", async () => {
    renderFinance("/finance?sub=channels");
    expect(await screen.findByText("「支付通道」尚未接入")).toBeTruthy();
    expect(screen.getByText(/connectors\/payment\/ 里只有 \.gitkeep/)).toBeTruthy();
    expect(screen.getByRole("columnheader", { name: "24h 成功率" })).toBeTruthy();
    expect(screen.getByRole("columnheader", { name: "结算币种" })).toBeTruthy();
  });

  it("财务对账：说清没有对账域，并保留对账批次的列头", async () => {
    renderFinance("/finance?sub=reconciliation");
    expect(await screen.findByText("「财务对账」尚未接入")).toBeTruthy();
    expect(screen.getByText(/没有对账批次表/)).toBeTruthy();
    expect(screen.getByRole("columnheader", { name: "支付金额" })).toBeTruthy();
  });

  it("异常与冻结：两条阻塞都说出来（没有来源，且执行入口未解锁）", async () => {
    renderFinance("/finance?sub=exceptions");
    expect(await screen.findByText("「异常与冻结」尚未接入")).toBeTruthy();
    expect(screen.getByText(/Advanced Controls（Foundation-B \/ XM-0030）/)).toBeTruthy();
  });
});

describe("开票集成：由开票系统自己的控制台承载", () => {
  it("配置了来源时嵌入 global 模式的控制台", async () => {
    window.__XM_CONFIG__ = { invoiceConsoleOrigin: INVOICE_ORIGIN };
    renderFinance("/finance?sub=invoicing");
    const frame = (await screen.findByTitle("开票")) as HTMLIFrameElement;
    expect(frame.tagName).toBe("IFRAME");
    expect(frame.src).toBe(`${INVOICE_ORIGIN}/embed/admin/global`);
  });

  it("未配置来源时如实说缺哪个环境变量，而不是回落到蓝图卡", async () => {
    renderFinance("/finance?sub=invoicing");
    expect(
      await screen.findByText(/未配置开票控制台来源（XM_INVOICE_CONSOLE_ORIGIN）/),
    ).toBeTruthy();
    // 蓝图那张「集成契约状态」卡不该在这一格出现——它是被真实组件接管的
    expect(screen.queryByText("集成契约状态")).toBeNull();
  });
});

describe("财务配置：已冻结的决定，不是查询结果", () => {
  it("逐字给出 ADR-006 的核心规则与四个金额键", async () => {
    renderFinance("/finance?sub=settings");
    expect(
      await screen.findByText(
        /只有实际真实充值且已经消费的现金金额可开票；赠送、返利、兑换码、管理员赠额和其他非现金额度不可开票。/,
      ),
    ).toBeTruthy();
    for (const key of ["cash_paid", "bonus_granted", "cash_refunded", "invoice_eligible"]) {
      expect(screen.getByText(key)).toBeTruthy();
    }
    expect(screen.getByText(/开票资格算法只在开票系统实现/)).toBeTruthy();
  });

  it("三项写入功能全部锁定，逐条写出解锁条件，且不出现任何执行入口", async () => {
    renderFinance("/finance?sub=settings");
    const table = (await screen.findByRole("table", {
      name: /写入功能、当前状态与解锁条件/,
    })) as HTMLTableElement;
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows.map((row) => row.querySelector("td")?.textContent)).toEqual([
      "退款",
      "补单",
      "对账纠正",
    ]);
    expect(within(table).getAllByText("锁定")).toHaveLength(3);
    expect(within(table).getByText(/ADVANCED_CONTROLS_REQUIRED/)).toBeTruthy();
    // 锁定项不出现执行入口：整页除了页头的「刷新」不该有任何动作按钮
    expect(screen.getAllByRole("button").map((button) => button.textContent)).toEqual(["刷新"]);
  });
});
