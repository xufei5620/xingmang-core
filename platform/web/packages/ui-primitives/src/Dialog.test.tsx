import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { Dialog } from "./Dialog";

describe("Dialog", () => {
  it("点击触发器打开，关闭按钮关闭", async () => {
    const user = userEvent.setup();
    render(
      <Dialog trigger={<button type="button">打开</button>} title="确认删除">
        <p>此操作不可撤销</p>
      </Dialog>,
    );

    expect(screen.queryByRole("dialog")).toBeNull();
    await user.click(screen.getByRole("button", { name: "打开" }));
    expect(screen.getByRole("dialog", { name: "确认删除" })).toBeTruthy();
    expect(screen.getByText("此操作不可撤销")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "关闭" }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("Escape 关闭对话框", async () => {
    const user = userEvent.setup();
    render(
      <Dialog
        trigger={<button type="button">打开</button>}
        title="提示"
        defaultOpen
      >
        内容
      </Dialog>,
    );

    expect(screen.getByRole("dialog")).toBeTruthy();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});
