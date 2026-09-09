import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ApiError } from "../api/client";
import { ActionErrorNote } from "./ActionErrorNote";

/** 写路径失败时那一条错误说明（XM-I18N-LABELS 之后带中文错误码）。
 *
 *  这一组盯的是**两个方向都不能错**：
 *  认识的码要有中文，认不出来的码要原样吐出来且不掺兜底词。
 *  只测一边的话，一个「所有码都原样显示」或者一个「所有码都翻成未知错误」的
 *  实现都能过。 */

describe("ActionErrorNote：错误码的中文与原码", () => {
  it("认识的码：中文在前，原码逐字留在同一行里", () => {
    render(
      <ActionErrorNote
        error={new ApiError(403, "PERMISSION_DENIED", "缺少权限 cards.card.issue", "req-1")}
      />,
    );

    const alert = screen.getByRole("alert");
    expect(alert.textContent).toBe("缺少权限 cards.card.issue（权限不足，错误码 PERMISSION_DENIED）");
    // 原码要能被人整段选中复制去 grep 日志——它必须和中文在同一个元素里。
    expect(alert.textContent).toContain("PERMISSION_DENIED");
    // 完整解释挂在 title 上，不占正文。
    expect(alert.getAttribute("title")).toMatch(/权限/);
  });

  it("认不出来的码：原码逐字上屏，且没有任何兜底词", () => {
    // 后端将来会加新码。那一刻界面上唯一可信的事实就是这个码本身。
    render(<ActionErrorNote error={new ApiError(429, "QUOTA_EXHAUSTED", "额度用完了")} />);

    // 先取到正向锚点，再同步断言缺席：锚点没到就断言「没有未知」的话，
    // 那条断言在一个还没渲染的 DOM 上恒真。
    const alert = screen.getByRole("alert");
    expect(alert.textContent).toContain("QUOTA_EXHAUSTED");
    expect(alert.textContent).toBe("额度用完了（错误码 QUOTA_EXHAUSTED）");

    // 缺席断言：整个组件里都不能出现「未知 / 不明 / 其他错误」这类说法——
    // 把一个有名有姓的问题说成没名字的，比不翻译更糟。
    expect(document.body.textContent).not.toMatch(/未知|不明|其他错误/);
    // 也不能给一个猜出来的中文名：正文逐字就是上面那一句，多一个字都算猜。
    expect(alert.getAttribute("title")).toBeNull();
  });

  it("不是 ApiError 时才说「未知错误」，且只在连一句话都没有的时候", () => {
    // 上一条的对照：组件确实有「未知错误」这个词，只是它不该落在
    // 「有码但前端不认识」那条路径上。少了这条对照，上面那个 not.toMatch
    // 可能只是因为组件里压根没有这个词。
    render(<ActionErrorNote error={{ nope: true }} />);
    expect(screen.getByRole("alert").textContent).toBe("未知错误");
  });

  it("ADVANCED_CONTROLS_REQUIRED 除了中文名，还要**可见地**说清下一步", () => {
    // 后端那句原话是「需要 Action Advanced Controls（Foundation-B / XM-0030）」
    // ——对着它，使用者不知道该干什么。中文名说了原因，下一步必须另起一行说，
    // 而且不能只挂在悬停里：藏起来等于没说。
    render(
      <ActionErrorNote
        error={
          new ApiError(
            501,
            "ADVANCED_CONTROLS_REQUIRED",
            "action cards.card.issue 风险等级 L3 需要 Action Advanced Controls（Foundation-B / XM-0030）",
          )
        }
      />,
    );

    expect(screen.getByRole("alert").textContent).toContain("需要高级管控");
    // 口径与 api/approvals.ts 的 APPROVALS_NOT_MOUNTED_DESCRIPTION 一致：
    // 审批中心**已经**接入了，看到这个码是后端版本旧了，不是功能没做。
    const nextStep = screen.getByText(/platform-api 仍是启用之前的版本/);
    expect(nextStep.textContent).toContain("确认它已滚到含该变更的版本");
    // 不能把话说成「等排期」——那会让人一直等一个不会来的东西。
    expect(document.body.textContent).not.toMatch(/等待排期|敬请期待|即将上线/);
  });

  it("没有下一步的码不留一个空段落", () => {
    // 缺席断言：先取到正向锚点，再数段落。
    render(<ActionErrorNote error={new ApiError(409, "CONFLICT", "状态变了")} />);
    const alert = screen.getByRole("alert");
    expect(alert.textContent).toContain("状态冲突");
    // 整个组件只有 alert 那一段：多出来的空 <p> 会在错误条下面留一道空白。
    const paragraphs = document.body.querySelectorAll("p");
    expect(paragraphs.length).toBe(1);
  });

  it("403 才补「需要权限」那一句，别的状态不掺", () => {
    const { unmount } = render(
      <ActionErrorNote error={new ApiError(403, "PERMISSION_DENIED", "缺少权限 alerts.silence.manage")} />,
    );
    expect(screen.getByText(/需要权限：alerts\.silence\.manage/)).not.toBeNull();
    unmount();

    render(<ActionErrorNote error={new ApiError(409, "CONFLICT", "状态变了")} permission="x.y" />);
    expect(screen.queryByText(/需要权限/)).toBeNull();
  });
});
