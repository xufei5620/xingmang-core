import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ExtIntegrationPage } from "./ExtIntegrationPage";

function fakeResponse(body: unknown, status = 200): Response {
  return {
    ok: status < 400,
    status,
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(typeof body === "string" ? body : JSON.stringify(body)),
  } as unknown as Response;
}

/** 后端随响应下发的两句限定，逐字照抄 httpapi/integration.go 里的常量。
 *
 *  为什么在 fixture 里抄一遍而不是随便写一句：这两句是这一页唯一能防止误读
 *  的东西，页面必须**原样显示服务端给的那份**而不是自己另写一句。fixture 抄
 *  真值，断言才能真的对上（两边同源的对账在后端那侧的用例里）。 */
const OBSERVED_NOTE =
  "只统计经 Action 内核的**写操作**（action.action_run）。" +
  "读操作（GET）今天只进进程访问日志，不落库也没有查询端点——" +
  "所以「没有观测记录」只能说明它没做过写操作，不能说明它没来过。";
const REGISTRY_NOTE =
  "登记簿不是授权面：登记不发凭据、不授予权限、不设配额，" +
  "停用也不会让任何请求被拒绝。授权仍由 Keycloak 角色与员工账号角色决定。";
const EXECUTION_NOTE =
  "规则登记在此，但当前不会自动执行：平台没有规则执行器。" +
  "自动触发 Action 会绕开人工审批那道闸——L2 及以上必须有人批准，而机器凑不出审批人。" +
  "要不要让规则引擎真执行、能执行到哪个风险等级，是单独要裁定的事（ADMIN-IA §5.4.1）。";

function apiClientsBody(overrides: Record<string, unknown> = {}) {
  return {
    items: [
      {
        id: "11111111-1111-1111-1111-111111111111",
        principal_id: "svc-billing-sync",
        principal_type: "SERVICE",
        display_name: "对账同步",
        purpose: "每日拉取上游账单",
        owner: "财务运营",
        expected_scopes: ["finance.read"],
        credential_ref: "secret://integration/billing-sync",
        status: "active",
        notes: "",
        environment: "development",
        created_at: "2026-09-01T00:00:00Z",
        created_by: "staff_alice",
        updated_at: "2026-09-01T00:00:00Z",
        updated_by: "staff_alice",
        observed: {
          principal_id: "svc-billing-sync",
          principal_type: "SERVICE",
          run_count: 12,
          failed_count: 1,
          first_seen_at: "2026-09-06T00:00:00Z",
          last_seen_at: "2026-09-08T01:00:00Z",
          last_action_id: "finance.upstream_account.set",
          last_status: "succeeded",
        },
      },
      {
        id: "22222222-2222-2222-2222-222222222222",
        principal_id: "svc-legacy-import",
        principal_type: "SERVICE",
        display_name: "老导入任务",
        purpose: "",
        owner: "",
        expected_scopes: [],
        credential_ref: "",
        status: "active",
        notes: "",
        environment: "development",
        created_at: "2026-08-01T00:00:00Z",
        created_by: "staff_alice",
        updated_at: "2026-08-01T00:00:00Z",
        updated_by: "staff_alice",
        observed: null,
      },
    ],
    unregistered: [
      {
        principal_id: "staff_bob",
        principal_type: "HUMAN",
        run_count: 3,
        failed_count: 0,
        first_seen_at: "2026-09-07T00:00:00Z",
        last_seen_at: "2026-09-08T02:00:00Z",
        last_action_id: "server.asset.set",
        last_status: "succeeded",
      },
    ],
    window_days: 7,
    observed_since: "2026-09-01T03:00:00Z",
    observed_truncated: false,
    observed_source: "action.action_run",
    observed_note: OBSERVED_NOTE,
    registry_note: REGISTRY_NOTE,
    ...overrides,
  };
}

function automationRulesBody(overrides: Record<string, unknown> = {}) {
  return {
    items: [
      {
        id: "33333333-3333-3333-3333-333333333333",
        name: "余额低时补充",
        description: "上游余额跌破阈值时补一次充值登记",
        trigger_kind: "event",
        trigger_detail: "alerts.alert.opened(rule_key=upstream_balance_low)",
        target_action_id: "finance.upstream_account.set",
        target_action_version: "1",
        target_action_registered: true,
        target_action_risk_level: "L1",
        // 刻意用**已登记**这个最容易被误读成「已生效」的状态：
        // 若页面把它渲染成运行态，下面那条缺席断言就该红。
        status: "registered",
        notes: "",
        environment: "development",
        created_at: "2026-09-08T00:00:00Z",
        created_by: "staff_alice",
        updated_at: "2026-09-08T00:00:00Z",
        updated_by: "staff_alice",
      },
      {
        id: "44444444-4444-4444-4444-444444444444",
        name: "草稿规则",
        description: "",
        trigger_kind: "schedule",
        trigger_detail: "0 3 * * *",
        target_action_id: "nowhere.no_such.action",
        target_action_version: "1",
        target_action_registered: false,
        target_action_risk_level: "",
        status: "draft",
        notes: "",
        environment: "development",
        created_at: "2026-09-08T00:00:00Z",
        created_by: "staff_alice",
        updated_at: "2026-09-08T00:00:00Z",
        updated_by: "staff_alice",
      },
    ],
    automatic_execution: false,
    execution_note: EXECUTION_NOTE,
    ...overrides,
  };
}

function stubFetch(handler: (url: string) => Response) {
  const fetchImpl = vi.fn((input: string) => Promise.resolve(handler(input)));
  vi.stubGlobal("fetch", fetchImpl);
  return fetchImpl;
}

function defaultHandler(
  clients = apiClientsBody(),
  rules = automationRulesBody(),
): (url: string) => Response {
  return (url: string) => {
    if (url.includes("/api/v1/integration/api-clients")) return fakeResponse(clients);
    if (url.includes("/api/v1/integration/automation-rules")) return fakeResponse(rules);
    throw new Error(`测试没有为这个地址准备响应：${url}`);
  };
}

function renderPage(initialEntry = "/ext/integration") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <ExtIntegrationPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("接口与自动化页", () => {
  it("四格页签都在，默认落在 API调用方", async () => {
    stubFetch(defaultHandler());
    renderPage();
    for (const label of ["API调用方", "Webhook", "自动化流程", "运行记录"]) {
      expect(screen.getByRole("tab", { name: label })).toBeTruthy();
    }
    expect(screen.getByRole("tab", { name: "API调用方", selected: true })).toBeTruthy();
    expect(await screen.findByText("对账同步")).toBeTruthy();
  });

  it("认不出来的 ?sub= 不静默回落到第一格", () => {
    stubFetch(defaultHandler());
    renderPage("/ext/integration?sub=nope");
    expect(screen.getByText("「nope」子页尚未接入")).toBeTruthy();
    // 正向锚点：正常地址下这一行是不出现的（否则上面那条断言在任何情况下都绿）。
  });

  it("正常地址下不会显示「子页尚未接入」", async () => {
    stubFetch(defaultHandler());
    renderPage();
    await screen.findByText("对账同步");
    expect(screen.queryByText(/子页尚未接入/)).toBeNull();
  });

  it("登记簿与观测两侧分开呈现：登记没来过的 observed 为空，来过没登记的单独一张表", async () => {
    stubFetch(defaultHandler());
    renderPage();

    // 登记且调用过：带出最近一次的时刻与 Action。
    const active = (await screen.findByText("对账同步")).closest("tr");
    expect(active).toBeTruthy();
    expect(within(active!).getByText("finance.upstream_account.set")).toBeTruthy();

    // 登记却没来过：明说「窗口内没有写操作」，不编一个 0。
    const dormant = screen.getByText("老导入任务").closest("tr");
    expect(within(dormant!).getByText("窗口内没有写操作")).toBeTruthy();

    // 做过写操作却没登记：在另一张表里。
    expect(screen.getByText("做过写操作却没登记")).toBeTruthy();
    const stranger = screen.getByText("staff_bob").closest("tr");
    expect(within(stranger!).getByText("3")).toBeTruthy();
  });

  it("「只覆盖写操作」这个限定出现在会被误读的那句话旁边，不只在下面的卡里", async () => {
    stubFetch(defaultHandler());
    renderPage();
    await screen.findByText("对账同步");
    // 页签自己的说明里就带限定——「谁在调我们的 API」不加限定会被读成
    // 包含读请求，而读请求今天查不到。
    expect(
      screen.getByText(/谁在调我们的 API \*\*做写操作\*\*/),
    ).toBeTruthy();
    expect(screen.getByText(/\*\*读操作不在这份观测里\*\*/)).toBeTruthy();
    // 未登记那张表的标题同样带限定，不写成「来过却没登记」。
    expect(screen.getByText("做过写操作却没登记")).toBeTruthy();
    expect(screen.queryByText("来过却没登记")).toBeNull();
  });

  it("两句限定原样取自服务端，不是前端自己写的一句", async () => {
    stubFetch(defaultHandler());
    renderPage();
    expect(await screen.findByText(REGISTRY_NOTE)).toBeTruthy();
    expect(screen.getByText(OBSERVED_NOTE)).toBeTruthy();
  });

  it("IP限制与调用配额留列不留数：列头在、格子里写「未接入」", async () => {
    stubFetch(defaultHandler());
    renderPage();
    await screen.findByText("对账同步");
    expect(screen.getByRole("columnheader", { name: /IP限制/ })).toBeTruthy();
    expect(screen.getByRole("columnheader", { name: /调用配额/ })).toBeTruthy();
    const row = screen.getByText("对账同步").closest("tr");
    // 两列各一个「未接入」——正向断言（数得出个数），不是「没有数字」那种
    // 一旦整行渲染失败就恒真的缺席断言。
    expect(within(row!).getAllByText("未接入")).toHaveLength(2);
  });

  it("观测被截断时如实说这份清单不完整", async () => {
    stubFetch(defaultHandler(apiClientsBody({ observed_truncated: true })));
    renderPage();
    expect(await screen.findByText(/这份观测\*\*不完整\*\*/)).toBeTruthy();
  });

  it("没截断时不显示那条不完整提示", async () => {
    stubFetch(defaultHandler());
    renderPage();
    // 先 await 正向锚点，再同步断言缺席——把缺席断言包进 waitFor 里几乎必然
    // 恒真（第一次检查时页面还没渲染出任何东西）。
    await screen.findByText("对账同步");
    expect(screen.queryByText(/这份观测\*\*不完整\*\*/)).toBeNull();
  });
});

describe("自动化流程格：不会自动执行", () => {
  it("最显眼的位置就说清规则不会自动执行，且文案取自服务端", async () => {
    stubFetch(defaultHandler());
    renderPage("/ext/integration?sub=flows");
    expect(await screen.findByText("规则登记在此，但当前不会自动执行")).toBeTruthy();
    expect(screen.getByText(EXECUTION_NOTE)).toBeTruthy();
    expect(screen.getByText("自动执行：关")).toBeTruthy();
  });

  it("已登记的规则不呈现为运行态：状态写「已登记」，运行记录写「不会运行」", async () => {
    stubFetch(defaultHandler());
    renderPage("/ext/integration?sub=flows");

    // 正向锚点：那条 status=registered 的规则确实渲染出来了。
    const row = (await screen.findByText("余额低时补充")).closest("tr");
    expect(row).toBeTruthy();
    // 正向断言取代「不含『已启用』」那种缺席写法：断言它**是**什么，
    // 比断言它不是什么更难恒真。
    expect(within(row!).getByText("已登记")).toBeTruthy();
    expect(within(row!).getByText("不会运行")).toBeTruthy();
    // 缺席断言放在锚点之后、同步执行：整页里都不该出现暗示运行态的词。
    for (const forbidden of [/已启用/, /运行中/, /生效中/]) {
      expect(screen.queryByText(forbidden)).toBeNull();
    }
  });

  it("指向未注册 Action 的规则被如实标出，而不是与正常规则长得一样", async () => {
    stubFetch(defaultHandler());
    renderPage("/ext/integration?sub=flows");

    const good = (await screen.findByText("余额低时补充")).closest("tr");
    expect(within(good!).getByText(/风险等级 L1/)).toBeTruthy();
    const bad = screen.getByText("草稿规则").closest("tr");
    expect(
      within(bad!).getByText("这个操作当前没有注册——登记指向了一个不存在的动作"),
    ).toBeTruthy();
  });

  it("automatic_execution 若变成 true，徽章立刻改口", async () => {
    // 这一条不是在测一个不会发生的分支：它证明上面「自动执行：关」那条断言
    // **认得出**另一种取值——一个恒显示「关」的徽章会让那条断言恒真。
    stubFetch(defaultHandler(apiClientsBody(), automationRulesBody({ automatic_execution: true })));
    renderPage("/ext/integration?sub=flows");
    expect(await screen.findByText("自动执行：开")).toBeTruthy();
    expect(screen.queryByText("自动执行：关")).toBeNull();
  });
});

describe("Webhook 格：告警外发不是业务事件 Webhook", () => {
  it("说清两者不是一回事，并列出四条已有的出站通道", async () => {
    stubFetch(defaultHandler());
    renderPage("/ext/integration?sub=webhooks");
    expect(
      await screen.findByText("出站通知通道（告警外发，不是业务事件 Webhook）"),
    ).toBeTruthy();
    expect(screen.getByText("告警（企业微信群机器人）")).toBeTruthy();
    expect(screen.getByText("卡片事件（企业微信群机器人）")).toBeTruthy();
    expect(screen.getByText("接码验证码（企业微信群机器人）")).toBeTruthy();
    // 两条没有投递记录的通道必须说出来，不能只列通道名让人以为都一样。
    expect(screen.getAllByText(/\*\*没有\*\*投递记录/)).toHaveLength(2);
  });

  it("只显示凭据引用，不显示群机器人地址", async () => {
    stubFetch(defaultHandler());
    renderPage("/ext/integration?sub=webhooks");
    // 先 await 正向锚点：引用确实渲染出来了。
    expect(await screen.findByText(/secret:\/\/cards\/notify-webhook/)).toBeTruthy();
    // 再同步断言缺席：整页里不该出现任何 https 群机器人地址。
    expect(screen.queryByText(/qyapi\.weixin\.qq\.com/)).toBeNull();
    expect(screen.queryByText(/api\.telegram\.org/)).toBeNull();
  });

  it("保留冻结的蓝图列头，并就地写清在等什么", async () => {
    stubFetch(defaultHandler());
    renderPage("/ext/integration?sub=webhooks");
    await screen.findByText("出站通知通道（告警外发，不是业务事件 Webhook）");
    for (const header of ["方向", "事件类型", "目标地址", "签名", "成功率"]) {
      expect(screen.getByRole("columnheader", { name: new RegExp(header) })).toBeTruthy();
    }
    expect(
      screen.getAllByText(/要做通用 Webhook 还缺四样/).length,
    ).toBeGreaterThan(0);
  });
});

describe("运行记录格", () => {
  it("五类记录逐条说清有没有、在哪里", async () => {
    stubFetch(defaultHandler());
    renderPage("/ext/integration?sub=runs");
    expect(await screen.findByText("五类运行记录，今天各在哪里")).toBeTruthy();
    const flowRow = screen.getByText("流程运行记录").closest("tr");
    expect(within(flowRow!).getByText("没有")).toBeTruthy();
    expect(
      within(flowRow!).getByText(/没有执行器就没有运行/),
    ).toBeTruthy();
    // 真有的那两类给出可点的入口，而不是只说一句「在别处」。
    expect(
      screen.getByRole("link", { name: /操作与审批 → 执行记录/ }).getAttribute("href"),
    ).toBe("/actions?sub=runs");
  });
});

describe("顶部四格", () => {
  // StatTile 把标签渲染成 <h3 title={label}>，所以按 title 取——按可见文字取会
  // 同时命中页签、表头与统计格三处同名文本。
  function tile(label: string): HTMLElement {
    const el = screen.getByTitle(label).closest("article");
    if (!el) throw new Error(`没有找到「${label}」统计格`);
    return el as HTMLElement;
  }

  it("能算的算真值，算不出的显式未接入并说清缺什么", async () => {
    stubFetch(defaultHandler());
    renderPage();
    // 等真值出现，而不是等标签出现：标签一开始就在，等它等于没等。
    await waitFor(() => expect(within(tile("API调用方")).getByText("2")).toBeTruthy());

    // Webhook异常算不出来：主数位是「—」，副行说清为什么。
    expect(within(tile("Webhook异常")).getByText("—")).toBeTruthy();
    expect(
      within(tile("Webhook异常")).getByText(/平台没有 Webhook 投递记录，这一格算不出来/),
    ).toBeTruthy();
  });

  it("流程草稿数的是草稿，且副行说清已登记的规则同样不会执行", async () => {
    stubFetch(defaultHandler());
    renderPage();
    // fixture 里两条规则：一条 registered、一条 draft，所以是 1 不是 2。
    await waitFor(() => expect(within(tile("流程草稿")).getByText("1")).toBeTruthy());
    expect(
      within(tile("流程草稿")).getByText(/\*\*已登记的规则同样不会自动执行\*\*/),
    ).toBeTruthy();
  });
});
