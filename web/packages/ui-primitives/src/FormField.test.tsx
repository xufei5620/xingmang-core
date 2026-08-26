import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { FormField } from "./FormField";
import { Input } from "./Input";

describe("FormField", () => {
  it("点击标签聚焦控件", async () => {
    const user = userEvent.setup();
    render(
      <FormField label="名称" htmlFor="name">
        <Input id="name" />
      </FormField>,
    );

    await user.click(screen.getByText("名称"));
    expect(document.activeElement).toBe(screen.getByRole("textbox"));
  });

  it("error 时以 alert 展示并关联到控件", () => {
    render(
      <FormField label="名称" htmlFor="name" error="必填">
        <Input id="name" invalid />
      </FormField>,
    );

    const input = screen.getByRole("textbox");
    const alert = screen.getByRole("alert");
    expect(alert).toHaveProperty("textContent", "必填");
    expect(input.getAttribute("aria-invalid")).toBe("true");
    expect(input.getAttribute("aria-describedby")).toBe(alert.id);
  });
});
