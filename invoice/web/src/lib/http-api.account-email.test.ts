import { afterEach, describe, expect, it, vi } from "vitest";

import { httpInvoiceApi } from "./http-api";

// XM-INV-LEDGER-ACCOUNT-EMAIL: both operator lists identify an account by its
// upstream numeric ID, which tells a human nothing. The server now sends the
// account's verified address alongside it, and only when it actually has one.
// These cases pin all three states the screen depends on: present, absent, and
// malformed.
//
// Fetch and timer stubs follow http-api.source-instance-filter.test.ts; this
// project's vitest runs in plain Node.

function stubWindowTimers() {
  vi.stubGlobal("window", {
    setTimeout: globalThis.setTimeout.bind(globalThis),
    clearTimeout: globalThis.clearTimeout.bind(globalThis),
  });
}

// Both page envelopes require their full key set, so the helpers below wrap
// items in the exact envelope each endpoint validates.
function ledgerPage(items: unknown[]) {
  return {
    items,
    has_more: false,
    next_before_invoiceable_minor: null,
    next_before_id: "",
  };
}

function freezePage(items: unknown[]) {
  return { items, has_more: false, next_before_opened_at: "", next_before_id: "" };
}

function stubFetchReturning(payload: unknown) {
  stubWindowTimers();
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify(payload), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
    ),
  );
}

function ledgerItem(overrides: Record<string, unknown> = {}) {
  return {
    external_account_id: "1a2b0000-0000-4000-8000-000000000001",
    source_type: "sub2api",
    external_user_id: "1147",
    policy_start_at: "2026-08-31T16:00:00.000Z",
    recharges_since_start_count: 1,
    recharges_since_start_minor: 11_000,
    consumed_since_start_minor: 0,
    invoiceable_now_minor: 0,
    issued_minor: 0,
    threshold_reached: false,
    block_state: "below_threshold",
    last_checkpoint_at: null,
    ...overrides,
  };
}

function freezeItem(overrides: Record<string, unknown> = {}) {
  return {
    id: "21000000-0000-4000-8000-000000000001",
    principal_id: "99000000-0000-4000-8000-000000000001",
    source_instance_id: "10000000-0000-4000-8000-000000000001",
    source_type: "sub2api",
    source_name: "SoloV API",
    funding_lot_id: "",
    scope: "account",
    freeze_reason: "SOURCE_INTEGRITY_GAP",
    status: "open",
    eligibility_status: "frozen",
    opened_at: "2026-09-03T05:38:00.000Z",
    version: 1,
    external_user_id: "2092",
    ...overrides,
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("account ledger carries the account email", () => {
  it("maps the address when the server sends one", async () => {
    stubFetchReturning(ledgerPage([ledgerItem({ account_email: "chen.yuan@example.com" })]));

    const page = await httpInvoiceApi.getAccountLedger({});

    expect(page.items[0].accountEmail).toBe("chen.yuan@example.com");
    expect(page.items[0].externalUserId).toBe("1147");
  });

  it("leaves it undefined when the key is absent", async () => {
    // Absent is the honest shape for an account with no verified address, and
    // it must not be turned into an empty string that renders as a blank line.
    stubFetchReturning(ledgerPage([ledgerItem()]));

    const page = await httpInvoiceApi.getAccountLedger({});

    expect(page.items[0].accountEmail).toBeUndefined();
    expect(page.items[0].externalUserId).toBe("1147");
  });

  it("rejects a malformed address the same way any other bad field is rejected", async () => {
    stubFetchReturning(ledgerPage([ledgerItem({ account_email: "" })]));

    await expect(httpInvoiceApi.getAccountLedger({})).rejects.toMatchObject({
      code: "INVALID_ACCOUNT_LEDGER_RESPONSE",
    });
  });
});

describe("eligibility freeze queue carries the account email", () => {
  it("maps the address when the server sends one", async () => {
    stubFetchReturning(freezePage([freezeItem({ account_email: "zhao.min@example.org" })]));

    const page = await httpInvoiceApi.getEligibilityFreezes({ status: "open" });

    expect(page.items[0].accountEmail).toBe("zhao.min@example.org");
    expect(page.items[0].externalUserId).toBe("2092");
  });

  it("leaves it undefined when the key is absent", async () => {
    stubFetchReturning(freezePage([freezeItem()]));

    const page = await httpInvoiceApi.getEligibilityFreezes({ status: "open" });

    expect(page.items[0].accountEmail).toBeUndefined();
  });

  it("rejects an address carrying a control character", async () => {
    stubFetchReturning(
      freezePage([freezeItem({ account_email: "a@b.com\nX-Injected: 1" })]),
    );

    await expect(
      httpInvoiceApi.getEligibilityFreezes({ status: "open" }),
    ).rejects.toMatchObject({ code: "INVALID_ELIGIBILITY_FREEZE_RESPONSE" });
  });
});
