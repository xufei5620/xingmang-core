import { afterEach, describe, expect, it, vi } from "vitest";

import { httpInvoiceApi } from "./http-api";

// CR-0007 problem one: external_user_id is a new, always-present field on
// GET /invoice-api/v1/admin/eligibility-freezes items, and a new optional exact-match
// query filter on the same endpoint. This follows the same mocked-fetch
// style as http-api.source-instance-filter.test.ts (the only existing
// precedent for a fetch-level test in this codebase, which also explains why
// this runs in vitest's plain Node environment rather than jsdom/happy-dom).

const VALID_ITEM = {
  id: "61000000-0000-4000-8000-000000000001",
  principal_id: "20000000-0000-4000-8000-000000000001",
  source_instance_id: "10000000-0000-4000-8000-000000000001",
  source_type: "sub2api" as const,
  source_name: "Sub2API 生产实例",
  funding_lot_id: "",
  scope: "account" as const,
  freeze_reason: "SOURCE_GAP",
  status: "open" as const,
  eligibility_status: "frozen" as const,
  opened_at: "2026-09-02T02:00:00.000Z",
  version: 3,
  external_user_id: "1147",
};

function stubWindowTimers() {
  vi.stubGlobal("window", {
    setTimeout: globalThis.setTimeout.bind(globalThis),
    clearTimeout: globalThis.clearTimeout.bind(globalThis),
  });
}

function stubFetchReturningPage(items: unknown[]) {
  stubWindowTimers();
  const calls: string[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    calls.push(typeof input === "string" ? input : input.toString());
    return new Response(
      JSON.stringify({
        items,
        has_more: false,
        next_before_opened_at: "",
        next_before_id: "",
      }),
      { status: 200, headers: { "Content-Type": "application/json" } },
    );
  });
  vi.stubGlobal("fetch", fetchMock);
  return calls;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("eligibility freeze external_user_id mapping", () => {
  it("maps external_user_id onto the returned item's externalUserId", async () => {
    stubFetchReturningPage([VALID_ITEM]);
    const page = await httpInvoiceApi.getEligibilityFreezes({ status: "open" });
    expect(page.items).toHaveLength(1);
    expect(page.items[0].externalUserId).toBe("1147");
  });

  it("rejects a response item missing external_user_id instead of silently defaulting it", async () => {
    const { external_user_id: _dropped, ...withoutExternalUserId } = VALID_ITEM;
    stubFetchReturningPage([withoutExternalUserId]);
    await expect(
      httpInvoiceApi.getEligibilityFreezes({ status: "open" }),
    ).rejects.toThrow();
  });

  it("rejects a response item whose external_user_id is not a string", async () => {
    stubFetchReturningPage([{ ...VALID_ITEM, external_user_id: 1147 }]);
    await expect(
      httpInvoiceApi.getEligibilityFreezes({ status: "open" }),
    ).rejects.toThrow();
  });
});

describe("eligibility freeze external_user_id filter", () => {
  it("puts external_user_id on the request URL when provided, omits it otherwise", async () => {
    const calls = stubFetchReturningPage([]);
    await httpInvoiceApi.getEligibilityFreezes({
      status: "open",
      externalUserId: "1147",
    });
    await httpInvoiceApi.getEligibilityFreezes({ status: "open" });
    expect(calls[0]).toContain("external_user_id=1147");
    expect(calls[1]).not.toContain("external_user_id");
  });

  it("can combine external_user_id with sourceInstanceId on the same request", async () => {
    const calls = stubFetchReturningPage([]);
    await httpInvoiceApi.getEligibilityFreezes({
      status: "open",
      externalUserId: "1147",
      sourceInstanceId: "10000000-0000-4000-8000-000000000022",
    });
    expect(calls[0]).toContain("external_user_id=1147");
    expect(calls[0]).toContain(
      "source_instance_id=10000000-0000-4000-8000-000000000022",
    );
  });

  it("rejects a malformed external_user_id filter without ever calling fetch", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    await expect(
      httpInvoiceApi.getEligibilityFreezes({
        status: "open",
        externalUserId: "bad\r\nvalue",
      }),
    ).rejects.toThrow();
    await expect(
      httpInvoiceApi.getEligibilityFreezes({
        status: "open",
        externalUserId: "a".repeat(513),
      }),
    ).rejects.toThrow();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
