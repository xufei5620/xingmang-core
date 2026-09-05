import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { SMSQuotaPanel } from "./SMSQuotaPanel";

vi.mock("../api/sms", async () => {
  const actual = await vi.importActual<typeof import("../api/sms")>("../api/sms");
  return { ...actual, listSMSQuotas: vi.fn(), setSMSQuota: vi.fn() };
});

import { listSMSQuotas, setSMSQuota, type SMSQuota } from "../api/sms";
import type { ActionRun } from "../api/platform";

const QUOTA: SMSQuota = {
  consumer: "svc:worker",
  daily_requests: 100,
  daily_spend_cap: "5.00",
  enabled: true,
  updated_at: "2026-09-06T05:00:00Z",
  used_numbers: 12,
  used_spend: [{ currency: "", amount: "4.20" }],
};

function renderPanel() {
  const onWrite = vi.fn();
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <SMSQuotaPanel onWrite={onWrite} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return onWrite;
}

afterEach(() => vi.clearAllMocks());

// 「谁快到顶了」要一眼看得出来：今日用量与配额摆在一起。
it("列出消费者与今日用量", async () => {
  vi.mocked(listSMSQuotas).mockResolvedValue([QUOTA]);
  renderPanel();

  expect(await screen.findByText("svc:worker")).toBeTruthy();
  expect(screen.getByText("12 / 100")).toBeTruthy();
  expect(screen.getByText("4.20")).toBeTruthy();
});

// 没有登记任何消费者时要说清这是**刻意的默认**，不是页面坏了。
it("空表说明没登记就调不动", async () => {
  vi.mocked(listSMSQuotas).mockResolvedValue([]);
  renderPanel();

  const empty = await screen.findByText(/没有登记任何消费者/);
  expect(empty.textContent).toContain("刻意的默认");
});

it("保存配额把留空的上限省掉", async () => {
  vi.mocked(listSMSQuotas).mockResolvedValue([]);
  vi.mocked(setSMSQuota).mockResolvedValue({ runId: "run-1" } as unknown as ActionRun);
  const onWrite = renderPanel();

  fireEvent.change(await screen.findByLabelText("消费者"), { target: { value: " svc:new " } });
  fireEvent.change(screen.getByLabelText("日号数"), { target: { value: "50" } });
  fireEvent.click(screen.getByRole("button", { name: "保存配额" }));

  await waitFor(() => expect(setSMSQuota).toHaveBeenCalledTimes(1));
  expect(vi.mocked(setSMSQuota).mock.calls[0]![0]).toEqual({
    consumer: "svc:new",
    daily_requests: 50,
    enabled: true,
  });
  await waitFor(() => expect(onWrite).toHaveBeenCalled());
});

it("消费者为空时不能保存", async () => {
  vi.mocked(listSMSQuotas).mockResolvedValue([]);
  renderPanel();

  await waitFor(() =>
    expect((screen.getByRole("button", { name: "保存配额" }) as HTMLButtonElement).disabled).toBe(true),
  );
});

it("编辑把已有配额填进表单", async () => {
  vi.mocked(listSMSQuotas).mockResolvedValue([QUOTA]);
  renderPanel();

  fireEvent.click(await screen.findByRole("button", { name: "编辑" }));
  expect((screen.getByLabelText("消费者") as HTMLInputElement).value).toBe("svc:worker");
  expect((screen.getByLabelText("日号数") as HTMLInputElement).value).toBe("100");
  expect((screen.getByLabelText("日花费上限") as HTMLInputElement).value).toBe("5.00");
});
