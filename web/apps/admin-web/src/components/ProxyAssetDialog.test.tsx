import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ProxyAssetItem, UpstreamAccountItem } from "../api/finance";
import { ProxyAssetDialog } from "./ProxyAssetDialog";

const REF_SCHEME = "secret://";
const SAMPLE_REF = `${REF_SCHEME}finance/proxy-a`;

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
    upstream_name: "",
    upstream_contact: "",
    upstream_group: "",
    group_rate: "",
  };
}

function money(amount_minor: string) {
  return { amount_minor, currency: "USD", scale: 6 };
}

function proxy(): ProxyAssetItem {
  return {
    id: "22222222-2222-4222-8222-222222222222",
    paid: money("6200000"),
    surcharge: money("0"),
    refunded: money("0"),
    cost_basis: money("6200000"),
    account_share: money("3100000"),
    daily_amortization: money("100000"),
    currency: "USD",
    opened_on: "2026-08-01",
    expires_on: "2026-08-31",
    effective_days: 31,
    refunded_on: null,
    terminated_on: null,
    shared_account_count: 2,
    buy_platform: "Example",
    buy_address: "https://example.test",
    credential_ref: SAMPLE_REF,
    mounted: true,
    environment: "development",
  };
}

function fakeResponse(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function postedParams(fetchMock: ReturnType<typeof vi.fn>): Record<string, unknown> {
  const call = fetchMock.mock.calls.at(-1) as unknown as [string, { body?: string }];
  if (!call) throw new Error("没有发出请求");
  return (JSON.parse(call[1].body ?? "{}") as { params: Record<string, unknown> }).params;
}

async function openDialog(editing = false, onDone = vi.fn()) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <ProxyAssetDialog
          account={account()}
          {...(editing ? { proxy: proxy() } : {})}
          onDone={onDone}
        />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  fireEvent.click(screen.getByRole("button", { name: editing ? "修改代理" : "登记代理资产" }));
  return within(await screen.findByRole("dialog"));
}

describe("代理资产登记 / 修改对话框", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("新建走 set@1，mounted=false 可逆且原样提交", async () => {
    const fetchMock = vi.fn(() => Promise.resolve(fakeResponse({ action_run_id: "run-proxy-1" })));
    vi.stubGlobal("fetch", fetchMock);
    const onDone = vi.fn();
    const dialog = await openDialog(false, onDone);

    expect((dialog.getByLabelText(/实际支付/) as HTMLInputElement).maxLength).toBe(40);
    expect((dialog.getByLabelText(/附加费用/) as HTMLInputElement).maxLength).toBe(40);
    expect((dialog.getByLabelText(/共享账号数量/) as HTMLInputElement).maxLength).toBe(10);

    fireEvent.change(dialog.getByLabelText(/实际支付/), { target: { value: "6.20" } });
    fireEvent.change(dialog.getByLabelText(/开通日期/), { target: { value: "2026-08-01" } });
    fireEvent.change(dialog.getByLabelText(/到期日期/), { target: { value: "2026-08-31" } });
    fireEvent.change(dialog.getByLabelText(/共享账号数量/), { target: { value: "2" } });
    fireEvent.change(dialog.getByLabelText("购买平台"), { target: { value: "Example" } });
    fireEvent.change(dialog.getByLabelText("购买地址"), {
      target: { value: "https://example.test" },
    });
    fireEvent.change(dialog.getByLabelText("CredentialRef"), { target: { value: SAMPLE_REF } });
    fireEvent.click(dialog.getByLabelText("当前挂载"));
    fireEvent.click(dialog.getByRole("button", { name: "登记代理资产" }));

    await vi.waitFor(() => expect(onDone).toHaveBeenCalledWith("run-proxy-1"));
    expect(postedParams(fetchMock)).toEqual({
      paid_minor: "6200000",
      surcharge_minor: "0",
      currency: "USD",
      opened_on: "2026-08-01",
      expires_on: "2026-08-31",
      shared_account_count: 2,
      buy_platform: "Example",
      buy_address: "https://example.test",
      credential_ref: SAMPLE_REF,
      mounted: false,
    });
  });

  it("编辑冻结金额、币种、期间、共享数，只提交可编辑字段", async () => {
    const fetchMock = vi.fn(() => Promise.resolve(fakeResponse({ action_run_id: "run-proxy-2" })));
    vi.stubGlobal("fetch", fetchMock);
    const dialog = await openDialog(true);

    for (const name of ["实际支付（不可修改）", "币种（不可修改）", "有效期（不可修改）", "共享账号数（不可修改）"]) {
      expect((dialog.getByLabelText(new RegExp(name)) as HTMLInputElement).readOnly).toBe(true);
    }
    fireEvent.change(dialog.getByLabelText("购买平台"), { target: { value: "Example 2" } });
    fireEvent.click(dialog.getByRole("button", { name: "保存代理" }));

    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalled());
    expect(postedParams(fetchMock)).toEqual({
      proxy_asset_id: proxy().id,
      buy_platform: "Example 2",
      buy_address: "https://example.test",
      credential_ref: SAMPLE_REF,
      mounted: true,
    });
  });

  it("拒绝 URL userinfo 且错误不回显敏感原文", async () => {
    const dialog = await openDialog();
    const sensitive = "https://sensitive-user:sensitive-pass@example.test/buy";
    fireEvent.change(dialog.getByLabelText("购买地址"), { target: { value: sensitive } });
    fireEvent.click(dialog.getByRole("button", { name: "登记代理资产" }));
    expect(await dialog.findByText(/user:pass@/)).toBeTruthy();
    expect(dialog.queryByText(/sensitive-user|sensitive-pass/)).toBeNull();
  });

  it("没有退款、终止、购买账号或明文密码入口", async () => {
    const dialog = await openDialog();
    expect(dialog.queryByLabelText(/退款|终止|购买账号|密码/)).toBeNull();
  });
});
