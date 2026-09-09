import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import {
  CommandPalette,
  CommandPaletteTrigger,
  filterCommandItems,
  useCommandPaletteHotkey,
  type CommandItem,
} from "./CommandPalette";

const items: CommandItem[] = [
  { id: "page-audit", type: "页面", title: "审计记录", meta: "全局", keywords: "/audit" },
  { id: "page-registry", type: "页面", title: "资源目录", meta: "平台治理", keywords: "/registry" },
  { id: "tab-upstream", type: "页签", title: "渠道管理", meta: "Sub2API", keywords: "sub2api" },
];

function open(props: Partial<React.ComponentProps<typeof CommandPalette>> = {}) {
  const onSelect = vi.fn();
  const onClose = vi.fn();
  render(
    <CommandPalette
      open
      onClose={onClose}
      items={items}
      onSelect={onSelect}
      placeholder="搜索页面、平台、页签"
      {...props}
    />,
  );
  return { onSelect, onClose };
}

const options = () => screen.getAllByRole("option");
const activeOption = () => options().find((o) => o.getAttribute("aria-selected") === "true");

describe("filterCommandItems", () => {
  it("按 id + 标题 + 副标题 + 关键词做子串匹配，大小写不敏感", () => {
    expect(filterCommandItems(items, "审计").map((i) => i.id)).toEqual(["page-audit"]);
    expect(filterCommandItems(items, "SUB2API").map((i) => i.id)).toEqual(["tab-upstream"]);
    expect(filterCommandItems(items, "/registry").map((i) => i.id)).toEqual(["page-registry"]);
  });

  it("空查询给全部，首尾空格不影响", () => {
    expect(filterCommandItems(items, "")).toHaveLength(3);
    expect(filterCommandItems(items, "   ")).toHaveLength(3);
  });

  it("不做模糊兜底：搜不到就是空", () => {
    // 一个「搜什么都能出点东西」的框，人没法判断自己搜的东西到底存不存在
    expect(filterCommandItems(items, "zzz")).toHaveLength(0);
  });
});

describe("CommandPalette", () => {
  it("关着的时候什么都不渲染", () => {
    render(
      <CommandPalette
        open={false}
        onClose={() => {}}
        items={items}
        onSelect={() => {}}
        placeholder="搜索"
      />,
    );
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("打开时焦点落在输入框，列表给出全部条目", () => {
    open();
    expect(document.activeElement).toBe(screen.getByRole("textbox", { name: "搜索全平台" }));
    expect(options()).toHaveLength(3);
  });

  it("是模态对话框，且有可访问名", () => {
    open();
    const dialog = screen.getByRole("dialog");
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    expect(dialog.getAttribute("aria-labelledby")).toBeTruthy();
  });

  it("输入即过滤，第一条自动成为活动项", () => {
    open();
    fireEvent.change(screen.getByRole("textbox", { name: "搜索全平台" }), {
      target: { value: "资源" },
    });
    expect(options()).toHaveLength(1);
    expect(activeOption()?.textContent).toContain("资源目录");
  });

  it("aria-activedescendant 指向当前活动项——焦点留在输入框，读屏才念得出选中的是哪条", () => {
    open();
    const input = screen.getByRole("textbox", { name: "搜索全平台" });
    expect(input.getAttribute("aria-activedescendant")).toBe(activeOption()?.id);
  });

  it("↑↓ 移动活动项，且不越界", () => {
    open();
    const input = screen.getByRole("textbox", { name: "搜索全平台" });
    fireEvent.keyDown(input, { key: "ArrowDown" });
    expect(activeOption()?.textContent).toContain("资源目录");
    fireEvent.keyDown(input, { key: "ArrowUp" });
    fireEvent.keyDown(input, { key: "ArrowUp" });
    expect(activeOption()?.textContent).toContain("审计记录");
  });

  it("Enter 打开当前活动项并关闭面板", () => {
    const { onSelect, onClose } = open();
    const input = screen.getByRole("textbox", { name: "搜索全平台" });
    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ id: "page-registry" }));
    expect(onClose).toHaveBeenCalled();
  });

  it("没有匹配结果时 Enter 什么也不做", () => {
    const { onSelect } = open();
    const input = screen.getByRole("textbox", { name: "搜索全平台" });
    fireEvent.change(input, { target: { value: "zzz" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onSelect).not.toHaveBeenCalled();
    expect(screen.getByText("没有匹配结果")).not.toBeNull();
  });

  it("Esc 关闭", () => {
    const { onClose } = open();
    fireEvent.keyDown(screen.getByRole("textbox", { name: "搜索全平台" }), { key: "Escape" });
    expect(onClose).toHaveBeenCalled();
  });

  it("点选项直接打开", () => {
    const { onSelect } = open();
    fireEvent.click(screen.getByText("渠道管理"));
    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ id: "tab-upstream" }));
  });

  it("鼠标移过去就跟着选中——否则鼠标指着 A、Enter 却打开 B", () => {
    const { onSelect } = open();
    fireEvent.mouseEnter(screen.getByText("渠道管理").closest("[role=option]")!);
    fireEvent.keyDown(screen.getByRole("textbox", { name: "搜索全平台" }), { key: "Enter" });
    expect(onSelect).toHaveBeenCalledWith(expect.objectContaining({ id: "tab-upstream" }));
  });

  it("点面板之外关闭，点面板内部不关", () => {
    const { onClose } = open();
    fireEvent.mouseDown(screen.getByRole("dialog"));
    expect(onClose).not.toHaveBeenCalled();
    fireEvent.mouseDown(screen.getByRole("dialog").parentElement!);
    expect(onClose).toHaveBeenCalled();
  });

  it("范围说明照实显示——面板要自己说清楚现在搜得到什么", () => {
    open({ scopeNote: "本阶段只搜导航" });
    expect(screen.getByText("本阶段只搜导航")).not.toBeNull();
  });

  it("底部给出键盘用法", () => {
    open();
    expect(screen.getByText(/↑↓ 选择/)).not.toBeNull();
  });
});

describe("useCommandPaletteHotkey", () => {
  function Harness({ enabled }: { enabled: boolean }) {
    const [count, setCount] = useState(0);
    useCommandPaletteHotkey(() => setCount((n) => n + 1), enabled);
    return (
      <div>
        <span data-testid="count">{count}</span>
        <input aria-label="别的输入框" />
      </div>
    );
  }

  const count = () => Number(screen.getByTestId("count").textContent);

  it("Ctrl+K 与 Cmd+K 都能触发", () => {
    render(<Harness enabled />);
    fireEvent.keyDown(document, { key: "k", ctrlKey: true });
    expect(count()).toBe(1);
    fireEvent.keyDown(document, { key: "K", metaKey: true });
    expect(count()).toBe(2);
  });

  it("焦点在输入框里时不劫持——人正在打字", () => {
    render(<Harness enabled />);
    const input = screen.getByLabelText("别的输入框");
    input.focus();
    fireEvent.keyDown(input, { key: "k", ctrlKey: true });
    expect(count()).toBe(0);
  });

  it("面板已打开时停用：再按一次会把已经输入的词清掉", () => {
    render(<Harness enabled={false} />);
    fireEvent.keyDown(document, { key: "k", ctrlKey: true });
    expect(count()).toBe(0);
  });

  it("不带修饰键的 k 不触发", () => {
    render(<Harness enabled />);
    fireEvent.keyDown(document, { key: "k" });
    expect(count()).toBe(0);
  });
});

describe("CommandPaletteTrigger", () => {
  it("是真按钮，且把快捷键告诉辅助技术", () => {
    // 只在视觉上摆一个 ⌘K，这条信息对读屏用户等于不存在
    const onOpen = vi.fn();
    render(<CommandPaletteTrigger onOpen={onOpen} />);
    const button = screen.getByRole("button");
    expect(button.getAttribute("aria-keyshortcuts")).toBe("Control+K Meta+K");
    expect(button.getAttribute("aria-disabled")).toBeNull();
    fireEvent.click(button);
    expect(onOpen).toHaveBeenCalled();
  });
});
