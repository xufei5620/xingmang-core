import { describe, expect, it } from "vitest";

import type { EligibilityFreeze } from "../types";
import { isMechanicalReconciliationFreeze } from "./workflow";

// CR-0009 "变更范围" item 5: the "资格冻结" tab's default-narrow toggle
// hides exactly the freeze shapes the auto-reconciliation/queue-narrowing
// slices made obsolete (see this function's own doc comment in
// workflow.ts), and shows every other reason unchanged.

function freeze(overrides: Partial<EligibilityFreeze>): EligibilityFreeze {
  return {
    id: "31000000-0000-4000-8000-000000000001",
    source: "sub2api",
    sourceInstanceId: "10000000-0000-4000-8000-000000000001",
    sourceLabel: "SoloV API",
    scope: "account",
    reason: "SOURCE_GAP",
    status: "open",
    eligibilityStatus: "frozen",
    openedAt: "2026-09-03T02:00:00.000Z",
    version: 1,
    externalUserId: "1147",
    ...overrides,
  };
}

describe("isMechanicalReconciliationFreeze", () => {
  it("hides UNKNOWN_NEGATIVE_BALANCE (auto-reconciliation made this obsolete)", () => {
    expect(
      isMechanicalReconciliationFreeze(
        freeze({ reason: "UNKNOWN_NEGATIVE_BALANCE" }),
      ),
    ).toBe(true);
  });

  it("hides USAGE_EXCEEDS_LEDGER (auto-reconciliation made this obsolete)", () => {
    expect(
      isMechanicalReconciliationFreeze(freeze({ reason: "USAGE_EXCEEDS_LEDGER" })),
    ).toBe(true);
  });

  it("hides a generalized (account-scoped) LATE_FINALIZED_EVENT", () => {
    expect(
      isMechanicalReconciliationFreeze(
        freeze({ reason: "LATE_FINALIZED_EVENT", scope: "account" }),
      ),
    ).toBe(true);
  });

  it("keeps a precise, funding-lot-scoped LATE_FINALIZED_EVENT (a real red-reversal)", () => {
    expect(
      isMechanicalReconciliationFreeze(
        freeze({ reason: "LATE_FINALIZED_EVENT", scope: "funding_lot" }),
      ),
    ).toBe(false);
  });

  it.each([
    "AMBIGUOUS_EVENT_ORDER",
    "EVENT_PAYLOAD_DRIFT",
    "UNIT_MISMATCH",
    "STREAM_WATERMARK_REGRESSION",
    "SOURCE_GAP",
    "SOURCE_REFUND",
  ] as const)("keeps %s (design 3(C)'s own retained manual-review list)", (reason) => {
    expect(isMechanicalReconciliationFreeze(freeze({ reason }))).toBe(false);
  });
});
