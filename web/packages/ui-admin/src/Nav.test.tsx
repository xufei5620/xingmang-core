import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { NavItemDisabled, NavItemLabel, NavSection, NavSectionCollapsible } from "./Nav";

describe("NavSection", () => {
  it("渲染段标题与段内条目", () => {
    render(
      <NavSection title="平台">
        <a href="/platforms/sub2api">Sub2API</a>
      </NavSection>,
    );
    expect(screen.getByRole("heading", { name: "平台" })).not.toBeNull();
    expect(screen.getByRole("link", { name: "Sub2API" })).not.toBeNull();
  });
});

describe("NavSectionCollapsible", () => {
  it("默认收起，但标题与阶段标签始终在场", () => {
    render(
      <NavSectionCollapsible title="扩展能力" hint="后置">
        <a href="/ext/app">应用与配置</a>
      </NavSectionCollapsible>,
    );
    // 收起不是隐藏：看不见这一段，人会以为平台压根没有这块（§12 惯例）
    expect(screen.getByRole("heading", { name: "扩展能力" })).not.toBeNull();
    expect(screen.getByText("后置")).not.toBeNull();
    expect(document.querySelector("details")?.open).toBe(false);
  });

  it("open 置 true 时展开——路由进到这一段时用它", () => {
    render(
      <NavSectionCollapsible title="扩展能力" open>
        <a href="/ext/app">应用与配置</a>
      </NavSectionCollapsible>,
    );
    expect(document.querySelector("details")?.open).toBe(true);
  });

  it("段标题仍然是 heading：靠标题跳转的读屏用户不能少掉这一段", () => {
    render(
      <NavSectionCollapsible title="扩展能力">
        <a href="/ext/app">应用与配置</a>
      </NavSectionCollapsible>,
    );
    const heading = screen.getByRole("heading", { name: "扩展能力" });
    expect(heading.closest("summary")).not.toBeNull();
  });

  it("人手动展开后状态跟着 DOM 走，不被组件掰回去", () => {
    render(
      <NavSectionCollapsible title="扩展能力">
        <a href="/ext/app">应用与配置</a>
      </NavSectionCollapsible>,
    );
    const details = document.querySelector("details") as HTMLDetailsElement;
    details.open = true;
    fireEvent(details, new Event("toggle", { bubbles: false }));
    expect(details.open).toBe(true);
  });
});

describe("NavItemLabel", () => {
  it("标签与状态提示都显示——未接入要看得见，不是藏起来（§12 惯例）", () => {
    render(<NavItemLabel label="服务器" hint="未接入·M2" />);
    expect(screen.getByText("服务器")).not.toBeNull();
    expect(screen.getByText("未接入·M2")).not.toBeNull();
  });

  it("没有 hint 就只有标签——已实装的页不该挂一个什么也没说明的标签", () => {
    const { container } = render(<NavItemLabel label="运营工作台" />);
    expect(container.textContent).toBe("运营工作台");
  });
});

describe("NavItemDisabled", () => {
  it("既不是链接也不是按钮：点不动才叫禁用", () => {
    render(<NavItemDisabled label="CPA" hint="读取失败" />);
    // 一个没有目标的链接会被键盘和读屏当成可达入口，点下去什么都不发生
    expect(screen.queryByRole("link", { name: /CPA/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /CPA/ })).toBeNull();
    expect(screen.getByText("读取失败")).not.toBeNull();
  });

  it("标记 aria-disabled，辅助技术能读出它是禁用的", () => {
    render(<NavItemDisabled label="Sub2API" hint="读取中" />);
    const item = screen.getByText("Sub2API").closest("[aria-disabled='true']");
    expect(item).not.toBeNull();
  });
});
