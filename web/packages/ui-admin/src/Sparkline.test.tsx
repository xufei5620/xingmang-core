import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Sparkline } from "./Sparkline";
import type { SparkSample } from "./sparklineGeometry";

function series(values: Array<[number, number]>): SparkSample[] {
  return values.map(([at, value]) => ({ at, value, failed: false }));
}

describe("Sparkline：无障碍摘要（Codex #9）", () => {
  it("aria-label 在标签之外带上方向与采样点数", () => {
    render(<Sparkline samples={series([[0, 1], [1, 2], [2, 5]])} label="日收入趋势" />);
    const svg = screen.getByRole("img");
    expect(svg.getAttribute("aria-label")).toContain("日收入趋势");
    expect(svg.getAttribute("aria-label")).toContain("整体上升");
  });

  it("样本不足时给占位文案，不画一条编出来的水平线", () => {
    render(<Sparkline samples={series([[0, 1]])} label="日收入趋势" />);
    expect(screen.getByText("暂无趋势")).not.toBeNull();
    expect(screen.queryByRole("img")).toBeNull();
  });
});

describe("Sparkline：部分数据（Codex #5）", () => {
  // 部分数据放在末尾：前一段两端都完整（实线），末段触及部分数据（虚线），
  // 一张图里两种线型都在，才能验出「只有该虚的那段虚了」
  const withPartial: SparkSample[] = [
    { at: 0, value: 1, failed: false },
    { at: 1, value: 2, failed: false },
    { at: 2, value: 5, failed: false, partial: true },
  ];

  it("部分数据走虚线，不是只换个颜色——色觉障碍者也要能看出来", () => {
    const { container } = render(<Sparkline samples={withPartial} label="日收入趋势" />);
    const dashed = container.querySelectorAll("polyline[stroke-dasharray]");
    expect(dashed.length).toBeGreaterThan(0);
    // 同时还有实线段：完整数据没有被一起虚线化
    const solid = [...container.querySelectorAll("polyline")].filter(
      (p) => !p.getAttribute("stroke-dasharray"),
    );
    expect(solid.length).toBeGreaterThan(0);
  });

  it("可见文字说明「含 N 个部分数据点」，不只写在 aria-label 里", () => {
    render(<Sparkline samples={withPartial} label="日收入趋势" />);
    expect(screen.getByText(/含 1 个部分数据点/)).not.toBeNull();
  });

  it("全是完整数据时没有这行提示，也没有虚线", () => {
    const { container } = render(
      <Sparkline samples={series([[0, 1], [1, 2], [2, 5]])} label="日收入趋势" />,
    );
    expect(screen.queryByText(/部分数据点/)).toBeNull();
    expect(container.querySelectorAll("polyline[stroke-dasharray]").length).toBe(0);
  });
});
