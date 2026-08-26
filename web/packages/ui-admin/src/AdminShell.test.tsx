import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { AdminShell } from "./AdminShell";

describe("AdminShell", () => {
  it("渲染标题、导航与内容区", () => {
    render(
      <AdminShell title="星芒统一控制平台" nav={<a href="/dashboard">运营总览</a>}>
        <p>内容区</p>
      </AdminShell>,
    );
    expect(screen.getByText("星芒统一控制平台")).not.toBeNull();
    expect(screen.getByRole("navigation")).not.toBeNull();
    expect(screen.getByText("运营总览")).not.toBeNull();
    expect(screen.getByRole("main").textContent).toContain("内容区");
  });
  it("显示用户并可触发退出", () => {
    const onLogout = vi.fn();
    render(
      <AdminShell nav={null} user={{ name: "管理员" }} onLogout={onLogout}>
        x
      </AdminShell>,
    );
    expect(screen.getByText("管理员")).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "退出登录" }));
    expect(onLogout).toHaveBeenCalledOnce();
  });
});
