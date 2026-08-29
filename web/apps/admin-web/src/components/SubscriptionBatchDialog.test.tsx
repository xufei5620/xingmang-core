import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ProxyAssetPage, UpstreamAccountItem } from "../api/finance";
import { SubscriptionBatchDialog } from "./SubscriptionBatchDialog";

function account(): UpstreamAccountItem {
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
  };
}

const emptyProxies: ProxyAssetPage = {
  items: [],
  truncated: false,
  limit: 200,
  as_of: "2026-08-28",
};

function fakeResponse(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function postedParams(fetchMock: ReturnType<typeof vi.fn>): Record<string, unknown> {
  const call = fetchMock.mock.calls.at(-1) as unknown as [string, { body?: string }];
  if (!call) throw new Error("没有发出请求");
  return (JSON.parse(call[1].body ?? "{}") as { params: Record<string, unknown> }).params;
}

async function openDialog(
  props: Partial<React.ComponentProps<typeof SubscriptionBatchDialog>> = {},
) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <SubscriptionBatchDialog
          account={account()}
          proxyPage={emptyProxies}
          proxyPageStatus="success"
          onDone={() => {}}
          {...props}
        />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  fireEvent.click(screen.getByRole("button", { name: "登记/续费新增批次" }));
  return within(await screen.findByRole("dialog"));
}

describe("订阅批次登记对话框", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("从账号带入币种，日期只派生含两端天数，不计算摊销金额", async () => {
    const dialog = await openDialog();
    expect((dialog.getByLabelText(/实际支付/) as HTMLInputElement).maxLength).toBe(40);
    expect((dialog.getByLabelText(/附加费用/) as HTMLInputElement).maxLength).toBe(40);
    expect((dialog.getByLabelText(/账号数量/) as HTMLInputElement).maxLength).toBe(10);
    expect(dialog.getByRole("combobox", { name: "币种" }).textContent).toContain("USD");
    fireEvent.change(dialog.getByLabelText(/开始日期/), { target: { value: "2026-08-01" } });
    fireEvent.change(dialog.getByLabelText(/到期日期/), { target: { value: "2026-08-31" } });
    expect(dialog.getByText(/有效天数：31 天.*起止日均计入/)).toBeTruthy();
    expect(dialog.queryByText(/每日账号成本|剩余未摊销成本|成本归属/)).toBeNull();
  });

  it("提交 register@1 的最小白名单，并把 run id 交给既有回执 UI", async () => {
    const fetchMock = vi.fn(() => Promise.resolve(fakeResponse({ action_run_id: "run-batch-1" })));
    vi.stubGlobal("fetch", fetchMock);
    const onDone = vi.fn();
    const dialog = await openDialog({ onDone });

    fireEvent.change(dialog.getByLabelText(/实际支付/), { target: { value: "29.99" } });
    fireEvent.change(dialog.getByLabelText(/附加费用/), { target: { value: "" } });
    fireEvent.change(dialog.getByLabelText(/开始日期/), { target: { value: "2026-08-01" } });
    fireEvent.change(dialog.getByLabelText(/到期日期/), { target: { value: "2026-08-31" } });
    fireEvent.change(dialog.getByLabelText(/账号数量/), { target: { value: "2" } });
    fireEvent.click(dialog.getByRole("button", { name: "登记批次" }));

    await vi.waitFor(() => expect(onDone).toHaveBeenCalledWith("run-batch-1"));
    expect(postedParams(fetchMock)).toEqual({
      upstream_account_id: account().id,
      paid_minor: "29990000",
      surcharge_minor: "0",
      currency: "USD",
      starts_on: "2026-08-01",
      expires_on: "2026-08-31",
      account_count: 2,
    });
  });

  it("代理选择列表被截断时明确说结果不完整", async () => {
    const dialog = await openDialog({
      proxyPage: { ...emptyProxies, truncated: true, limit: 1 },
    });
    expect(dialog.getByText(/代理选择结果不完整.*limit 1/)).toBeTruthy();
  });

  it("锁定时触发器不可用，不会出现貌似可提交的表单", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <SubscriptionBatchDialog
            account={account()}
            proxyPage={emptyProxies}
            proxyPageStatus="success"
            disabled
            onDone={() => {}}
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    const trigger = screen.getByRole("button", { name: "登记/续费新增批次" });
    expect((trigger as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(trigger);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it.each([
    ["pending", emptyProxies, /正在读取代理资产/],
    ["error", emptyProxies, /代理资产读取失败/],
    ["success", undefined, /代理资产列表状态未知/],
  ] as const)("代理页 %s 时 fail closed，不开放 Action", (status, page, message) => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <SubscriptionBatchDialog
            account={account()}
            proxyPage={page}
            proxyPageStatus={status}
            onDone={() => {}}
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    const trigger = screen.getByRole("button", { name: "登记/续费新增批次" });
    expect((trigger as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(message)).toBeTruthy();
    fireEvent.click(trigger);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("代理页从 pending 转为成功且有 page 后才开放", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const view = render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <SubscriptionBatchDialog
            account={account()}
            proxyPageStatus="pending"
            onDone={() => {}}
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect((screen.getByRole("button", { name: "登记/续费新增批次" }) as HTMLButtonElement).disabled)
      .toBe(true);

    view.rerender(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <SubscriptionBatchDialog
            account={account()}
            proxyPage={emptyProxies}
            proxyPageStatus="success"
            onDone={() => {}}
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    const trigger = screen.getByRole("button", { name: "登记/续费新增批次" });
    expect((trigger as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(trigger);
    expect(await screen.findByRole("dialog")).toBeTruthy();
  });
});
