import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Select } from "./Select";

const options = [
  { value: "prod", label: "生产" },
  { value: "staging", label: "预发" },
];

describe("Select", () => {
  it("打开后可选择选项", async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    render(
      <Select aria-label="环境" options={options} onValueChange={onValueChange} />,
    );

    await user.click(screen.getByRole("combobox", { name: "环境" }));
    await user.click(await screen.findByRole("option", { name: "预发" }));

    expect(onValueChange).toHaveBeenCalledWith("staging");
    expect(screen.getByRole("combobox").textContent).toContain("预发");
  });

  it("disabled 时不可打开，键盘也无法展开", async () => {
    const user = userEvent.setup();
    render(<Select aria-label="环境" disabled options={options} />);

    const trigger = screen.getByRole("combobox", { name: "环境" });
    expect(trigger).toHaveProperty("disabled", true);

    await user.click(trigger);
    await user.keyboard("{ArrowDown}");
    expect(screen.queryByRole("listbox")).toBeNull();
  });
});
