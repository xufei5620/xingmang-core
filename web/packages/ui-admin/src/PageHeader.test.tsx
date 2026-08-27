import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { PageHeader } from "./PageHeader";

describe("PageHeader", () => {
  it("渲染标题与说明", () => {
    render(<PageHeader title="运营总览" description="全平台横切" />);
    expect(screen.getByRole("heading", { name: "运营总览" })).not.toBeNull();
    expect(screen.getByText("全平台横切")).not.toBeNull();
  });

  it("状态与标题在同一层（§11.2）：读标题时状态已经在眼里", () => {
    render(<PageHeader title="Sub2API" status={<span>降级</span>} />);
    const heading = screen.getByRole("heading", { name: "Sub2API" });
    const status = screen.getByText("降级");
    // 同一个父容器 = 同一行；挪到下一行或右侧就变成「先读名字，再去找情况」
    expect(heading.parentElement?.contains(status)).toBe(true);
  });

  it("不给 status 就不占位", () => {
    render(<PageHeader title="设置" />);
    expect(screen.getByRole("heading", { name: "设置" }).parentElement?.childElementCount).toBe(1);
  });

  it("给了 onRefresh 才有刷新按钮", () => {
    const onRefresh = vi.fn();
    const { rerender } = render(<PageHeader title="总览" />);
    expect(screen.queryByRole("button", { name: "刷新" })).toBeNull();

    rerender(<PageHeader title="总览" onRefresh={onRefresh} />);
    fireEvent.click(screen.getByRole("button", { name: "刷新" }));
    expect(onRefresh).toHaveBeenCalledOnce();
  });

  it("刷新中时按钮禁用并标 aria-busy，防重复点", () => {
    render(<PageHeader title="总览" onRefresh={() => {}} refreshing />);
    const button = screen.getByRole("button", { name: "刷新" });
    expect(button.getAttribute("aria-busy")).toBe("true");
    expect((button as HTMLButtonElement).disabled).toBe(true);
  });

  it("显示最后刷新时刻——分不清「数值没变」和「轮询停了」是最要命的", () => {
    // 固定到本地时间的某一秒；只断言出现了 HH:mm:ss 形状，不锁具体时区
    render(<PageHeader title="总览" lastRefreshedAt={Date.parse("2026-08-26T10:00:00Z")} />);
    expect(screen.getByText(/最后刷新 \d{2}:\d{2}:\d{2}/)).not.toBeNull();
  });
});
