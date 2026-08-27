import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { NavItemDisabled, NavSection } from "./Nav";

describe("NavSection", () => {
  it("渲染段标题与段内条目", () => {
    render(
      <NavSection title="被管平台">
        <a href="/platforms/sub2api">Sub2API</a>
      </NavSection>,
    );
    expect(screen.getByRole("heading", { name: "被管平台" })).not.toBeNull();
    expect(screen.getByRole("link", { name: "Sub2API" })).not.toBeNull();
  });
});

describe("NavItemDisabled", () => {
  it("标签与状态提示都显示——未接入要看得见，不是藏起来（§12 惯例）", () => {
    render(<NavItemDisabled label="告警中心" hint="即将上线" />);
    expect(screen.getByText("告警中心")).not.toBeNull();
    expect(screen.getByText("即将上线")).not.toBeNull();
  });

  it("既不是链接也不是按钮：点不动才叫禁用", () => {
    render(<NavItemDisabled label="NewAPI" hint="未接入·M1" />);
    // 一个没有目标的链接会被键盘和读屏当成可达入口，点下去什么都不发生
    expect(screen.queryByRole("link", { name: /NewAPI/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /NewAPI/ })).toBeNull();
  });

  it("标记 aria-disabled，辅助技术能读出它是禁用的", () => {
    render(<NavItemDisabled label="财务中心" hint="未接入·M3" />);
    const item = screen.getByText("财务中心").closest("[aria-disabled='true']");
    expect(item).not.toBeNull();
  });
});
