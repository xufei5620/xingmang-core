import { describe, expect, it, vi } from "vitest";
import type { ApiClient } from "./client";
import type { PlatformApiConfig } from "./config";
import {
  listMetrics,
  listServices,
  serviceFreshness,
  SERVICE_STALENESS_THRESHOLD_SECONDS,
  type ServiceItem,
} from "./platform";

const config: PlatformApiConfig = {
  baseUrl: "",
  principalId: "dev-operator",
  principalType: "HUMAN",
  scopes: ["registry.read", "ops.read"],
};

function service(over: Partial<ServiceItem> = {}): ServiceItem {
  return {
    id: "11111111-1111-1111-1111-111111111111",
    service_type: "sub2api",
    instance_id: "sub2api-prod",
    environment: "production",
    endpoint: "https://sub2api.example.com",
    owner: "平台组",
    status: "active",
    source_watermark: "wm-1",
    observed_at: "2026-08-26T10:00:00Z",
    stale_seconds: 60,
    ...over,
  };
}

function fakeClient(body: unknown): ApiClient {
  return { get: vi.fn().mockResolvedValue(body) } as unknown as ApiClient;
}

describe("listServices / listMetrics", () => {
  it("items 为 null 时按空数组处理，页面不会炸", async () => {
    expect(await listServices({}, fakeClient({ items: null }), config)).toEqual([]);
    expect(await listMetrics({}, fakeClient({ items: null }), config)).toEqual([]);
  });

  it("配置里没有 environment 时不拼这个查询参数", async () => {
    const client = fakeClient({ items: [] });
    await listServices({}, client, config);
    expect(client.get).toHaveBeenCalledWith("/api/v1/services", {
      searchParams: { environment: undefined },
    });
  });

  it("显式配置了 environment 才传（传错会被后端 403）", async () => {
    const client = fakeClient({ items: [] });
    await listMetrics({}, client, { ...config, environment: "staging" });
    expect(client.get).toHaveBeenCalledWith("/api/v1/metrics", {
      searchParams: { environment: "staging" },
    });
  });
});

describe("serviceFreshness：把采集时间折算成统一新鲜度形状", () => {
  it("从未采集 → uninitialized，且不给落后秒数", () => {
    const f = serviceFreshness(service({ observed_at: null, stale_seconds: null }));
    expect(f.state).toBe("uninitialized");
    expect(f.staleness_seconds).toBeNull();
    expect(f.observed_at).toBeNull();
  });

  it("落后未超阈值 → fresh", () => {
    const f = serviceFreshness(service({ stale_seconds: SERVICE_STALENESS_THRESHOLD_SECONDS - 1 }));
    expect(f.state).toBe("fresh");
  });

  it("落后达到阈值 → stale", () => {
    const f = serviceFreshness(service({ stale_seconds: SERVICE_STALENESS_THRESHOLD_SECONDS }));
    expect(f.state).toBe("stale");
    expect(f.threshold_seconds).toBe(SERVICE_STALENESS_THRESHOLD_SECONDS);
  });

  it("绝不凭空造出 failed / partial —— Service 记录里没有这些信息", () => {
    for (const stale of [0, 100, 999999]) {
      const state = serviceFreshness(service({ stale_seconds: stale })).state;
      expect(["fresh", "stale"]).toContain(state);
    }
    expect(serviceFreshness(service()).is_partial).toBe(false);
    expect(serviceFreshness(service()).last_error_code).toBe("");
  });
});
