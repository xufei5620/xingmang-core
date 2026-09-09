import { describe, expect, it, vi } from "vitest";
import { createApiClient, type FetchLike } from "./client";
import type { PlatformApiConfig } from "./config";
import {
  getRunwayThresholds,
  listRunwayThresholdHistory,
  previewRunwayThresholds,
} from "./runwayThresholds";

function response(body: unknown): Response {
  return { ok: true, status: 200, json: () => Promise.resolve(body) } as unknown as Response;
}

const config: PlatformApiConfig = { baseUrl: "http://api.test", environment: "staging", principalId: "staff", principalType: "HUMAN", scopes: ["finance.read"] };

describe("runway threshold API", () => {
  it("maps current snapshot and sends the environment", async () => {
    let requested = "";
    const fetchImpl: FetchLike = vi.fn(async (input) => {
      requested = input;
      return response({ environment: "staging", critical_days: 5, warning_days: 10, serious_days: 20, revision: 7, source: "database", updated_at: "2026-08-28T10:00:00Z", updated_by: "staff:operator", reason: "refresh" });
    });
    const snapshot = await getRunwayThresholds(createApiClient({ config, fetchImpl }), config);
    expect(new URL(requested).pathname).toBe("/api/v1/finance/runway-thresholds");
    expect(new URL(requested).searchParams.get("environment")).toBe("staging");
    expect(snapshot).toMatchObject({ criticalDays: 5, warningDays: 10, seriousDays: 20, revision: 7, source: "database" });
  });

  it("maps bounded history and forwards cursor/limit", async () => {
    let requested = "";
    const fetchImpl: FetchLike = vi.fn(async (input) => {
      requested = input;
      return response({ has_more: true, items: [{ environment: "staging", revision: 3, critical_days: 5, warning_days: 10, serious_days: 20, changed_at: "2026-08-28T10:00:00Z", changed_by: "staff", reason: "r", request_id: "req", change_source: "action" }] });
    });
    const page = await listRunwayThresholdHistory({ limit: 2, beforeRevision: 4 }, createApiClient({ config, fetchImpl }), config);
    const url = new URL(requested);
    expect(url.pathname).toBe("/api/v1/finance/runway-thresholds/history");
    expect(url.searchParams.get("limit")).toBe("2");
    expect(url.searchParams.get("before_revision")).toBe("4");
    expect(page).toEqual({ hasMore: true, items: [expect.objectContaining({ revision: 3, changeSource: "action" })] });
  });

  it("keeps evaluation_at separate from each observation and forwards AbortSignal", async () => {
    let requested = "";
    const signal = new AbortController().signal;
    const fetchImpl: FetchLike = vi.fn(async (input, init) => {
      requested = input;
      expect(init?.signal).toBe(signal);
      return response({
        current: { critical_days: 5, warning_days: 10, serious_days: 20 },
        proposed: { critical_days: 4, warning_days: 9, serious_days: 19 },
        current_revision: 8,
        evaluation_at: "2026-08-29T02:00:00Z",
        coverage: { total: 2, known: 1, unknown_reasons: { no_balance: 1 } },
        counts: { would_open: 1, would_escalate: 0, would_deescalate: 0, would_resolve: 0, unchanged: 0, current_inconsistent: 1 },
        items: [{ account_id: "a", name: "Relay", days: 0, old_level: "critical", new_level: "critical", alert_transition: "unchanged", consistency_reason: null, observed_at: "2026-08-29T01:00:00Z" }],
        has_more: false,
        alert_coverage_complete: true,
      });
    });
    const preview = await previewRunwayThresholds({ criticalDays: 4, warningDays: 9, seriousDays: 19 }, { limit: 20, signal }, createApiClient({ config, fetchImpl }), config);
    const url = new URL(requested);
    expect(url.pathname).toBe("/api/v1/finance/runway-thresholds/preview");
    expect(url.searchParams.get("critical_days")).toBe("4");
    expect(url.searchParams.get("warning_days")).toBe("9");
    expect(url.searchParams.get("serious_days")).toBe("19");
    expect(preview.evaluationAt).not.toBe(preview.items[0]?.observedAt);
    expect(preview.counts.currentInconsistent).toBe(1);
    expect(preview.alertCoverageComplete).toBe(true);
    expect(preview.items[0]?.days).toBe(0);
  });

  it("maps incomplete active-alert coverage as an explicit untrusted result", async () => {
    const fetchImpl: FetchLike = vi.fn(async () => response({
      current: { critical_days: 5, warning_days: 10, serious_days: 20 },
      proposed: { critical_days: 4, warning_days: 9, serious_days: 20 },
      current_revision: 8,
      evaluation_at: "2026-08-29T02:00:00Z",
      coverage: { total: 1, known: 1, unknown_reasons: {} },
      counts: { would_open: 1, would_escalate: 0, would_deescalate: 0, would_resolve: 0, unchanged: 0, current_inconsistent: 0 },
      items: [],
      has_more: false,
      alert_coverage_complete: false,
    }));
    const preview = await previewRunwayThresholds({ criticalDays: 4, warningDays: 9, seriousDays: 20 }, {}, createApiClient({ config, fetchImpl }), config);
    expect(preview.alertCoverageComplete).toBe(false);
  });

  it("fails closed on malformed successful envelopes instead of inventing zero values", async () => {
    const fetchImpl: FetchLike = vi.fn(async (input) => {
      if (String(input).includes("/history")) return response({ has_more: false });
      return response({ current: { critical_days: 5, warning_days: 10, serious_days: 20 }, proposed: { critical_days: 5, warning_days: 10, serious_days: 20 }, current_revision: 1, evaluation_at: "2026-08-29T00:00:00Z", coverage: { total: 0, known: 0, unknown_reasons: {} }, counts: { would_open: 0, would_escalate: 0, would_deescalate: 0, would_resolve: 0, unchanged: 0, current_inconsistent: 0 }, items: [], alert_coverage_complete: true });
    });
    await expect(listRunwayThresholdHistory({}, createApiClient({ config, fetchImpl }), config)).rejects.toThrow("items 数组");
    await expect(previewRunwayThresholds({ criticalDays: 5, warningDays: 10, seriousDays: 20 }, {}, createApiClient({ config, fetchImpl }), config)).rejects.toThrow("has_more");
  });
});
