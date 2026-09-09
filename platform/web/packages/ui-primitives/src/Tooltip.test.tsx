import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { Tooltip } from "./Tooltip";

describe("Tooltip", () => {
  it("聚焦触发器后显示提示", async () => {
    const user = userEvent.setup();
    render(
      <Tooltip content="危险操作" delayDuration={0}>
        <button type="button">删除</button>
      </Tooltip>,
    );

    expect(screen.queryByRole("tooltip")).toBeNull();
    await user.tab();
    expect((await screen.findByRole("tooltip")).textContent).toContain("危险操作");
  });

  it("Escape 关闭提示", async () => {
    const user = userEvent.setup();
    render(
      <Tooltip content="危险操作" delayDuration={0}>
        <button type="button">删除</button>
      </Tooltip>,
    );

    await user.tab();
    expect(await screen.findByRole("tooltip")).toBeTruthy();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("tooltip")).toBeNull();
  });
});
