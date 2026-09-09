import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { SMSRequestDialog } from "./SMSRequestDialog";

vi.mock("../api/sms", async () => {
  const actual = await vi.importActual<typeof import("../api/sms")>("../api/sms");
  return { ...actual, requestSMSNumbers: vi.fn() };
});

import { requestSMSNumbers, type SMSProvider } from "../api/sms";

const PROVIDERS: SMSProvider[] = [
  { provider: "sms62", label: "62-US", capabilities: ["purchase", "catalog"], enabled: true, verified: true, supports_lifecycle: false },
  { provider: "hero_sms", label: "Hero-SMS", capabilities: ["purchase", "lifecycle"], enabled: true, verified: true, supports_lifecycle: true },
];

function renderDialog(providers: SMSProvider[] = PROVIDERS) {
  const onDone = vi.fn();
  const onRequested = vi.fn();
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <SMSRequestDialog providers={providers} onDone={onDone} onRequested={onRequested} />
    </QueryClientProvider>,
  );
  return { onDone, onRequested };
}

/** 打开对话框并填好表单。 */
async function openAndFill(quantity = "2") {
  fireEvent.click(await screen.findByRole("button", { name: "要号" }));
  fireEvent.change(await screen.findByLabelText("要号服务"), { target: { value: " Google " } });
  fireEvent.change(screen.getByLabelText("要号国家"), { target: { value: "12" } });
  fireEvent.change(screen.getByLabelText("要号数量"), { target: { value: quantity } });
}

function armAndConfirm(quantity = 2) {
  // 对话框里那个「要号」是上膛按钮；触发器也叫「要号」，取最后一个。
  const arms = screen.getAllByRole("button", { name: "要号" });
  fireEvent.click(arms[arms.length - 1]!);
  expect(requestSMSNumbers).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: `确认要 ${quantity} 个` }));
}

afterEach(() => vi.clearAllMocks());

// 默认不选供应商：由路由规则选。人只在想指定时才选。
it("要号两步确认，默认不带供应商", async () => {
  vi.mocked(requestSMSNumbers).mockResolvedValue({
    runId: "run-1",
    result: { state: "succeeded", provider: "hero_sms", resource_ids: ["res-9"], attempts: [] },
  });
  const { onDone, onRequested } = renderDialog();
  await openAndFill();
  armAndConfirm();

  await waitFor(() => expect(requestSMSNumbers).toHaveBeenCalledTimes(1));
  const params = vi.mocked(requestSMSNumbers).mock.calls[0]![0];
  expect(params).toEqual(expect.objectContaining({ service: "Google", country: "12", quantity: 2 }));
  expect(params.provider).toBeUndefined();
  expect(typeof params.request_id).toBe("string");
  expect(params.request_id.length).toBeGreaterThan(0);
  await waitFor(() => expect(onRequested).toHaveBeenCalledWith(["res-9"]));
  expect(onDone).toHaveBeenCalledWith(expect.objectContaining({ runId: "run-1" }));
});

it("指定供应商时带 provider", async () => {
  vi.mocked(requestSMSNumbers).mockResolvedValue({
    runId: "run-2",
    result: { state: "succeeded", provider: "hero_sms", resource_ids: ["res-1"], attempts: [] },
  });
  renderDialog();
  await openAndFill("1");
  fireEvent.click(screen.getByRole("button", { name: "Hero-SMS" }));
  armAndConfirm(1);

  await waitFor(() => expect(requestSMSNumbers).toHaveBeenCalledTimes(1));
  expect(vi.mocked(requestSMSNumbers).mock.calls[0]![0].provider).toBe("hero_sms");
});

// 失败要说清**每一家**为什么没要到——人下一步是去改规则、充值还是换国家，
// 全看原因。
it("全部失败时列出每家的原因，不选中任何号", async () => {
  vi.mocked(requestSMSNumbers).mockResolvedValue({
    runId: "run-3",
    result: {
      state: "failed",
      resource_ids: [],
      attempts: [
        { provider: "sms62", operation_id: "op-a", state: "", reason: "62 没有匹配「google / 12」的商品" },
        { provider: "hero_sms", operation_id: "op-b", state: "failed", reason: "余额不足" },
      ],
    },
  });
  const { onRequested } = renderDialog();
  await openAndFill("1");
  armAndConfirm(1);

  expect(await screen.findByText(/全部供应商都没要到/)).toBeTruthy();
  expect(screen.getByText(/62 没有匹配/)).toBeTruthy();
  expect(screen.getByText(/余额不足/)).toBeTruthy();
  expect(onRequested).not.toHaveBeenCalled();
});

// unknown = 钱可能已经花了。要亮出来，给操作 ID 去核对，不能当失败让人重来。
it("结果未知时提示人工核对并显示操作 ID", async () => {
  vi.mocked(requestSMSNumbers).mockResolvedValue({
    runId: "run-4",
    result: {
      state: "unknown",
      provider: "sms62",
      operation_id: "op-unknown-1",
      needs_review: true,
      resource_ids: [],
      attempts: [{ provider: "sms62", operation_id: "op-unknown-1", state: "unknown", reason: "read timeout" }],
    },
  });
  const { onRequested } = renderDialog();
  await openAndFill("1");
  armAndConfirm(1);

  expect(await screen.findByText(/人工核对/)).toBeTruthy();
  expect(screen.getByText(/op-unknown-1/)).toBeTruthy();
  expect(onRequested).not.toHaveBeenCalled();
});

it("没有可用的供应商时按钮禁用", async () => {
  renderDialog([{ ...PROVIDERS[0]!, verified: false }, { ...PROVIDERS[1]!, enabled: false }]);
  const button = (await screen.findByRole("button", { name: "要号" })) as HTMLButtonElement;
  expect(button.disabled).toBe(true);
});
