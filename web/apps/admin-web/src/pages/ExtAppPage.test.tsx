import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ExtAppItem, ExtAppReleaseItem } from "../api/extapp";
import { ExtAppPage } from "./ExtAppPage";

function fakeResponse(body: unknown, status = 200): Response {
  return {
    ok: status < 400,
    status,
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(typeof body === "string" ? body : JSON.stringify(body)),
  } as unknown as Response;
}

const APP_ID = "11111111-1111-4111-8111-111111111111";
const OTHER_APP_ID = "22222222-2222-4222-8222-222222222222";
const RELEASE_ID = "33333333-3333-4333-8333-333333333333";

function release(over: Partial<ExtAppReleaseItem> = {}): ExtAppReleaseItem {
  return {
    id: RELEASE_ID,
    app_id: APP_ID,
    version: "2026.09.08-1",
    commit_sha: "8c446e5",
    kind: "deploy",
    released_at: "2026-09-08T10:00:00Z",
    released_by: "平台组",
    notes: "",
    created_at: "2026-09-08T10:05:00Z",
    ...over,
  };
}

function app(over: Partial<ExtAppItem> = {}): ExtAppItem {
  return {
    id: APP_ID,
    app_key: "admin-web",
    display_name: "运营后台",
    primary_domain: "admin.example.test",
    auth_mode: "oidc",
    owner: "平台组",
    status: "active",
    notes: "",
    environment: "production",
    created_at: "2026-09-01T00:00:00Z",
    updated_at: "2026-09-08T00:00:00Z",
    current_release: release(),
    ...over,
  };
}

/** 没有登记过任何发布的站点：current_release 是 null，**不是**空对象。 */
function plannedApp(): ExtAppItem {
  return app({
    id: OTHER_APP_ID,
    app_key: "console",
    display_name: "控制台",
    primary_domain: "",
    auth_mode: "",
    status: "planned",
    current_release: null,
  });
}

function stubFetch(handler: (url: string, init?: RequestInit) => Response) {
  const fetchImpl = vi.fn((input: string, init?: RequestInit) =>
    Promise.resolve(handler(input, init)),
  );
  vi.stubGlobal("fetch", fetchImpl);
  return fetchImpl;
}

interface StubOptions {
  apps?: ExtAppItem[];
  releases?: ExtAppReleaseItem[];
}

function defaultHandler(options: StubOptions = {}) {
  const apps = options.apps ?? [app()];
  const releases = options.releases ?? [release()];
  return (url: string): Response => {
    // 顺序要紧：/ext/apps/releases 也包含 /ext/apps，先判长的那条。
    if (url.includes("/api/v1/ext/apps/releases")) return fakeResponse({ items: releases });
    if (url.includes("/api/v1/ext/apps")) return fakeResponse({ items: apps });
    if (url.includes("/api/v1/actions/")) {
      return fakeResponse({ run_id: "run-1", status: "SUCCEEDED" });
    }
    throw new Error(`测试没有为这个地址准备响应：${url}`);
  };
}

function renderExtApp(initialEntry = "/ext/app") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <ExtAppPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("应用与配置页", () => {
  it("四格页签都在，默认落在应用目录", async () => {
    stubFetch(defaultHandler());
    renderExtApp();

    for (const label of ["应用目录", "页面配置", "页面组件", "版本与发布"]) {
      expect(screen.getByRole("tab", { name: label })).toBeTruthy();
    }
    expect(screen.getByRole("tab", { name: "应用目录", selected: true })).toBeTruthy();
    expect(await screen.findByText("运营后台")).toBeTruthy();
  });

  // 建成之后，PlaceholderPage 给 /ext/* 挂的那条横幅对这一页已经是假话
  // （这一页真的会保存）。
  //
  // 缺席型断言，因此：① 先 await 一个正向锚点，确认页面**真的渲染出来了**
  // （直接 waitFor 包住缺席断言几乎必然恒真——一个渲染失败的页面上什么都
  // 找不到）；② 同一条用例里再正向断言那句**取而代之**的门禁文案确实在，
  // 这样「两句都没有」这种退化也会被抓住。
  it("不再挂「只读蓝图：仅预览、不保存、不发布、不执行」，换成如实的门禁说明", async () => {
    stubFetch(defaultHandler());
    renderExtApp();

    // 正向锚点：真实数据已经渲染。
    expect(await screen.findByText("运营后台")).toBeTruthy();

    expect(screen.queryByText(/只读蓝图：仅预览、不保存、不发布、不执行/)).toBeNull();
    expect(screen.queryByText(/这一段不会因为页面存在就提前建后端/)).toBeNull();

    // 取而代之的那条：本页只登记，不发布。
    const gate = screen.getByRole("status");
    expect(gate.textContent).toMatch(/本页只做/);
    expect(gate.textContent).toMatch(/不发布、不回滚、不重启任何站点/);
    expect(gate.textContent).toMatch(/Platform Lifecycle Operation/);
  });

  it("应用目录的列头逐字照冻结的蓝图，「配置版本」留列不留数", async () => {
    stubFetch(defaultHandler());
    renderExtApp();

    const table = await screen.findByRole("table");
    const headers = within(table)
      .getAllByRole("columnheader")
      .map((th) => th.textContent?.replace(/[↑↓⇅\s]/g, "") ?? "");
    for (const column of [
      "应用",
      "域名",
      "环境",
      "登录方式",
      "配置版本",
      "发布版本",
      "负责人",
      "状态",
    ]) {
      expect(headers.some((h) => h.includes(column)), `列头缺少「${column}」`).toBe(true);
    }
    // 「配置版本」这一列属于页面搭建器，平台没有那个对象：列在、值是「未接入」。
    // 摆一个空值会被读成「这个应用还没有配置版本」。
    const row = within(table).getByText("运营后台").closest("tr");
    expect(row).not.toBeNull();
    expect(within(row!).getByText("未接入")).toBeTruthy();
  });

  it("没登记过发布的站点显示「未登记发布」，登记过的显示版本号", async () => {
    stubFetch(defaultHandler({ apps: [app(), plannedApp()] }));
    renderExtApp();

    const table = await screen.findByRole("table");
    const withRelease = within(table).getByText("运营后台").closest("tr");
    expect(within(withRelease!).getByText("2026.09.08-1")).toBeTruthy();
    expect(within(withRelease!).getByText("8c446e5")).toBeTruthy();

    // 对照：没登记过发布的那一行必须说「未登记发布」，而不是一个空版本号。
    const withoutRelease = within(table).getByText("控制台").closest("tr");
    expect(within(withoutRelease!).getByText("未登记发布")).toBeTruthy();
    // 域名与登录方式同理：空串是「未登记」，不是一个空白格。
    expect(within(withoutRelease!).getAllByText("未登记").length).toBeGreaterThanOrEqual(2);
  });

  it("页面配置 / 页面组件保持蓝图态，并写清等的是裁定不是排期", async () => {
    stubFetch(defaultHandler());
    const user = userEvent.setup();
    renderExtApp();
    await screen.findByText("运营后台");

    await user.click(screen.getByRole("tab", { name: "页面配置" }));
    // getAllByText 而不是 getByText：这句落款在页签级与区块级各渲染一次，
    // 用单数版本会以「找到多个」失败，而那与「文案不对」是两回事。
    expect((await screen.findAllByText(/平台没有页面搭建器/)).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/要不要做这个功能本身还没有裁定/).length).toBeGreaterThan(0);
    // 正向锚点：冻结的字段名还在（蓝图态 ≠ 空白页）。
    expect(screen.getByText("品牌与主题")).toBeTruthy();

    await user.click(screen.getByRole("tab", { name: "页面组件" }));
    expect((await screen.findAllByText(/组件库属于页面搭建器/)).length).toBeGreaterThan(0);
    expect(screen.getByText("支持应用")).toBeTruthy();
  });

  it("版本与发布：发布记录簿是真表，蓝图那张「配置版本」表仍是冻结列头", async () => {
    stubFetch(defaultHandler());
    const user = userEvent.setup();
    renderExtApp();
    await screen.findByText("运营后台");

    await user.click(screen.getByRole("tab", { name: "版本与发布" }));

    // 真表：发布记录（正向锚点）
    expect(await screen.findByText("2026.09.08-1")).toBeTruthy();
    expect(screen.getByRole("button", { name: "记录发布" })).toBeTruthy();

    // 蓝图那张表的列头仍在，且落款说清它要的是页面搭建器的配置版本
    expect(screen.getByText("回滚来源")).toBeTruthy();
    // 落款说清它要的是页面搭建器的配置版本，不是上面那张发布记录簿。
    // 这些落款是**纯文本**渲染（BlueprintView 直接 {tab.source}），所以文案里
    // 一个 markdown 星号都不该有——有的话会原样显示在页面上。
    const legend = screen.getByText(/这张表要的是页面搭建器的「配置版本」/);
    expect(legend).toBeTruthy();
    expect(legend.textContent).not.toMatch(/\*\*/);

    // **不该有**发布 / 回滚按钮：这一页只登记。
    expect(screen.queryByRole("button", { name: /^发布$/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /^回滚$/ })).toBeNull();
  });

  it("发布记录里当前版本挂「当前」徽章，历史版本不挂", async () => {
    const current = release({ id: RELEASE_ID, version: "新的" });
    const historic = release({
      id: "44444444-4444-4444-8444-444444444444",
      version: "旧的",
      released_at: "2026-09-01T10:00:00Z",
    });
    stubFetch(
      defaultHandler({
        apps: [app({ current_release: current })],
        releases: [current, historic],
      }),
    );
    const user = userEvent.setup();
    renderExtApp();
    await screen.findByText("运营后台");
    await user.click(screen.getByRole("tab", { name: "版本与发布" }));

    const currentRow = (await screen.findByText("新的")).closest("tr");
    expect(within(currentRow!).getByText("当前")).toBeTruthy();
    // 对照组：历史那一行不挂。少了它，「当前」徽章挂给每一行也能让上一句绿。
    const historicRow = screen.getByText("旧的").closest("tr");
    expect(within(historicRow!).queryByText("当前")).toBeNull();
  });

  it("拼错的 ?sub= 不静默回落到第一格", async () => {
    stubFetch(defaultHandler());
    renderExtApp("/ext/app?sub=拼错了");

    expect(await screen.findByText(/「拼错了」子页尚未接入/)).toBeTruthy();
    // 没有悄悄渲染应用目录。
    expect(screen.queryByRole("tab", { name: "应用目录" })).toBeNull();
    expect(screen.getByRole("link", { name: /返回应用目录/ })).toBeTruthy();
  });

  it("登记表单把整条 URL 当域名时就地拦下，不发请求", async () => {
    const fetchImpl = stubFetch(defaultHandler());
    const user = userEvent.setup();
    renderExtApp();
    await screen.findByText("运营后台");
    const before = fetchImpl.mock.calls.length;

    await user.click(screen.getByRole("button", { name: "登记应用" }));
    // 在弹窗里查字段：页面上还有一张表，「域名」这个词不止一处
    // （同 ServerDomainsPanel.test.tsx 的做法）。
    const dialog = within(await screen.findByRole("dialog"));
    await user.type(dialog.getByLabelText(/应用键/), "console");
    await user.type(dialog.getByLabelText(/显示名/), "控制台");
    await user.type(dialog.getByLabelText(/^域名/), "https://console.example.test/admin");
    await user.type(dialog.getByLabelText(/负责人/), "平台组");
    await user.click(dialog.getByRole("button", { name: "登记" }));

    // 正向锚点：错误提示真的出现了（而不是弹窗根本没打开）。
    // findAllByText：这句同时出现在字段的 role="alert" 与提交汇总里。
    expect((await screen.findAllByText(/填主机名/)).length).toBeGreaterThan(0);
    // 拦下来了就不该发请求——一条带凭据的 URL 不该先过一次网络再被后端拒。
    expect(fetchImpl.mock.calls.length).toBe(before);
  });

  it("记录发布走 extapp.release.record@1，不走任何 deploy/release 执行动作", async () => {
    const fetchImpl = stubFetch(defaultHandler());
    const user = userEvent.setup();
    renderExtApp();
    await screen.findByText("运营后台");
    await user.click(screen.getByRole("tab", { name: "版本与发布" }));
    await screen.findByRole("button", { name: "记录发布" });

    await user.click(screen.getByRole("button", { name: "记录发布" }));
    const dialog = within(await screen.findByRole("dialog"));
    // Select 是 Radix 的组合框，不是原生 <select>：先打开再点选项。
    await user.click(dialog.getByRole("combobox", { name: "应用" }));
    await user.click(await screen.findByRole("option", { name: /运营后台/ }));
    await user.type(dialog.getByLabelText(/^版本/), "2026.09.09-1");
    await user.type(dialog.getByLabelText(/发布人/), "平台组");
    await user.click(dialog.getByRole("button", { name: "记录" }));

    await waitFor(() => {
      const called = fetchImpl.mock.calls.map((c) => String(c[0]));
      expect(
        called.some((u) => u.includes("/api/v1/actions/extapp.release.record/versions/1/execute")),
      ).toBe(true);
    });
    const called = fetchImpl.mock.calls.map((c) => String(c[0]));
    // 正向锚点已经过了，这里再同步断言：没有任何 deploy.* / release.* 执行动作。
    expect(called.some((u) => /actions\/(deploy|release)\./.test(u))).toBe(false);
  });
});
