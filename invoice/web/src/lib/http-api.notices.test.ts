import { afterEach, describe, expect, it, vi } from "vitest";

import { httpInvoiceApi } from "./http-api";
import type { InvoiceRequest } from "../types";

// XM-INV-NOTICE-UI：管理端读"那条企业微信通知发出去了没有"。
//
// 这一层要保证三件事：打的是 **admin** 那条路径（后端没有 user 变体）；
// null 与缺失都归成 undefined（界面只需要区分"有"和"没有"）；
// 尝试次数说不通时**整块停掉**，不显示一个编出来的数字。

const REQUEST = { id: "60000000-0000-4000-8000-000000000099" } as InvoiceRequest;

function stubWindowTimers() {
  vi.stubGlobal("window", {
    setTimeout: globalThis.setTimeout.bind(globalThis),
    clearTimeout: globalThis.clearTimeout.bind(globalThis),
  });
}

function stubFetch(body: unknown) {
  stubWindowTimers();
  const calls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      calls.push(typeof input === "string" ? input : input.toString());
      return new Response(JSON.stringify(body), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  return calls;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("listRequestNotices", () => {
  it("打的是 admin 路径，不是 user——后端没有 user 变体", async () => {
    const calls = stubFetch({ items: [] });
    await httpInvoiceApi.listRequestNotices(REQUEST);
    expect(calls[0]).toContain(`/invoice-api/v1/admin/invoice-requests/${REQUEST.id}/notices`);
    expect(calls[0]).not.toContain("/invoice-api/v1/user/");
  });

  it("送达与未送达：null 归成 undefined，不是零时刻也不是空串", async () => {
    stubFetch({
      items: [
        {
          id: "n1",
          kind: "request.submitted",
          status: "sent",
          attempt_count: 1,
          next_attempt_at: "2026-09-06T08:12:00Z",
          delivered_at: "2026-09-06T08:13:00Z",
          last_error_code: "",
          created_at: "2026-09-06T08:12:00Z",
        },
        {
          id: "n2",
          kind: "request.submitted",
          status: "queued",
          attempt_count: 3,
          next_attempt_at: "2026-09-06T08:20:00Z",
          delivered_at: null,
          last_error_code: "wecom: errcode 93000",
          created_at: null,
        },
      ],
    });
    const notices = await httpInvoiceApi.listRequestNotices(REQUEST);
    expect(notices).toHaveLength(2);
    expect(notices[0]?.deliveredAt).toBe("2026-09-06T08:13:00Z");
    expect(notices[1]?.deliveredAt).toBeUndefined();
    expect(notices[1]?.createdAt).toBeUndefined();
    // 失败短码要带出来：它是"为什么没发出去"的唯一线索。
    expect(notices[1]?.lastErrorCode).toBe("wecom: errcode 93000");
    expect(notices[1]?.attemptCount).toBe(3);
  });

  it("items 缺失或为 null 时返回空数组，不抛错", async () => {
    stubFetch({});
    expect(await httpInvoiceApi.listRequestNotices(REQUEST)).toEqual([]);
    stubFetch({ items: null });
    expect(await httpInvoiceApi.listRequestNotices(REQUEST)).toEqual([]);
  });

  it("未知状态原样带出，不被藏起来", async () => {
    stubFetch({
      items: [{ id: "n3", kind: "invoice.issued", status: "brand_new", attempt_count: 0 }],
    });
    const notices = await httpInvoiceApi.listRequestNotices(REQUEST);
    expect(notices[0]?.status).toBe("brand_new");
    expect(notices[0]?.kind).toBe("invoice.issued");
  });

  it("尝试次数说不通时整块停掉，不显示一个编出来的数字", async () => {
    stubFetch({ items: [{ id: "n4", status: "queued", attempt_count: -1 }] });
    await expect(httpInvoiceApi.listRequestNotices(REQUEST)).rejects.toThrow();
    stubFetch({ items: [{ id: "n5", status: "queued", attempt_count: 1.5 }] });
    await expect(httpInvoiceApi.listRequestNotices(REQUEST)).rejects.toThrow();
  });
});
