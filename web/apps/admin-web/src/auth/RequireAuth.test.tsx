import { render, screen } from "@testing-library/react";
import { RouterProvider, createMemoryRouter, useLocation } from "react-router";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { devLogin, devLogout } from "./devSession";
import { SESSION_KEY } from "./oidc";
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
      sessionStorage.removeItem(SESSION_KEY);
    });
    afterEach(() => {
      delete window.__XM_CONFIG__;
      devLogout();
      sessionStorage.removeItem(SESSION_KEY);
    });

    it("dev-header：看 localStorage 的开发开关", async () => {
      devLogin();
      renderGate("/dashboard");
      expect(await screen.findByText("受保护内容")).not.toBeNull();
    });

    it("oidc：开发开关不算数，没有 OIDC 会话就去登录", async () => {
      window.__XM_CONFIG__ = {
        authMode: "oidc",
        oidcIssuer: "https://auth.example.test/realms/solov-staff",
        oidcClientId: "xingmang-admin-web",
      };
      devLogin();
      renderGate("/dashboard");
      expect(await screen.findByText(/登录页/)).not.toBeNull();
    });

    it("oidc：sessionStorage 里有会话就放行", async () => {
      window.__XM_CONFIG__ = {
        authMode: "oidc",
        oidcIssuer: "https://auth.example.test/realms/solov-staff",
        oidcClientId: "xingmang-admin-web",
      };
      sessionStorage.setItem(
        SESSION_KEY,
        JSON.stringify({ access_token: "at", expires_at: Date.now() + 300_000 }),
      );
      renderGate("/dashboard");
      expect(await screen.findByText("受保护内容")).not.toBeNull();
    });
  });
});
