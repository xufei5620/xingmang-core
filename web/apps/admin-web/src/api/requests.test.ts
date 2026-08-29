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

const stats = {
  request_count: 0,
  success_count: 0,
  failure_count: 0,
  average_duration_ms: null,
};

describe("listPlatformRequests", () => {
  it("items 为 null 时按空数组处理，页面不会炸", async () => {
    const page = await listPlatformRequests(
      "sub2api",
      {},
      fakeClient({ items: null, stats, freshness }),
    );
    expect(page.items).toEqual([]);
  });

  it("空筛选项不进 URL——「不传」和「传空串」对后端不是一回事", async () => {
    const client = fakeClient({ items: [], stats, freshness });
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
    const client = fakeClient({ items: [], stats, freshness });
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
    const client = fakeClient({ items: [], stats, freshness });
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
      fakeClient({ items: [], next_cursor: "reqlog:50", stats, freshness }),
    );
    expect(withCursor.nextCursor).toBe("reqlog:50");

    const atEnd = await listPlatformRequests("sub2api", {}, fakeClient({ items: [], stats, freshness }));
    expect(atEnd.nextCursor).toBe("");
  });

  it("保留期与来源透传上来——界面靠它们说清「只覆盖 N 天」和「这是不是演示数据」", async () => {
    const page = await listPlatformRequests(
      "sub2api",
      {},
      fakeClient({ items: [], stats, retention_days: 30, data_source: "reqlog-fake", freshness }),
    );
    expect(page.retentionDays).toBe(30);
    expect(page.dataSource).toBe("reqlog-fake");
  });

  it("映射完整过滤集统计与渠道、上游、计费元数据", async () => {
    const page = await listPlatformRequests(
      "sub2api",
      {},
      fakeClient({
        items: [
          {
            id: "req-1",
            source: "sub2api",
            occurred_at: "2026-08-28T09:00:00Z",
            username: "zhang.wei",
            token_prefix: "tok-a1b2",
            model: "gpt-4o",
            channel: "OpenAI 中转·主",
            upstream: "OpenAI Relay A",
            status: 200,
            duration_ms: 1200,
            ttfb_ms: 300,
            tokens_in: 100,
            tokens_out: 50,
            tokens_cache: 20,
            billed_amount: { amount_minor: "0", currency: "USD", scale: 3 },
            stream: false,
            upstream_request_id: "up-1",
            client_ip: "203.0.113.x",
          },
        ],
        stats: {
          request_count: 9,
          success_count: 7,
          failure_count: 2,
          average_duration_ms: 845,
        },
        freshness,
      }),
    );
    expect(page.stats).toEqual({
      requestCount: 9,
      successCount: 7,
      failureCount: 2,
      averageDurationMs: 845,
    });
    expect(page.items[0]).toMatchObject({
      channel: "OpenAI 中转·主",
      upstream: "OpenAI Relay A",
      billed_amount: { amount_minor: "0", currency: "USD", scale: 3 },
    });
  });

  it("计费未知保留 null，非法定点金额 fail closed", async () => {
    const base = {
      id: "req-1",
      source: "sub2api",
      occurred_at: "2026-08-28T09:00:00Z",
      username: "",
      token_prefix: "",
      model: "gpt-4o",
      channel: "",
      upstream: "",
      status: 200,
      duration_ms: 1,
      ttfb_ms: null,
      tokens_in: 0,
      tokens_out: 0,
      tokens_cache: 0,
      stream: false,
      upstream_request_id: "",
      client_ip: "",
    };
    const unknown = await listPlatformRequests(
      "sub2api",
      {},
      fakeClient({ items: [{ ...base, billed_amount: null }], stats, freshness }),
    );
    expect(unknown.items[0]?.billed_amount).toBeNull();

    await expect(
      listPlatformRequests(
        "sub2api",
        {},
        fakeClient({
          items: [{ ...base, billed_amount: { amount_minor: "-1", currency: "USD", scale: 2 } }],
          stats,
          freshness,
        }),
      ),
    ).rejects.toThrow(/计费/);
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
