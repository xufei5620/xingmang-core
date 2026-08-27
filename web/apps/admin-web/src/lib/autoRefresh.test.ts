import { renderHook } from "@testing-library/react";
import { act } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { OVERVIEW_POLL_INTERVAL_MS, shouldPoll, useAutoRefresh } from "./autoRefresh";

/** jsdom 的 document.visibilityState 是只读的，测试里只能改属性描述符。 */
function setVisibility(state: DocumentVisibilityState) {
  Object.defineProperty(document, "visibilityState", { value: state, configurable: true });
}

function fireVisibilityChange() {
  act(() => {
    document.dispatchEvent(new Event("visibilitychange"));
  });
}

describe("shouldPoll", () => {
  it("只有页面可见时才拉", () => {
    expect(shouldPoll("visible")).toBe(true);
    expect(shouldPoll("hidden")).toBe(false);
    expect(shouldPoll(undefined)).toBe(false);
  });
});

describe("useAutoRefresh", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    setVisibility("visible");
  });
  afterEach(() => {
    vi.useRealTimers();
    setVisibility("visible");
  });

  it("页面可见时每隔一个周期回调一次", () => {
    const onRefresh = vi.fn();
    renderHook(() => useAutoRefresh(onRefresh, 1000));

    expect(onRefresh).not.toHaveBeenCalled(); // 挂载时不额外拉一次，首帧由 useQuery 负责
    act(() => void vi.advanceTimersByTime(1000));
    expect(onRefresh).toHaveBeenCalledTimes(1);
    act(() => void vi.advanceTimersByTime(2000));
    expect(onRefresh).toHaveBeenCalledTimes(3);
  });

  it("页面不可见时定时器照跑但不拉数据——没人在看的界面刷新给谁看", () => {
    const onRefresh = vi.fn();
    renderHook(() => useAutoRefresh(onRefresh, 1000));

    setVisibility("hidden");
    act(() => void vi.advanceTimersByTime(5000));
    expect(onRefresh).not.toHaveBeenCalled();

    setVisibility("visible");
    act(() => void vi.advanceTimersByTime(1000));
    expect(onRefresh).toHaveBeenCalledTimes(1);
  });

  it("切回前台立刻补一次，人看到的第一眼就是新数据", () => {
    const onRefresh = vi.fn();
    renderHook(() => useAutoRefresh(onRefresh, 1000));

    setVisibility("hidden");
    fireVisibilityChange();
    expect(onRefresh).not.toHaveBeenCalled();

    setVisibility("visible");
    fireVisibilityChange();
    expect(onRefresh).toHaveBeenCalledTimes(1);
  });

  it("卸载后不再回调，也不留下定时器", () => {
    const onRefresh = vi.fn();
    const { unmount } = renderHook(() => useAutoRefresh(onRefresh, 1000));
    unmount();

    act(() => void vi.advanceTimersByTime(10_000));
    fireVisibilityChange();
    expect(onRefresh).not.toHaveBeenCalled();
  });

  it("回调换了新引用也不会重建定时器（否则永远等不到一个完整周期）", () => {
    const first = vi.fn();
    const second = vi.fn();
    const { rerender } = renderHook(({ cb }: { cb: () => void }) => useAutoRefresh(cb, 1000), {
      initialProps: { cb: first },
    });

    act(() => void vi.advanceTimersByTime(600));
    rerender({ cb: second });
    act(() => void vi.advanceTimersByTime(400));

    // 周期没有被重置：第 1000ms 照常触发，且用的是最新的回调
    expect(first).not.toHaveBeenCalled();
    expect(second).toHaveBeenCalledTimes(1);
  });

  it("间隔为 0 表示关掉自动刷新", () => {
    const onRefresh = vi.fn();
    renderHook(() => useAutoRefresh(onRefresh, 0));
    act(() => void vi.advanceTimersByTime(60_000));
    fireVisibilityChange();
    expect(onRefresh).not.toHaveBeenCalled();
  });

  it("默认间隔是 60 秒", () => {
    expect(OVERVIEW_POLL_INTERVAL_MS).toBe(60_000);
    const onRefresh = vi.fn();
    renderHook(() => useAutoRefresh(onRefresh));
    act(() => void vi.advanceTimersByTime(59_000));
    expect(onRefresh).not.toHaveBeenCalled();
    act(() => void vi.advanceTimersByTime(1000));
    expect(onRefresh).toHaveBeenCalledTimes(1);
  });
});
