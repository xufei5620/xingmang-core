import { describe, expect, it, vi } from "vitest";
import type { ApiClient } from "./client";
import { listPlatformChannels } from "./platformChannels";

function client(body: unknown): ApiClient {
  return { get: vi.fn().mockResolvedValue(body), post: vi.fn() } as unknown as ApiClient;
}

describe("平台渠道逐行 Query", () => {
  it("只映射服务端行，不把 nullable economics/balance 算成 0", async () => {
    const api = client({
      service: { id: "svc", service_type: "sub2api", instance_id: "s2", environment: "development" },
      inventory: { state: "ok", source: "s2", observed_at: null, complete: true, truncated: false, reported_count: 2, fetched_count: 2, coverage_partial: true, evidence: "reported_count" },
      from: "2026-08-29", to: "2026-08-29",
      items: [{
        channel_ref: { service_id: "svc", external_channel_id: "1" }, name: "未映射",
        binding: null, candidate: { state: "unmapped", evidence_status: "insufficient", upstream_account_ids: [], reason_codes: [] },
        economics: null, economics_state: "unmapped", conflicts: [], health: null, models: null, assurance: null, runway: null,
        observed: { source: "s2", observed_at: null, is_stale: false },
      }], runway_coverage: { total: 0, known: 0, reasons: {} }, next_cursor: null,
    });
    const page = await listPlatformChannels("sub2api", "svc", {}, api);
    expect(page.items).toHaveLength(1);
    expect(page.items[0]?.economics).toBeNull();
    expect(page.items[0]?.candidate.state).toBe("unmapped");
    expect(page.inventory.reportedCount).toBe(2);
  });

  it("保留 service_id、日期窗口和游标参数，502 由 ApiError 原样交给页面状态", async () => {
    const api = client({ items: [], service: {}, inventory: {}, from: "2026-08-29", to: "2026-08-29", runway_coverage: {}, next_cursor: null });
    await listPlatformChannels("newapi", "service/1", { from: "2026-08-01", to: "2026-08-29", limit: 50, cursor: "opaque" }, api);
    expect(api.get).toHaveBeenCalledWith("/api/v1/platforms/newapi/channels", {
      searchParams: { service_id: "service/1", from: "2026-08-01", to: "2026-08-29", limit: "50", cursor: "opaque" },
    });
  });
});
