import { cleanup, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { BlueprintTabView, blueprintForPlatform } from "../blueprints";
import { ServerDetailPage } from "./ServerDetailPage";

function renderPage(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/platforms/server/detail/:serverId" element={<ServerDetailPage />} />
      </Routes>
    </MemoryRouter>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
  cleanup();
});

describe("服务器资产详情 UI-only 蓝图", () => {
  it("已声明的 fixture ID 展示只读字段、返回入口与未接入状态，且不发请求", () => {
    const fetchMock = vi.fn(() => Promise.reject(new Error("详情蓝图不应调用 fetch")));
    vi.stubGlobal("fetch", fetchMock);

    renderPage("/platforms/server/detail/srv_sin_01");

    expect(screen.getByRole("heading", { name: "服务器资产详情", level: 2 })).not.toBeNull();
    expect(screen.getAllByText("srv_sin_01").length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText(/服务器详情蓝图：Server Agent 尚未接入/)).not.toBeNull();
    expect(screen.getByRole("link", { name: "返回服务器资产" }).getAttribute("href")).toBe(
      "/platforms/server?tab=assets",
    );
    expect(screen.getByText("访问与凭据")).not.toBeNull();
    expect(screen.getByText(/密码、私钥或 Token/)).not.toBeNull();
    expect(screen.queryAllByRole("button")).toHaveLength(0);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("未知 ID 显示统一 Not Found，而不是回退到另一台服务器", () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    const { queryByText, getByRole } = renderPage("/platforms/server/detail/not-a-server");

    expect(getByRole("heading", { name: "页面不存在", level: 2 })).not.toBeNull();
    expect(screen.getByText("没有这个服务器资产：not-a-server")).not.toBeNull();
    expect(queryByText("服务器资产详情")).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("服务器蓝图详情入口", () => {
  it("资产总览与资产明细保留空表体，同时提供只读详情深链", () => {
    const blueprint = blueprintForPlatform("server");
    for (const tabId of ["overview", "assets"]) {
      const tab = blueprint?.tabs.find((entry) => entry.id === tabId);
      expect(tab, `${tabId} 蓝图缺失`).toBeDefined();

      const { unmount } = render(<BlueprintTabView tab={tab!} />);
      const links = screen.getAllByRole("link", { name: /服务器详情 · srv_/ });
      expect(links.length).toBeGreaterThan(0);
      expect(links[0]?.getAttribute("href")).toBe("/platforms/server/detail/srv_sin_01");
      const tables = screen.getAllByRole("table");
      expect(tables.length).toBeGreaterThan(0);
      expect(within(tables[0]).queryAllByRole("row")).toHaveLength(1);
      unmount();
    }
  });
});
