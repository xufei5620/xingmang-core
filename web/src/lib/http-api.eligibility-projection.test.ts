import { afterEach, describe, expect, it, vi } from "vitest";

import { httpInvoiceApi } from "./http-api";

// XM-INV-PROJECTION-HEALTH-UI: the admin source-health endpoint has carried
// an eligibility_projection block since XM-INV-PROJECTION-FAILURE-GRADING,
// and the client dropped it on the floor. These cases pin the three
// behaviours the screen depends on: the block maps through when present, an
// absent block is not an error (a server predating the field returns none),
// and a present-but-malformed block fails the same way a malformed stream row
// does rather than rendering half of it.
//
// Follows http-api.source-instance-filter.test.ts for the fetch/timer stubs;
// this project's vitest runs in plain Node, so window is stubbed down to the
// two timer functions requestSourceHealth actually uses.

function stubWindowTimers() {
  vi.stubGlobal("window", {
    setTimeout: globalThis.setTimeout.bind(globalThis),
    clearTimeout: globalThis.clearTimeout.bind(globalThis),
  });
}

function streamRow(
  sourceType: "sub2api" | "newapi",
  streamId: "payments" | "identities" | "usage" | "credits" | "balances",
  index: number,
) {
  return {
    source_instance_id: `10000000-0000-4000-8000-00000000000${index}`,
    source_type: sourceType,
    source_name: "ignored",
    source_enabled: true,
    stream_id: streamId,
    sequence: 12,
    approved_runtime_version: "0.1.179",
    observed_runtime_version: "0.1.179",
    observed_agent_version: "0.3.2",
    projection_status: "healthy" as const,
    last_accepted_at: new Date().toISOString(),
    economic_watermark_at:
      streamId === "identities" ? undefined : new Date().toISOString(),
    maximum_age_seconds: streamId === "identities" ? 900 : 300,
    economic_watermark_maximum_age_seconds:
      streamId === "identities" ? undefined : 900,
    pending_events: 0,
    dead_events: 0,
    waiting_dependencies: 0,
    ready: true,
    reasons: [] as string[],
  };
}

function healthPayload(projection?: unknown) {
  const items = (["sub2api", "newapi"] as const).flatMap((sourceType, index) =>
    (["payments", "identities", "usage", "credits", "balances"] as const).map(
      (streamId) => streamRow(sourceType, streamId, index + 1),
    ),
  );
  const payload: Record<string, unknown> = { ready: true, items };
  if (projection !== undefined) payload.eligibility_projection = projection;
  return payload;
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

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("source health carries the eligibility projection block", () => {
  it("maps every counter and both optional timestamps", async () => {
    const oldestPending = "2026-09-03T17:40:00Z";
    const oldestProofPending = "2026-09-03T17:20:00Z";
    stubFetchReturning(
      healthPayload({
        queued: 3,
        processing: 1,
        retrying: 2,
        dead: 4,
        proof_pending: 5,
        oldest_pending: oldestPending,
        oldest_proof_pending: oldestProofPending,
      }),
    );

    const report = await httpInvoiceApi.getSourceHealth();

    expect(report.eligibilityProjection).toEqual({
      queued: 3,
      processing: 1,
      retrying: 2,
      dead: 4,
      proofPending: 5,
      oldestPendingAt: oldestPending,
      oldestProofPendingAt: oldestProofPending,
    });
  });

  it("omits the timestamps the server left out", async () => {
    stubFetchReturning(
      healthPayload({
        queued: 0,
        processing: 0,
        retrying: 0,
        dead: 0,
        proof_pending: 0,
      }),
    );

    const report = await httpInvoiceApi.getSourceHealth();

    expect(report.eligibilityProjection?.oldestPendingAt).toBeUndefined();
    expect(report.eligibilityProjection?.oldestProofPendingAt).toBeUndefined();
    expect(report.eligibilityProjection?.dead).toBe(0);
  });

  it("treats an absent block as unreported rather than as zeros", async () => {
    // A server predating XM-INV-PROJECTION-FAILURE-GRADING sends no block at
    // all. Reporting zeros there would read as an idle, healthy queue on a
    // screen an operator consults during an incident.
    stubFetchReturning(healthPayload());

    const report = await httpInvoiceApi.getSourceHealth();

    expect(report.eligibilityProjection).toBeUndefined();
    expect(report.items).toHaveLength(10);
    expect(report.ready).toBe(true);
  });

  it("rejects a malformed block the same way a malformed stream row is rejected", async () => {
    stubFetchReturning(
      healthPayload({
        queued: -1,
        processing: 0,
        retrying: 0,
        dead: 0,
        proof_pending: 0,
      }),
    );

    await expect(httpInvoiceApi.getSourceHealth()).rejects.toMatchObject({
      code: "INVALID_SOURCE_HEALTH_RESPONSE",
    });
  });

  it("rejects a block whose timestamp is not a date", async () => {
    stubFetchReturning(
      healthPayload({
        queued: 0,
        processing: 0,
        retrying: 0,
        dead: 0,
        proof_pending: 0,
        oldest_pending: "not-a-timestamp",
      }),
    );

    await expect(httpInvoiceApi.getSourceHealth()).rejects.toMatchObject({
      code: "INVALID_SOURCE_HEALTH_RESPONSE",
    });
  });
});
