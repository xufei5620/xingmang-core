import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChannelDetailPage } from "./ChannelDetailPage";
import { SupplierCreatePage } from "./SupplierCreatePage";
import { UpstreamDetailPage } from "./UpstreamDetailPage";

function renderPage(path: string, element: React.ReactElement, route: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path={route} element={element} />
      </Routes>
    </MemoryRouter>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("渠道 / 上游详情 UI-only 深链", () => {
  it("渠道详情只展示平台、ID 与未接入字段，不发 API、不提供写操作", () => {
    const fetchMock = vi.fn(() => Promise.reject(new Error("detail shell must not fetch")));
    vi.stubGlobal("fetch", fetchMock);

    renderPage(
      "/platforms/sub2api/upstream/detail/channel-a",
      <ChannelDetailPage />,
      "/platforms/:serviceType/upstream/detail/:channelId",
    );

    expect(screen.getByRole("heading", { name: "渠道详情", level: 2 })).not.toBeNull();
    expect(screen.getAllByText("channel-a").length).toBeGreaterThanOrEqual(2);
    expect(screen.getAllByText("Sub2API").length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText(/只读结构预览：渠道详情读契约尚未接入/)).not.toBeNull();
    expect(screen.getByText("上游分组")).not.toBeNull();
    expect(screen.getAllByText("未接入").length).toBeGreaterThan(5);
    expect(screen.getByRole("link", { name: "返回 Sub2API 渠道管理" }).getAttribute("href")).toBe(
      "/platforms/sub2api?tab=upstream",
    );
    expect(screen.queryAllByRole("button")).toHaveLength(0);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("未知平台与空语义不渲染为另一平台的详情", () => {
    renderPage(
      "/platforms/cpa/upstream/detail/channel-a",
      <ChannelDetailPage />,
      "/platforms/:serviceType/upstream/detail/:channelId",
    );
    expect(screen.getByRole("heading", { name: "页面不存在", level: 2 })).not.toBeNull();
    expect(screen.getByText(/平台 cpa 不支持渠道详情/)).not.toBeNull();
    expect(screen.queryByRole("heading", { name: "渠道详情", level: 2 })).toBeNull();
  });

  it("上游详情明确展示比例、余额、成本和凭据边界，并提供返回入口", () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    renderPage(
      "/platforms/newapi/suppliers/upstream-a",
      <UpstreamDetailPage />,
      "/platforms/:serviceType/suppliers/:upstreamId",
    );

    expect(screen.getByRole("heading", { name: "上游详情", level: 2 })).not.toBeNull();
    expect(screen.getAllByText("upstream-a").length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText("充值比例与成本口径")).not.toBeNull();
    expect(screen.getByText("上游全部分组")).not.toBeNull();
    expect(screen.getByText(/上游账号密码、API Key 与 Token 永不回显/)).not.toBeNull();
    expect(screen.getByRole("link", { name: "返回 NewAPI 上游管理" }).getAttribute("href")).toBe(
      "/platforms/newapi?tab=suppliers",
    );
    expect(screen.queryAllByRole("button")).toHaveLength(0);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("`suppliers/new` 是只读字段蓝图，所有输入禁用且没有提交按钮", () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    renderPage(
      "/platforms/sub2api/suppliers/new",
      <SupplierCreatePage />,
      "/platforms/:serviceType/suppliers/new",
    );

    expect(screen.getByRole("heading", { name: "添加上游", level: 2 })).not.toBeNull();
    expect(screen.getByText(/只读表单蓝图：当前不会保存、提交或调用任何 Action/)).not.toBeNull();
    expect(screen.getAllByRole("textbox").length).toBeGreaterThan(0);
    expect(screen.getAllByRole("textbox").every((input) => (input as HTMLInputElement).disabled)).toBe(true);
    expect(screen.getByRole("link", { name: "返回 Sub2API 上游管理" }).getAttribute("href")).toBe(
      "/platforms/sub2api?tab=suppliers",
    );
    expect(screen.queryAllByRole("button")).toHaveLength(0);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
