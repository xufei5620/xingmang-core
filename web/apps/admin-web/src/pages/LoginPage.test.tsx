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
    ],
    { initialEntries: [`/login${search}`] },
  );
  render(<RouterProvider router={router} />);
  return router;
}

const oidcConfig = {
  authMode: "oidc",
  oidcIssuer: "https://auth.example.test/realms/solov-staff",
  oidcClientId: "xingmang-admin-web",
};

describe("登录页", () => {
  beforeEach(() => devLogout());
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

  describe("oidc 模式", () => {
    it("只有一个「使用 solov 账号登录」按钮，没有开发模式入口", () => {
      window.__XM_CONFIG__ = oidcConfig;
      renderLogin();
      expect(screen.getByRole("button", { name: "使用 solov 账号登录" })).not.toBeNull();
      expect(screen.queryByRole("button", { name: "开发模式进入" })).toBeNull();
      expect(screen.queryByText(/开发模式：无需登录/)).toBeNull();
    });

    it("按下按钮走发现文档并整页跳去授权端点", async () => {
      window.__XM_CONFIG__ = oidcConfig;
      const assign = vi.fn();
      vi.stubGlobal("location", { ...window.location, origin: "http://localhost:3000", assign });
      vi.stubGlobal(
        "fetch",
        vi.fn(() =>
          Promise.resolve({
            ok: true,
            status: 200,
            json: () =>
              Promise.resolve({
                issuer: oidcConfig.oidcIssuer,
                authorization_endpoint: `${oidcConfig.oidcIssuer}/protocol/openid-connect/auth`,
                token_endpoint: `${oidcConfig.oidcIssuer}/protocol/openid-connect/token`,
              }),
          }),
        ),
      );
      renderLogin("?next=%2Falerts");
      fireEvent.click(screen.getByRole("button", { name: "使用 solov 账号登录" }));
      await vi.waitFor(() => expect(assign).toHaveBeenCalledOnce());
      const target = new URL(String(assign.mock.calls[0]?.[0]));
      expect(target.pathname).toBe("/realms/solov-staff/protocol/openid-connect/auth");
      expect(target.searchParams.get("code_challenge_method")).toBe("S256");
      expect(target.searchParams.get("redirect_uri")).toBe("http://localhost:3000/auth/callback");
    });

    it("身份服务不可达时把错误写在页面上，按钮恢复可点", async () => {
      // 换一个 issuer：应用级客户端是单例，上一个用例的发现文档还在它的缓存里
      window.__XM_CONFIG__ = { ...oidcConfig, oidcIssuer: "https://down.example.test/realms/x" };
      vi.stubGlobal("fetch", vi.fn(() => Promise.reject(new TypeError("Failed to fetch"))));
      renderLogin();
      fireEvent.click(screen.getByRole("button", { name: "使用 solov 账号登录" }));
      const alert = await screen.findByRole("alert");
      expect(alert.textContent).toContain("无法连接身份服务");
      expect((screen.getByRole("button", { name: "使用 solov 账号登录" }) as HTMLButtonElement).disabled).toBe(false);
    });

    it("reason=session_expired 时说明为什么回到了登录页", () => {
      window.__XM_CONFIG__ = oidcConfig;
      renderLogin("?reason=session_expired");
      expect(screen.getByRole("status").textContent).toContain("登录已过期");
    });

    it("配置不完整（缺 issuer）：按钮禁用并列出缺什么", () => {
      window.__XM_CONFIG__ = { authMode: "oidc", oidcClientId: "xingmang-admin-web" };
      renderLogin();
      const button = screen.getByRole("button", { name: "使用 solov 账号登录" }) as HTMLButtonElement;
      expect(button.disabled).toBe(true);
      expect(screen.getByRole("alert").textContent).toContain("XM_WEB_OIDC_ISSUER");
    });
  });
});
