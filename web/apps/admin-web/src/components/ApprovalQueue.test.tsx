import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApprovalQueue } from "./ApprovalQueue";

function fakeResponse(body: unknown, status = 200): Response {
  return {
    ok: status < 400,
    status,
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(typeof body === "string" ? body : JSON.stringify(body)),
  } as unknown as Response;
}

/** chi 对没有挂载的路由回**纯文本** 404，解析不出 `error.code`——
 *  api/client.ts 的 looksLikeUnmountedRoute 正是靠这层结构差异判「整组端点
 *  不存在」。这里逐字模拟它，否则测的就不是真实的未启用形态。 */
function bareNotFound(): Response {
  return {
    ok: false,
    status: 404,
    json: () => Promise.reject(new Error("not json")),
    text: () => Promise.resolve("404 page not found"),
  } as unknown as Response;
}

const NOW = new Date("2026-09-07T10:00:00Z");

function approval(overrides: Record<string, unknown> = {}) {
  return {
    id: "11111111-2222-3333-4444-555555555555",
    action_id: "registry.connection.set_status",
    action_version: "1",
    risk_level: "L3",
    params: { connection_id: "c-1", status: "ACTIVE" },
    params_hash: "sha256:abc",
    requester_id: "staff_bob",
    requester_type: "HUMAN",
    reason: "上游换域名，需要重新登记",
    status: "PENDING",
    created_at: "2026-09-07T08:00:00Z",
    expires_at: "2026-09-08T08:00:00Z",
    decisions: [],
    votes_required: 2,
    votes_cast: 0,
    privileged_vote_required: false,
    privileged_vote_cast: false,
    ...overrides,
  };
}

function listBody(items: unknown[], extra: Record<string, unknown> = {}) {
  return { items, limit: 50, truncated: false, ...extra };
}

function renderQueue() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <ApprovalQueue />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("审批队列", () => {
  beforeEach(() => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(NOW);
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("按风险等级分组，L4 在 L3 前面", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          fakeResponse(
            listBody([
              approval({ id: "a", risk_level: "L3", action_id: "registry.connector.create" }),
              approval({ id: "b", risk_level: "L4", action_id: "ops.killswitch.pull" }),
            ]),
          ),
        ),
      ),
    );
    renderQueue();
    await screen.findByText("ops.killswitch.pull@1");
    const headings = screen.getAllByRole("heading", { level: 3 }).map((h) => h.textContent ?? "");
    expect(headings[0]).toContain("L4");
    expect(headings[1]).toContain("L3");
  });

  it("显示理由、提交人与「还差几票」", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(listBody([approval({ votes_cast: 1 })])))),
    );
    renderQueue();
    expect(await screen.findByText("上游换域名，需要重新登记")).not.toBeNull();
    expect(screen.getByText(/还差 1 票/)).not.toBeNull();
    expect(screen.getByText(/staff_bob/)).not.toBeNull();
  });

  // ExpirePending 定时任务在 XM-0030c 之前没人调，库里会一直停在 PENDING。
  // 界面照抄 status 的话，队列里混着一批谁也批不动的单。
  it("库里说 PENDING 但已过期时显示为已过期，并说明库中仍记为待审批", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          fakeResponse(listBody([approval({ expires_at: "2026-09-07T09:00:00Z" })])),
        ),
      ),
    );
    renderQueue();
    expect(await screen.findByText("已过期")).not.toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "详情" }));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByText(/库中仍记为待审批/)).not.toBeNull();
    // 过期的单不给投票按钮——服务端也会拒。
    expect(dialog.queryByRole("button", { name: "同意" })).toBeNull();
  });

  it("详情里列出冻结的参数与 params_hash", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(listBody([approval()])))),
    );
    renderQueue();
    fireEvent.click(await screen.findByRole("button", { name: "详情" }));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByText("connection_id")).not.toBeNull();
    expect(dialog.getByText("c-1")).not.toBeNull();
    expect(dialog.getByText(/sha256:abc/)).not.toBeNull();
  });

  it("投票把 verdict 与意见发给 decide 端点", async () => {
    const fetchMock = vi.fn((url: string, init?: RequestInit) => {
      if (init?.method === "POST") return Promise.resolve(fakeResponse(approval()));
      return Promise.resolve(fakeResponse(listBody([approval()])));
    });
    vi.stubGlobal("fetch", fetchMock);
    renderQueue();
    fireEvent.click(await screen.findByRole("button", { name: "详情" }));
    const dialog = within(await screen.findByRole("dialog"));

    fireEvent.change(dialog.getByRole("textbox"), { target: { value: "核对过参数" } });
    fireEvent.click(dialog.getByRole("button", { name: "同意" }));

    await waitFor(() => {
      const post = fetchMock.mock.calls.find(
        ([, init]) => (init as RequestInit | undefined)?.method === "POST",
      );
      expect(post).toBeDefined();
      expect(post?.[0] as string).toContain(
        "/api/v1/approvals/11111111-2222-3333-4444-555555555555/decide",
      );
      expect(JSON.parse((post?.[1] as RequestInit).body as string)).toEqual({
        verdict: "APPROVE",
        comment: "核对过参数",
      });
    });
  });

  it("只有已批准的单给执行按钮，且执行不带参数", async () => {
    const fetchMock = vi.fn((url: string, init?: RequestInit) => {
      if (init?.method === "POST") return Promise.resolve(fakeResponse({ action_run_id: "run-1" }));
      return Promise.resolve(fakeResponse(listBody([approval({ status: "APPROVED", votes_cast: 2 })])));
    });
    vi.stubGlobal("fetch", fetchMock);
    renderQueue();
    fireEvent.click(await screen.findByRole("button", { name: "详情" }));
    const dialog = within(await screen.findByRole("dialog"));

    // 已批准的单不再收票。
    expect(dialog.queryByRole("button", { name: "同意" })).toBeNull();
    fireEvent.click(dialog.getByRole("button", { name: "执行" }));

    await waitFor(() => {
      const post = fetchMock.mock.calls.find(
        ([, init]) => (init as RequestInit | undefined)?.method === "POST",
      );
      expect(post?.[0] as string).toContain("/execute");
      // 参数在审批那一刻就冻结了；执行请求带一份参数会让人误以为界面能改它。
      expect(JSON.parse((post?.[1] as RequestInit).body as string)).toEqual({});
    });
  });

  it("待审批的单不给执行按钮", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(listBody([approval()])))),
    );
    renderQueue();
    fireEvent.click(await screen.findByRole("button", { name: "详情" }));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.queryByRole("button", { name: "执行" })).toBeNull();
    // 对照：同一个对话框里投票按钮是在的——确认缺的是「执行」而不是整块没渲染。
    expect(dialog.getByRole("button", { name: "同意" })).not.toBeNull();
  });

  it("服务端拒绝时就地显示原因与所缺权限，队列不被替换成错误页", async () => {
    const fetchMock = vi.fn((url: string, init?: RequestInit) => {
      if (init?.method === "POST") {
        return Promise.resolve(
          fakeResponse(
            { error: { code: "PERMISSION_DENIED", message: "缺少权限 approval.decide", request_id: "req-9" } },
            403,
          ),
        );
      }
      return Promise.resolve(fakeResponse(listBody([approval()])));
    });
    vi.stubGlobal("fetch", fetchMock);
    renderQueue();
    fireEvent.click(await screen.findByRole("button", { name: "详情" }));
    const dialog = within(await screen.findByRole("dialog"));
    fireEvent.click(dialog.getByRole("button", { name: "同意" }));

    expect(await dialog.findByRole("alert")).not.toBeNull();
    expect(dialog.getByText(/缺少权限 approval.decide/)).not.toBeNull();
    expect(dialog.getByText(/req-9/)).not.toBeNull();
    // 写失败必须把内容留在原地——单还在，人还要继续处理它。
    expect(dialog.getByText("上游换域名，需要重新登记")).not.toBeNull();
  });

  // 端点整组没挂载时说清是**后端版本旧了**，不是一个泛型报错。这条同时钉住
  // api/approvals.ts 把裸 404 翻成 FeatureNotMountedError 这件事。
  // 口径随 XM-0030-ENABLE 改过：审批服务现在是无条件注入的，所以 404 已经
  // 不再是「等启用」，而是这套前端在对一个启用之前的旧 platform-api 说话。
  it("审批中心端点整组 404 时说明原因，而不是显示报错", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(bareNotFound())));
    renderQueue();
    expect(await screen.findByText(/还没有审批中心/)).not.toBeNull();
    // 逐字钉住指向后端版本的那句：只断言「有说明」不够，说错方向的说明
    // 会把人支去等一个不会再来的排期。
    expect(
      screen.getByText(/请确认 platform-api 已滚到含该变更的版本，而不是等待排期/),
    ).not.toBeNull();
    // 版本不对不给重试按钮：再点一次也不会让旧后端长出这组路由。
    expect(screen.queryByRole("button", { name: "重试" })).toBeNull();
  });

  it("取满上限时说这一屏可能不是全部", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(fakeResponse(listBody([approval()], { limit: 1, truncated: true }))),
      ),
    );
    renderQueue();
    expect(await screen.findByText(/已取满 1 条，这一屏可能不是全部/)).not.toBeNull();
  });

  it("没取满就不说被截断", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(listBody([approval()])))),
    );
    renderQueue();
    await screen.findByText("上游换域名，需要重新登记");
    expect(screen.queryByText(/已取满/)).toBeNull();
  });

  it("队列为空时说清楚 L0/L1 不经过审批", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(listBody([])))),
    );
    renderQueue();
    expect(await screen.findByText("没有待审批的动作")).not.toBeNull();
    expect(screen.getByText(/L0\/L1 直接执行/)).not.toBeNull();
  });

  it("默认只查待审批", async () => {
    const fetchMock = vi.fn((_url: string, _init?: RequestInit) =>
      Promise.resolve(fakeResponse(listBody([approval()]))),
    );
    vi.stubGlobal("fetch", fetchMock);
    renderQueue();
    await screen.findByText("上游换域名，需要重新登记");
    const urls = fetchMock.mock.calls.map(([url]) => url);
    expect(urls.some((u) => u.includes("status=PENDING"))).toBe(true);
    // 对照：默认这一次**不该**带别的状态——否则「默认只查待审批」就是空话。
    expect(urls.some((u) => u.includes("status=EXECUTED"))).toBe(false);
  });

  it("切换筛选会用新的 status 重新取数", async () => {
    const fetchMock = vi.fn((_url: string, _init?: RequestInit) =>
      Promise.resolve(fakeResponse(listBody([approval({ status: "EXECUTED" })]))),
    );
    vi.stubGlobal("fetch", fetchMock);
    renderQueue();
    await screen.findByText("上游换域名，需要重新登记");

    // Select 是 Radix 的，展开后选项才在 DOM 里；这里直接触发它的 onValueChange
    // 走不通，改用键盘打开再选——与 primitives 的真实交互一致。
    const trigger = screen.getByRole("combobox", { name: "按状态筛选审批单" });
    fireEvent.keyDown(trigger, { key: "Enter" });
    fireEvent.click(await screen.findByRole("option", { name: "已执行" }));

    await waitFor(() => {
      const urls = fetchMock.mock.calls.map(([url]) => url);
      expect(urls.some((u) => u.includes("status=EXECUTED"))).toBe(true);
    });
  });
  // --- 中文对照（XM-I18N-LABELS）-------------------------------------------

  it("风险等级徽章带中文，原始等级串仍逐字在最前面", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(listBody([approval({ risk_level: "L4" })])))),
    );
    renderQueue();
    await screen.findByText("上游换域名，需要重新登记");

    // 「L4」三个字符说不出这一级有多重；中文补上，但 L4 本身一个字符不改——
    // 运维查 ADR-003、跟人开口说的都是那个串。
    const badge = screen.getByText("L4 最高风险");
    expect(badge.textContent).toContain("L4");
    // 票数、能不能自批、多久过期挂在悬停里（数字来自 approval.DefaultPolicy()）。
    expect(badge.getAttribute("title")).toMatch(/2 票/);
    expect(badge.getAttribute("title")).toMatch(/4 小时/);
  });

  it("认不出来的风险等级原样显示，不兜底成某个已知等级", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(listBody([approval({ risk_level: "L9" })])))),
    );
    renderQueue();
    // 先拿到正向锚点：卡片确实渲染出来了，下面的缺席断言才有意义。
    await screen.findByText("上游换域名，需要重新登记");

    const heading = screen.getAllByRole("heading", { level: 3 })[0];
    expect(heading?.textContent).toContain("L9");
    // 缺席：不能给它安上任何一个已知等级的中文名——把一个更危险的新等级
    // 显示成较轻的那个，比不翻译危险得多。
    expect(heading?.textContent).not.toMatch(/最低风险|低风险|中风险|高风险|最高风险/);
  });

  it("提交人类别有中文，原码留在括号里", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(listBody([approval({ requester_type: "HUMAN" })])))),
    );
    renderQueue();
    await screen.findByText("上游换域名，需要重新登记");
    fireEvent.click(screen.getByRole("button", { name: "详情" }));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByText("人（HUMAN）")).not.toBeNull();
  });

  it("状态徽章与筛选下拉出自同一份对照表：徽章「已批准」，下拉「已批准（待执行）」", async () => {
    // 这两处曾经各自写死一份，措辞已经分叉。现在限定语由对照表的 note 承载，
    // 徽章取短名、下拉取全名——**同一条记录的两种渲染**，不是两份表。
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(listBody([approval({ status: "APPROVED" })])))),
    );
    renderQueue();
    await screen.findByText("上游换域名，需要重新登记");

    const badge = screen.getAllByText("已批准")[0];
    expect(badge).toBeDefined();
    // 「已批准」很容易被读成「这件事做完了」，所以悬停必须说清动作还没发生。
    expect(badge?.getAttribute("title")).toMatch(/还没有发生/);

    const trigger = screen.getByRole("combobox", { name: "按状态筛选审批单" });
    fireEvent.keyDown(trigger, { key: "Enter" });
    expect(await screen.findByRole("option", { name: "已批准（待执行）" })).not.toBeNull();
  });
});
