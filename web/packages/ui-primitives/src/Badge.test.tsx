import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Badge } from "./Badge";

describe("Badge", () => {
  it("默认使用 neutral 语气", () => {
    render(<Badge>未初始化</Badge>);
    const el = screen.getByText("未初始化");
    expect(el.className).toContain("text-fg-muted");
  });

  it("按语气切换令牌类，且不出现字面色值", () => {
    const { container } = render(
      <>
        <Badge tone="danger">同步失败</Badge>
        <Badge tone="warning">数据延迟</Badge>
        <Badge tone="success">新鲜</Badge>
        <Badge tone="info">提示</Badge>
      </>,
    );
    expect(screen.getByText("同步失败").className).toContain("border-danger");
    expect(screen.getByText("数据延迟").className).toContain("border-warning");
    expect(screen.getByText("新鲜").className).toContain("border-success");
    expect(screen.getByText("提示").className).toContain("border-accent");
    // 规格 §7.7：禁止硬编码颜色。类名里出现 # 或 rgb( 就说明漏了令牌
    expect(container.innerHTML).not.toMatch(/#[0-9a-fA-F]{3,8}|rgb\(/);
  });

  it("title 作为悬停说明透传", () => {
    render(<Badge title="超过新鲜度阈值">数据延迟</Badge>);
    expect(screen.getByTitle("超过新鲜度阈值")).not.toBeNull();
  });
});
