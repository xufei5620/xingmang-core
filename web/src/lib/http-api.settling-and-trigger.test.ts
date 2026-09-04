import { afterEach, describe, expect, it, vi } from "vitest";

import { httpInvoiceApi } from "./http-api";

// Two operator-screen fixes that the same production screenshot surfaced:
//
//   XM-INV-LEDGER-SETTLING-STATE -- a queued projection job used to report
//   block_state=not_invoiceable_pending_reconciliation, which claims the books
//   and the source disagree. Jobs are enqueued every finalization cycle, so
//   perfectly healthy accounts flickered into that label all day; the operator
//   saw two of eight accounts showing it while the database had none in that
//   eligibility_status at all.
//
//   XM-INV-FREEZE-TRIGGER-VISIBLE -- four open SOURCE_GAP freezes on one
//   account rendered as four identical rows, because the only field that told
//   them apart (which checkpoint tripped each) was withheld from the wire.
//
// Fetch and timer stubs follow http-api.account-email.test.ts; this project's
// vitest runs in plain Node.

function stubWindowTimers() {
  vi.stubGlobal("window", {
    setTimeout: globalThis.setTimeout.bind(globalThis),
    clearTimeout: globalThis.clearTimeout.bind(globalThis),
  });
}

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
    freeze_reason: "SOURCE_GAP",
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

describe("the settling block state", () => {
  it("accepts it as a first-class state rather than rejecting the page", async () => {
    stubFetchReturning(ledgerPage([ledgerItem({ block_state: "settling" })]));

    const page = await httpInvoiceApi.getAccountLedger({});

    expect(page.items[0].blockState).toBe("settling");
  });

  it("still accepts a genuine reconciliation problem, which is a different thing", async () => {
    stubFetchReturning(
      ledgerPage([ledgerItem({ block_state: "not_invoiceable_pending_reconciliation" })]),
    );

    const page = await httpInvoiceApi.getAccountLedger({});

    expect(page.items[0].blockState).toBe("not_invoiceable_pending_reconciliation");
  });

  it("rejects a block state the server has no business sending", async () => {
    stubFetchReturning(ledgerPage([ledgerItem({ block_state: "settled" })]));

    await expect(httpInvoiceApi.getAccountLedger({})).rejects.toThrow(
      /用户账本记录包含无效字段/,
    );
  });
});

describe("freeze rows carry the trigger that tells them apart", () => {
  it("maps the trigger when the server sends one", async () => {
    stubFetchReturning(
      freezePage([
        freezeItem({
          trigger_object_type: "balance_checkpoint",
          trigger_object_id: "a9e2f44623b7f63b9a2b8df595da",
        }),
      ]),
    );

    const page = await httpInvoiceApi.getEligibilityFreezes({ status: "open" });

    expect(page.items[0].triggerObjectType).toBe("balance_checkpoint");
    expect(page.items[0].triggerObjectId).toBe("a9e2f44623b7f63b9a2b8df595da");
  });

  it("leaves both undefined when the row has no trigger, rather than inventing one", async () => {
    stubFetchReturning(freezePage([freezeItem()]));

    const page = await httpInvoiceApi.getEligibilityFreezes({ status: "open" });

    expect(page.items[0].triggerObjectType).toBeUndefined();
    expect(page.items[0].triggerObjectId).toBeUndefined();
  });

  it("keeps four same-reason freezes distinguishable", async () => {
    // The production shape: one account, one reason, one scope, one second.
    const triggers = [
      "a9e2f44623b7f63b9a2b8df595da",
      "25e36ba7e37a184cda995ddd8309",
      "7cd8945ba4148f2a4a112937fd1e",
      "6ef54ebb6e4fa463b3c96375ebf7",
    ];
    stubFetchReturning(
      freezePage(
        triggers.map((trigger, index) =>
          freezeItem({
            id: `2100000${index}-0000-4000-8000-00000000000${index + 1}`,
            trigger_object_type: "balance_checkpoint",
            trigger_object_id: trigger,
          }),
        ),
      ),
    );

    const page = await httpInvoiceApi.getEligibilityFreezes({ status: "open" });

    expect(new Set(page.items.map((item) => item.triggerObjectId)).size).toBe(4);
  });
});
