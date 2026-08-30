import { describe, expect, it, vi } from "vitest";
import { getActionRun, listActionDefinitions, listActionRuns } from "./actions";
import type { ApiClient } from "./client";

function fakeClient(body: unknown): ApiClient {
  return {
    get: vi.fn().mockResolvedValue(body),
    post: vi.fn().mockResolvedValue(body),
  } as unknown as ApiClient;
}

describe("listActionDefinitions", () => {
  it("items 为 null 时按空数组处理，页面不会炸", async () => {
    const client = fakeClient({ items: null });
    await expect(listActionDefinitions({}, client)).resolves.toEqual([]);
  });

  it("environments / principal_types 为 null 时按空数组处理，blocked_reason 缺省为空串", async () => {
    const client = fakeClient({
      items: [
        {
          id: "registry.service.create",
          version: "1",
          risk_level: "L1",
          permission: "registry.service.manage",
          environments: null,
          principal_types: null,
          executable: true,
        },
      ],
    });
    const items = await listActionDefinitions({}, client);
    expect(items).toEqual([
      {
        id: "registry.service.create",
        version: "1",
        risk_level: "L1",
        permission: "registry.service.manage",
        environments: [],
        principal_types: [],
        executable: true,
        blocked_reason: "",
      },
    ]);
  });

  it("不带查询参数：目录不按权限过滤，真正的授权判定在执行时", async () => {
    const client = fakeClient({ items: [] });
    await listActionDefinitions({}, client);
    expect(client.get).toHaveBeenCalledWith("/api/v1/actions", {});
  });
});

describe("listActionRuns", () => {
  it("不传过滤条件时不带对应查询参数（后端只按 Principal 环境过滤）", async () => {
    const client = fakeClient({ items: [] });
    await listActionRuns({}, client);
    expect(client.get).toHaveBeenCalledWith("/api/v1/actions/runs", {
      searchParams: {
        action_id: undefined,
        status: undefined,
        principal: undefined,
        limit: undefined,
        cursor: undefined,
      },
    });
  });

  it("过滤条件与 cursor 原样透传", async () => {
    const client = fakeClient({ items: [], next_cursor: "" });
    await listActionRuns(
      { actionId: "registry.service.create", status: "failed", principal: "staff_bob", limit: 20, cursor: "abc" },
      client,
    );
    expect(client.get).toHaveBeenCalledWith("/api/v1/actions/runs", {
      searchParams: {
        action_id: "registry.service.create",
        status: "failed",
        principal: "staff_bob",
        limit: "20",
        cursor: "abc",
      },
    });
  });

  it("items 为 null 时按空数组处理；next_cursor 缺省为空串（表示已到底）", async () => {
    const client = fakeClient({ items: null });
    await expect(listActionRuns({}, client)).resolves.toEqual({ items: [], nextCursor: "" });
  });

  it("满页时透传 next_cursor，供下一页原样带回", async () => {
    const client = fakeClient({ items: [], next_cursor: "opaque-token" });
    await expect(listActionRuns({}, client)).resolves.toEqual({ items: [], nextCursor: "opaque-token" });
  });

  it("空白 status/principal 视同不传（不是「传了空字符串」）", async () => {
    const client = fakeClient({ items: [] });
    await listActionRuns({ status: "", principal: "   " }, client);
    expect(client.get).toHaveBeenCalledWith("/api/v1/actions/runs", {
      searchParams: {
        action_id: undefined,
        status: undefined,
        principal: undefined,
        limit: undefined,
        cursor: undefined,
      },
    });
  });
});

describe("getActionRun", () => {
  it("按 run_id 编码请求详情端点", async () => {
    const detail = {
      run: {
        id: "r1",
        action_id: "registry.service.create",
        action_version: "1",
        principal_id: "staff_alice",
        principal_type: "HUMAN",
        environment: "development",
        request_id: "req-1",
        risk_level: "L1",
        status: "succeeded",
        error_code: "",
        duration_ms: 12,
        started_at: "2026-08-29T10:00:00Z",
        finished_at: "2026-08-29T10:00:00.012Z",
      },
      audit: null,
    };
    const client = fakeClient(detail);
    await expect(getActionRun("r1 with space", {}, client)).resolves.toEqual(detail);
    expect(client.get).toHaveBeenCalledWith("/api/v1/actions/runs/r1%20with%20space", {});
  });
});
