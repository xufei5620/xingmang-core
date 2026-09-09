import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { SavedViewStateV1 } from "@xingmang/ui-admin";
import { ApiError } from "../api/client";

const api = vi.hoisted(() => ({
  listSavedViews: vi.fn(),
  setSavedView: vi.fn(),
  removeSavedView: vi.fn(),
}));

vi.mock("../api/savedViews", async () => {
  const actual = await vi.importActual<typeof import("../api/savedViews")>("../api/savedViews");
  return { ...actual, ...api };
});

import { savedViewQueryKey, useSavedViews } from "./useSavedViews";

const state: SavedViewStateV1 = {
  schema_version: 1,
  query: "",
  filters: {},
  sort: null,
  columns: { known: ["name"], visible: ["name"] },
  density: "compact",
};

function setup() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>{children}</QueryClientProvider>
  );
  return { client, wrapper };
}

describe("useSavedViews", () => {
  afterEach(() => vi.clearAllMocks());

  it("每个 table_key 使用独立 Query cache", async () => {
    api.listSavedViews.mockResolvedValue([]);
    const { client, wrapper } = setup();
    const one = renderHook(() => useSavedViews("global.audit.events"), { wrapper });
    const two = renderHook(() => useSavedViews("platform.sub2api.channels"), { wrapper });
    await waitFor(() => expect(one.result.current.status).toBe("ready"));
    await waitFor(() => expect(two.result.current.status).toBe("ready"));
    expect(api.listSavedViews).toHaveBeenCalledWith("global.audit.events", expect.any(Object));
    expect(api.listSavedViews).toHaveBeenCalledWith("platform.sub2api.channels", expect.any(Object));
    expect(client.getQueryState(savedViewQueryKey("global.audit.events"))).toBeTruthy();
    expect(client.getQueryState(savedViewQueryKey("platform.sub2api.channels"))).toBeTruthy();
  });

  it("set 成功后只失效当前表，并返回 action run id", async () => {
    api.listSavedViews.mockResolvedValue([]);
    api.setSavedView.mockResolvedValue({ runId: "run-set-1", result: {} });
    const { client, wrapper } = setup();
    const invalidate = vi.spyOn(client, "invalidateQueries");
    const hook = renderHook(() => useSavedViews("global.audit.events"), { wrapper });
    await waitFor(() => expect(hook.result.current.status).toBe("ready"));

    let outcome: { runId: string } | undefined;
    await act(async () => {
      outcome = await hook.result.current.save("我的视图", state);
    });
    expect(outcome).toEqual({ runId: "run-set-1" });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: savedViewQueryKey("global.audit.events"), exact: true });
    expect(invalidate).not.toHaveBeenCalledWith({ queryKey: savedViewQueryKey("platform.sub2api.channels"), exact: true });
  });

  it("Action 返回前不把项目乐观插入耐久列表", async () => {
    api.listSavedViews.mockResolvedValue([]);
    let resolveAction: ((value: { runId: string; result: unknown }) => void) | undefined;
    api.setSavedView.mockImplementation(
      () => new Promise((resolve) => { resolveAction = resolve; }),
    );
    const { wrapper } = setup();
    const hook = renderHook(() => useSavedViews("global.audit.events"), { wrapper });
    await waitFor(() => expect(hook.result.current.status).toBe("ready"));

    let promise: Promise<{ runId: string }> | undefined;
    act(() => { promise = hook.result.current.save("未完成", state); });
    expect(hook.result.current.items).toEqual([]);
    await waitFor(() => expect(hook.result.current.saving).toBe(true));
    resolveAction?.({ runId: "run-later", result: {} });
    await act(async () => { await promise; });
  });

  it("Query 失败仍返回空个人组，供适配器保留内置视图", async () => {
    api.listSavedViews.mockRejectedValue(new ApiError(500, "INTERNAL", "读取失败"));
    const { wrapper } = setup();
    const hook = renderHook(() => useSavedViews("global.audit.events"), { wrapper });
    await waitFor(() => expect(hook.result.current.status).toBe("error"));
    expect(hook.result.current.items).toEqual([]);
    expect(hook.result.current.message).toMatch(/读取失败/);
  });

  it("403 显示缺少专用 scope，不提供伪持久化回退", async () => {
    api.listSavedViews.mockRejectedValue(
      new ApiError(403, "PERMISSION_DENIED", "缺少权限 ui.saved_view.manage"),
    );
    const { wrapper } = setup();
    const hook = renderHook(() => useSavedViews("global.audit.events"), { wrapper });
    await waitFor(() => expect(hook.result.current.status).toBe("denied"));
    expect(hook.result.current.message).toContain("ui.saved_view.manage");
    expect(hook.result.current.items).toEqual([]);
  });
});
