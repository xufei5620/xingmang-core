import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  FINANCE_READ_PERMISSION,
  PROXY_ASSETS_QUERY,
  SUBSCRIPTION_BATCHES_QUERY,
  SUBSCRIPTION_MANAGE_PERMISSION,
  UPSTREAM_ACCOUNTS_QUERY,
  UPSTREAM_SUMMARY_QUERY,
  type UpstreamAccountItem,
} from "../api/finance";
import { formatMoneyItem, UpstreamAccountDetail } from "./UpstreamAccountDetail";

describe("登记簿金额的展示", () => {
  it("**按响应里的 scale 降标度**，不硬编码", () => {
    // 成本线是 scale-6 微单位，币种最小单位是 2 位。把前者当后者显示，
    // ¥1,450.00 会变成 ¥14,500,000.00 —— 差一万倍，且不报错
    expect(formatMoneyItem({ amount_minor: "1450000000", currency: "CNY", scale: 6 })).toBe(
      "¥1,450.00",
    );
    expect(formatMoneyItem({ amount_minor: "145000", currency: "CNY", scale: 2 })).toBe(
      "¥1,450.00",
    );
  });

  it("大额不丢精度——中途转 Number 就会", () => {
    // 2^53 + 1 个最小单位：Number 走一遍会变成 2^53
    expect(formatMoneyItem({ amount_minor: "9007199254740993", currency: "CNY", scale: 2 })).toBe(
      "¥90,071,992,547,409.93",
    );
  });

  it("币种符号交给公共表，不在这里各写一份", () => {
    expect(formatMoneyItem({ amount_minor: "150000", currency: "USD", scale: 2 })).toBe(
      "$1,500.00",
    );
    // 零小数位币种照 0 位算：硬按 2 位除会把日元缩小 100 倍
    expect(formatMoneyItem({ amount_minor: "1500", currency: "JPY", scale: 0 })).toBe("JP¥1,500");
  });

  it("负数保留符号", () => {
    expect(formatMoneyItem({ amount_minor: "-12345", currency: "CNY", scale: 2 })).toBe("-¥123.45");
  });

  it("**没有这个金额**给「—」：批次没算出摊销与摊销为零是两件事", () => {
    expect(formatMoneyItem(null)).toBe("—");
    expect(formatMoneyItem(undefined)).toBe("—");
    expect(formatMoneyItem({ amount_minor: "", currency: "CNY", scale: 2 })).toBe("—");
  });

  it("**值的形状不对**说「数值异常」，与「没有这个金额」分开", () => {
    // 压成同一个「—」会把「后端给了个算不了的值」这条要有人看一眼的信号盖掉；
    // 宁可显眼地说不对，也不能悄悄显示一个算错的金额
    expect(formatMoneyItem({ amount_minor: "12.34", currency: "CNY", scale: 2 })).toBe("数值异常");
    // scale 缺失时后端映射层会给 NaN，同样要显眼地说不对
    expect(formatMoneyItem({ amount_minor: "100", currency: "CNY", scale: Number.NaN })).toBe(
      "数值异常",
    );
  });
});

function subscriptionAccount(over: Partial<UpstreamAccountItem> = {}): UpstreamAccountItem {
  return {
    id: "11111111-1111-4111-8111-111111111111",
    system_type: "sub2api",
    access_method: "subscription_account",
    base_url: "",
    credential_ref: "",
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
    ...over,
  };
}

function fakeResponse(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function stubSubscriptionPages(truncated = false) {
  const fetchMock = vi.fn((url: string) => {
    if (url.includes("/actions/")) {
      return Promise.resolve(fakeResponse({ action_run_id: "run-batch-1" }));
    }
    return Promise.resolve(
      fakeResponse({ items: [], truncated, limit: truncated ? 1 : 200, as_of: "2026-08-28" }),
    );
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderDetail(scopes: string[], account = subscriptionAccount()) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onDone = vi.fn();
  const invalidate = vi.spyOn(queryClient, "invalidateQueries");
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <UpstreamAccountDetail account={account} scopes={scopes} onDone={onDone} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return { queryClient, onDone, invalidate };
}

describe("订阅维护权限与列表诚实性", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("read+write 才显示可用的两个独立 Action 控件", async () => {
    const fetchMock = stubSubscriptionPages();
    renderDetail([FINANCE_READ_PERMISSION, SUBSCRIPTION_MANAGE_PERMISSION]);
    expect(
      (await screen.findByRole("button", { name: "登记/续费新增批次" }) as HTMLButtonElement)
        .disabled,
    ).toBe(false);
    expect((screen.getByRole("button", { name: "登记代理资产" }) as HTMLButtonElement).disabled)
      .toBe(false);
    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes("/finance/")).length).toBe(2);
  });

  it("只有 read 时表仍可见，控件锁定并逐字指出缺失 scope", async () => {
    stubSubscriptionPages();
    renderDetail([FINANCE_READ_PERMISSION]);
    expect(await screen.findByText("这个账号还没有订阅批次")).toBeTruthy();
    expect(screen.getByText(new RegExp(SUBSCRIPTION_MANAGE_PERMISSION))).toBeTruthy();
    expect((screen.getByRole("button", { name: "登记/续费新增批次" }) as HTMLButtonElement).disabled)
      .toBe(true);
    expect((screen.getByRole("button", { name: "登记代理资产" }) as HTMLButtonElement).disabled)
      .toBe(true);
  });

  it("有 write 没 read 时显示授权不一致锁定态，不查询 unseen state", () => {
    const fetchMock = stubSubscriptionPages();
    renderDetail([SUBSCRIPTION_MANAGE_PERMISSION]);
    expect(screen.getByText(/授权状态不一致.*finance\.read/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "登记/续费新增批次" })).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("read page 截断与 as_of 都可见，不声称已展示全部", async () => {
    stubSubscriptionPages(true);
    renderDetail([FINANCE_READ_PERMISSION]);
    expect((await screen.findAllByText(/列表已截断.*limit 1/)).length).toBe(2);
    expect(screen.getAllByText(/派生金额截至 2026-08-28/).length).toBe(2);
  });

  it("非 subscription_account 行不渲染订阅维护区，也不发批次/代理查询", () => {
    const fetchMock = stubSubscriptionPages();
    renderDetail(
      [FINANCE_READ_PERMISSION, SUBSCRIPTION_MANAGE_PERMISSION],
      subscriptionAccount({ access_method: "official_api" }),
    );
    expect(screen.queryByRole("button", { name: "登记/续费新增批次" })).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("登记成功后失效 batch/proxy/account/summary 四类查询，并保留 run receipt", async () => {
    stubSubscriptionPages();
    const { onDone, invalidate } = renderDetail([
      FINANCE_READ_PERMISSION,
      SUBSCRIPTION_MANAGE_PERMISSION,
    ]);
    fireEvent.click(await screen.findByRole("button", { name: "登记/续费新增批次" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(screen.getByLabelText(/实际支付/), { target: { value: "29.99" } });
    fireEvent.change(screen.getByLabelText(/开始日期/), { target: { value: "2026-08-01" } });
    fireEvent.change(screen.getByLabelText(/到期日期/), { target: { value: "2026-08-31" } });
    fireEvent.change(screen.getByLabelText(/账号数量/), { target: { value: "1" } });
    fireEvent.click(dialog.querySelector('button[type="submit"]')!);

    await vi.waitFor(() => expect(onDone).toHaveBeenCalledWith({ title: "订阅批次已登记", runId: "run-batch-1" }));
    for (const queryKey of [
      [SUBSCRIPTION_BATCHES_QUERY, subscriptionAccount().id],
      [PROXY_ASSETS_QUERY],
      [UPSTREAM_ACCOUNTS_QUERY],
      [UPSTREAM_SUMMARY_QUERY],
    ]) {
      expect(invalidate).toHaveBeenCalledWith({ queryKey });
    }
  });
});
