import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { RequestPeriodControl } from "./RequestPeriodControl";

function renderControl({ since = "", until = "" }: { since?: string; until?: string } = {}) {
  const onClear = vi.fn();
  render(
    <RequestPeriodControl
      mode="custom"
      since={since}
      until={until}
      onModeChange={vi.fn()}
      onApply={vi.fn()}
      onClear={onClear}
    />,
  );
  return { onClear };
}

describe("请求统计区间说明", () => {
  it("只有 since 时如实显示自该时刻起，不冒充全部记录", () => {
    renderControl({ since: "2026-08-28T09:00:00Z" });
    expect(screen.getByText(/^自 .+ 起$/)).toBeTruthy();
    expect(screen.queryByText(/覆盖保留期内全部记录/)).toBeNull();
  });

  it("只有 until 时如实显示截至该时刻，不冒充全部记录", () => {
    renderControl({ until: "2026-08-28T10:00:00Z" });
    expect(screen.getByText(/^截至 .+$/)).toBeTruthy();
    expect(screen.queryByText(/覆盖保留期内全部记录/)).toBeNull();
  });

  it("两端都没有时才显示覆盖保留期内全部记录", () => {
    renderControl();
    expect(screen.getByText(/覆盖保留期内全部记录/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "清除区间" })).toBeNull();
  });

  it("任一时间边界存在时提供明确的清除区间入口", () => {
    const { onClear } = renderControl({ since: "2026-08-28T09:00:00Z" });
    fireEvent.click(screen.getByRole("button", { name: "清除区间" }));
    expect(onClear).toHaveBeenCalledTimes(1);
  });
});
