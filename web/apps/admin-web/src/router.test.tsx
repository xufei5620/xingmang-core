import { render, screen } from "@testing-library/react";
import { RouterProvider, createMemoryRouter } from "react-router";
import { beforeEach, describe, expect, it } from "vitest";
import { devLogin, devLogout } from "./auth";
import { routes } from "./router";

describe("admin-web 路由（登录前/后壳）", () => {
  beforeEach(() => devLogout());

  it("未登录访问 /dashboard 重定向到登录壳", async () => {
    const router = createMemoryRouter(routes, { initialEntries: ["/dashboard"] });
    render(<RouterProvider router={router} />);
    expect(await screen.findByText("开发模式进入")).not.toBeNull();
  });

  it("已登录访问 /dashboard 显示 AdminShell 与运营总览", async () => {
    devLogin();
    const router = createMemoryRouter(routes, { initialEntries: ["/dashboard"] });
    render(<RouterProvider router={router} />);
    // "运营总览" 同时出现在侧边导航与页面标题，因此按 role 精确断言
    expect(await screen.findByRole("heading", { name: "运营总览" })).not.toBeNull();
    expect(screen.getByRole("link", { name: "运营总览" })).not.toBeNull();
    expect(screen.getByText("Sub2API 数据未接入")).not.toBeNull();
    expect(screen.getByRole("navigation")).not.toBeNull();
  });
});
