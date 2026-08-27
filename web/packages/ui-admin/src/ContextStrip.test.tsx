import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ContextStrip } from "./ContextStrip";

describe("ContextStrip", () => {
  it("按顺序渲染面包屑，最后一项标成当前位置", () => {
    render(
      <ContextStrip
        crumbs={[
          { key: "section", label: "被管平台" },
          { key: "page", label: "Sub2API" },
        ]}
      />,
    );
    const nav = screen.getByRole("navigation", { name: "面包屑" });
    expect(nav.textContent).toContain("被管平台");
    // aria-current 只给最后一项：前面几级是路径，最后一级才是「你在这」
    expect(screen.getByText("Sub2API").getAttribute("aria-current")).toBe("page");
    expect(screen.getByText("被管平台").getAttribute("aria-current")).toBeNull();
  });

  it("分隔符不进无障碍树：它是形状，不是内容", () => {
    render(
      <ContextStrip
        crumbs={[
          { key: "a", label: "全局" },
          { key: "b", label: "审计事件" },
        ]}
      />,
    );
    const separators = screen
      .getByRole("navigation", { name: "面包屑" })
      .querySelectorAll("[aria-hidden='true']");
    expect(separators.length).toBe(1);
  });

  it("显示环境标识与它的悬停说明", () => {
    render(
      <ContextStrip
        environment={{ label: "环境 staging", hint: "前端显式请求 environment=staging" }}
      />,
    );
    const badge = screen.getByText("环境 staging");
    expect(badge.getAttribute("title")).toBe("前端显式请求 environment=staging");
  });

  it("什么都没有就整条不渲染，不留一条空边框", () => {
    const { container } = render(<ContextStrip />);
    expect(container.firstChild).toBeNull();
  });

  it("只有环境、没有面包屑时不渲染空的面包屑导航", () => {
    render(<ContextStrip environment={{ label: "环境 production" }} />);
    expect(screen.queryByRole("navigation", { name: "面包屑" })).toBeNull();
    expect(screen.getByText("环境 production")).not.toBeNull();
  });
});
