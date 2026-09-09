import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { PaymentSummaryCards } from "./PaymentSummaryCards";

function response(status: number, body: unknown): Response {
  return { ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function renderCards(platform: "sub2api" | "newapi", range = { from: "2026-08-28", to: "2026-08-28" }) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <PaymentSummaryCards platform={platform} range={range} />
    </QueryClientProvider>,
  );
}

const fresh = {
  state: "fresh",
  staleness_seconds: 5,
  threshold_seconds: 1800,
  is_partial: false,
  observed_at: "2026-08-28T10:00:00Z",
  last_success: "2026-08-28T10:00:00Z",
  last_error_code: "",
};

function paymentsMetric(overrides: Record<string, unknown> = {}, valueOverrides: Record<string, unknown> = {}) {
  return {
    metric_key: "sub2api.payments.daily",
    source: "sub2api-prod",
    environment: "development",
    watermark: "day:2026-08-28",
    value: {
      day: "2026-08-28",
      currency: "USD",
      by_status: {
        succeeded: { count: 42, amount_minor_units: 418000 },
        pending: { count: 3, amount_minor_units: 9900 },
        failed: { count: 1, amount_minor_units: 3000 },
        refunded: { count: 1, amount_minor_units: 20200 },
      },
      fee_minor_units: 20000,
      net_minor_units: null,
      ...valueOverrides,
    },
    freshness: fresh,
    ...overrides,
  };
}

async function cardFor(label: string): Promise<HTMLElement> {
  const heading = await screen.findByRole("heading", { name: label, level: 3 });
  return heading.closest("article") as HTMLElement;
}

describe("PaymentSummaryCards（XM-PAY1）", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("六张卡在无匹配指标时全部诚实显示未接入，不编 0", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response(200, { items: [] }))));
    renderCards("sub2api");

    for (const label of ["区间成功到账", "区间待处理", "区间失败", "退款与冲正", "支付手续费", "净现金流入"]) {
      const card = await cardFor(label);
      expect(within(card).getByText("未接入")).toBeTruthy();
      expect(within(card).getByText("—")).toBeTruthy();
    }
    expect(screen.queryByText("¥0.00")).toBeNull();
    expect(screen.queryByText("$0.00")).toBeNull();
  });

  it("Sub2API 有匹配指标时四个分桶卡显示真实金额与笔数", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response(200, { items: [paymentsMetric()] }))));
    renderCards("sub2api");

    const succeeded = await cardFor("区间成功到账");
    expect(within(succeeded).getByText("$4,180.00")).toBeTruthy();
    expect(within(succeeded).getByText(/42 笔/)).toBeTruthy();

    const refunded = await cardFor("退款与冲正");
    expect(within(refunded).getByText("$202.00")).toBeTruthy();

    const fee = await cardFor("支付手续费");
    expect(within(fee).getByText("$200.00")).toBeTruthy();

    const net = await cardFor("净现金流入");
    expect(within(net).getByText("未接入")).toBeTruthy();
    expect(within(net).getByText(/净现金流公式尚未确定/)).toBeTruthy();
  });

  it("NewAPI 的退款与冲正恒为「不适用」，与「未接入」不是同一个状态", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response(200, { items: [] }))));
    renderCards("newapi");

    const refunded = await cardFor("退款与冲正");
    expect(within(refunded).getByText("不适用")).toBeTruthy();
    expect(within(refunded).getByText(/NewAPI 没有退款概念/)).toBeTruthy();
    expect(within(refunded).queryByText("未接入")).toBeNull();
  });

  it("NewAPI 即使有 payments.daily 指标，退款与冲正仍固定不适用（不看指标里有没有 refunded 键）", async () => {
    const metric = paymentsMetric({ metric_key: "newapi.payments.daily", source: "newapi-prod" });
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response(200, { items: [metric] }))));
    renderCards("newapi");

    const succeeded = await cardFor("区间成功到账");
    expect(within(succeeded).getByText("$4,180.00")).toBeTruthy();
    const refunded = await cardFor("退款与冲正");
    expect(within(refunded).getByText("不适用")).toBeTruthy();
  });

  it("覆盖不全：命中非合约币种时分桶卡与手续费卡都要说清楚，不是笼统的「数据不完整」", async () => {
    const metric = paymentsMetric({ freshness: { ...fresh, is_partial: true } });
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response(200, { items: [metric] }))));
    renderCards("sub2api");

    const succeeded = await cardFor("区间成功到账");
    expect(within(succeeded).getByText(/覆盖不全：可能有非合约币种订单未计入金额/)).toBeTruthy();
    const fee = await cardFor("支付手续费");
    expect(within(fee).getByText(/覆盖不全：可能有非合约币种订单未计入/)).toBeTruthy();
  });

  it("覆盖不全且分桶本身缺席时不断言 0，说清楚是「无法确认」", async () => {
    const metric = paymentsMetric(
      { freshness: { ...fresh, is_partial: true } },
      { by_status: { succeeded: { count: 1, amount_minor_units: 100 } } }, // pending/failed/refunded 键缺席
    );
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response(200, { items: [metric] }))));
    renderCards("sub2api");

    const pending = await cardFor("区间待处理");
    expect(within(pending).getByText("覆盖不全：本次汇总翻页到顶或遇到非合约状态，无法确认这个分桶是否真的是零")).toBeTruthy();
    expect(within(pending).queryByText("$0.00")).toBeNull();
  });

  it("周/月区间：单日指标不能冒充区间合计，六卡说明原因", async () => {
    const metric = paymentsMetric();
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response(200, { items: [metric] }))));
    renderCards("sub2api", { from: "2026-08-24", to: "2026-08-30" });

    const succeeded = await cardFor("区间成功到账");
    expect(within(succeeded).getByText(/周\/月需要按天聚合/)).toBeTruthy();
  });

  // 非单日区间的「—」是**口径不覆盖**，不是数据缺失。此前两者都挂「未接入」，
  // 在屏幕上长得一模一样，而下一步完全相反（前者不用管、后者要查）。
  it("周/月区间的徽章说「仅支持单日」，不与真·未接入混为一谈", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response(200, { items: [paymentsMetric()] }))));
    renderCards("sub2api", { from: "2026-08-24", to: "2026-08-30" });

    for (const label of ["区间成功到账", "区间待处理", "区间失败", "退款与冲正", "支付手续费"]) {
      expect(within(await cardFor(label)).getByText("仅支持单日")).toBeTruthy();
    }
    // 「净现金流入」两平台恒为未接入，与区间无关——它**不该**跟着改口径措辞，
    // 这是同一次渲染里的对照组。
    const net = await cardFor("净现金流入");
    expect(within(net).getByText("未接入")).toBeTruthy();
    expect(within(net).queryByText("仅支持单日")).toBeNull();
  });

  // team-lead 点名要钉住的：`badge` 是可选参数，不传时逐字回到改动前的样子。
  // 免得以后有人以为它必填而给所有调用点都传上，把真·未接入也改掉。
  it("单日区间取不到指标时仍逐字显示「未接入」——badge 缺省行为未变", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response(200, { items: [] }))));
    renderCards("sub2api", { from: "2026-08-28", to: "2026-08-28" });

    for (const label of ["区间成功到账", "区间待处理", "区间失败", "退款与冲正", "支付手续费", "净现金流入"]) {
      expect(within(await cardFor(label)).getByText("未接入")).toBeTruthy();
    }
  });

  // 两条缺席型断言各自单开：与上面的正向断言写在一起时 getByText 会先抛出，
  // 它们根本跑不到，变异也就证明不了它们不是恒真。两条都先 await 正向锚点
  // （卡片标题渲染出来）再同步断言。
  it("周/月区间的卡片上不再出现「未接入」", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response(200, { items: [paymentsMetric()] }))));
    renderCards("sub2api", { from: "2026-08-24", to: "2026-08-30" });

    const succeeded = await cardFor("区间成功到账");
    expect(within(succeeded).queryByText("未接入")).toBeNull();
  });

  it("单日区间的卡片上不出现「仅支持单日」", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response(200, { items: [] }))));
    renderCards("sub2api", { from: "2026-08-28", to: "2026-08-28" });

    const succeeded = await cardFor("区间成功到账");
    expect(within(succeeded).queryByText("仅支持单日")).toBeNull();
  });

  it("未初始化的指标不展示残留的 0 值", async () => {
    const metric = paymentsMetric({
      freshness: { ...fresh, state: "uninitialized", observed_at: null, last_success: null, staleness_seconds: null },
    });
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response(200, { items: [metric] }))));
    renderCards("sub2api");

    const succeeded = await cardFor("区间成功到账");
    expect(within(succeeded).getByText("未接入")).toBeTruthy();
    expect(screen.queryByText("$4,180.00")).toBeNull();
  });
});
