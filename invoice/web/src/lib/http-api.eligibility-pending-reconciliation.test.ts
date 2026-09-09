import { afterEach, describe, expect, it, vi } from "vitest";

import {
  httpInvoiceApi,
  lotReasonDescription,
  mapLot,
  requiredEligibilityStartAt,
  type BackendFundingLot,
} from "./http-api";
import {
  eligibilityReasonLabel,
  eligibilityReasonLabels,
  eligibilityStatusLabel,
  eligibilityStatusLabels,
} from "./eligibility-labels";
import {
  lotEligibilityStatuses,
  lotReasonCodes,
  summaryReasons,
  summaryStatuses,
} from "./eligibility-wire.generated";

// XM-INV-LOT-REASON-CONTRACT.
//
// XM-INV-ELIG-AUTO-RECONCILE shipped a fourth persisted eligibility_status,
// not_invoiceable_pending_reconciliation, and a matching lot reason code,
// LEDGER_PENDING_RECONCILIATION. This bundle's three separate hand-copied
// allow-lists (mapLot, mapEligibilitySummary, mapEligibilityFreeze) all still
// held the older values, so for the two affected accounts every one of these
// cases threw and the whole 开票中心 rendered an error banner -- with the
// "已关联账号" panel falling back to the binding wizard, telling users with
// verified bindings to go and re-bind.
//
// Every case below fails on the pre-slice implementation. The mutation table in
// docs/handoffs/XM-INV-LOT-REASON-CONTRACT.md records that each was observed
// red before being made green, including the two half-fixes that look correct
// and are not.

const PENDING = "not_invoiceable_pending_reconciliation";

const BASE_LOT: BackendFundingLot = {
  id: "50000000-0000-4000-8000-000000000001",
  source: "sub2api",
  source_instance_id: "10000000-0000-4000-8000-000000000001",
  source_label: "SoloV API",
  display_reference: "PAY-00000001",
  completed_at: requiredEligibilityStartAt,
  original_paid_minor: 30_000,
  consumed_cash_minor: 30_000,
  reserved_minor: 0,
  issued_minor: 0,
  available_minor: 0,
  eligibility_kind: "wallet",
  eligibility_status: PENDING,
  reason_code: "LEDGER_PENDING_RECONCILIATION",
  verification: "verified",
  refund_frozen: false,
};

function lot(overrides: Partial<BackendFundingLot> = {}): BackendFundingLot {
  return { ...BASE_LOT, ...overrides } as BackendFundingLot;
}

describe("mapLot: the auto-reconcile status", () => {
  it("renders a pending-reconciliation lot instead of rejecting the response", () => {
    const order = mapLot(lot());
    expect(order.eligibilityStatus).toBe(PENDING);
    expect(order.reasonCode).toBe("LEDGER_PENDING_RECONCILIATION");
    expect(order.availableMinor).toBe(0);
    expect(order.eligibilityDegraded).toBe(false);
    // Word-for-word: the product owner reads Chinese only, and this sentence
    // has to say the state clears itself or it reads as a freeze.
    expect(order.description).toBe("账本对账中，暂不可开票（完成后自动恢复）");
  });

  // The half-fix lock. Reading the incident as "the backend added a reason code
  // the frontend did not know" leads to widening only the reason_code list --
  // and every one of these lots would still throw, because the status check is
  // the first term of the same `||` chain and the status is PENDING for all of
  // them. The backend probe
  // (TestUserFundingLotPendingReconciliationCarriesFiveDistinctReasons) proves
  // these four reasons are genuinely reachable under this status; upstream user
  // 12's eleven lots are a mixture.
  it.each([
    ["SOURCE_REFUND", { refund_frozen: true }],
    [
      "BEFORE_ELIGIBILITY_START",
      {
        eligibility_kind: "legacy" as const,
        completed_at: "2026-08-01T00:00:00.000Z",
      },
    ],
    [
      "SUBSCRIPTION_USAGE_UNSUPPORTED",
      { eligibility_kind: "subscription" as const },
    ],
    ["NO_POST_START_CONSUMPTION", { consumed_cash_minor: 0 }],
  ])(
    "renders a pending-reconciliation lot whose reason is %s",
    (reasonCode, overrides) => {
      const order = mapLot(
        lot({
          ...overrides,
          reason_code: reasonCode as BackendFundingLot["reason_code"],
        }),
      );
      expect(order.eligibilityStatus).toBe(PENDING);
      expect(order.reasonCode).toBe(reasonCode);
      expect(order.availableMinor).toBe(0);
      expect(order.eligibilityDegraded).toBe(false);
    },
  );

  it("keeps a pending-reconciliation lot unselectable by pinning availableMinor to 0", () => {
    // The selection guards in App.tsx all require eligibilityStatus ===
    // "active", but the money check is the one that cannot be bypassed by a
    // future refactor of those guards: a non-active status makes any positive
    // available_minor an inconsistent response.
    expect(() => mapLot(lot({ available_minor: 30_000 }))).toThrow(
      "开票金额账本响应不一致",
    );
  });
});

describe("mapLot: values this bundle has never heard of", () => {
  it("renders an unknown status rather than blanking the list, and flags it", () => {
    const order = mapLot(
      lot({
        eligibility_status: "some_future_state",
        reason_code: "SOME_FUTURE_REASON",
      } as Partial<BackendFundingLot>),
    );
    expect(order.eligibilityStatus).toBe("some_future_state");
    expect(order.eligibilityDegraded).toBe(true);
    expect(order.availableMinor).toBe(0);
    expect(order.description).toBe("账本状态待确认（SOME_FUTURE_REASON）");
  });

  // The reverse lock: degrading the enum check must not degrade the money
  // check. An unknown status with a positive amount is still refused, so
  // "unrecognised" can never become "invoiceable".
  it("still refuses an unknown status that claims invoiceable money", () => {
    expect(() =>
      mapLot(
        lot({
          eligibility_status: "some_future_state",
          reason_code: "SOME_FUTURE_REASON",
          available_minor: 30_000,
        } as Partial<BackendFundingLot>),
      ),
    ).toThrow("开票金额账本响应不一致");
  });

  it("degrades rather than throws when source_unavailable loses its SOURCE_NOT_READY pairing", () => {
    // RC58: the backend briefly paired source_unavailable with
    // NO_POST_START_CONSUMPTION and this client refused the entire page. The
    // pairing is still required -- it is pinned in Go by
    // TestUserFundingLotSourceUnavailableAlwaysMapsToSourceNotReady, where a
    // violation belongs -- but a violation must not blank the user's page.
    const order = mapLot(
      lot({
        eligibility_status: "source_unavailable",
        reason_code: "NO_POST_START_CONSUMPTION",
      }),
    );
    expect(order.eligibilityDegraded).toBe(true);
    expect(order.availableMinor).toBe(0);
  });

  it.each([
    ["", "empty string"],
    ["ledger_pending", "lowercase"],
    ["1_LEDGER", "does not start with a letter"],
    ["LEDGER PENDING", "contains a space"],
    ["LEDGER\r\nInjected: x", "CR/LF injection-shaped"],
    ["A".repeat(64), "over the length cap"],
  ])("still rejects a malformed reason_code (%s: %s)", (reasonCode) => {
    expect(() =>
      mapLot(lot({ reason_code: reasonCode as BackendFundingLot["reason_code"] })),
    ).toThrow("充值记录包含无效的资金账本状态");
  });

  it.each([
    ["", "empty string"],
    ["NOT_INVOICEABLE", "uppercase"],
    ["9_pending", "does not start with a letter"],
    ["pending recon", "contains a space"],
    ["pending\r\nInjected: x", "CR/LF injection-shaped"],
    ["a".repeat(64), "over the length cap"],
  ])("still rejects a malformed eligibility_status (%s: %s)", (status) => {
    expect(() =>
      mapLot(
        lot({
          eligibility_status: status,
        } as Partial<BackendFundingLot>),
      ),
    ).toThrow("充值记录包含无效的资金账本状态");
  });

  it("rejects a non-string reason_code", () => {
    expect(() =>
      mapLot(lot({ reason_code: 42 as unknown as undefined })),
    ).toThrow("充值记录包含无效的资金账本状态");
  });
});

function stubWindowTimers() {
  vi.stubGlobal("window", {
    setTimeout: globalThis.setTimeout.bind(globalThis),
    clearTimeout: globalThis.clearTimeout.bind(globalThis),
  });
}

function stubFetchReturning(body: unknown) {
  stubWindowTimers();
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
    ),
  );
}

const SUMMARY_ITEM = {
  source_instance_id: "10000000-0000-4000-8000-000000000001",
  source_type: "sub2api" as const,
  source_name: "Sub2API 生产实例",
  binding_status: "verified" as const,
  status: PENDING,
  currency: "CNY",
  available_minor: 0,
  consumed_minor: 30_000,
  unconsumed_minor: 0,
  reserved_minor: 0,
  issued_minor: 0,
  legacy_noninvoiceable: { service_units: "0", unit_code: "SUB2_BALANCE_1E8" },
  noncash: { service_units: "0", unit_code: "SUB2_BALANCE_1E8" },
  reasons: ["PENDING_RECONCILIATION"],
};

afterEach(() => {
  vi.unstubAllGlobals();
});

// The second, independent crash point. Even with mapLot fixed, the summary
// endpoint sits beside the orders request in the same load, carries its own
// copy of the status list AND its own reason list (which never had
// PENDING_RECONCILIATION at all), and would keep rejecting -- the user would
// see the same red banner and the fix would look like it had not worked.
describe("getUserEligibilitySummary: the auto-reconcile status", () => {
  it("accepts a pending-reconciliation summary", async () => {
    stubFetchReturning({ items: [SUMMARY_ITEM] });
    const summaries = await httpInvoiceApi.getUserEligibilitySummary();
    expect(summaries).toHaveLength(1);
    expect(summaries[0].status).toBe(PENDING);
    expect(summaries[0].reasons).toEqual(["PENDING_RECONCILIATION"]);
    expect(summaries[0].availableMinor).toBe(0);
    expect(summaries[0].eligibilityDegraded).toBe(false);
  });

  it("accepts a summary carrying every reason the backend can emit at once", async () => {
    // The old length cap was the length of the known-reason list, so it grew
    // whenever the list did. The contract now publishes the real maximum.
    stubFetchReturning({
      items: [
        {
          ...SUMMARY_ITEM,
          binding_status: "pending",
          reasons: [
            "BINDING_NOT_VERIFIED",
            "ACCOUNT_FROZEN",
            "PENDING_RECONCILIATION",
            "PROJECTION_PENDING",
            "SOURCE_NOT_READY",
          ],
        },
      ],
    });
    const summaries = await httpInvoiceApi.getUserEligibilitySummary();
    expect(summaries[0].reasons).toHaveLength(5);
  });

  it("degrades on an unknown status or reason instead of blanking the panel", async () => {
    stubFetchReturning({
      items: [
        { ...SUMMARY_ITEM, status: "some_future_state", reasons: ["SOME_FUTURE_REASON"] },
      ],
    });
    const summaries = await httpInvoiceApi.getUserEligibilitySummary();
    expect(summaries[0].status).toBe("some_future_state");
    expect(summaries[0].eligibilityDegraded).toBe(true);
    expect(summaries[0].availableMinor).toBe(0);
  });

  it("still refuses an unknown status that claims invoiceable money", async () => {
    stubFetchReturning({
      items: [
        {
          ...SUMMARY_ITEM,
          status: "some_future_state",
          reasons: ["SOME_FUTURE_REASON"],
          available_minor: 30_000,
        },
      ],
    });
    await expect(httpInvoiceApi.getUserEligibilitySummary()).rejects.toThrow(
      "开票资格金额或安全状态不一致",
    );
  });

  it("still rejects a malformed reason", async () => {
    stubFetchReturning({
      items: [{ ...SUMMARY_ITEM, reasons: ["pending reconciliation"] }],
    });
    await expect(httpInvoiceApi.getUserEligibilitySummary()).rejects.toThrow(
      "开票资格摘要包含未识别状态",
    );
  });

  it("still rejects more reasons than the backend can produce", async () => {
    stubFetchReturning({
      items: [
        {
          ...SUMMARY_ITEM,
          reasons: [
            "BINDING_NOT_VERIFIED",
            "ACCOUNT_FROZEN",
            "PENDING_RECONCILIATION",
            "PROJECTION_PENDING",
            "SOURCE_NOT_READY",
            "NO_CONSUMED_CASH",
          ],
        },
      ],
    });
    await expect(httpInvoiceApi.getUserEligibilitySummary()).rejects.toThrow(
      "开票资格摘要包含未识别状态",
    );
  });

  // The pairing check that used to be a hard throw here. It is unreachable
  // today -- the summary rides COALESCE(...,'syncing') with no freshness
  // override, so its status can only be one of the four persisted values -- and
  // "it never fires" is a fact about the backend, not a property of this code.
  // Left as a throw it would be one backend ordering bug away from blanking the
  // panel, which is exactly what RC58 did with mapLot's version of it.
  it("degrades a source_unavailable summary that lost its SOURCE_NOT_READY pairing", async () => {
    stubFetchReturning({
      items: [
        {
          ...SUMMARY_ITEM,
          status: "source_unavailable",
          reasons: ["PROJECTION_PENDING"],
        },
      ],
    });
    const summaries = await httpInvoiceApi.getUserEligibilitySummary();
    expect(summaries[0].status).toBe("source_unavailable");
    expect(summaries[0].eligibilityDegraded).toBe(true);
    expect(summaries[0].availableMinor).toBe(0);
  });

  // Same failure mode as the enum lists, on the same request, one function
  // away: the backend adds a field, ships a deploy ahead of this bundle, and a
  // strict key set rejects the whole summary. Widening the enums while leaving
  // this a hard throw would have closed the door and left the window open.
  it("ignores a field this bundle predates instead of rejecting the summary", async () => {
    stubFetchReturning({
      items: [
        { ...SUMMARY_ITEM, reconciliation_started_at: "2026-09-02T00:00:00Z" },
      ],
    });
    const summaries = await httpInvoiceApi.getUserEligibilitySummary();
    expect(summaries[0].status).toBe(PENDING);
    expect(summaries[0].availableMinor).toBe(0);
    expect(summaries[0].eligibilityDegraded).toBe(true);
  });

  it("ignores an unexpected field on the nested service-unit summaries too", async () => {
    stubFetchReturning({
      items: [
        {
          ...SUMMARY_ITEM,
          noncash: {
            service_units: "0",
            unit_code: "SUB2_BALANCE_1E8",
            unit_scale: "1e8",
          },
        },
      ],
    });
    const summaries = await httpInvoiceApi.getUserEligibilitySummary();
    expect(summaries[0].noncash.serviceUnits).toBe("0");
    expect(summaries[0].eligibilityDegraded).toBe(true);
  });

  // Tolerating an unknown key is not the same as tolerating a missing one: a
  // key this client reads by name and does not get would validate as
  // `undefined` and could reach the UI as a blank amount.
  it("still rejects a summary that is missing a field this client reads", async () => {
    const { available_minor: _dropped, ...withoutAmount } = SUMMARY_ITEM;
    stubFetchReturning({ items: [withoutAmount] });
    await expect(httpInvoiceApi.getUserEligibilitySummary()).rejects.toThrow(
      "服务返回的开票资格摘要字段超出安全白名单",
    );
  });

  it("still rejects a summary that is not an object at all", async () => {
    stubFetchReturning({ items: ["not-an-object"] });
    await expect(httpInvoiceApi.getUserEligibilitySummary()).rejects.toThrow(
      "服务返回的开票资格摘要格式无效",
    );
  });

  // The tolerance is about the NAME of a key, never about the value behind a
  // known one: an extra field must not become a way to smuggle in an amount.
  it("still enforces the amount invariants on a response carrying unknown keys", async () => {
    stubFetchReturning({
      items: [
        {
          ...SUMMARY_ITEM,
          reconciliation_started_at: "2026-09-02T00:00:00Z",
          available_minor: 30_000,
        },
      ],
    });
    await expect(httpInvoiceApi.getUserEligibilitySummary()).rejects.toThrow(
      "开票资格金额或安全状态不一致",
    );
  });
});

// The third crash point, which production has not hit only because there are
// currently zero open freezes on the two affected accounts. It is green because
// of an external fact, not because it is correct.
describe("getEligibilityFreezes: the auto-reconcile status", () => {
  it("accepts a freeze row on a pending-reconciliation account", async () => {
    stubFetchReturning({
      items: [
        {
          id: "61000000-0000-4000-8000-000000000001",
          principal_id: "20000000-0000-4000-8000-000000000001",
          source_instance_id: "10000000-0000-4000-8000-000000000001",
          source_type: "sub2api",
          source_name: "Sub2API 生产实例",
          funding_lot_id: "",
          scope: "account",
          freeze_reason: "SOURCE_GAP",
          status: "open",
          eligibility_status: PENDING,
          opened_at: "2026-09-02T02:00:00.000Z",
          version: 3,
          external_user_id: "1147",
        },
      ],
      has_more: false,
      next_before_opened_at: "",
      next_before_id: "",
    });
    const page = await httpInvoiceApi.getEligibilityFreezes({ status: "open" });
    expect(page.items).toHaveLength(1);
    expect(page.items[0].eligibilityStatus).toBe(PENDING);
  });
});

// "The page renders" is not the requirement -- the requirement is that a user
// who reads only Chinese learns what is happening and whether to act. These
// walk every value the contract declares, so a value added to the contract
// without copy fails here as well as at typecheck.
describe("every wire value a user can see has Chinese copy", () => {
  it.each([...lotReasonCodes])("lot reason %s has a description", (code) => {
    const description = lotReasonDescription(code);
    expect(description).not.toContain("待确认");
    expect(description).toMatch(/[一-龥]/);
  });

  it.each([...summaryReasons])("summary reason %s has a label", (reason) => {
    const label = eligibilityReasonLabel(reason);
    expect(label).not.toContain("待确认");
    expect(label).toMatch(/[一-龥]/);
  });

  it.each([...summaryStatuses])("summary status %s has a label", (status) => {
    const label = eligibilityStatusLabel(status);
    expect(label).not.toContain("待确认");
    expect(label).toMatch(/[一-龥]/);
  });

  it.each([...lotEligibilityStatuses])(
    "lot status %s has a label",
    (status) => {
      expect(eligibilityStatusLabel(status)).not.toContain("待确认");
    },
  );

  it("words the auto-reconcile state as self-clearing, not as a freeze", () => {
    // These two states are one letter apart in the ledger and worlds apart for
    // the user: one resolves itself, the other waits on a human. If the copy
    // for PENDING_RECONCILIATION ever reads like ACCOUNT_FROZEN, users will
    // open tickets that nobody can action.
    expect(eligibilityReasonLabels.PENDING_RECONCILIATION).toBe(
      "来源账本正在自动对账，完成后会自动恢复",
    );
    expect(eligibilityReasonLabels.ACCOUNT_FROZEN).toBe("资金资格已安全冻结");
    expect(eligibilityStatusLabels.not_invoiceable_pending_reconciliation).toBe(
      "对账中暂不可开票",
    );
    expect(lotReasonDescription("LEDGER_PENDING_RECONCILIATION")).toBe(
      "账本对账中，暂不可开票（完成后自动恢复）",
    );
  });

  it("quotes the raw code in the fallback so a screenshot is diagnosable", () => {
    expect(eligibilityStatusLabel("some_future_state")).toBe(
      "账本状态待确认（some_future_state）",
    );
    expect(eligibilityReasonLabel("SOME_FUTURE_REASON")).toBe(
      "原因待确认（SOME_FUTURE_REASON）",
    );
    expect(lotReasonDescription("SOME_FUTURE_REASON")).toBe(
      "账本状态待确认（SOME_FUTURE_REASON）",
    );
  });
});

// The response ENVELOPE was the last strict key set left on this request after
// the item DTO and its nested service units were relaxed. A backend that adds
// `generated_at` beside `items` -- one deploy ahead of this bundle -- rejected
// the whole summary with the same banner as the original incident.
describe("getUserEligibilitySummary: the response envelope", () => {
  it("ignores an unknown top-level key and marks the rows degraded", async () => {
    stubFetchReturning({
      items: [SUMMARY_ITEM],
      generated_at: "2026-09-08T17:00:00Z",
      next_cursor: null,
    });
    const summaries = await httpInvoiceApi.getUserEligibilitySummary();
    expect(summaries).toHaveLength(1);
    expect(summaries[0].status).toBe(PENDING);
    expect(summaries[0].availableMinor).toBe(0);
    expect(summaries[0].eligibilityDegraded).toBe(true);
  });

  it("does not mark rows degraded when the envelope is exactly what it expects", async () => {
    stubFetchReturning({ items: [SUMMARY_ITEM] });
    const summaries = await httpInvoiceApi.getUserEligibilitySummary();
    expect(summaries[0].eligibilityDegraded).toBe(false);
  });

  it("still requires items", async () => {
    stubFetchReturning({ generated_at: "2026-09-08T17:00:00Z" });
    await expect(httpInvoiceApi.getUserEligibilitySummary()).rejects.toThrow(
      "开票资格摘要响应",
    );
  });

  it("still refuses a non-array or oversized items list", async () => {
    stubFetchReturning({ items: { length: 1 } });
    await expect(httpInvoiceApi.getUserEligibilitySummary()).rejects.toThrow(
      "开票资格摘要数量无效",
    );
    stubFetchReturning({ items: Array.from({ length: 33 }, () => SUMMARY_ITEM) });
    await expect(httpInvoiceApi.getUserEligibilitySummary()).rejects.toThrow(
      "开票资格摘要数量无效",
    );
  });
});
