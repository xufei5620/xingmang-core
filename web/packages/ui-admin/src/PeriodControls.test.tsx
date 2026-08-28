import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import {
  describePeriod,
  GRANULARITY_OPTIONS,
  granularityLabel,
  PeriodControls,
} from "./PeriodControls";

describe("PeriodControls helpers", () => {
  it("keeps the shared day/week/month order and labels", () => {
    expect(GRANULARITY_OPTIONS).toEqual([
      { value: "day", label: "日" },
      { value: "week", label: "周" },
      { value: "month", label: "月" },
    ]);
    expect(granularityLabel("day")).toBe("按日查看");
    expect(granularityLabel("week")).toBe("按周查看");
    expect(granularityLabel("month")).toBe("按月查看");
  });

  it("describes only the caller-resolved range and never invents a pending value", () => {
    expect(describePeriod(undefined)).toBe("—");
    expect(
      describePeriod({
        day: "2026-08-27",
        granularity: "day",
        from: "2026-08-27",
        to: "2026-08-27",
      }),
    ).toBe("2026-08-27 · 按日查看");
    expect(
      describePeriod({
        day: "2026-08-27",
        granularity: "week",
        from: "2026-08-24",
        to: "2026-08-30",
      }),
    ).toBe("2026-08-24 ~ 2026-08-30 · 按周查看");
  });
});

describe("PeriodControls", () => {
  it("renders an accessible controlled date and segmented granularity control", () => {
    const onDayChange = vi.fn();
    const onGranularityChange = vi.fn();
    render(
      <PeriodControls
        day=""
        granularity="day"
        period={undefined}
        onDayChange={onDayChange}
        onGranularityChange={onGranularityChange}
      />,
    );

    expect(screen.getByText("统计区间")).not.toBeNull();
    expect(screen.getByText("—").getAttribute("aria-live")).toBe("polite");
    const date = screen.getByLabelText("统计区间的日期");
    fireEvent.change(date, { target: { value: "2026-08-20" } });
    expect(onDayChange).toHaveBeenCalledWith("2026-08-20");

    const group = screen.getByRole("group", { name: "统计粒度" });
    const buttons = within(group).getAllByRole("button");
    expect(buttons.map((button) => button.textContent)).toEqual(["日", "周", "月"]);
    expect(buttons.every((button) => button.className.includes("min-h-9"))).toBe(true);
    expect(buttons[0]?.getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(within(group).getByRole("button", { name: "月" }));
    expect(onGranularityChange).toHaveBeenCalledWith("month");
  });

  it("offers a reset only for an explicit business day", () => {
    const onDayChange = vi.fn();
    const { rerender } = render(
      <PeriodControls
        day=""
        granularity="day"
        period={undefined}
        onDayChange={onDayChange}
        onGranularityChange={vi.fn()}
      />,
    );
    expect(screen.queryByRole("button", { name: "回到今天" })).toBeNull();

    rerender(
      <PeriodControls
        day="2026-08-20"
        granularity="week"
        period={{
          day: "2026-08-20",
          granularity: "week",
          from: "2026-08-17",
          to: "2026-08-23",
        }}
        onDayChange={onDayChange}
        onGranularityChange={vi.fn()}
      />,
    );
    expect(screen.getByText("2026-08-17 ~ 2026-08-23 · 按周查看")).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "回到今天" }));
    expect(onDayChange).toHaveBeenCalledWith("");
  });

  it("can be labelled for another business context without changing interaction semantics", () => {
    render(
      <PeriodControls
        title="结算区间"
        dateLabel="业务日"
        dateAriaLabel="结算业务日"
        resetLabel="跟随账期"
        granularityAriaLabel="结算区间粒度"
        day="2026-08-20"
        granularity="month"
        period={undefined}
        onDayChange={vi.fn()}
        onGranularityChange={vi.fn()}
      />,
    );
    expect(screen.getByText("结算区间")).not.toBeNull();
    expect(screen.getByText("业务日")).not.toBeNull();
    expect(screen.getByLabelText("结算业务日")).not.toBeNull();
    expect(screen.getByRole("button", { name: "跟随账期" })).not.toBeNull();
    expect(screen.getByRole("group", { name: "结算区间粒度" })).not.toBeNull();
  });
});
