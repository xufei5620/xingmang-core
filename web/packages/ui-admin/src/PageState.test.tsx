import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { PageState } from "./PageState";

describe("PageState", () => {
  it("加载态给读屏播报，其余四种不播报", () => {
    // 静态说明每次换页都播报一遍，会打断正在听的人
    const { unmount } = render(<PageState kind="loading" />);
    expect(screen.getByRole("status").textContent).toBe("加载中…");
    unmount();
    render(<PageState kind="empty" title="没有渠道" />);
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("错误态显示正文，传了 onRetry 才有重试按钮", () => {
    const onRetry = vi.fn();
    const { unmount } = render(<PageState kind="error" message="503 upstream" onRetry={onRetry} />);
    expect(screen.getByText("加载失败")).not.toBeNull();
    expect(screen.getByText("503 upstream")).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "重试" }));
    expect(onRetry).toHaveBeenCalled();

    unmount();
    // 对 400 这类错误再点一次还是同样的答案，就不该给按钮
    render(<PageState kind="error" message="参数不合法" />);
    expect(screen.queryByRole("button", { name: "重试" })).toBeNull();
  });

  it("无权访问说出缺哪个 scope，并且**不给重试**", () => {
    render(<PageState kind="denied" permission="ops.read" />);
    expect(screen.getByText("无权访问")).not.toBeNull();
    expect(screen.getByText(/ops\.read/)).not.toBeNull();
    // 权限不足重试一万次都是同一个答案
    expect(screen.queryByRole("button", { name: "重试" })).toBeNull();
    // 前端隐藏不构成安全控制，这句话必须在
    expect(screen.getByText(/服务端为最终裁决/)).not.toBeNull();
  });

  it("「未接入」与「空」是两种状态，不共用一句话", () => {
    // 混起来就是让运营以为「这个平台今天没有告警」，而事实是我们没接
    const { unmount } = render(<PageState kind="unavailable" description="随 M3 上线" />);
    expect(screen.getByText("未接入")).not.toBeNull();
    unmount();
    render(<PageState kind="empty" title="没有渠道" />);
    expect(screen.getByText("没有渠道")).not.toBeNull();
    expect(screen.queryByText("未接入")).toBeNull();
  });

  it("补充行与操作槽照渲染", () => {
    render(
      <PageState
        kind="error"
        message="服务内部错误"
        footnote="request_id: req-7"
        action={<button type="button">去登记一个</button>}
      />,
    );
    expect(screen.getByText("request_id: req-7")).not.toBeNull();
    expect(screen.getByRole("button", { name: "去登记一个" })).not.toBeNull();
  });

  it("compact 收窄留白，供嵌在表格或卡片内部使用", () => {
    const { container, unmount } = render(<PageState kind="loading" compact />);
    expect(container.firstElementChild?.className).toContain("p-4");
    unmount();
    const full = render(<PageState kind="loading" />);
    expect(full.container.firstElementChild?.className).toContain("p-8");
  });

  it("标题可以被调用方覆盖——空的是什么只有它知道", () => {
    render(<PageState kind="empty" title="这个环境还没有登记任何服务" />);
    expect(screen.getByText("这个环境还没有登记任何服务")).not.toBeNull();
  });
});
