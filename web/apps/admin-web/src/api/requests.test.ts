import { describe, expect, it, vi } from "vitest";
import type { ApiClient } from "./client";
import {
  getPlatformRequestContent,
  listPlatformRequests,
  REQUEST_PAGE_SIZE,
} from "./requests";

function fakeClient(body: unknown): ApiClient {
  return {
    get: vi.fn().mockResolvedValue(body),
    post: vi.fn().mockResolvedValue(body),
  } as unknown as ApiClient;
}

const freshness = {
  state: "fresh",
  staleness_seconds: 0,
  threshold_seconds: 60,
  is_partial: false,
  observed_at: "2026-08-28T09:00:00Z",
  last_success: "2026-08-28T09:00:00Z",
  last_error_code: "",
};

describe("listPlatformRequests", () => {
  it("items 为 null 时按空数组处理，页面不会炸", async () => {
    const page = await listPlatformRequests(
      "sub2api",
      {},
      fakeClient({ items: null, freshness }),
    );
    expect(page.items).toEqual([]);
  });

  it("空筛选项不进 URL——「不传」和「传空串」对后端不是一回事", async () => {
    const client = fakeClient({ items: [], freshness });
    await listPlatformRequests("sub2api", { username: "  ", model: "", status: "" }, client);
    expect(client.get).toHaveBeenCalledWith("/api/v1/platforms/sub2api/requests", {
      searchParams: {
        limit: String(REQUEST_PAGE_SIZE),
        cursor: undefined,
        username: undefined,
        model: undefined,
        status: undefined,
        since: undefined,
        until: undefined,
      },
    });
  });

  it("填了的筛选项原样传下去", async () => {
    const client = fakeClient({ items: [], freshness });
    await listPlatformRequests(
      "newapi",
      {
        username: "li.na",
        model: "gpt-4o",
        status: "error",
        since: "2026-08-28T00:00:00Z",
        until: "2026-08-29T00:00:00Z",
        cursor: "reqlog:25",
        limit: 25,
      },
      client,
    );
    expect(client.get).toHaveBeenCalledWith("/api/v1/platforms/newapi/requests", {
      searchParams: {
        limit: "25",
        cursor: "reqlog:25",
        username: "li.na",
        model: "gpt-4o",
        status: "error",
        since: "2026-08-28T00:00:00Z",
        until: "2026-08-29T00:00:00Z",
      },
    });
  });

  it("平台名进路径前先转义", async () => {
    const client = fakeClient({ items: [], freshness });
    await listPlatformRequests("a/b", {}, client);
    expect(client.get).toHaveBeenCalledWith(
      "/api/v1/platforms/a%2Fb/requests",
      expect.anything(),
    );
  });

  it("next_cursor 缺失或为空都表示翻到底了", async () => {
    const withCursor = await listPlatformRequests(
      "sub2api",
      {},
      fakeClient({ items: [], next_cursor: "reqlog:50", freshness }),
    );
    expect(withCursor.nextCursor).toBe("reqlog:50");

    const atEnd = await listPlatformRequests("sub2api", {}, fakeClient({ items: [], freshness }));
    expect(atEnd.nextCursor).toBe("");
  });

  it("保留期与来源透传上来——界面靠它们说清「只覆盖 N 天」和「这是不是演示数据」", async () => {
    const page = await listPlatformRequests(
      "sub2api",
      {},
      fakeClient({ items: [], retention_days: 30, data_source: "reqlog-fake", freshness }),
    );
    expect(page.retentionDays).toBe(30);
    expect(page.dataSource).toBe("reqlog-fake");
  });
});

describe("getPlatformRequestContent", () => {
  const content = {
    summary: { id: "20260828-000117" },
    messages: [{ role: "user", content: "hi", truncated: false, original_bytes: 2 }],
    messages_parsed: true,
    final_reply: "hello",
    final_reply_truncated: false,
    final_reply_bytes: 5,
    raw_request: { body: "{}", truncated: false, original_bytes: 2, content_type: "application/json" },
    raw_response: { body: "{}", truncated: false, original_bytes: 2, content_type: "application/json" },
    data_source: "reqlog-fake",
    freshness,
  };

  it("路径里的平台与记录 id 都转义", async () => {
    const client = fakeClient(content);
    await getPlatformRequestContent("sub2api", "2026/08 28", {}, client);
    expect(client.get).toHaveBeenCalledWith(
      "/api/v1/platforms/sub2api/requests/2026%2F08%2028",
      expect.anything(),
    );
  });

  it("reason 为空时不进 URL", async () => {
    const client = fakeClient(content);
    await getPlatformRequestContent("sub2api", "x", { reason: "  " }, client);
    expect(client.get).toHaveBeenCalledWith("/api/v1/platforms/sub2api/requests/x", {
      searchParams: { reason: undefined },
    });
  });

  it("reason 填了就带上——它会写进审计事件", async () => {
    const client = fakeClient(content);
    await getPlatformRequestContent("sub2api", "x", { reason: "客诉核查" }, client);
    expect(client.get).toHaveBeenCalledWith("/api/v1/platforms/sub2api/requests/x", {
      searchParams: { reason: "客诉核查" },
    });
  });

  it("messages 为 null 时按空数组处理", async () => {
    const body = await getPlatformRequestContent(
      "sub2api",
      "x",
      {},
      fakeClient({ ...content, messages: null }),
    );
    expect(body.messages).toEqual([]);
  });
});
