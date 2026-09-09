import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { SMSRoutingPanel } from "./SMSRoutingPanel";

vi.mock("../api/sms", async () => {
  const actual = await vi.importActual<typeof import("../api/sms")>("../api/sms");
  return {
    ...actual,
    listSMSProviders: vi.fn(),
    listSMSRoutingRules: vi.fn(),
    setSMSRoutingRule: vi.fn(),
    removeSMSRoutingRule: vi.fn(),
  };
});

import {
  listSMSProviders,
  listSMSRoutingRules,
  removeSMSRoutingRule,
  setSMSRoutingRule,
  type SMSRoutingRule,
} from "../api/sms";
import type { ActionRun } from "../api/platform";

const RUN = { runId: "run-1" } as unknown as ActionRun;

const RULE: SMSRoutingRule = {
  rule_id: "r1",
  service: "go",
  country: "*",
  providers: ["hero_sms", "sms62"],
  max_unit_price: "0.5",
  enabled: true,
};

function seed(rules: SMSRoutingRule[]) {
  vi.mocked(listSMSProviders).mockResolvedValue([
    {
      provider: "sms62",
      label: "62-US",
      capabilities: ["purchase", "catalog", "orders", "token"],
      enabled: true,
      verified: true,
      supports_lifecycle: false,
    },
    {
      provider: "hero_sms",
      label: "Hero-SMS",
      capabilities: ["purchase", "lifecycle", "rent"],
      enabled: true,
      verified: true,
      supports_lifecycle: true,
    },
  ]);
  vi.mocked(listSMSRoutingRules).mockResolvedValue({
    items: rules,
    default_order: ["sms62", "hero_sms"],
    wildcard: "*",
  });
}

function renderPanel() {
  const onWrite = vi.fn();
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <SMSRoutingPanel onWrite={onWrite} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return onWrite;
}

afterEach(() => vi.clearAllMocks());

// 规则里存的是供应商 ID，页面要显示标签并保留优先级顺序；没有规则命中时
// 要号按默认顺序试——这件事页面得说出来，不然人以为没规则就不能要号。
it("列出规则：供应商按优先级用标签显示，并说明默认顺序", async () => {
  seed([RULE]);
  renderPanel();

  expect(await screen.findByText("Hero-SMS → 62-US")).toBeTruthy();
  expect(screen.getByText("0.5")).toBeTruthy();
  expect(screen.getByText(/默认顺序：62-US → Hero-SMS/)).toBeTruthy();
});

// 优先级就是「加入」的先后：先加的先试。
it("保存规则按加入顺序提交供应商", async () => {
  seed([]);
  vi.mocked(setSMSRoutingRule).mockResolvedValue(RUN);
  const onWrite = renderPanel();

  await screen.findByRole("button", { name: "加入 Hero-SMS" });
  fireEvent.change(screen.getByLabelText("路由服务"), { target: { value: " GO " } });
  fireEvent.change(screen.getByLabelText("路由国家"), { target: { value: "12" } });
  fireEvent.click(screen.getByRole("button", { name: "加入 Hero-SMS" }));
  fireEvent.click(screen.getByRole("button", { name: "加入 62-US" }));
  fireEvent.change(screen.getByLabelText("路由单价上限"), { target: { value: "0.5" } });
  fireEvent.click(screen.getByRole("button", { name: "保存规则" }));

  await waitFor(() => expect(setSMSRoutingRule).toHaveBeenCalledTimes(1));
  expect(vi.mocked(setSMSRoutingRule).mock.calls[0]![0]).toEqual({
    service: "GO",
    country: "12",
    providers: ["hero_sms", "sms62"],
    max_unit_price: "0.5",
    enabled: true,
  });
  await waitFor(() => expect(onWrite).toHaveBeenCalledWith({ runId: "run-1", title: "已保存路由 GO / 12" }));
});

it("没有供应商时不能保存；上移与移除能调顺序", async () => {
  seed([]);
  renderPanel();

  await screen.findByRole("button", { name: "加入 Hero-SMS" });
  fireEvent.change(screen.getByLabelText("路由服务"), { target: { value: "go" } });
  expect((screen.getByRole("button", { name: "保存规则" }) as HTMLButtonElement).disabled).toBe(true);

  fireEvent.click(screen.getByRole("button", { name: "加入 62-US" }));
  fireEvent.click(screen.getByRole("button", { name: "加入 Hero-SMS" }));
  expect(screen.getByText("1. 62-US")).toBeTruthy();
  expect(screen.getByText("2. Hero-SMS")).toBeTruthy();
  // 加过的不能再加。
  expect(screen.queryByRole("button", { name: "加入 62-US" })).toBeNull();

  fireEvent.click(screen.getByRole("button", { name: "上移 Hero-SMS" }));
  expect(screen.getByText("1. Hero-SMS")).toBeTruthy();
  expect(screen.getByText("2. 62-US")).toBeTruthy();

  fireEvent.click(screen.getByRole("button", { name: "移除 62-US" }));
  expect(screen.queryByText("2. 62-US")).toBeNull();
  expect(screen.getByRole("button", { name: "加入 62-US" })).toBeTruthy();
  expect((screen.getByRole("button", { name: "保存规则" }) as HTMLButtonElement).disabled).toBe(false);
});

// 删除是可逆的，但一条规则决定以后每次要号的钱花到哪家——点两次。
it("删除要点两次才真删", async () => {
  seed([RULE]);
  vi.mocked(removeSMSRoutingRule).mockResolvedValue(RUN);
  const onWrite = renderPanel();

  fireEvent.click(await screen.findByRole("button", { name: "删除" }));
  expect(removeSMSRoutingRule).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "确认删除" }));

  await waitFor(() => expect(removeSMSRoutingRule).toHaveBeenCalledWith("r1"));
  await waitFor(() => expect(onWrite).toHaveBeenCalled());
});

it("编辑把规则填进表单", async () => {
  seed([RULE]);
  renderPanel();

  fireEvent.click(await screen.findByRole("button", { name: "编辑" }));
  expect((screen.getByLabelText("路由服务") as HTMLInputElement).value).toBe("go");
  expect((screen.getByLabelText("路由国家") as HTMLInputElement).value).toBe("*");
  expect(screen.getByText("1. Hero-SMS")).toBeTruthy();
  expect(screen.getByText("2. 62-US")).toBeTruthy();
  expect((screen.getByLabelText("路由单价上限") as HTMLInputElement).value).toBe("0.5");
});

it("停用的规则标为停用", async () => {
  seed([{ ...RULE, enabled: false }]);
  renderPanel();

  expect(await screen.findByText("停用")).toBeTruthy();
});

// 只有能买号的供应商才能进规则：邮箱专用或只读的那家出现在这里没有意义。
it("只列出有购买能力的供应商", async () => {
  seed([]);
  vi.mocked(listSMSProviders).mockResolvedValue([
    { provider: "sms62", label: "62-US", capabilities: ["purchase"], enabled: true, verified: true, supports_lifecycle: false },
    { provider: "mail_only", label: "只有邮箱", capabilities: ["email"], enabled: true, verified: true, supports_lifecycle: false },
  ]);
  renderPanel();

  await screen.findByRole("button", { name: "加入 62-US" });
  expect(screen.queryByRole("button", { name: "加入 只有邮箱" })).toBeNull();
});
