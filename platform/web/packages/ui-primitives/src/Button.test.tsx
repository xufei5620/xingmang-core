import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Button } from "./Button";

describe("Button", () => {
  it("渲染文本并默认为 primary", () => {
    render(<Button>保存</Button>);
    const btn = screen.getByRole("button", { name: "保存" });
    expect(btn.className).toContain("bg-accent");
  });
  it("loading 时禁用并显示忙碌状态", () => {
    render(<Button loading>提交</Button>);
    const btn = screen.getByRole("button");
    expect(btn).toHaveProperty("disabled", true);
    expect(btn.getAttribute("aria-busy")).toBe("true");
  });
  it("danger 变体使用 danger 令牌", () => {
    render(<Button variant="danger">删除</Button>);
    expect(screen.getByRole("button").className).toContain("bg-danger");
  });
});
