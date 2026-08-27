import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { StatTile } from "./StatTile";

describe("StatTile", () => {
  it("标签、数值与说明都在——一个孤零零的数字回答不了「哪三个」", () => {
    render(<StatTile label="紧急" value="3" note="未解决的严重告警" />);
    expect(screen.getByText("紧急")).not.toBeNull();
    expect(screen.getByText("3")).not.toBeNull();
    expect(screen.getByText("未解决的严重告警")).not.toBeNull();
  });

  it("没有数据源时显示「—」而不是 0", () => {
    // 0 会被读成「今天没有到期项」，而事实是这条线还没接
    render(
      <StatTile label="今日到期" value="—" unavailable note="随 Foundation-B 上线" />,
    );
    expect(screen.getByText("—")).not.toBeNull();
    expect(screen.queryByText("0")).toBeNull();
  });

  it("不带新鲜度徽章——它数的是告警与待办，不是采集来的指标", () => {
    const { container } = render(<StatTile label="紧急" value="3" note="说明" />);
    expect(container.textContent).not.toContain("数据新鲜");
    expect(container.textContent).not.toContain("未初始化");
  });

  it("状态与入口两个插槽照渲染", () => {
    render(
      <StatTile
        label="阻塞"
        value="—"
        unavailable
        note="说明"
        status={<span>未接入</span>}
        link={<a href="/alerts">查看全部告警</a>}
      />,
    );
    expect(screen.getByText("未接入")).not.toBeNull();
    expect(screen.getByRole("link", { name: "查看全部告警" })).not.toBeNull();
  });
});
