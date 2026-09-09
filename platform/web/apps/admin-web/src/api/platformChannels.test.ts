import { describe, expect, it, vi } from "vitest";
import type { ApiClient } from "./client";
import {
  confirmPlatformChannelBinding,
  listPlatformChannels,
  removePlatformChannelBinding,
} from "./platformChannels";

function client(body: unknown): ApiClient {
  return { get: vi.fn().mockResolvedValue(body), post: vi.fn() } as unknown as ApiClient;
}

function postClient(body: unknown): ApiClient {
  return { get: vi.fn(), post: vi.fn().mockResolvedValue(body) } as unknown as ApiClient;
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

describe("渠道绑定的两个 L1 Action（后端 finance.platform_channel_binding.{set,remove}@1 已注册）", () => {
  it("确认绑定：走 Action 执行入口，只带 service_id/external_channel_id/upstream_account_id/reason", async () => {
    const api = postClient({ action_run_id: "run-9" });
    const run = await confirmPlatformChannelBinding(
      {
        serviceId: "svc-1",
        externalChannelId: "ch-1",
        upstreamAccountId: "up-1",
        reason: "人工核对已确认",
      },
      {},
      api,
    );
    expect(api.post).toHaveBeenCalledWith(
      "/api/v1/actions/finance.platform_channel_binding.set/versions/1/execute",
      {
        params: {
          service_id: "svc-1",
          external_channel_id: "ch-1",
          upstream_account_id: "up-1",
          reason: "人工核对已确认",
        },
      },
      expect.objectContaining({ requestId: expect.any(String) }),
    );
    expect(run.runId).toBe("run-9");
  });

  it("改绑时带上 expected_binding_id 做乐观并发校验；首次绑定不带这个字段", async () => {
    const api = postClient({ action_run_id: "run-10" });
    await confirmPlatformChannelBinding(
      {
        serviceId: "svc-1",
        externalChannelId: "ch-1",
        upstreamAccountId: "up-2",
        expectedBindingId: "bind-old",
        reason: "上游账号登记错了，改绑",
      },
      {},
      api,
    );
    const body = (api.post as unknown as { mock: { calls: unknown[][] } }).mock.calls[0]?.[1] as {
      params: Record<string, string>;
    };
    expect(body.params.expected_binding_id).toBe("bind-old");
  });

  it("解绑：必须带 expected_binding_id 与 reason，走 remove Action", async () => {
    const api = postClient({ action_run_id: "run-11" });
    await removePlatformChannelBinding(
      {
        serviceId: "svc-1",
        externalChannelId: "ch-1",
        expectedBindingId: "bind-1",
        reason: "该渠道已停用，解除绑定",
      },
      {},
      api,
    );
    expect(api.post).toHaveBeenCalledWith(
      "/api/v1/actions/finance.platform_channel_binding.remove/versions/1/execute",
      {
        params: {
          service_id: "svc-1",
          external_channel_id: "ch-1",
          expected_binding_id: "bind-1",
          reason: "该渠道已停用，解除绑定",
        },
      },
      expect.objectContaining({ requestId: expect.any(String) }),
    );
  });
});
