import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ApiError, FeatureNotMountedError } from "../api/client";
import { ApiStateView } from "./ApiStateView";

const noop = () => {};
const CHILD = <p>真实数据</p>;

describe("ApiStateView", () => {
  it("加载中显示加载态，不泄漏还没到的数据", () => {
    render(
      <ApiStateView isPending error={null} onRetry={noop}>
        {CHILD}
      </ApiStateView>,
    );
    expect(screen.getByRole("status").textContent).toContain("加载中");
    expect(screen.queryByText("真实数据")).toBeNull();
  });

  it("既不加载也没出错时渲染子节点", () => {
    render(
      <ApiStateView isPending={false} error={null} onRetry={noop}>
        {CHILD}
      </ApiStateView>,
    );
    expect(screen.getByText("真实数据")).not.toBeNull();
  });

  it("出错时不渲染子节点——半截数据比没有数据更危险", () => {
    render(
      <ApiStateView isPending={false} error={new Error("boom")} onRetry={noop}>
        {CHILD}
      </ApiStateView>,
    );
    expect(screen.queryByText("真实数据")).toBeNull();
  });

  it("403 提示缺少的权限名", () => {
    render(
      <ApiStateView
        isPending={false}
        error={new ApiError(403, "PERMISSION_DENIED", "缺少权限 ops.read", "req-9")}
        onRetry={noop}
      >
        {CHILD}
      </ApiStateView>,
    );
    expect(screen.getByText("无权访问")).not.toBeNull();
    expect(screen.getByText(/ops\.read/)).not.toBeNull();
    // request_id 一并显示，报障时能对上服务端日志
    expect(screen.getByText(/req-9/)).not.toBeNull();
  });

  it("403 但没有权限名时显示后端原文（例如跨环境读取）", () => {
    render(
      <ApiStateView
        isPending={false}
        error={new ApiError(403, "PERMISSION_DENIED", "不允许跨环境读取：调用者身份属于 staging")}
        onRetry={noop}
      >
        {CHILD}
      </ApiStateView>,
    );
    expect(screen.getByText(/不允许跨环境读取/)).not.toBeNull();
  });

  it("网络不通显示可重试的错误态", () => {
    const onRetry = vi.fn();
    render(
      <ApiStateView
        isPending={false}
        error={new ApiError(0, "NETWORK_UNAVAILABLE", "无法连接平台 API")}
        onRetry={onRetry}
      >
        {CHILD}
      </ApiStateView>,
    );
    fireEvent.click(screen.getByRole("button", { name: "重试" }));
    expect(onRetry).toHaveBeenCalledOnce();
  });

  it("400 这类不可重试的错误不给重试按钮，但要带上错误码", () => {
    render(
      <ApiStateView
        isPending={false}
        error={new ApiError(400, "INVALID_PARAMS", "environment 必须是三个值之一")}
        onRetry={noop}
      >
        {CHILD}
      </ApiStateView>,
    );
    expect(screen.getByText(/INVALID_PARAMS/)).not.toBeNull();
    expect(screen.queryByRole("button", { name: "重试" })).toBeNull();
  });

  it("非 ApiError 的意外异常也有兜底文案", () => {
    render(
      <ApiStateView isPending={false} error={new Error("boom")} onRetry={noop}>
        {CHILD}
      </ApiStateView>,
    );
    expect(screen.getByText("boom")).not.toBeNull();
  });

  it("FeatureNotMountedError 显示「未接入」而不是失败/错误，且不给重试按钮（XM-UX-OFFSTATE）", () => {
    const cause = new ApiError(404, "UNKNOWN", "请求失败（HTTP 404）");
    render(
      <ApiStateView
        isPending={false}
        error={
          new FeatureNotMountedError(
            cause,
            "用户管理在当前环境未启用（XM_PLATFORM_USERS_MODE=off）。接入真实用户数据源后会自动出现，无需手动开启。",
          )
        }
        onRetry={noop}
      >
        {CHILD}
      </ApiStateView>,
    );
    expect(screen.getByText("未接入")).not.toBeNull();
    expect(screen.getByText(/XM_PLATFORM_USERS_MODE=off/)).not.toBeNull();
    // 不该出现「失败/错误」字样，也不该给一个点了也没用的重试按钮
    expect(screen.queryByText(/失败|错误/)).toBeNull();
    expect(screen.queryByRole("button", { name: "重试" })).toBeNull();
    expect(screen.queryByText("真实数据")).toBeNull();
  });
});
