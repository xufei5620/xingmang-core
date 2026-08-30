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

  it("改角色：提交调用 staff.account.set_roles@1，参数是逗号拼接的角色集合", async () => {
    const fetchMock = handler();
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();
    await screen.findByText("alice");

    fireEvent.click(screen.getByRole("button", { name: "修改 alice 的角色" }));
    fireEvent.click(screen.getByLabelText("审计"));
    fireEvent.click(screen.getByRole("button", { name: "保存" }));

    await waitFor(() => expect(postCallTo(fetchMock, "staff.account.set_roles")).toBeTruthy());
    const [url, init] = postCallTo(fetchMock, "staff.account.set_roles");
    expect(url).toContain("/api/v1/actions/staff.account.set_roles/versions/1/execute");
    expect(JSON.parse(init.body as string)).toEqual({
      params: { username: "alice", roles: "staff,admin,auditor" },
    });
  });

  it("不能禁用自己当前登录使用的账号", async () => {
    setCachedLocalUser({ username: "alice", display_name: "Alice", roles: ["admin"], must_change_password: false });
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
