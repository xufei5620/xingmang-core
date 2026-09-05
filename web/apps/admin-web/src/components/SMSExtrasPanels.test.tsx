import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { ExtendDialog, HeroEmailsPanel, HeroRentPanel, UpstreamCodes } from "./SMSExtrasPanels";

vi.mock("../api/sms", async () => {
  const actual = await vi.importActual<typeof import("../api/sms")>("../api/sms");
  return {
    ...actual,
    listExtendOptions: vi.fn(),
    listUpstreamCodes: vi.fn(),
    listSMSEmails: vi.fn(),
    listHeroEmailDomains: vi.fn(),
    getSMSEmail: vi.fn(),
    executeSMSResourceAction: vi.fn(),
    executeSMSEmailAction: vi.fn(),
    rentSMSNumber: vi.fn(),
    purchaseSMSEmails: vi.fn(),
  };
});

import {
  executeSMSResourceAction,
  getSMSEmail,
  listExtendOptions,
  listSMSEmails,
  listUpstreamCodes,
  rentSMSNumber,
  type SMSResource,
} from "../api/sms";

function wrap(node: React.ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>{node}</MemoryRouter>
    </QueryClientProvider>,
  );
}

const resource: SMSResource = {
  resource_id: "r1",
  provider: "hero_sms",
  phone_mask: "****0001",
  service: "go",
  country: "12",
};

afterEach(() => vi.clearAllMocks());

// 延长 / 重激活**花钱**：必须先选一个档位才能确认，而且传出去的是那个档位的时长。
//
// 一个不选档位就能点的确认按钮，等于让人在不知道价格的情况下花钱。
it("延长要先选档位，确认时把该档位的时长传给 Action", async () => {
  vi.mocked(listExtendOptions).mockResolvedValue([
    { duration: 4, unit: "hour", price_text: "0.50" },
    { duration: 24, unit: "hour", price_text: "2.00" },
  ]);
  vi.mocked(executeSMSResourceAction).mockResolvedValue({ runId: "run-x" } as never);
  const onWrite = vi.fn();
  wrap(<ExtendDialog resource={resource} kind="prolong" onWrite={onWrite} />);

  fireEvent.click(screen.getByRole("button", { name: "延长" }));
  const confirm = (await screen.findByRole("button", { name: /先选一个档位/ })) as HTMLButtonElement;
  expect(confirm.disabled).toBe(true);

  fireEvent.click(await screen.findByRole("button", { name: /24 小时 · 2\.00/ }));
  fireEvent.click(screen.getByRole("button", { name: /确认延长 24 小时/ }));

  await waitFor(() =>
    expect(vi.mocked(executeSMSResourceAction).mock.calls[0]?.[0]).toMatchObject({
      resource_id: "r1",
      kind: "prolong",
      duration: 24,
    }),
  );
  expect(onWrite).toHaveBeenCalled();
});

// 没有 sms.reveal 时后端不回 code；页面要说「需要权限」，不能显示成空码。
it("上游验证码列表在没有 reveal 权限时明说，不显示空码", async () => {
  vi.mocked(listUpstreamCodes).mockResolvedValue([
    { code_id: "o1", resource_id: "r1", sender: "Google", received_at: "2026-09-06T01:02:03Z" },
  ]);
  wrap(<UpstreamCodes resource={resource} />);
  expect(await screen.findByText("（需要 sms.reveal 权限）")).toBeTruthy();
});

// 租用是花钱的两步确认：小时数不为正时不能进入下一步。
it("租用小时数不为正时不能进入下一步", async () => {
  wrap(<HeroRentPanel onWrite={vi.fn()} />);
  fireEvent.change(screen.getByLabelText("租用服务代号"), { target: { value: "go" } });
  fireEvent.change(screen.getByLabelText("租用国家代码"), { target: { value: "12" } });
  fireEvent.change(screen.getByLabelText("租用小时数"), { target: { value: "0" } });
  expect((screen.getByRole("button", { name: "下一步" }) as HTMLButtonElement).disabled).toBe(true);

  fireEvent.change(screen.getByLabelText("租用小时数"), { target: { value: "4" } });
  fireEvent.click(screen.getByRole("button", { name: "下一步" }));
  vi.mocked(rentSMSNumber).mockResolvedValue({ runId: "run-r" } as never);
  fireEvent.click(await screen.findByRole("button", { name: /确认租 4 小时/ }));
  await waitFor(() =>
    expect(vi.mocked(rentSMSNumber).mock.calls[0]?.[0]).toMatchObject({ service: "go", country: 12, duration_hours: 4 }),
  );
});

// 邮箱刷新是**人发起**的，且要真的带 refresh 去打上游。
it("邮箱行的刷新按钮带 refresh=true 读上游", async () => {
  vi.mocked(listSMSEmails).mockResolvedValue([
    {
      email_id: "e1", provider: "hero_sms", external_id: "9", site: "example.com", email: "a@x",
      status: "WAIT", cost_text: "0.20", currency: 840, has_value: false,
    },
  ]);
  vi.mocked(getSMSEmail).mockResolvedValue({
    email_id: "e1", provider: "hero_sms", external_id: "9", site: "example.com", email: "a@x",
    status: "SUCCESS", cost_text: "0.20", currency: 840, has_value: true, value: "DEMO",
  });
  wrap(<HeroEmailsPanel onWrite={vi.fn()} />);
  expect(await screen.findByText("a@x")).toBeTruthy();
  expect(screen.getByText(/还没收到验证内容/)).toBeTruthy();

  fireEvent.click(screen.getByRole("button", { name: "刷新" }));
  await waitFor(() => expect(vi.mocked(getSMSEmail)).toHaveBeenCalledWith("e1", true));
});

// 强调用 <strong>，不要把 markdown 星号当字面量吐到页面上——
// 这一页被强调的都是「花钱」「不退」这种句子，星号让它们读起来像排版事故。
it("扩展面板里没有裸露的 markdown 星号", async () => {
  vi.mocked(listSMSEmails).mockResolvedValue([]);
  wrap(
    <>
      <HeroRentPanel onWrite={vi.fn()} />
      <HeroEmailsPanel onWrite={vi.fn()} />
    </>,
  );
  await screen.findByText(/还没有邮箱/);
  expect(screen.queryByText(/\*\*/)).toBeNull();
});
