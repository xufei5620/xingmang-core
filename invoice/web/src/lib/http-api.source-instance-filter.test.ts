import { afterEach, describe, expect, it, vi } from "vitest";

import { httpInvoiceApi } from "./http-api";

// XM-INV-ADMIN-EMBED: proves the three admin list methods that gained an
// optional sourceInstanceId parameter actually put it on the request URL
// (and, just as importantly, leave it off when the caller omits it -- the
// unscoped/standalone-admin case must stay byte-for-byte unchanged). This is
// the one place in this task that specifically needs a mocked fetch rather
// than a pure function: the codebase has no existing fetch-mock test to
// follow a precedent from, so this stubs global fetch directly rather than
// introducing a heavier mocking library for three call sites.

const SOURCE_INSTANCE_ID = "10000000-0000-4000-8000-000000000022";

// This project's vitest config runs in the plain Node environment (no
// jsdom/happy-dom dependency exists here to switch to -- consistent with
// there being no other fetch-level test anywhere in this codebase to follow
// a precedent from either). requestJSON's only DOM dependency on this path
// is window.setTimeout/clearTimeout for its abort timer; stub just that
// rather than pulling in a full DOM environment for four test cases.
function stubWindowTimers() {
  vi.stubGlobal("window", {
    setTimeout: globalThis.setTimeout.bind(globalThis),
    clearTimeout: globalThis.clearTimeout.bind(globalThis),
  });
}

function stubFetchReturningEmptyPage() {
  stubWindowTimers();
  const calls: string[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
    calls.push(typeof input === "string" ? input : input.toString());
    return new Response(JSON.stringify({ items: [], has_more: false }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
  return calls;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("admin list requests carry source_instance_id only when scoped", () => {
  it("getAdminRequestPage puts it on the URL when provided, omits it otherwise", async () => {
    const calls = stubFetchReturningEmptyPage();
    await httpInvoiceApi.getAdminRequestPage(undefined, SOURCE_INSTANCE_ID);
    await httpInvoiceApi.getAdminRequestPage();
    expect(calls[0]).toContain(`source_instance_id=${SOURCE_INSTANCE_ID}`);
    expect(calls[1]).not.toContain("source_instance_id");
  });

  it("getPaymentCandidates puts it on the URL when provided, omits it otherwise", async () => {
    const calls = stubFetchReturningEmptyPage();
    await httpInvoiceApi.getPaymentCandidates(undefined, SOURCE_INSTANCE_ID);
    await httpInvoiceApi.getPaymentCandidates();
    expect(calls[0]).toContain(`source_instance_id=${SOURCE_INSTANCE_ID}`);
    expect(calls[1]).not.toContain("source_instance_id");
  });

  it("getRefundCases puts it on the URL when provided, omits it otherwise", async () => {
    const calls = stubFetchReturningEmptyPage();
    await httpInvoiceApi.getRefundCases("open", undefined, SOURCE_INSTANCE_ID);
    await httpInvoiceApi.getRefundCases("open");
    expect(calls[0]).toContain(`source_instance_id=${SOURCE_INSTANCE_ID}`);
    expect(calls[1]).not.toContain("source_instance_id");
  });

  it("rejects a malformed source instance filter without ever calling fetch", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    await expect(
      httpInvoiceApi.getPaymentCandidates(undefined, "not-a-uuid"),
    ).rejects.toThrow();
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
