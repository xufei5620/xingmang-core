import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, expect, it, vi } from "vitest";
import { SMSPanel } from "./SMSPanel";

vi.mock("../api/sms", async () => {
  const actual = await vi.importActual<typeof import("../api/sms")>("../api/sms");
  return {
    ...actual,
    listSMSProviders: vi.fn(),
    listSMSResources: vi.fn(),
    listSMSOperations: vi.fn(),
    listSMSCodes: vi.fn(),
    listSMSCatalog: vi.fn(),
    purchaseSMSNumbers: vi.fn(),
    verifySMSProvider: vi.fn(),
    setSMSProviderEnabled: vi.fn(),
    fetchSMSCode: vi.fn(),
    importSMSOrder: vi.fn(),
    executeSMSResourceAction: vi.fn(),
    resolveSMSOperation: vi.fn(),
  };
});

import {
  fetchSMSCode,
  listSMSCatalog,
  listSMSCodes,
  listSMSOperations,
  listSMSProviders,
  listSMSResources,
  purchaseSMSNumbers,
  setSMSProviderEnabled,
  type SMSOperation,
  type SMSProvider,
  type SMSResource,
} from "../api/sms";

function renderPanel() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <SMSPanel />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function seed(over: {
  providers?: SMSProvider[];
  resources?: SMSResource[];
  operations?: SMSOperation[];
} = {}) {
  vi.mocked(listSMSProviders).mockResolvedValue(
    over.providers ?? [
      {
        provider: "hero_sms",
        enabled: true,
        verified: true,
        supports_lifecycle: true,
        verified_at: "2026-09-05T00:00:00Z",
      },
    ],
  );
  vi.mocked(listSMSResources).mockResolvedValue(over.resources ?? []);
  vi.mocked(listSMSOperations).mockResolvedValue(over.operations ?? []);
  vi.mocked(listSMSCodes).mockResolvedValue([]);
  vi.mocked(listSMSCatalog).mockResolvedValue([]);
}

afterEach(() => vi.clearAllMocks());

// 没做过连接测试的供应商**买不了号**，而且这一点要在人点之前就看得见。
//
// 后端也会拦，但那时钱虽然没花，人已经填完一整个表单了。
it("未验证的供应商买号按钮是禁用的", async () => {
  seed({ providers: [{ provider: "sms62", enabled: true, verified: false, supports_lifecycle: false }] });
  renderPanel();

  // **先等数据到达**：供应商清单是异步的，而空态下按钮同样是禁用的——
  // 不等就会在「还没加载」那一刻断言成功，测不到想测的东西。
  await screen.findAllByText("62-US");
  const button = (await screen.findByRole("button", { name: "买号" })) as HTMLButtonElement;
  expect(button.disabled).toBe(true);
  expect(screen.getByText(/还没做过连接测试/)).toBeTruthy();
});

// 买号是两步的：第一次点只是「上膛」。
//
// 买到的号不可退，而这个按钮和它周围的按钮长得一样。
it("买号需要两步确认，第一次点击不发请求", async () => {
  seed();
  renderPanel();

  // 同上：不等数据到达，按钮还是禁用的，点了没反应。
  await screen.findAllByText("Hero-SMS");
  fireEvent.click(await screen.findByRole("button", { name: "买号" }));
  // 对话框里那个「买号」是上膛按钮。
  const armButtons = await screen.findAllByRole("button", { name: "买号" });
  fireEvent.click(armButtons[armButtons.length - 1]!);

  expect(purchaseSMSNumbers).not.toHaveBeenCalled();
  expect(await screen.findByRole("button", { name: /确认买/ })).toBeTruthy();
});

// 62 一个生命周期动作都没有，**明说而不是把按钮灰掉**。
//
// 一个灰按钮看起来像「暂时不能用」，而这是永远不能用。
it("62 的号码不显示取消/换号按钮", async () => {
  seed({
    providers: [{ provider: "sms62", enabled: true, verified: true, supports_lifecycle: false }],
    resources: [
      { resource_id: "r1", provider: "sms62", phone_mask: "1555****1111", status: "active" },
    ],
  });
  renderPanel();

  await screen.findByText(/这家不支持取消\/延长/);
  expect(screen.queryByRole("button", { name: "取消" })).toBeNull();
  expect(screen.queryByRole("button", { name: "换号" })).toBeNull();
});

// Hero 的号码有那三个按钮。
it("Hero 的号码显示生命周期按钮", async () => {
  seed({
    resources: [
      { resource_id: "r1", provider: "hero_sms", phone_mask: "1555****2222", status: "active" },
    ],
  });
  renderPanel();

  expect(await screen.findByRole("button", { name: "取消" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "换号" })).toBeTruthy();
});

// unknown 的操作要亮红条，并给出核对入口。
//
// 它的含义是「不知道钱花没花出去」——那是唯一需要人立刻做点什么的状态，
// 混在一列普通记录里等于没有。
it("结果未知的操作亮红条并给出核对入口", async () => {
  seed({
    operations: [
      {
        operation_id: "op-1",
        provider: "sms62",
        kind: "purchase",
        state: "unknown",
        needs_review: true,
        retry_allowed: false,
        provider_ref: "order-9",
        started_at: "2026-09-05T00:00:00Z",
      },
    ],
  });
  renderPanel();

  expect(await screen.findByText(/有 1 笔操作结果未知/)).toBeTruthy();
  expect(screen.getByRole("button", { name: "核对" })).toBeTruthy();
  // 上游引用要显示出来：unknown 时它是人去供应商侧对账的唯一抓手。
  expect(screen.getByText("order-9")).toBeTruthy();
});

// 已有定论的操作不给核对入口。
it("已成功的操作不显示核对按钮", async () => {
  seed({
    operations: [
      {
        operation_id: "op-1",
        provider: "hero_sms",
        kind: "purchase",
        state: "succeeded",
        needs_review: false,
        retry_allowed: false,
        started_at: "2026-09-05T00:00:00Z",
      },
    ],
  });
  renderPanel();

  await screen.findByText("成功");
  expect(screen.queryByRole("button", { name: "核对" })).toBeNull();
  expect(screen.queryByText(/结果未知/)).toBeNull();
});

// 核对必须写依据：空备注时提交按钮是禁用的。
it("核对没写依据时提交按钮禁用", async () => {
  seed({
    operations: [
      {
        operation_id: "op-1",
        provider: "sms62",
        kind: "purchase",
        state: "unknown",
        needs_review: true,
        retry_allowed: false,
        started_at: "2026-09-05T00:00:00Z",
      },
    ],
  });
  renderPanel();

  fireEvent.click(await screen.findByRole("button", { name: "核对" }));
  const submit = (await screen.findByRole("button", { name: "记录结论" })) as HTMLButtonElement;
  expect(submit.disabled).toBe(true);
});

// 出口 IP 显示出来——它的用处是排查 IP 白名单，而那种失败看起来只是「没权限」。
it("显示供应商观察到的出口 IP", async () => {
  seed({
    providers: [
      {
        provider: "sms62",
        enabled: true,
        verified: true,
        supports_lifecycle: false,
        client_ip: "203.0.113.10",
      },
    ],
  });
  renderPanel();

  expect(await screen.findByText(/203\.0\.113\.10/)).toBeTruthy();
});

// 关掉的供应商买不了号，而且提示要说清是**哪一种**不能买。
//
// 「关着」与「没验证」的下一步完全不同：前者去页面上打开，后者去做连接测试。
// 合成一句「不可用」，人就只能挨个试。
it("停用的供应商买号按钮禁用，且提示与未验证区分开", async () => {
  seed({
    providers: [{ provider: "sms62", enabled: false, verified: true, supports_lifecycle: false }],
  });
  renderPanel();

  await screen.findAllByText("62-US");
  const button = (await screen.findByRole("button", { name: "买号" })) as HTMLButtonElement;
  expect(button.disabled).toBe(true);
  expect(screen.getByText(/在后台被停用/)).toBeTruthy();
  // 它验证过，所以**不该**看到「还没做过连接测试」那句。
  expect(screen.queryByText(/还没做过连接测试/)).toBeNull();
});

// 开关走 Action，参数是 enabled 的**目标值**。
//
// 传「切换」而不是目标值，会让两个人同时点变成一次开一次关；
// 传目标值时同向的两次点击是幂等的。
it("点停用把 enabled=false 发给 Action", async () => {
  seed({
    providers: [{ provider: "sms62", enabled: true, verified: true, supports_lifecycle: false }],
  });
  vi.mocked(setSMSProviderEnabled).mockResolvedValue({ runId: "run-1" } as never);
  renderPanel();

  await screen.findAllByText("62-US");
  fireEvent.click(await screen.findByRole("button", { name: "停用" }));

  await waitFor(() => expect(setSMSProviderEnabled).toHaveBeenCalledWith("sms62", false));
});

// 强调用 <strong>，不要把 markdown 星号当字面量吐到页面上。
//
// 这三个字是整页最要紧的一句（买号花的是真钱且退不回来），而 `**不可退**`
// 在界面上读起来像个排版事故——它没有变粗，反倒让那句警告显得不可信。
it("买号说明里没有裸露的 markdown 星号", async () => {
  seed();
  renderPanel();

  // 默认夹具里只有 Hero-SMS。
  await screen.findAllByText("Hero-SMS");
  expect(screen.queryByText(/\*\*/)).toBeNull();
  expect(screen.getByText("不可退").tagName).toBe("STRONG");
});

// 取码是人发起的入口：点「向上游取码」要真的调 sms.code.fetch，带这张号的 id。
//
// SMS0 时 Service.FetchCode 后端有、入口没有，页面上永远是「还没收到码」——
// 这条测试钉住入口存在。
it("号码详情里的「向上游取码」调 fetchSMSCode 并带上 resource_id", async () => {
  seed({
    resources: [
      { resource_id: "r1", provider: "hero_sms", phone_mask: "****0001", service: "go", country: "12" },
    ],
  });
  vi.mocked(fetchSMSCode).mockResolvedValue({ runId: "run-code" } as never);
  renderPanel();

  fireEvent.click(await screen.findByRole("button", { name: "向上游取码" }));
  await waitFor(() => expect(fetchSMSCode).toHaveBeenCalledWith("r1"));
});

// 号码栏显示**平台自己的**统一状态；上游原话（Hero 的 4、62 的「正常」）
// 只在悬停时看。两家的原话没有统一含义，直接摆出来等于让人背两张表。
it("号码显示统一状态，上游原话放在悬停里", async () => {
  seed({
    resources: [
      {
        resource_id: "r1",
        provider: "hero_sms",
        phone_mask: "1555****3333",
        status: "4",
        state: "code_received",
        effective_state: "code_received",
      },
    ],
  });
  renderPanel();

  const badges = await screen.findAllByText("已收码");
  expect(badges.length).toBeGreaterThan(0);
  expect(screen.queryByText("4")).toBeNull();
  const hover = badges[0]!.closest("[title]");
  expect(hover?.getAttribute("title")).toBe("上游状态：4");
});

// 「待收码但已过期」由服务端算成 expired 回来，页面按它显示，不自己再算一遍。
it("过期的号按 effective_state 显示已过期", async () => {
  seed({
    resources: [
      {
        resource_id: "r1",
        provider: "hero_sms",
        phone_mask: "1555****4444",
        status: "1",
        state: "waiting_code",
        effective_state: "expired",
      },
    ],
  });
  renderPanel();

  expect((await screen.findAllByText("已过期")).length).toBeGreaterThan(0);
  expect(screen.queryByText("待收码")).toBeNull();
});

// 旧数据还没映射（state 为空）时退回显示上游原话，不显示空白。
it("没有统一状态的旧号码退回显示上游原话", async () => {
  seed({
    resources: [
      { resource_id: "r1", provider: "sms62", phone_mask: "1555****5555", status: "正常" },
    ],
  });
  renderPanel();

  expect((await screen.findAllByText("正常")).length).toBeGreaterThan(0);
});
