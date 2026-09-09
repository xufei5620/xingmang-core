import { afterEach, describe, expect, it, vi } from "vitest";

import { httpInvoiceApi } from "./http-api";

// XM-INV-DEAD-CONTAINMENT. The slice added one wire field,
// `contained_dead_events`, and it is optional on purpose: a server from before
// the slice omits it entirely, and the admin screen has to render 0 rather
// than NaN. Both halves of that were written and neither was covered -- a
// final review deleted the `?? 0` and the whole suite stayed green.
//
// This is a fetch-level test rather than a unit test of the mapper because
// mapSourceHealth is not exported, and the reason it is not exported is that
// its counter validation is part of the contract: a payload that omits the
// field has to survive that validation too, not just the assignment at the
// end of it. Stubbing fetch exercises both.

const SOURCE_INSTANCE = "10000000-0000-4000-8000-000000000001";

function healthItem(overrides: Record<string, unknown> = {}) {
  return {
    source_instance_id: SOURCE_INSTANCE,
    source_type: "sub2api",
    source_name: "SoloV API",
    source_enabled: true,
    stream_id: "usage",
    sequence: 12,
    approved_runtime_version: "0.3.0",
    cutover_runtime_version: "0.3.0",
    observed_runtime_version: "0.3.0",
    observed_agent_version: "0.3.0",
    projection_status: "healthy",
    last_accepted_at: "2026-09-09T05:00:00Z",
    last_nonempty_batch_at: "2026-09-09T05:00:00Z",
    economic_watermark_at: "2026-09-09T05:00:00Z",
    maximum_age_seconds: 900,
    economic_watermark_maximum_age_seconds: 900,
    pending_events: 0,
    dead_events: 3,
    contained_dead_events: 2,
    waiting_dependencies: 0,
    ready: true,
    reasons: ["EVENTS_DEAD_CONTAINED"],
    ...overrides,
  };
}

function stubFetchReturning(payload: unknown) {
  vi.stubGlobal("window", {
    setTimeout: globalThis.setTimeout.bind(globalThis),
    clearTimeout: globalThis.clearTimeout.bind(globalThis),
  });
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

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("source health contained dead events", () => {
  it("carries the server's contained count onto the item", async () => {
    stubFetchReturning({ ready: true, items: [healthItem()] });
    const report = await httpInvoiceApi.getSourceHealth();
    expect(report.items).toHaveLength(1);
    expect(report.items[0].deadEvents).toBe(3);
    expect(report.items[0].containedDeadEvents).toBe(2);
  });

  it("reads a server that omits the field as zero contained, not undefined", async () => {
    const { contained_dead_events: _omitted, ...withoutField } = healthItem();
    stubFetchReturning({ ready: true, items: [withoutField] });
    const report = await httpInvoiceApi.getSourceHealth();
    // Not toBeFalsy: undefined would satisfy that, and undefined is exactly
    // the bug -- `待处理 0 / 死信 3` would then be followed by NaN once the
    // row compares it to zero.
    expect(report.items[0].containedDeadEvents).toBe(0);
    expect(Number.isSafeInteger(report.items[0].containedDeadEvents)).toBe(true);
  });

  it("keeps a contained count of zero distinguishable from a missing field", async () => {
    stubFetchReturning({
      ready: true,
      items: [healthItem({ contained_dead_events: 0, dead_events: 0, reasons: [] })],
    });
    const report = await httpInvoiceApi.getSourceHealth();
    expect(report.items[0].containedDeadEvents).toBe(0);
  });

  it("rejects a contained count that is not a whole non-negative number", async () => {
    stubFetchReturning({
      ready: true,
      items: [healthItem({ contained_dead_events: -1 })],
    });
    // The counter validation is the reason this test goes through fetch: the
    // new field was added to the validated `counters` array, and a field that
    // is mapped but not validated is a different, weaker contract.
    await expect(httpInvoiceApi.getSourceHealth()).rejects.toThrow();
  });
});
