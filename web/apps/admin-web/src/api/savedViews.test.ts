import { describe, expect, it, vi } from "vitest";
import type { SavedViewStateV1 } from "@xingmang/ui-admin";
import type { ApiClient } from "./client";
import {
  listSavedViews,
  removeSavedView,
  setSavedView,
} from "./savedViews";

function fakeClient(body: unknown): ApiClient {
  return {
    get: vi.fn().mockResolvedValue(body),
    post: vi.fn().mockResolvedValue(body),
  } as unknown as ApiClient;
}

const state: SavedViewStateV1 = {
  schema_version: 1,
  query: "openai",
  filters: { status: "需关注" },
  sort: { column_id: "grossProfit", direction: "desc" },
  columns: { known: ["name", "status", "grossProfit"], visible: ["name", "status"] },
  density: "compact",
};

describe("SavedView API", () => {
  it("Query 只传 table_key，特殊字符由统一客户端编码", async () => {
    const client = fakeClient({ items: null });
    await expect(listSavedViews("platform.sub2api.channels/测试", {}, client)).resolves.toEqual([]);
    expect(client.get).toHaveBeenCalledWith("/api/v1/ui/saved-views", {
      searchParams: { table_key: "platform.sub2api.channels/测试" },
    });
  });

  it("set 走 ui.saved_view.set@1，v1 状态展平成既有 Schema 原语", async () => {
    const client = fakeClient({ action_run_id: "run-set-1", result: { id: "view-1" } });
    const run = await setSavedView(
      { tableKey: "platform.sub2api.channels", name: "需关注", state },
      {},
      client,
    );
    expect(run.runId).toBe("run-set-1");
    const call = (client.post as ReturnType<typeof vi.fn>).mock.calls[0];
    expect(call?.[0]).toBe("/api/v1/actions/ui.saved_view.set/versions/1/execute");
    expect(call?.[1]).toEqual({
      params: {
        table_key: "platform.sub2api.channels",
        name: "需关注",
        schema_version: 1,
        query: "openai",
        filters_json: `{"status":"需关注"}`,
        sort_column: "grossProfit",
        sort_direction: "desc",
        known_columns: ["name", "status", "grossProfit"],
        visible_columns: ["name", "status"],
        density: "compact",
      },
    });
    const params = (call?.[1] as { params: Record<string, unknown> }).params;
    for (const forbidden of ["owner_issuer", "owner_subject", "identity_zone", "environment"]) {
      expect(params).not.toHaveProperty(forbidden);
    }
  });

  it("无排序时两个排序字段都传空串，不能出现半对", async () => {
    const client = fakeClient({ action_run_id: "run-set-2" });
    await setSavedView(
      { tableKey: "global.audit.events", name: "默认列", state: { ...state, sort: null } },
      {},
      client,
    );
    const body = (client.post as ReturnType<typeof vi.fn>).mock.calls[0]?.[1] as {
      params: Record<string, unknown>;
    };
    expect(body.params.sort_column).toBe("");
    expect(body.params.sort_direction).toBe("");
  });

  it("remove 走 ui.saved_view.remove@1，只传 UUID", async () => {
    const client = fakeClient({ action_run_id: "run-remove-1" });
    const run = await removeSavedView("view-1", {}, client);
    expect(run.runId).toBe("run-remove-1");
    const call = (client.post as ReturnType<typeof vi.fn>).mock.calls[0];
    expect(call?.[0]).toBe("/api/v1/actions/ui.saved_view.remove/versions/1/execute");
    expect(call?.[1]).toEqual({ params: { saved_view_id: "view-1" } });
  });
});
