import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { WithdrawPanel } from "./WithdrawPanel";

vi.mock("../api/withdraw", async () => {
  const actual = await vi.importActual<typeof import("../api/withdraw")>("../api/withdraw");
  return {
    ...actual,
    listWithdrawAddresses: vi.fn(),
    listWithdrawals: vi.fn(),
    registerWithdrawAddress: vi.fn(),
    executeWithdraw: vi.fn(),
  };
});

import {
  executeWithdraw,
  listWithdrawAddresses,
  listWithdrawals,
  type WithdrawAddress,
  type WithdrawItem,
} from "../api/withdraw";

const coldWallet: WithdrawAddress = {
  address_id: "a1",
  account: "MAIN",
  chain: "TRON",
  address: "TCold1111111111111111111111111111",
  label: "冷钱包",
};

function renderPanel(accounts = ["MAIN"]) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <WithdrawPanel accounts={accounts} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.clearAllMocks();
});

// 提现按钮是两步的：第一次点只是「上膛」。
//
// 与关停卡同一条理由，但更重：链上转账没有撤回，而这个按钮和它周围的
// 「登记地址」长得一样、挨在一起。一次误点的代价是钱出去了。
it("提现需要两步确认，第一次点击不发请求", async () => {
  vi.mocked(listWithdrawAddresses).mockResolvedValue([coldWallet]);
  vi.mocked(listWithdrawals).mockResolvedValue([]);
  renderPanel();

  await screen.findByText(/冷钱包/);
  fireEvent.change(screen.getByLabelText("金额"), { target: { value: "10" } });
  fireEvent.click(screen.getByRole("button", { name: "提现" }));

  expect(executeWithdraw).not.toHaveBeenCalled();
  await screen.findByRole("button", { name: /确认提现/ });
  expect(executeWithdraw).not.toHaveBeenCalled();
});

// 第二次点击才真的发，且**只发 address_id**。
//
// 地址由服务端从白名单里取。前端传地址进去再让后端比对是另一回事——
// 那样一个比对逻辑的疏漏就能让任意地址过去。
it("确认后只提交 address_id，不提交地址本身", async () => {
  vi.mocked(listWithdrawAddresses).mockResolvedValue([coldWallet]);
  vi.mocked(listWithdrawals).mockResolvedValue([]);
  vi.mocked(executeWithdraw).mockResolvedValue({ runId: "run-1" } as never);
  renderPanel();

  await screen.findByText(/冷钱包/);
  fireEvent.change(screen.getByLabelText("金额"), { target: { value: "10" } });
  fireEvent.click(screen.getByRole("button", { name: "提现" }));
  fireEvent.click(await screen.findByRole("button", { name: /确认提现/ }));

  await waitFor(() => expect(executeWithdraw).toHaveBeenCalledTimes(1));
  const params = vi.mocked(executeWithdraw).mock.calls[0]![0];
  expect(params.address_id).toBe("a1");
  expect(JSON.stringify(params)).not.toContain(coldWallet.address);
});

// 幂等键在**上膛时**生成并保持不变。
//
// 上游对 request_id 有真幂等；两次点击若换了键，就等于告诉双方
// 「这是另一笔提现」——那正是重复转账的路径。
it("两步之间幂等键不变", async () => {
  vi.mocked(listWithdrawAddresses).mockResolvedValue([coldWallet]);
  vi.mocked(listWithdrawals).mockResolvedValue([]);
  vi.mocked(executeWithdraw).mockRejectedValue(new Error("上游超时"));
  renderPanel();

  await screen.findByText(/冷钱包/);
  fireEvent.change(screen.getByLabelText("金额"), { target: { value: "10" } });
  fireEvent.click(screen.getByRole("button", { name: "提现" }));
  fireEvent.click(await screen.findByRole("button", { name: /确认提现/ }));
  await waitFor(() => expect(executeWithdraw).toHaveBeenCalledTimes(1));
  const first = vi.mocked(executeWithdraw).mock.calls[0]![0].request_id;

  // 失败后按钮**停在已上膛态**——这正是键稳定的实现方式：
  // 回到未上膛会重新生成一个键，而那时人看到的还是同一笔提现。
  expect(screen.queryByRole("button", { name: "提现" })).toBeNull();

  // 直接再确认一次：仍是同一笔业务，键必须一样。
  fireEvent.click(await screen.findByRole("button", { name: /确认提现/ }));
  await waitFor(() => expect(executeWithdraw).toHaveBeenCalledTimes(2));
  expect(vi.mocked(executeWithdraw).mock.calls[1]![0].request_id).toBe(first);
});

// 没有登记地址时不给提现表单，而是指向登记。
//
// 一个「选不出地址的提现按钮」只会让人反复点然后看后端报错。
it("没有登记地址时不显示提现按钮", async () => {
  vi.mocked(listWithdrawAddresses).mockResolvedValue([]);
  vi.mocked(listWithdrawals).mockResolvedValue([]);
  renderPanel();

  await screen.findByText(/还没有登记任何提现地址/);
  expect(screen.queryByRole("button", { name: "提现" })).toBeNull();
});

// 历史里带链上哈希，且是可点开的区块浏览器链接。
//
// 「平台说转成功了」和「链上确实有这笔」是两件事，后者才是钱到没到的依据。
it("提现历史给出可核对的链上哈希", async () => {
  const done: WithdrawItem = {
    request_id: "w-1",
    account: "MAIN",
    chain: "TRON",
    token_type: "USDT",
    amount: "100",
    address_id: "a1",
    address: coldWallet.address,
    status: "completed",
    tx_hash: "abcdef123456",
    started_at: "2026-09-05T08:30:00Z",
    updated_at: "2026-09-05T08:31:00Z",
  };
  vi.mocked(listWithdrawAddresses).mockResolvedValue([coldWallet]);
  vi.mocked(listWithdrawals).mockResolvedValue([done]);
  renderPanel();

  const link = await screen.findByRole("link", { name: /abcdef/ });
  expect(link.getAttribute("href")).toContain("abcdef123456");
});
