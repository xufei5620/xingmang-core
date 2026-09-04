import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { setCachedLocalUser } from "../auth/localSession";
import { StaffAccountsPanel } from "./StaffAccountsPanel";

function response(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

const aliceRow = {
  username: "alice",
  display_name: "Alice",
  roles: ["staff", "admin"],
  disabled: false,
  must_change_password: false,
  locked_until: null,
  last_login_at: "2026-08-28T03:00:00Z",
  created_at: "2026-01-01T00:00:00Z",
  totp_enrolled: false,
  must_enroll_totp: false,
  totp_enrolled_at: null,
};

const bobEnrolledRow = {
  username: "bob",
  display_name: "Bob",
  roles: ["admin"],
  disabled: false,
  must_change_password: false,
  locked_until: null,
  last_login_at: null,
  created_at: "2026-01-02T00:00:00Z",
  totp_enrolled: true,
  must_enroll_totp: false,
  totp_enrolled_at: "2026-08-01T00:00:00Z",
};

function renderPanel() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <StaffAccountsPanel />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function handler({
  rows = [aliceRow],
  createResult = { action_run_id: "run-create-1", result: { initial_password: "Gk8x!fQ2mZ7p" } },
  setRolesResult = { action_run_id: "run-roles-1", result: {} },
}: {
  rows?: unknown[];
  createResult?: Record<string, unknown>;
  setRolesResult?: Record<string, unknown>;
} = {}) {
  return vi.fn((url: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      if (url.includes("staff.account.create")) return Promise.resolve(response(createResult));
      if (url.includes("staff.account.set_roles")) return Promise.resolve(response(setRolesResult));
      return Promise.resolve(response({ action_run_id: "run-x", result: {} }));
    }
    if (url.startsWith("/api/v1/staff/accounts")) return Promise.resolve(response({ items: rows }));
    return Promise.resolve(response({ items: [] }));
  });
}

function postCallTo(fetchMock: ReturnType<typeof vi.fn>, urlSubstring: string): [string, RequestInit] {
  const call = fetchMock.mock.calls.find(
    ([url, init]) => (init as RequestInit | undefined)?.method === "POST" && (url as string).includes(urlSubstring),
  );
  return call as unknown as [string, RequestInit];
}

describe("人员与权限 · 账号与身份", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    setCachedLocalUser(null);
  });

  it("列表展示用户名、显示名、角色、状态与最后登录", async () => {
    vi.stubGlobal("fetch", handler());
    renderPanel();

    const table = within(await screen.findByRole("table"));
    expect(table.getByText("alice")).toBeTruthy();
    expect(table.getByText("Alice")).toBeTruthy();
    expect(table.getByText("员工")).toBeTruthy();
    expect(table.getByText("管理员")).toBeTruthy();
    expect(table.getByText("正常")).toBeTruthy();
    expect(table.getByText(/2026-08-28 03:00:00 UTC/)).toBeTruthy();
  });

  it("空列表显示诚实占位，而不是一张空表", async () => {
    vi.stubGlobal("fetch", handler({ rows: [] }));
    renderPanel();
    expect(await screen.findByText("暂无账号")).toBeTruthy();
  });

  it("新建账号：留空初始密码，服务端生成的密码在对话框内显示一次并可复制", async () => {
    vi.stubGlobal("fetch", handler({ rows: [] }));
    renderPanel();
    await screen.findByText("暂无账号");

    fireEvent.click(screen.getByRole("button", { name: "新建账号" }));
    fireEvent.change(screen.getByLabelText(/用户名/), { target: { value: "bob" } });
    fireEvent.change(screen.getByLabelText(/显示名/), { target: { value: "Bob" } });
    fireEvent.click(screen.getByLabelText("员工"));
    fireEvent.click(screen.getByRole("button", { name: "创建" }));

    expect(await screen.findByText("Gk8x!fQ2mZ7p")).toBeTruthy();
    expect(screen.getByText(/只会显示这一次/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "复制" })).toBeTruthy();
    // 密码不会被悄悄存进表单或以别的形式留在页面上——只在这一处出现
    expect(screen.queryByLabelText("初始密码")).toBeNull();
  });

  it("创建表单校验：用户名/显示名/角色任一缺失就不提交", async () => {
    const fetchMock = handler({ rows: [] });
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();
    await screen.findByText("暂无账号");

    fireEvent.click(screen.getByRole("button", { name: "新建账号" }));
    fireEvent.click(screen.getByRole("button", { name: "创建" }));

    expect(await screen.findByText(/还有 \d+ 处需要修正/)).toBeTruthy();
    expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === "POST")).toBe(
      false,
    );
  });

  it("两步验证列展示状态，且只有已启用/待启用的账号才出示「重置 TOTP」按钮", async () => {
    vi.stubGlobal("fetch", handler({ rows: [aliceRow, bobEnrolledRow] }));
    renderPanel();
    await screen.findByText("alice");

    const table = within(screen.getByRole("table"));
    expect(table.getByText("未启用")).toBeTruthy();
    expect(table.getByText("已启用")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "重置 alice 的两步验证" })).toBeNull();
    expect(screen.getByRole("button", { name: "重置 bob 的两步验证" })).toBeTruthy();
  });

  it("重置 TOTP：确认后调用 staff.account.reset_totp@1，参数是目标用户名", async () => {
    const fetchMock = handler({ rows: [bobEnrolledRow] });
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();
    await screen.findByText("bob");

    fireEvent.click(screen.getByRole("button", { name: "重置 bob 的两步验证" }));
    fireEvent.click(screen.getByRole("button", { name: "确认重置" }));

    await waitFor(() => expect(postCallTo(fetchMock, "staff.account.reset_totp")).toBeTruthy());
    const [url, init] = postCallTo(fetchMock, "staff.account.reset_totp");
    expect(url).toContain("/api/v1/actions/staff.account.reset_totp/versions/1/execute");
    expect(JSON.parse(init.body as string)).toEqual({ params: { username: "bob" } });
  });

  it("改角色：提交调用 staff.account.set_roles@1，参数是逗号拼接的角色集合", async () => {
    const fetchMock = handler();
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();
    await screen.findByText("alice");

    fireEvent.click(screen.getByRole("button", { name: "修改 alice 的角色" }));
    // 用 credential-admin 而不是原先的 auditor：auditor 是**后端不认识**的
    // 幽灵角色，用它做断言等于把一个必然被拒的请求固化成"期望行为"。
    fireEvent.click(screen.getByLabelText("凭据管理员"));
    fireEvent.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() => expect(postCallTo(fetchMock, "staff.account.set_roles")).toBeTruthy());
    const [url, init] = postCallTo(fetchMock, "staff.account.set_roles");
    expect(url).toContain("/api/v1/actions/staff.account.set_roles/versions/1/execute");
    expect(JSON.parse(init.body as string)).toEqual({
      params: { username: "alice", roles: "staff,admin,credential-admin" },
    });
  });

  // 账号持有的、目录里没有的角色**必须显示出来**，否则它取消不掉。
  //
  // 2026-09-05 生产上卡住的就是这个：admin 账号带着一个 auditor（后端已经
  // 不认识它了），复选框只渲染目录里的项，于是那个角色看不见、勾不掉，
  // 却仍然被整体提交回去——每次保存都报「未知角色 auditor」，
  // 这个账号的角色再也改不动。
  it("改角色：账号持有的未知角色也要能看见并取消", async () => {
    const fetchMock = handler({
      rows: [{ ...aliceRow, roles: ["staff", "admin", "auditor"] }],
    });
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();
    await screen.findByText("alice");

    fireEvent.click(screen.getByRole("button", { name: "修改 alice 的角色" }));

    // 看得见：以内部名显示，并标注它已不在目录里。
    const ghost = screen.getByLabelText(/auditor/) as HTMLInputElement;
    expect(ghost.checked).toBe(true);

    // 取消得掉，且提交出去的集合里不再有它。
    fireEvent.click(ghost);
    fireEvent.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() => expect(postCallTo(fetchMock, "staff.account.set_roles")).toBeTruthy());
    const [, init] = postCallTo(fetchMock, "staff.account.set_roles");
    expect(JSON.parse(init.body as string)).toEqual({
      params: { username: "alice", roles: "staff,admin" },
    });
  });

  it("不能禁用自己当前登录使用的账号", async () => {
    setCachedLocalUser({
      username: "alice",
      display_name: "Alice",
      roles: ["admin"],
      must_change_password: false,
      totp_enrolled: false,
      must_enroll_totp: false,
      totp_enrolled_at: null,
      recovery_codes_remaining: null,
    });
    vi.stubGlobal("fetch", handler());
    renderPanel();
    await screen.findByText("alice");

    const disableButton = screen.getByRole("button", { name: "禁用 alice" }) as HTMLButtonElement;
    expect(disableButton.disabled).toBe(true);
  });

  it("禁用其他账号：调用 staff.account.set_disabled@1，disabled:true", async () => {
    const fetchMock = handler();
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();
    await screen.findByText("alice");

    fireEvent.click(screen.getByRole("button", { name: "禁用 alice" }));

    await waitFor(() => expect(postCallTo(fetchMock, "staff.account.set_disabled")).toBeTruthy());
    const [, init] = postCallTo(fetchMock, "staff.account.set_disabled");
    expect(JSON.parse(init.body as string)).toEqual({ params: { username: "alice", disabled: true } });
  });
});
