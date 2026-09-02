import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { EmbeddedConsoleFrame, type EmbeddedConsolePath } from "./EmbeddedConsoleFrame";

const ORIGIN = "https://invoice.example.test";
const TITLE = "开票";

const EMBED_PATHS: readonly EmbeddedConsolePath[] = [
  "/embed/admin/sub2api",
  "/embed/admin/newapi",
  "/embed/admin/global",
];

function postMessageFrom(origin: string, data: unknown) {
  // window.dispatchEvent 不经过 React 的合成事件系统，手动包一层 act
  // 避免「state 更新没被 act 包住」的告警,也保证断言发生在更新落地之后
  act(() => {
    window.dispatchEvent(new MessageEvent("message", { origin, data }));
  });
}

describe("EmbeddedConsoleFrame", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("src 由 origin + path 拼出；不带 sandbox；带 allow 与 referrerPolicy", () => {
    render(<EmbeddedConsoleFrame origin={ORIGIN} path="/embed/admin/sub2api" title={TITLE} />);
    const frame = screen.getByTitle(TITLE) as HTMLIFrameElement;
    expect(frame.src).toBe(`${ORIGIN}/embed/admin/sub2api`);
    // 不能用 toBeNull() 判断"没有这个属性":jsdom 对未设置的布尔类属性可能
    // 返回 "" 而不是 null,直接断言 hasAttribute 更准确
    expect(frame.hasAttribute("sandbox")).toBe(false);
    expect(frame.getAttribute("allow")).toBe("clipboard-write");
    expect(frame.getAttribute("referrerpolicy")).toBe("strict-origin");
  });

  it.each(EMBED_PATHS)("入口路径 %s 拼出对应 src", (path) => {
    const { unmount } = render(<EmbeddedConsoleFrame origin={ORIGIN} path={path} title={TITLE} />);
    expect((screen.getByTitle(TITLE) as HTMLIFrameElement).src).toBe(`${ORIGIN}${path}`);
    unmount();
  });

  it("配置来源发来的合法高度消息：设置高度", () => {
    render(<EmbeddedConsoleFrame origin={ORIGIN} path="/embed/admin/sub2api" title={TITLE} />);
    const frame = screen.getByTitle(TITLE) as HTMLIFrameElement;
    postMessageFrom(ORIGIN, { type: "xm-embed", version: 1, kind: "height", height: 900 });
    expect(frame.style.height).toBe("900px");
  });

  it("不是配置里的来源：忽略，不改高度", () => {
    render(<EmbeddedConsoleFrame origin={ORIGIN} path="/embed/admin/sub2api" title={TITLE} />);
    const frame = screen.getByTitle(TITLE) as HTMLIFrameElement;
    postMessageFrom("https://evil.example.test", {
      type: "xm-embed",
      version: 1,
      kind: "height",
      height: 900,
    });
    expect(frame.style.height).toBe("");
  });

  const invalidMessages: readonly (readonly [string, unknown])[] = [
    ["version 不是 1", { type: "xm-embed", version: 2, kind: "height", height: 900 }],
    ["缺 type 字段", { version: 1, kind: "height", height: 900 }],
    ["kind 不是 height", { type: "xm-embed", version: 1, kind: "resize", height: 900 }],
    ["height 是字符串", { type: "xm-embed", version: 1, kind: "height", height: "900" }],
    ["height 是 NaN", { type: "xm-embed", version: 1, kind: "height", height: Number.NaN }],
    ["消息本体是 null", null],
    ["消息本体是字符串", "900"],
  ];

  it.each(invalidMessages)("形状不对的消息忽略——%s", (_label, data) => {
    render(<EmbeddedConsoleFrame origin={ORIGIN} path="/embed/admin/sub2api" title={TITLE} />);
    const frame = screen.getByTitle(TITLE) as HTMLIFrameElement;
    postMessageFrom(ORIGIN, data);
    expect(frame.style.height).toBe("");
  });

  it("高度按上下限钳制，不会被一条离谱的消息撑成没法用的长条", () => {
    render(<EmbeddedConsoleFrame origin={ORIGIN} path="/embed/admin/sub2api" title={TITLE} />);
    const frame = screen.getByTitle(TITLE) as HTMLIFrameElement;
    postMessageFrom(ORIGIN, { type: "xm-embed", version: 1, kind: "height", height: 10 });
    expect(frame.style.height).toBe("480px");
    postMessageFrom(ORIGIN, { type: "xm-embed", version: 1, kind: "height", height: 999_999 });
    expect(frame.style.height).toBe("4000px");
  });

  it("收到消息之前用视口高度兜底，不是写死的像素值", () => {
    render(<EmbeddedConsoleFrame origin={ORIGIN} path="/embed/admin/sub2api" title={TITLE} />);
    const frame = screen.getByTitle(TITLE) as HTMLIFrameElement;
    expect(frame.style.height).toBe("");
    expect(frame.className).toContain("h-[70vh]");
  });

  it("onLoad 触发后，就算超时窗口过去了也不判定失败", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    render(<EmbeddedConsoleFrame origin={ORIGIN} path="/embed/admin/sub2api" title={TITLE} />);
    fireEvent.load(screen.getByTitle(TITLE));
    await act(async () => {
      vi.advanceTimersByTime(20_000);
    });
    expect(screen.getByTitle(TITLE)).not.toBeNull();
    expect(screen.queryByText(/加载失败/)).toBeNull();
  });

  it("onError 立即显示失败态，带「在新窗口打开」的链接", () => {
    render(<EmbeddedConsoleFrame origin={ORIGIN} path="/embed/admin/sub2api" title={TITLE} />);
    fireEvent.error(screen.getByTitle(TITLE));
    expect(screen.getByText(/加载失败/)).not.toBeNull();
    const link = screen.getByRole("link", { name: "在新窗口打开" });
    expect(link.getAttribute("href")).toBe(`${ORIGIN}/embed/admin/sub2api`);
    expect(link.getAttribute("target")).toBe("_blank");
  });

  it("既不 load 也不 error：超时后判定失败（有些加载失败在浏览器里两个事件都不触发）", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    render(<EmbeddedConsoleFrame origin={ORIGIN} path="/embed/admin/sub2api" title={TITLE} />);
    await act(async () => {
      vi.advanceTimersByTime(15_000);
    });
    expect(screen.getByText(/加载失败/)).not.toBeNull();
  });

  describe("CR-0006 XM-INVCON1：断言 postMessage 投递与重新签发请求", () => {
    it("拿到断言时向 iframe 投递，targetOrigin 精确等于 origin", () => {
      render(
        <EmbeddedConsoleFrame
          origin={ORIGIN}
          path="/embed/admin/sub2api"
          title={TITLE}
          assertion={{ assertion: "jws-compact-value" }}
        />,
      );
      const frame = screen.getByTitle(TITLE) as HTMLIFrameElement;
      const postMessage = vi.spyOn(frame.contentWindow!, "postMessage");
      fireEvent.load(frame);
      expect(postMessage).toHaveBeenCalledWith(
        { type: "xm-embed", version: 1, kind: "admin-assertion", assertion: "jws-compact-value" },
        ORIGIN,
      );
    });

    it("assertion 为 null 时不投递任何消息", () => {
      render(
        <EmbeddedConsoleFrame origin={ORIGIN} path="/embed/admin/sub2api" title={TITLE} assertion={null} />,
      );
      const frame = screen.getByTitle(TITLE) as HTMLIFrameElement;
      const postMessage = vi.spyOn(frame.contentWindow!, "postMessage");
      fireEvent.load(frame);
      expect(postMessage).not.toHaveBeenCalled();
    });

    it("assertion 从无到有：对象恒等性变化即重新投递", () => {
      const { rerender } = render(
        <EmbeddedConsoleFrame origin={ORIGIN} path="/embed/admin/sub2api" title={TITLE} assertion={null} />,
      );
      const frame = screen.getByTitle(TITLE) as HTMLIFrameElement;
      const postMessage = vi.spyOn(frame.contentWindow!, "postMessage");
      rerender(
        <EmbeddedConsoleFrame
          origin={ORIGIN}
          path="/embed/admin/sub2api"
          title={TITLE}
          assertion={{ assertion: "second-value" }}
        />,
      );
      expect(postMessage).toHaveBeenCalledWith(
        { type: "xm-embed", version: 1, kind: "admin-assertion", assertion: "second-value" },
        ORIGIN,
      );
    });

    it("收到 kind=admin-assertion-needed 时回调 onAssertionNeeded", () => {
      const onAssertionNeeded = vi.fn();
      render(
        <EmbeddedConsoleFrame
          origin={ORIGIN}
          path="/embed/admin/sub2api"
          title={TITLE}
          onAssertionNeeded={onAssertionNeeded}
        />,
      );
      postMessageFrom(ORIGIN, { type: "xm-embed", version: 1, kind: "admin-assertion-needed" });
      expect(onAssertionNeeded).toHaveBeenCalledTimes(1);
    });

    it("admin-assertion-needed 消息来自非配置来源时忽略", () => {
      const onAssertionNeeded = vi.fn();
      render(
        <EmbeddedConsoleFrame
          origin={ORIGIN}
          path="/embed/admin/sub2api"
          title={TITLE}
          onAssertionNeeded={onAssertionNeeded}
        />,
      );
      postMessageFrom("https://evil.example.test", {
        type: "xm-embed",
        version: 1,
        kind: "admin-assertion-needed",
      });
      expect(onAssertionNeeded).not.toHaveBeenCalled();
    });

    it("未传 onAssertionNeeded 时收到该消息不报错，也不误判成高度消息", () => {
      render(<EmbeddedConsoleFrame origin={ORIGIN} path="/embed/admin/sub2api" title={TITLE} />);
      const frame = screen.getByTitle(TITLE) as HTMLIFrameElement;
      expect(() =>
        postMessageFrom(ORIGIN, { type: "xm-embed", version: 1, kind: "admin-assertion-needed" }),
      ).not.toThrow();
      expect(frame.style.height).toBe("");
    });

    it("高度同步消息不会被误判成断言重签发请求", () => {
      const onAssertionNeeded = vi.fn();
      render(
        <EmbeddedConsoleFrame
          origin={ORIGIN}
          path="/embed/admin/sub2api"
          title={TITLE}
          onAssertionNeeded={onAssertionNeeded}
        />,
      );
      postMessageFrom(ORIGIN, { type: "xm-embed", version: 1, kind: "height", height: 900 });
      expect(onAssertionNeeded).not.toHaveBeenCalled();
    });
  });

  it("切换 path 会重置失败态与高度，不带着上一轮的判定", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { rerender } = render(
      <EmbeddedConsoleFrame origin={ORIGIN} path="/embed/admin/sub2api" title={TITLE} />,
    );
    fireEvent.error(screen.getByTitle(TITLE));
    expect(screen.getByText(/加载失败/)).not.toBeNull();

    rerender(<EmbeddedConsoleFrame origin={ORIGIN} path="/embed/admin/newapi" title={TITLE} />);
    expect(screen.queryByText(/加载失败/)).toBeNull();
    expect((screen.getByTitle(TITLE) as HTMLIFrameElement).src).toBe(
      `${ORIGIN}/embed/admin/newapi`,
    );
  });
});
