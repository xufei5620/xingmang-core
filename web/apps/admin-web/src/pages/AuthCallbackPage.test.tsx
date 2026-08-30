import { fireEvent, render, screen } from "@testing-library/react";
import { RouterProvider, createMemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import { OidcError } from "../auth/oidc";
import { AuthCallbackPage } from "./AuthCallbackPage";

function renderCallback(
  search: string,
  handle: (params: URLSearchParams) => Promise<{ next: string }>,
) {
  const router = createMemoryRouter(
    [
      { path: "/auth/callback", Component: () => <AuthCallbackPage handle={handle} /> },
      { path: "/login", element: <div>登录页</div> },
      { path: "/audit", element: <div>审计记录页</div> },
      { path: "/dashboard", element: <div>工作台</div> },
    ],
    { initialEntries: [`/auth/callback${search}`] },
  );
  render(<RouterProvider router={router} />);
  return router;
}

describe("/auth/callback", () => {
  it("把查询参数交给 handleCallback，成功后 replace 到 next", async () => {
    const handle = vi.fn((_params: URLSearchParams) => Promise.resolve({ next: "/audit" }));
    const router = renderCallback("?code=abc&state=xyz", handle);

    expect(await screen.findByText("审计记录页")).not.toBeNull();
    expect(handle).toHaveBeenCalledOnce();
    const params = handle.mock.calls[0]?.[0];
    expect(params?.get("code")).toBe("abc");
    expect(params?.get("state")).toBe("xyz");
    // replace：回退键不会再回到回调页（授权码只能用一次）
    expect(router.state.location.pathname).toBe("/audit");
  });

  it("state 不匹配被拒：把原因写在屏幕上，不假装登录成功", async () => {
    const handle = vi.fn(() =>
      Promise.reject(new OidcError("callback", "state 不匹配：回调不是本次登录发起的，已拒绝")),
    );
    renderCallback("?code=abc&state=forged", handle);

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("state 不匹配");
    expect(screen.queryByText("工作台")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "回到登录页" }));
    expect(await screen.findByText("登录页")).not.toBeNull();
  });

  it("处理中显示加载态", () => {
    renderCallback("?code=abc&state=xyz", () => new Promise(() => {}));
    expect(screen.getByText("正在完成登录…")).not.toBeNull();
  });
});
