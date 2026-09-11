import { fireEvent, render, screen } from "@testing-library/react";
import { RouterProvider, createMemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { devLogout, isAuthenticated } from "../auth/devSession";
import { LoginPage } from "./LoginPage";

function renderLogin(search = "") {
  const router = createMemoryRouter(
    [
      { path: "/login", Component: LoginPage },
      { path: "/alerts", element: <div>告警页</div> },
      { path: "/dashboard", element: <div>工作台</div> },
      { path: "/account/password", element: <div>改密页</div> },
    ],
    { initialEntries: [`/login${search}`] },
  );
  render(<RouterProvider router={router} />);
  return router;
}

/** local 模式登录请求的最小 fake fetch：只认 /api/v1/auth/login。 */
function stubLoginFetch(status: number, body: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn(() =>
      Promise.resolve({
        ok: status >= 200 && status < 300,
        status,
        json: () => Promise.resolve(body),
      }),
    ),
  );
}

describe("登录页", () => {
  beforeEach(() => { devLogout(); window.__XM_CONFIG__ = {authMode:"dev-header"}; });
  afterEach(() => {
    delete window.__XM_CONFIG__;
    vi.unstubAllGlobals();
    devLogout();
  });

  describe("dev-header 模式（现状不变）", () => {
    it("如实说明无需登录，按钮仍是「开发模式进入」", () => {
      renderLogin();
      expect(screen.getByText(/开发模式：无需登录/)).not.toBeNull();
      expect(screen.getByRole("button", { name: "开发模式进入" })).not.toBeNull();
      expect(screen.queryByRole("button", { name: "使用 solov 账号登录" })).toBeNull();
    });

    it("进入后回到 next 指定的页面", async () => {
      renderLogin("?next=%2Falerts");
      fireEvent.click(screen.getByRole("button", { name: "开发模式进入" }));
      expect(await screen.findByText("告警页")).not.toBeNull();
      expect(isAuthenticated()).toBe(true);
    });

    it("next 不是站内路径就回工作台", async () => {
      renderLogin("?next=https%3A%2F%2Fevil.example%2F");
      fireEvent.click(screen.getByRole("button", { name: "开发模式进入" }));
      expect(await screen.findByText("工作台")).not.toBeNull();
    });
  });

  describe("local 模式（XM-LOGIN 自带账号登录）", () => {
    beforeEach(() => {
      window.__XM_CONFIG__ = { authMode: "local" };
    });

    it("渲染用户名/密码表单，没有 oidc 或开发模式的入口", () => {
      renderLogin();
      expect(screen.getByLabelText(/用户名/)).not.toBeNull();
      expect(screen.getByLabelText(/密码/)).not.toBeNull();
      expect(screen.queryByRole("button", { name: "使用 solov 账号登录" })).toBeNull();
      expect(screen.queryByRole("button", { name: "开发模式进入" })).toBeNull();
    });

    it("登录成功（cookie 会话）：POST 带 credentials:same-origin，成功后回到 next", async () => {
      stubLoginFetch(200, {
        username: "alice",
        display_name: "Alice",
        roles: ["staff"],
        must_change_password: false,
      });
      renderLogin("?next=%2Falerts");
      fireEvent.change(screen.getByLabelText(/用户名/), { target: { value: "alice" } });
      fireEvent.change(screen.getByLabelText(/密码/), { target: { value: "hunter2000x" } });
      fireEvent.click(screen.getByRole("button", { name: "登录" }));

      expect(await screen.findByText("告警页")).not.toBeNull();
      const fetchMock = globalThis.fetch as unknown as ReturnType<typeof vi.fn>;
      const call = fetchMock.mock.calls[0] as [string, RequestInit];
      expect(call[0]).toContain("/api/v1/auth/login");
      expect(call[1].credentials).toBe("same-origin");
      expect(JSON.parse(call[1].body as string)).toEqual({
        username: "alice",
        password: "hunter2000x",
      });
    });

    it("must_change_password 为真：登录成功后去改密页，而不是 next", async () => {
      stubLoginFetch(200, {
        username: "alice",
        display_name: "Alice",
        roles: ["staff"],
        must_change_password: true,
      });
      renderLogin("?next=%2Falerts");
      fireEvent.change(screen.getByLabelText(/用户名/), { target: { value: "alice" } });
      fireEvent.change(screen.getByLabelText(/密码/), { target: { value: "hunter2000x" } });
      fireEvent.click(screen.getByRole("button", { name: "登录" }));

      expect(await screen.findByText("改密页")).not.toBeNull();
      expect(screen.queryByText("告警页")).toBeNull();
    });

    it.each([
      ["INVALID_CREDENTIALS", 401, "用户名或密码不正确"],
      ["ACCOUNT_LOCKED", 423, "账号已被锁定"],
      ["ACCOUNT_DISABLED", 403, "账号已被停用"],
    ])("错误码 %s（HTTP %i）：显示如实且具体的文案", async (code, status, expectedSubstring) => {
      stubLoginFetch(status, { error: { code } });
      renderLogin();
      fireEvent.change(screen.getByLabelText(/用户名/), { target: { value: "alice" } });
      fireEvent.change(screen.getByLabelText(/密码/), { target: { value: "wrong-password" } });
      fireEvent.click(screen.getByRole("button", { name: "登录" }));

      const alert = await screen.findByRole("alert");
      expect(alert.textContent).toContain(expectedSubstring);
      // 失败后仍停在登录页，表单原样可继续编辑，不会被跳走
      expect(screen.getByLabelText(/用户名/)).not.toBeNull();
    });

    it("不认识的错误码：回落到通用文案 + 错误码本身，不吞掉信息", async () => {
      stubLoginFetch(500, { error: { code: "INTERNAL", message: "服务器开小差了" } });
      renderLogin();
      fireEvent.change(screen.getByLabelText(/用户名/), { target: { value: "alice" } });
      fireEvent.change(screen.getByLabelText(/密码/), { target: { value: "whatever12" } });
      fireEvent.click(screen.getByRole("button", { name: "登录" }));

      const alert = await screen.findByRole("alert");
      expect(alert.textContent).toContain("服务器开小差了");
      expect(alert.textContent).toContain("INTERNAL");
    });

    it("reason=logged_out 时说明已退出登录", () => {
      renderLogin("?reason=logged_out");
      expect(screen.getByRole("status").textContent).toContain("已退出登录");
    });
  });
});
