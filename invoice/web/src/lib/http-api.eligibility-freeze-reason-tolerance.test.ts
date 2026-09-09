import { afterEach, describe, expect, it, vi } from "vitest";

import { httpInvoiceApi } from "./http-api";

// Follow-up to CR-0009 (XM-INV-CR0009-LEDGER-VIEW): this frontend's own
// known-freeze-reason list (eligibilityFreezeReasons in http-api.ts,
// eligibilityFreezeReasonLabels in App.tsx) had drifted behind the
// backend's actual freeze_reason CHECK constraint -- migration 0016 added
// EVENT_DEAD/POLICY_ANCHOR_BLOCKED, and mapEligibilityFreeze's own
// then-strict closed-list check meant either reason on any one row would
// throw and blank the *entire* "资格冻结" list, not just that row. Both
// reasons are now added, and mapEligibilityFreeze's response-side check was
// loosened from "is a member of the known list" to "is a well-formed,
// enum-shaped code" (freezeReasonPattern) specifically so this class of
// drift can never recur: a reason this frontend has not labeled yet still
// renders (App.tsx's freezeReasonLabel falls back to the raw code), instead
// of failing the whole page. The `reason` *filter* query param (a value the
// caller chooses, not the server) is intentionally still validated against
// the closed, labeled list -- see the last describe block below.

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
  const fetchMock = vi.fn(async () => {
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
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("mapEligibilityFreeze: newly-catalogued reasons", () => {
  it.each(["EVENT_DEAD", "POLICY_ANCHOR_BLOCKED"])(
    "maps %s through cleanly",
    async (reason) => {
      stubFetchReturningPage([{ ...VALID_ITEM, freeze_reason: reason }]);
      const page = await httpInvoiceApi.getEligibilityFreezes({ status: "open" });
      expect(page.items).toHaveLength(1);
      expect(page.items[0].reason).toBe(reason);
    },
  );
});

describe("mapEligibilityFreeze: an unrecognized-but-well-formed reason", () => {
  it("does not reject the item (or the rest of the page) over one uncatalogued reason", async () => {
    stubFetchReturningPage([
      { ...VALID_ITEM, id: "61000000-0000-4000-8000-000000000002", freeze_reason: "SOME_FUTURE_REASON" },
      VALID_ITEM,
    ]);
    const page = await httpInvoiceApi.getEligibilityFreezes({ status: "open" });
    expect(page.items).toHaveLength(2);
    expect(page.items[0].reason).toBe("SOME_FUTURE_REASON");
    expect(page.items[1].reason).toBe("SOURCE_GAP");
  });

  it.each([
    ["", "empty string"],
    ["source_gap", "lowercase"],
    ["1_SOURCE_GAP", "does not start with a letter"],
    ["SOURCE GAP", "contains a space"],
    ["SOURCE_GAP\r\nInjected: x", "CR/LF injection-shaped"],
    ["A".repeat(64), "over the length cap"],
  ])("still rejects a malformed reason (%s: %s)", async (reason) => {
    stubFetchReturningPage([{ ...VALID_ITEM, freeze_reason: reason }]);
    await expect(
      httpInvoiceApi.getEligibilityFreezes({ status: "open" }),
    ).rejects.toThrow();
  });

  it("rejects a non-string reason", async () => {
    stubFetchReturningPage([{ ...VALID_ITEM, freeze_reason: 42 }]);
    await expect(
      httpInvoiceApi.getEligibilityFreezes({ status: "open" }),
    ).rejects.toThrow();
  });
});

describe("the reason filter query param stays validated against the known, labeled list", () => {
  it("rejects filtering by an uncatalogued/unrecognized reason", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    await expect(
      httpInvoiceApi.getEligibilityFreezes({
        status: "open",
        // @ts-expect-error -- deliberately not a valid EligibilityFreezeReason
        reason: "SOME_FUTURE_REASON",
      }),
    ).rejects.toThrow();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it.each(["EVENT_DEAD", "POLICY_ANCHOR_BLOCKED"] as const)(
    "accepts filtering by the newly-catalogued reason %s",
    async (reason) => {
      stubWindowTimers();
      const calls: string[] = [];
      const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
        calls.push(typeof input === "string" ? input : input.toString());
        return new Response(
          JSON.stringify({
            items: [],
            has_more: false,
            next_before_opened_at: "",
            next_before_id: "",
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      });
      vi.stubGlobal("fetch", fetchMock);
      await httpInvoiceApi.getEligibilityFreezes({ status: "open", reason });
      expect(calls[0]).toContain(`reason=${reason}`);
    },
  );
});
