import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { Tabs } from "./Tabs";

const items = [
  { value: "overview", label: "概览", content: "概览内容" },
  { value: "logs", label: "日志", content: "日志内容" },
];

describe("Tabs", () => {
  it("点击标签切换内容", async () => {
    const user = userEvent.setup();
    render(<Tabs defaultValue="overview" items={items} />);

    expect(screen.getByText("概览内容")).toBeTruthy();
    expect(screen.queryByText("日志内容")).toBeNull();

    await user.click(screen.getByRole("tab", { name: "日志" }));
    expect(screen.getByText("日志内容")).toBeTruthy();
    expect(screen.queryByText("概览内容")).toBeNull();
  });

  it("方向键在标签间移动并激活", async () => {
    const user = userEvent.setup();
    render(<Tabs defaultValue="overview" items={items} />);

    screen.getByRole("tab", { name: "概览" }).focus();
    await user.keyboard("{ArrowRight}");

    expect(screen.getByRole("tab", { name: "日志" })).toHaveProperty("tabIndex", 0);
    expect(screen.getByText("日志内容")).toBeTruthy();
  });
});
