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
    listWithdrawLimits: vi.fn(),
    registerWithdrawAddress: vi.fn(),
    executeWithdraw: vi.fn(),
    setWithdrawLimits: vi.fn(),
    setWithdrawAddressEnabled: vi.fn(),
  };
});

import {
  executeWithdraw,
  listWithdrawAddresses,
  listWithdrawLimits,
  listWithdrawals,
  setWithdrawAddressEnabled,
  setWithdrawLimits,
  type WithdrawAddress,
  type WithdrawItem,
} from "../api/withdraw";

const coldWallet: WithdrawAddress = {
  address_id: "a1",
  account: "MAIN",
  chain: "TRON",
  address: "TCold1111111111111111111111111111",
  label: "冷钱包",
  enabled: true,
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
  vi.mocked(listWithdrawLimits).mockResolvedValue([]);
  renderPanel();

  // 用表单自身当就绪信号：地址名现在同时出现在清单行和下拉选项里，
  // 按文本找会撞到多个元素。
  fireEvent.change(await screen.findByLabelText("金额"), { target: { value: "10" } });
  fireEvent.click(screen.getByRole("button", { name: "提现" }));

  expect(executeWithdraw).not.toHaveBeenCalled();
  await screen.findByRole("button", { name: /提交提现审批/ });
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
  vi.mocked(listWithdrawLimits).mockResolvedValue([]);
  renderPanel();

  // 用表单自身当就绪信号：地址名现在同时出现在清单行和下拉选项里，
  // 按文本找会撞到多个元素。
  fireEvent.change(await screen.findByLabelText("金额"), { target: { value: "10" } });
  fireEvent.change(screen.getByLabelText("理由"), { target: { value: "季度结算，把冷钱包余额转回运营账户" } });
  fireEvent.click(screen.getByRole("button", { name: "提现" }));
  fireEvent.click(await screen.findByRole("button", { name: /提交提现审批/ }));

  await waitFor(() => expect(executeWithdraw).toHaveBeenCalledTimes(1));
  const params = vi.mocked(executeWithdraw).mock.calls[0]![0];
  expect(params.address_id).toBe("a1");
  expect(JSON.stringify(params)).not.toContain(coldWallet.address);
  // 理由是**第二个实参**，不在 params 里：它进审批单，不进 Action 参数
  // （params 要过后端 JSON Schema，多一个字段会被当场拒）。
  expect(vi.mocked(executeWithdraw).mock.calls[0]![1]).toBe("季度结算，把冷钱包余额转回运营账户");
});

// 幂等键在**上膛时**生成并保持不变。
//
// 上游对 request_id 有真幂等；两次点击若换了键，就等于告诉双方
// 「这是另一笔提现」——那正是重复转账的路径。
it("两步之间幂等键不变", async () => {
  vi.mocked(listWithdrawAddresses).mockResolvedValue([coldWallet]);
  vi.mocked(listWithdrawals).mockResolvedValue([]);
  vi.mocked(executeWithdraw).mockRejectedValue(new Error("上游超时"));
  vi.mocked(listWithdrawLimits).mockResolvedValue([]);
  renderPanel();

  // 用表单自身当就绪信号：地址名现在同时出现在清单行和下拉选项里，
  // 按文本找会撞到多个元素。
  fireEvent.change(await screen.findByLabelText("金额"), { target: { value: "10" } });
  fireEvent.change(screen.getByLabelText("理由"), { target: { value: "季度结算，把冷钱包余额转回运营账户" } });
  fireEvent.click(screen.getByRole("button", { name: "提现" }));
  fireEvent.click(await screen.findByRole("button", { name: /提交提现审批/ }));
  await waitFor(() => expect(executeWithdraw).toHaveBeenCalledTimes(1));
  const first = vi.mocked(executeWithdraw).mock.calls[0]![0].request_id;

  // 失败后按钮**停在已上膛态**——这正是键稳定的实现方式：
  // 回到未上膛会重新生成一个键，而那时人看到的还是同一笔提现。
  expect(screen.queryByRole("button", { name: "提现" })).toBeNull();

  // 直接再确认一次：仍是同一笔业务，键必须一样。
  fireEvent.click(await screen.findByRole("button", { name: /提交提现审批/ }));
  await waitFor(() => expect(executeWithdraw).toHaveBeenCalledTimes(2));
  expect(vi.mocked(executeWithdraw).mock.calls[1]![0].request_id).toBe(first);
});

// 没有登记地址时不给提现表单，而是指向登记。
//
// 一个「选不出地址的提现按钮」只会让人反复点然后看后端报错。
it("没有登记地址时不显示提现按钮", async () => {
  vi.mocked(listWithdrawAddresses).mockResolvedValue([]);
  vi.mocked(listWithdrawals).mockResolvedValue([]);
  vi.mocked(listWithdrawLimits).mockResolvedValue([]);
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
  vi.mocked(listWithdrawLimits).mockResolvedValue([]);
  renderPanel();

  const link = await screen.findByRole("link", { name: /abcdef/ });
  expect(link.getAttribute("href")).toContain("abcdef123456");
});

// 额度在页面上看得到、改得动，不用登服务器。
//
// 这是产品负责人 2026-09-05 的要求：一个要改文件再重启才能动的数字，
// 实际上没人会去动——它会永远停在第一次拍脑袋定的那个值上。
it("额度可以在页面上直接调整", async () => {
  vi.mocked(listWithdrawAddresses).mockResolvedValue([coldWallet]);
  vi.mocked(listWithdrawals).mockResolvedValue([]);
  vi.mocked(listWithdrawLimits).mockResolvedValue([
    { account: "MAIN", per_operation: "500", per_day: "2000", updated_by: "xufei" },
  ]);
  vi.mocked(setWithdrawLimits).mockResolvedValue({ runId: "run-1" } as never);
  renderPanel();

  // 当前额度看得见，且带「谁定的」。
  await screen.findByText(/500/);
  expect(screen.getByText(/xufei/)).toBeTruthy();

  fireEvent.click(screen.getByRole("button", { name: /改额度/ }));
  fireEvent.change(screen.getByLabelText("单笔上限"), { target: { value: "800" } });
  fireEvent.change(screen.getByLabelText("单日上限"), { target: { value: "3000" } });
  fireEvent.click(screen.getByRole("button", { name: "保存额度" }));

  await waitFor(() => expect(setWithdrawLimits).toHaveBeenCalledTimes(1));
  expect(vi.mocked(setWithdrawLimits).mock.calls[0]![0]).toEqual({
    account: "MAIN",
    per_operation: "800",
    per_day: "3000",
  });
});

// 没设过额度的账号显示成「未设置」，并说清后果。
//
// 显示成 0 会让人以为有人刻意关掉了它，而实际上是还没人管过——
// 两者的下一步动作完全不同。
it("没设过额度的账号显示未设置而不是 0", async () => {
  vi.mocked(listWithdrawAddresses).mockResolvedValue([coldWallet]);
  vi.mocked(listWithdrawals).mockResolvedValue([]);
  vi.mocked(listWithdrawLimits).mockResolvedValue([]);
  renderPanel();

  await screen.findByText(/未设置/);
  expect(screen.queryByText(/^0$/)).toBeNull();
});

// 地址清单在页面上,停用的也列出来但标记清楚。
//
// 留着而不是删掉：一条曾经被列入白名单的地址，它存在过这件事本身就是
// 审计事实——「这条地址当初是谁登记的、什么时候下线的」正是出事之后
// 第一个要问的问题。
it("停用的地址仍列出来，但提现表单不提供它", async () => {
  vi.mocked(listWithdrawAddresses).mockResolvedValue([
    coldWallet,
    { ...coldWallet, address_id: "a2", label: "旧钱包", enabled: false },
  ]);
  vi.mocked(listWithdrawals).mockResolvedValue([]);
  vi.mocked(listWithdrawLimits).mockResolvedValue([]);
  renderPanel();

  // 两条都在清单里。
  await screen.findByText(/旧钱包/);
  expect(screen.getByText(/已停用/)).toBeTruthy();

  // 表单在，且选中的是启用的那条。
  expect(screen.getByLabelText("到账地址").textContent).toContain("冷钱包");
});

// **全部地址都停用时，提现表单整个不出现。**
//
// 这条才是「表单只吃启用的地址」真正可感知的形态。不去断言下拉框里有没有
// 那一项：Select 是 Radix 的，选项在 Portal 里、只有展开才存在，
// 「查不到」不等于「没提供」——那种断言把过滤删掉照样通过，试过了。
it("地址全部停用时不显示提现表单", async () => {
  vi.mocked(listWithdrawAddresses).mockResolvedValue([{ ...coldWallet, enabled: false }]);
  vi.mocked(listWithdrawals).mockResolvedValue([]);
  vi.mocked(listWithdrawLimits).mockResolvedValue([]);
  renderPanel();

  // 清单还在（可查、可重新启用）。
  await screen.findByText(/已停用/);
  // 但没有任何可以发起提现的入口。
  expect(screen.queryByLabelText("金额")).toBeNull();
  expect(screen.queryByRole("button", { name: "提现" })).toBeNull();
});

// 下线一条地址就在清单里点。
it("可以在页面上下线一条地址", async () => {
  vi.mocked(listWithdrawAddresses).mockResolvedValue([coldWallet]);
  vi.mocked(listWithdrawals).mockResolvedValue([]);
  vi.mocked(listWithdrawLimits).mockResolvedValue([]);
  vi.mocked(setWithdrawAddressEnabled).mockResolvedValue({ runId: "run-1" } as never);
  renderPanel();

  fireEvent.click(await screen.findByRole("button", { name: /停用 冷钱包/ }));

  await waitFor(() => expect(setWithdrawAddressEnabled).toHaveBeenCalledTimes(1));
  expect(vi.mocked(setWithdrawAddressEnabled).mock.calls[0]![0]).toEqual({
    address_id: "a1",
    enabled: false,
  });
});

// 停用的地址能重新启用。
it("停用的地址可以重新启用", async () => {
  vi.mocked(listWithdrawAddresses).mockResolvedValue([{ ...coldWallet, enabled: false }]);
  vi.mocked(listWithdrawals).mockResolvedValue([]);
  vi.mocked(listWithdrawLimits).mockResolvedValue([]);
  vi.mocked(setWithdrawAddressEnabled).mockResolvedValue({ runId: "run-1" } as never);
  renderPanel();

  fireEvent.click(await screen.findByRole("button", { name: /启用 冷钱包/ }));

  await waitFor(() => expect(setWithdrawAddressEnabled).toHaveBeenCalledTimes(1));
  expect(vi.mocked(setWithdrawAddressEnabled).mock.calls[0]![0]).toEqual({
    address_id: "a1",
    enabled: true,
  });
});
