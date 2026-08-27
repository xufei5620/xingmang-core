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
  it("横幅在主内容区之外，跟着壳而不是跟着页面滚走", () => {
    render(
      <AdminShell nav={null} banner={<p>当前展示的是演示数据</p>}>
        <p>内容区</p>
      </AdminShell>,
    );
    const banner = screen.getByText("当前展示的是演示数据");
    expect(banner).not.toBeNull();
    // 不在 <main> 里：它是壳的一部分，不该被当成某一页的内容
    expect(screen.getByRole("main").contains(banner)).toBe(false);
  });

  it("不给 banner 就什么都不渲染", () => {
    render(
      <AdminShell nav={null}>
        <p>内容区</p>
      </AdminShell>,
    );
    expect(screen.queryByText("当前展示的是演示数据")).toBeNull();
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
