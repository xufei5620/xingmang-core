import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { AcknowledgeAlertButton } from "./AcknowledgeAlertButton";
import { CardWorkbench } from "./CardWorkbench";
import { IssueCardDialog } from "./CardsShared";
import { HeroEmailsPanel, HeroRentPanel } from "./SMSExtrasPanels";
import { SMSPanel } from "./SMSPanel";
import { WithdrawPanel } from "./WithdrawPanel";
import { type ActionResult } from "./ActionResultNote";
import type { AlertItem } from "../api/alerts";

/** XM-ACTION-REASON 的整链测试：**从按钮打进去，一路走到网络层**。
 *
 *  这一档测试存在的理由，是本仓库栽过的那个跟头：「规则」与「调用它的地方」
 *  是两段代码时，只在规则那一侧造一个假响应会让判定恒真。所以这里
 *  **一个 api 模块都不 mock**——替身只放在最外面那一层（globalThis.fetch），
 *  链条是：
 *
 *      按钮 → 组件 → api/{cards,sms,withdraw}.ts 的包装 → platform.ts 的
 *      submitAction → api/client.ts 的 apiClient → fetch
 *
 *  于是「组件到底有没有把人写的理由传下去」「202 到底有没有被认出来」这两件
 *  事只能靠真的走通才会绿。中间任何一段回退成旧实现（不发 reason、或者把
 *  202 读成 runId 为空的成功），这里都会红。 */

/** 后端 202 的响应体，字段名逐字照抄 httpapi/actions.go 的
 *  approvalRequiredResponse：approval_request_id / status / message。 */
const APPROVAL_BODY = {
  approval_request_id: "ap-7f3c9e",
  status: "APPROVAL_REQUIRED",
  message: "action cards.withdraw.execute 风险等级 L3 需要审批，已受理为审批单 ap-7f3c9e",
};

interface RecordedPost {
  url: string;
  body: { params?: Record<string, unknown>; reason?: string };
}

let posts: RecordedPost[] = [];
let fetchMock: Mock;

/** 只替身最外层的 fetch。
 *
 *  GET 一律回一个空壳（各页的只读查询在这一档测试里不是被测对象）；
 *  POST 只可能是 Action 执行入口，回 202 + 审批体。 */
function stubFetch(seed: Record<string, unknown> = {}) {
  fetchMock = vi.fn((input: string, init?: RequestInit) => {
    const url = String(input);
    if ((init?.method ?? "GET") === "POST") {
      posts.push({ url, body: JSON.parse(String(init?.body ?? "{}")) });
      return Promise.resolve({
        ok: true,
        status: 202,
        json: () => Promise.resolve(APPROVAL_BODY),
      } as unknown as Response);
    }
    const path = url.split("?")[0] ?? url;
    const body = seed[path] ?? { items: [] };
    return Promise.resolve({
      ok: true,
      status: 200,
      json: () => Promise.resolve(body),
    } as unknown as Response);
  });
  vi.stubGlobal("fetch", fetchMock);
}

function wrap(node: React.ReactNode, path = "/") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/cards/:account/:cardId" element={<>{node}</>} />
          <Route path="*" element={<>{node}</>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

/** 这次提交发出去的那一条 Action 执行请求。 */
function executePost(): RecordedPost {
  const hit = posts.find((p) => p.url.includes("/execute"));
  if (!hit) throw new Error(`没有发出任何 Action 执行请求，实际发出的是：${JSON.stringify(posts)}`);
  return hit;
}

beforeEach(() => {
  posts = [];
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

// ---------------------------------------------------------------------------
// 提现（cards.withdraw.execute@1，L3）——整条链最完整的一条：
// 请求体、回执文案、以及「回执里不能出现 run_id」三件事一次走完。
// ---------------------------------------------------------------------------

describe("提现按钮（L3）", () => {
  const coldWallet = {
    address_id: "a1",
    account: "MAIN",
    chain: "TRON",
    address: "TCold1111111111111111111111111111",
    label: "冷钱包",
    enabled: true,
  };

  function renderPanel() {
    stubFetch({ "/api/v1/cards/withdraw/addresses": { items: [coldWallet] } });
    wrap(<WithdrawPanel accounts={["MAIN"]} />);
  }

  async function fillAndSubmit(reason: string) {
    fireEvent.change(await screen.findByLabelText("金额"), { target: { value: "10" } });
    fireEvent.change(screen.getByLabelText("理由"), { target: { value: reason } });
    fireEvent.click(screen.getByRole("button", { name: "提现" }));
    fireEvent.click(await screen.findByRole("button", { name: /提交提现审批/ }));
  }

  it("请求体里带上人写的 reason（httpapi.executeActionBody 的字段名）", async () => {
    renderPanel();
    await fillAndSubmit("季度结算，把冷钱包余额转回运营账户");

    await waitFor(() => expect(posts.length).toBe(1));
    const post = executePost();
    expect(post.url).toContain("/api/v1/actions/cards.withdraw.execute/versions/1/execute");
    // reason 与 params 平级，**不在 params 里**：params 要过后端 JSON Schema，
    // 多一个字段会被 DisallowUnknownFields 当场拒掉。
    expect(post.body.reason).toBe("季度结算，把冷钱包余额转回运营账户");
    expect(post.body.params).not.toHaveProperty("reason");
  });

  // 这一条是本片的靶心。旧实现（返回 { runId: "" }）下它必红：那时界面显示的是
  // 绿色的「已提交提现请求 · 这次响应里没有 action_run_id」，既没有单号，
  // 也没有「钱还没有转出」这句话。
  it("202 时显示审批单号与「钱还没有转出」，不显示成一次执行成功", async () => {
    renderPanel();
    await fillAndSubmit("季度结算，把冷钱包余额转回运营账户");

    expect(await screen.findByText(/提现已提交审批，钱还没有转出/)).toBeTruthy();
    expect(screen.getByText(`审批单号 ${APPROVAL_BODY.approval_request_id}`)).toBeTruthy();
    // 回执里**不能**出现 run_id 那一支的任何一句：这次调用根本没有 run。
    expect(screen.queryByText(/run_id/)).toBeNull();
    expect(screen.queryByText(/这次响应里没有 action_run_id/)).toBeNull();
    expect(screen.queryByText(/已提交提现请求/)).toBeNull();
    // 并且要告诉人下一步去哪。
    expect(screen.getByRole("link", { name: /待审批/ }).getAttribute("href")).toContain(
      "sub=pending",
    );
  });

  // 理由不是可选项：内核对 L2+ 的空 reason 直接回 INVALID_PARAMS。
  // 在前端拦住只是为了不浪费人一次点击——服务端仍会再判一次。
  it("理由为空时一个请求都不发", async () => {
    renderPanel();
    fireEvent.change(await screen.findByLabelText("金额"), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "提现" }));
    fireEvent.click(await screen.findByRole("button", { name: /提交提现审批/ }));

    expect(await screen.findByText(/请写清为什么要做这件事/)).toBeTruthy();
    expect(posts).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// 开卡（cards.card.issue@1，L2）
// ---------------------------------------------------------------------------

describe("开卡按钮（L2）", () => {
  it("理由发得出去，回执给的是审批单而不是 run_id", async () => {
    stubFetch();
    let received: ActionResult | null = null;
    wrap(
      <IssueCardDialog
        accounts={["LINFENG"]}
        memberEmails={["a@x.com"]}
        onIssued={(r) => {
          received = r;
        }}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "开卡" }));
    fireEvent.change(await screen.findByLabelText("充值金额"), { target: { value: "10.00" } });
    fireEvent.change(screen.getByLabelText("企业成员邮箱"), { target: { value: "a@x.com" } });
    fireEvent.change(screen.getByLabelText("持卡人姓名"), { target: { value: "Claude" } });
    fireEvent.change(screen.getByLabelText("理由"), {
      target: { value: "新同事入职，按标准配一张订阅卡" },
    });
    fireEvent.click(screen.getByRole("button", { name: "确认开卡" }));

    await waitFor(() => expect(posts.length).toBe(1));
    const post = executePost();
    expect(post.url).toContain("/api/v1/actions/cards.card.issue/versions/1/execute");
    expect(post.body.reason).toBe("新同事入职，按标准配一张订阅卡");

    await waitFor(() => expect(received).not.toBeNull());
    // 断言的是审批那一支的形状——旧实现给的是 { runId: "" }，取不到这两个值。
    expect(received!).toMatchObject({
      title: "开卡已提交审批，卡还没有开",
      approvalRequestId: APPROVAL_BODY.approval_request_id,
    });
    expect(received!).not.toHaveProperty("runId");
  });

  it("理由为空时一个请求都不发", async () => {
    stubFetch();
    wrap(<IssueCardDialog accounts={["LINFENG"]} memberEmails={[]} />);

    fireEvent.click(screen.getByRole("button", { name: "开卡" }));
    fireEvent.change(await screen.findByLabelText("充值金额"), { target: { value: "10.00" } });
    fireEvent.change(screen.getByLabelText("企业成员邮箱"), { target: { value: "a@x.com" } });
    fireEvent.change(screen.getByLabelText("持卡人姓名"), { target: { value: "Claude" } });
    fireEvent.click(screen.getByRole("button", { name: "确认开卡" }));

    expect(await screen.findByText(/请写清为什么要做这件事/)).toBeTruthy();
    expect(posts).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// 关停（cards.card.delete@1，L2）
// ---------------------------------------------------------------------------

describe("关停按钮（L2）", () => {
  const card = {
    account: "LINFENG",
    card_id: "c1",
    mask: "441357******9228",
    holder_name: "Claude",
    card_alias: "xm-1",
    status: "active",
    currency: "USD",
    balance_minor: 100,
    renewal_risk: "none",
    freshness: { synced_at: "2026-09-05T00:00:00Z", age_seconds: 10, stale: false, never_synced: false },
  };

  it("上膛后要写理由，写了才发，回执是审批单", async () => {
    stubFetch({ "/api/v1/cards": { items: [card], accounts: ["LINFENG"], member_emails: [] } });
    wrap(<CardWorkbench />, "/cards/LINFENG/c1");

    fireEvent.click(await screen.findByRole("button", { name: "关停" }));
    // 上膛之前不该有理由框——它属于这一次关停，不是常驻的表单项。
    const reasonBox = await screen.findByLabelText("理由");

    // 先不写理由点一下：一个请求都不该发。
    fireEvent.click(screen.getByRole("button", { name: "提交关停审批" }));
    expect(posts).toEqual([]);

    fireEvent.change(reasonBox, { target: { value: "持卡人离职，按流程回收" } });
    fireEvent.click(screen.getByRole("button", { name: "提交关停审批" }));

    await waitFor(() => expect(posts.length).toBe(1));
    const post = executePost();
    expect(post.url).toContain("/api/v1/actions/cards.card.delete/versions/1/execute");
    expect(post.body.reason).toBe("持卡人离职，按流程回收");

    expect(await screen.findByText(/关停已提交审批，卡还没有关停/)).toBeTruthy();
    expect(screen.getByText(`审批单号 ${APPROVAL_BODY.approval_request_id}`)).toBeTruthy();
    expect(screen.queryByText(/run_id/)).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// 买号（sms.number.purchase@1，L2）
// ---------------------------------------------------------------------------

describe("买号按钮（L2）", () => {
  it("理由发得出去，回执是审批单", async () => {
    stubFetch({
      "/api/v1/sms/providers": {
        items: [{ provider: "hero_sms", enabled: true, verified: true, supports_lifecycle: true }],
      },
    });
    wrap(<SMSPanel />);

    // 先等供应商到达：买号按钮在那之前是禁用的（`disabled={!usable}`），
    // 这时点它不会开对话框，而 findByRole 找得到禁用按钮，等不出这个差别。
    await screen.findAllByText("Hero-SMS");
    fireEvent.click(await screen.findByRole("button", { name: "买号" }));
    fireEvent.change(await screen.findByLabelText("服务代号"), { target: { value: "tg" } });
    fireEvent.change(screen.getByLabelText("国家代码"), { target: { value: "12" } });
    fireEvent.change(screen.getByLabelText("理由"), {
      target: { value: "客服线新开的 TG 账号要收验证码" },
    });
    // 对话框里那个「买号」是上膛按钮（第一次点只生成幂等键）。
    const armButtons = screen.getAllByRole("button", { name: "买号" });
    fireEvent.click(armButtons[armButtons.length - 1]!);
    fireEvent.click(await screen.findByRole("button", { name: /提交买 1 个号的审批/ }));

    await waitFor(() => expect(posts.length).toBe(1));
    const post = executePost();
    expect(post.url).toContain("/api/v1/actions/sms.number.purchase/versions/1/execute");
    expect(post.body.reason).toBe("客服线新开的 TG 账号要收验证码");

    expect(await screen.findByText(/买号已提交审批，还没有下单/)).toBeTruthy();
    expect(screen.getByText(`审批单号 ${APPROVAL_BODY.approval_request_id}`)).toBeTruthy();
    expect(screen.queryByText(/run_id/)).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// 租号（sms.rent.purchase@1，L2）
// ---------------------------------------------------------------------------

describe("租号按钮（L2）", () => {
  it("理由发得出去，回执是审批单", async () => {
    stubFetch();
    let received: ActionResult | null = null;
    wrap(
      <HeroRentPanel
        onWrite={(r) => {
          received = r;
        }}
      />,
    );

    fireEvent.change(screen.getByLabelText("租用服务代号"), { target: { value: "go" } });
    fireEvent.change(screen.getByLabelText("租用国家代码"), { target: { value: "12" } });
    fireEvent.change(screen.getByLabelText("租用小时数"), { target: { value: "4" } });
    fireEvent.change(screen.getByLabelText("理由"), {
      target: { value: "给新接的注册流程压测收码链路" },
    });
    fireEvent.click(screen.getByRole("button", { name: "下一步" }));
    fireEvent.click(await screen.findByRole("button", { name: /提交租 4 小时的审批/ }));

    await waitFor(() => expect(posts.length).toBe(1));
    const post = executePost();
    expect(post.url).toContain("/api/v1/actions/sms.rent.purchase/versions/1/execute");
    expect(post.body.reason).toBe("给新接的注册流程压测收码链路");

    await waitFor(() => expect(received).not.toBeNull());
    expect(received!).toMatchObject({
      title: "租号已提交审批，还没有下单",
      approvalRequestId: APPROVAL_BODY.approval_request_id,
    });
  });

  it("理由为空时一个请求都不发", async () => {
    stubFetch();
    wrap(<HeroRentPanel onWrite={vi.fn()} />);

    fireEvent.change(screen.getByLabelText("租用服务代号"), { target: { value: "go" } });
    fireEvent.change(screen.getByLabelText("租用国家代码"), { target: { value: "12" } });
    fireEvent.change(screen.getByLabelText("租用小时数"), { target: { value: "4" } });
    fireEvent.click(screen.getByRole("button", { name: "下一步" }));
    fireEvent.click(await screen.findByRole("button", { name: /提交租 4 小时的审批/ }));

    expect(await screen.findByText(/请写清为什么要做这件事/)).toBeTruthy();
    expect(posts).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// 买邮箱（sms.email.purchase@1，L2）
// ---------------------------------------------------------------------------

describe("买邮箱按钮（L2）", () => {
  it("理由发得出去，回执是审批单", async () => {
    stubFetch({
      "/api/v1/sms/hero/email-domains": {
        items: [{ name: "mail.example.com", cost_text: "0.20", count: 9 }],
      },
    });
    let received: ActionResult | null = null;
    wrap(
      <HeroEmailsPanel
        onWrite={(r) => {
          received = r;
        }}
      />,
    );

    fireEvent.click(await screen.findByRole("button", { name: "买邮箱" }));
    fireEvent.change(await screen.findByLabelText("站点"), { target: { value: "example.com" } });
    fireEvent.change(screen.getByLabelText("理由"), {
      target: { value: "批量注册测试账号，需要一次性邮箱" },
    });

    // 域名是 Radix Select，选项在 Portal 里；这一档测试的靶心是理由与 202，
    // 不是下拉本身，所以直接用「下一步」是否可点来确认表单已就绪。
    const domainSelect = screen.getByLabelText("域名");
    fireEvent.keyDown(domainSelect, { key: "Enter" });
    const option = await screen.findByText(/mail\.example\.com/);
    fireEvent.click(option);

    fireEvent.click(await screen.findByRole("button", { name: "下一步" }));
    fireEvent.click(await screen.findByRole("button", { name: /提交买 1 个邮箱的审批/ }));

    await waitFor(() => expect(posts.length).toBe(1));
    const post = executePost();
    expect(post.url).toContain("/api/v1/actions/sms.email.purchase/versions/1/execute");
    expect(post.body.reason).toBe("批量注册测试账号，需要一次性邮箱");

    await waitFor(() => expect(received).not.toBeNull());
    expect(received!).toMatchObject({
      title: "买邮箱已提交审批，还没有下单",
      approvalRequestId: APPROVAL_BODY.approval_request_id,
    });
  });
});

// ---------------------------------------------------------------------------
// L0/L1 的调用点意外收到 202
//
// 这些按钮**没有**填理由的入口，也没有承接单号的地方——它们走的是
// `executeAction`，拿到审批受理时会抛 `ApprovalRequiredError`。
// 这一组钉住的正是那个设计的价值：**响亮地坏，而不是假装成功、也不是卡住**。
// 以后哪个 L0/L1 动作被提级而没人来改前端时，先炸的就是这条路径。
//
// 两条覆盖管理端仅有的两种写错误处理形态：
//   A. `useMutation({ onError: setError })` + `<ActionErrorNote error={error} />`
//   B. 不写 onError，直接 `<ActionErrorNote error={mutation.error} />`
// 两者最终都汇进 ActionErrorNote 的**非 ApiError** 分支
// （ApprovalRequiredError 继承 Error，不是 ApiError）。
// ---------------------------------------------------------------------------

describe("L0/L1 按钮意外收到 202", () => {
  const openAlert: AlertItem = {
    id: "al-1",
    rule_key: "runway.low",
    dedup_key: "runway.low:prod",
    severity: "warning",
    status: "OPEN",
    title: "余额可用天数偏低",
    detail: "",
    environment: "prod",
    source_metric_key: "",
    opened_at: "2026-09-07T00:00:00Z",
    last_seen_at: "2026-09-07T00:00:00Z",
    acknowledged_at: null,
    resolved_at: null,
    fire_count: 1,
    notify_status: "delivered",
    notify_error: "",
    notified_at: null,
  };

  // 形态 B：确认告警（alerts.alert.acknowledge@1，L0）。
  it("抛出并就地显示服务端原话，不回调成功、不停在 loading", async () => {
    stubFetch();
    const onAcknowledged = vi.fn();
    wrap(<AcknowledgeAlertButton alert={openAlert} onAcknowledged={onAcknowledged} />);

    const button = screen.getByRole("button", { name: "确认" });
    fireEvent.click(button);

    // 服务端那句人话被原样显示出来，而不是被吞成「未知错误」。
    // 逐字断言：只断言「有个 alert」的话，任何一句泛型报错都能让它绿。
    const alertNode = await screen.findByRole("alert");
    expect(alertNode.textContent).toContain(APPROVAL_BODY.message);
    // 没有把它当成一次成功。
    expect(onAcknowledged).not.toHaveBeenCalled();
    // 也没有卡在 loading：Button 的 loading 会同时置 disabled 与 aria-busy，
    // 两个都要查——只查 disabled 的话，一个「按钮可点但转圈不停」的界面照样绿。
    expect(button.getAttribute("aria-busy")).toBeNull();
    expect((button as HTMLButtonElement).disabled).toBe(false);
  });

  // 形态 A：登记提现地址（cards.withdraw.address.register@1，L1）。
  // 与「提现」在同一个面板里，正好对照：同一屏上一个是 L3（有理由框、
  // 落单给回执），一个是 L1（没有理由框，拿到 202 就该炸）。
  it("同一面板里的 L1 入口不给理由框，收到 202 时报错而不是出回执", async () => {
    stubFetch({
      "/api/v1/cards/withdraw/addresses": {
        items: [
          {
            address_id: "a1",
            account: "MAIN",
            chain: "TRON",
            address: "TCold1111111111111111111111111111",
            label: "冷钱包",
            enabled: true,
          },
        ],
      },
    });
    wrap(<WithdrawPanel accounts={["MAIN"]} />);

    fireEvent.click(await screen.findByRole("button", { name: "登记地址" }));
    const dialog = await screen.findByRole("dialog");
    // L1 不需要理由：这个对话框里**不该**有理由框。删掉这一格实现时它会红，
    // 因为下面紧接着就在同一个对话框里做了正向断言（地址输入框在）。
    expect(within(dialog).getByLabelText("地址")).toBeTruthy();
    expect(within(dialog).queryByLabelText("理由")).toBeNull();

    fireEvent.change(within(dialog).getByLabelText("地址"), {
      target: { value: "TNew2222222222222222222222222222" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "登记" }));

    await waitFor(() => expect(posts.length).toBe(1));
    expect(executePost().url).toContain(
      "/api/v1/actions/cards.withdraw.address.register/versions/1/execute",
    );

    // 报错出来了……
    expect((await screen.findByRole("alert")).textContent).toContain(APPROVAL_BODY.message);
    // ……而且**没有**出现任何回执：既不是执行成功那一支，也不是审批那一支。
    // 后者尤其重要：审批回执只该长在准备好了的调用点上。
    expect(screen.queryByText("已登记提现地址")).toBeNull();
    expect(screen.queryByText(new RegExp(`审批单号 ${APPROVAL_BODY.approval_request_id}`))).toBeNull();
  });
});
