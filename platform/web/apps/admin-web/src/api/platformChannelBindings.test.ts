import { describe, expect, it, vi } from "vitest";
import type { ApiClient } from "./client";
import {
  channelBindingHistoryStateNote,
  getChannelBindingHistory,
} from "./platformChannelBindings";

/** 依次返回若干页的 client 替身。`get` 的调用记录本身就是断言对象——
 *  这一片要钉的第一件事就是**请求参数名逐字**（服务端 channel_bindings.go
 *  读的是 `service_id` / `include_history` / `limit` / `cursor`）。 */
function pagedClient(...pages: unknown[]): ApiClient {
  const get = vi.fn();
  for (const page of pages) get.mockResolvedValueOnce(page);
  return { get, post: vi.fn() } as unknown as ApiClient;
}

function binding(over: Record<string, unknown> = {}) {
  return {
    id: "bind-2",
    upstream_account_id: "up-2",
    valid_from: "2026-09-01T02:00:00Z",
    provenance: "manual",
    reason: "改绑到甲供应商",
    created_by: "staff:alice",
    ...over,
  };
}

const PATH = "/api/v1/finance/platform-channel-bindings";

describe("渠道绑定历史 Query（此前前端零调用）", () => {
  it("请求参数逐字：service_id / include_history=true / limit=200，第一页不带 cursor", async () => {
    const api = pagedClient({ items: [], next_cursor: null });
    await getChannelBindingHistory({ serviceId: "svc-1", externalChannelId: "channel-a" }, {}, api);
    expect(api.get).toHaveBeenCalledWith(PATH, {
      searchParams: { service_id: "svc-1", include_history: "true", limit: "200" },
    });
  });

  it("signal 透传下去；不传就不出现在 options 里", async () => {
    const controller = new AbortController();
    const api = pagedClient({ items: [], next_cursor: null });
    await getChannelBindingHistory(
      { serviceId: "svc-1", externalChannelId: "channel-a" },
      { signal: controller.signal },
      api,
    );
    expect(api.get).toHaveBeenCalledWith(PATH, {
      searchParams: { service_id: "svc-1", include_history: "true", limit: "200" },
      signal: controller.signal,
    });
  });

  it("按 channel_ref.external_channel_id 挑出目标那一条，history 顺序原样保留", async () => {
    const api = pagedClient({
      items: [
        {
          channel_ref: { service_id: "svc-1", external_channel_id: "channel-b" },
          channel_name: "别的渠道",
          binding: null,
          history: [binding({ id: "other-1", upstream_account_id: "up-9" })],
        },
        {
          channel_ref: { service_id: "svc-1", external_channel_id: "channel-a" },
          channel_name: "OpenAI A",
          binding: binding(),
          history: [
            binding(),
            binding({
              id: "bind-1",
              upstream_account_id: "up-1",
              valid_from: "2026-08-20T00:00:00Z",
              provenance: "token_map_backfill",
              reason: "首次确认",
              created_by: "staff:bob",
            }),
          ],
        },
      ],
      next_cursor: null,
    });

    const result = await getChannelBindingHistory(
      { serviceId: "svc-1", externalChannelId: "channel-a" },
      {},
      api,
    );

    expect(result.state).toBe("ok");
    expect(result.currentBindingId).toBe("bind-2");
    // 服务端是 ORDER BY valid_from DESC, id DESC——最新的在前，前端不重排
    expect(result.history.map((r) => r.id)).toEqual(["bind-2", "bind-1"]);
    expect(result.history[1]).toEqual({
      id: "bind-1",
      upstreamAccountId: "up-1",
      validFrom: "2026-08-20T00:00:00Z",
      provenance: "token_map_backfill",
      reason: "首次确认",
      createdBy: "staff:bob",
    });
  });

  it("binding 为 null 时 currentBindingId 是 null，但历史照常返回（解绑之后就是这种形态）", async () => {
    const api = pagedClient({
      items: [
        {
          channel_ref: { external_channel_id: "channel-a" },
          binding: null,
          history: [binding({ id: "bind-1" })],
        },
      ],
      next_cursor: null,
    });
    const result = await getChannelBindingHistory(
      { serviceId: "svc-1", externalChannelId: "channel-a" },
      {},
      api,
    );
    expect(result.currentBindingId).toBeNull();
    expect(result.history.map((r) => r.id)).toEqual(["bind-1"]);
  });

  it("history 键缺失（服务端 omitempty）按空数组处理，state 仍是 ok", async () => {
    const api = pagedClient({
      items: [{ channel_ref: { external_channel_id: "channel-a" }, binding: null }],
      next_cursor: null,
    });
    const result = await getChannelBindingHistory(
      { serviceId: "svc-1", externalChannelId: "channel-a" },
      {},
      api,
    );
    expect(result.state).toBe("ok");
    expect(result.history).toEqual([]);
  });

  it("目标不在第一页时按服务端给的 next_cursor 翻页，游标原样回传（不自己拼）", async () => {
    const api = pagedClient(
      {
        items: [{ channel_ref: { external_channel_id: "channel-a" }, binding: null, history: [] }],
        next_cursor: "b3BhcXVl",
      },
      {
        items: [
          {
            channel_ref: { external_channel_id: "channel-z" },
            binding: binding({ id: "bind-7" }),
            history: [binding({ id: "bind-7" })],
          },
        ],
        next_cursor: null,
      },
    );

    const result = await getChannelBindingHistory(
      { serviceId: "svc-1", externalChannelId: "channel-z" },
      {},
      api,
    );

    expect(result.state).toBe("ok");
    expect(result.pagesFetched).toBe(2);
    expect(api.get).toHaveBeenNthCalledWith(2, PATH, {
      searchParams: {
        service_id: "svc-1",
        include_history: "true",
        limit: "200",
        cursor: "b3BhcXVl",
      },
    });
  });

  it("翻完（没有 next_cursor）仍没找到 = channel_absent，不是「没有历史」", async () => {
    const api = pagedClient({
      items: [{ channel_ref: { external_channel_id: "channel-b" }, binding: null, history: [] }],
      next_cursor: null,
    });
    const result = await getChannelBindingHistory(
      { serviceId: "svc-1", externalChannelId: "channel-a" },
      {},
      api,
    );
    expect(result.state).toBe("channel_absent");
    expect(result.history).toEqual([]);
    // 说明文案要指名道姓地说是哪条渠道，否则人不知道是不是自己看错了页面
    expect(channelBindingHistoryStateNote(result, "channel-a")).toContain(
      "绑定候选清单里没有渠道 channel-a",
    );
  });

  it("服务端一直给 next_cursor 时在第 5 页停下（truncated），不无限翻", async () => {
    const page = { items: [], next_cursor: "next" };
    const api = pagedClient(page, page, page, page, page, page, page);
    const result = await getChannelBindingHistory(
      { serviceId: "svc-1", externalChannelId: "channel-a" },
      {},
      api,
    );
    expect(result.state).toBe("truncated");
    expect(result.pagesFetched).toBe(5);
    expect(api.get).toHaveBeenCalledTimes(5);
    expect(channelBindingHistoryStateNote(result, "channel-a")).toContain("翻了 5 页（每页 200 条）");
  });
});
