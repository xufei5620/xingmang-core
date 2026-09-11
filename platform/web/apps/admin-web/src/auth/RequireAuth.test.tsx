import { render, screen } from "@testing-library/react";
import { RouterProvider, createMemoryRouter, useLocation, useNavigate } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { devLogin, devLogout } from "./devSession";
import { setCachedLocalUser, type LocalUser } from "./localSession";
import { RequireAuth } from "./RequireAuth";

function LoginStub() {
  const location = useLocation();
  return <div>登录页 {location.search}</div>;
}

function renderGate(path: string, signedIn?: () => boolean) {
  const router = createMemoryRouter(
    [
      { path: "/login", Component: LoginStub },
      {
        path: "/",
        Component: () => <RequireAuth signedIn={signedIn} />,
        children: [{ path: "dashboard", element: <div>受保护内容</div> }],
      },
    ],
    { initialEntries: [path] },
  );
  render(<RouterProvider router={router} />);
}

describe("RequireAuth：路由门禁", () => {
  beforeEach(() => { window.__XM_CONFIG__ = {authMode:"dev-header"}; });
  afterEach(() => { delete window.__XM_CONFIG__; });
  it("没登录：去 /login，并把当前地址（含查询串）带在 next 里", async () => {
    renderGate("/dashboard?work=alerts", () => false);
    const login = await screen.findByText(/登录页/);
    expect(login.textContent).toContain(`?next=${encodeURIComponent("/dashboard?work=alerts")}`);
    expect(screen.queryByText("受保护内容")).toBeNull();
  });

  it("登录了：渲染子路由", async () => {
    renderGate("/dashboard", () => true);
    expect(await screen.findByText("受保护内容")).not.toBeNull();
  });

  describe("默认判定跟着 authMode 走", () => {
    beforeEach(() => {
      devLogout();
    });
    afterEach(() => {
      delete window.__XM_CONFIG__;
      devLogout();
    });

    it("dev-header：看 localStorage 的开发开关", async () => {
      devLogin();
      renderGate("/dashboard");
      expect(await screen.findByText("受保护内容")).not.toBeNull();
    });


  });
});

function renderLocalGate(path: string, fetchLocalUser: () => Promise<LocalUser>) {
  const router = createMemoryRouter(
    [
      { path: "/login", Component: LoginStub },
      {
        path: "/",
        Component: () => <RequireAuth fetchLocalUser={fetchLocalUser} />,
        children: [
          { path: "dashboard", element: <div>受保护内容</div> },
          { path: "account/password", element: <div>改密页</div> },
          { path: "account/totp", element: <div>TOTP 启用页</div> },
        ],
      },
    ],
    { initialEntries: [path] },
  );
  render(<RouterProvider router={router} />);
  return router;
}

describe("RequireAuth：local 模式（XM-LOGIN）", () => {
  const user: LocalUser = {
    username: "alice",
    display_name: "Alice",
    roles: ["staff"],
    must_change_password: false,
    totp_enrolled: false,
    must_enroll_totp: false,
    totp_enrolled_at: null,
    recovery_codes_remaining: null,
  };

  beforeEach(() => {
    window.__XM_CONFIG__ = { authMode: "local" };
    setCachedLocalUser(null);
  });
  afterEach(() => {
    delete window.__XM_CONFIG__;
    setCachedLocalUser(null);
  });

  it("me() 成功且不需要强制改密：放行子路由", async () => {
    renderLocalGate("/dashboard", () => Promise.resolve(user));
    expect(await screen.findByText("受保护内容")).not.toBeNull();
  });

  it("me() 失败（没有有效会话）：去登录页，next 带上原地址", async () => {
    renderLocalGate("/dashboard?work=alerts", () => Promise.reject(new Error("no session")));
    const login = await screen.findByText(/登录页/);
    expect(login.textContent).toContain(`?next=${encodeURIComponent("/dashboard?work=alerts")}`);
  });

  it("must_change_password 为真：无论访问哪个页面都先拦到强制改密页", async () => {
    renderLocalGate("/dashboard", () => Promise.resolve({ ...user, must_change_password: true }));
    expect(await screen.findByText("改密页")).not.toBeNull();
    expect(screen.queryByText("受保护内容")).toBeNull();
  });

  it("must_change_password 为真时改密页本身照常放行，不会把自己重定向到自己", async () => {
    renderLocalGate("/account/password", () =>
      Promise.resolve({ ...user, must_change_password: true }),
    );
    expect(await screen.findByText("改密页")).not.toBeNull();
  });

  it("must_enroll_totp 为真且未激活：无论访问哪个页面都先拦到启用页", async () => {
    renderLocalGate("/dashboard", () =>
      Promise.resolve({ ...user, must_enroll_totp: true, totp_enrolled: false }),
    );
    expect(await screen.findByText("TOTP 启用页")).not.toBeNull();
    expect(screen.queryByText("受保护内容")).toBeNull();
  });

  it("must_enroll_totp 为真时启用页本身照常放行，不会把自己重定向到自己", async () => {
    renderLocalGate("/account/totp", () =>
      Promise.resolve({ ...user, must_enroll_totp: true, totp_enrolled: false }),
    );
    expect(await screen.findByText("TOTP 启用页")).not.toBeNull();
  });

  it("must_enroll_totp 为真但已经激活：不拦（已完成启用，字段还没来得及被后端清掉也不该再拦）", async () => {
    renderLocalGate("/dashboard", () =>
      Promise.resolve({ ...user, must_enroll_totp: true, totp_enrolled: true }),
    );
    expect(await screen.findByText("受保护内容")).not.toBeNull();
  });

  it("must_change_password 与 must_enroll_totp 同时为真：先拦到改密页（不是启用页）", async () => {
    renderLocalGate("/dashboard", () =>
      Promise.resolve({ ...user, must_change_password: true, must_enroll_totp: true, totp_enrolled: false }),
    );
    expect(await screen.findByText("改密页")).not.toBeNull();
    expect(screen.queryByText("TOTP 启用页")).toBeNull();
  });

  it("命中内存缓存（比如刚登录成功跳转过来）时不再重复请求 me()", async () => {
    setCachedLocalUser(user);
    const fetchLocalUser = vi.fn(() => Promise.resolve(user));
    renderLocalGate("/dashboard", fetchLocalUser);
    expect(await screen.findByText("受保护内容")).not.toBeNull();
    expect(fetchLocalUser).not.toHaveBeenCalled();
  });
});
