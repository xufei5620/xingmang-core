import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ServerOverviewPanel } from "./ServerOverviewPanel";

function response(body: unknown, status = 200): Response {
  return { ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function renderPanel() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <ServerOverviewPanel />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function handler({
  assets = [],
  suppliers = [],
  domains = [],
}: {
  assets?: unknown[];
  suppliers?: unknown[];
  domains?: unknown[];
} = {}) {
  return vi.fn((url: string) => {
    if (url.startsWith("/api/v1/servers/assets")) return Promise.resolve(response({ items: assets }));
    if (url.startsWith("/api/v1/servers/suppliers")) return Promise.resolve(response({ items: suppliers }));
    if (url.startsWith("/api/v1/servers/domains")) return Promise.resolve(response({ items: domains }));
    return Promise.resolve(response({ items: [] }));
  });
}

describe("服务器 · 概览", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("空登记簿时汇总数字都是 0，不是「—」或崩溃", async () => {
    vi.stubGlobal("fetch", handler());
    renderPanel();
    expect(await screen.findByText("服务器资产")).toBeTruthy();
    const zeroTiles = await screen.findAllByText("0");
    expect(zeroTiles.length).toBeGreaterThan(0);
  });

  it("月成本按币种分别合计，已退役资产不计入", async () => {
    vi.stubGlobal(
      "fetch",
      handler({
        assets: [
          { id: "1", hostname: "a", ip_addresses: [], datacenter: "", supplier_id: "", vcpu: null, memory_gb: null, disk_gb: null, purpose: "", status: "active", monthly_cost_minor_units: "9990", currency: "USD", billing_cycle: "monthly", expires_at: "", notes: "", environment: "development", created_at: "", updated_at: "" },
          { id: "2", hostname: "b", ip_addresses: [], datacenter: "", supplier_id: "", vcpu: null, memory_gb: null, disk_gb: null, purpose: "", status: "active", monthly_cost_minor_units: "500", currency: "USD", billing_cycle: "monthly", expires_at: "", notes: "", environment: "development", created_at: "", updated_at: "" },
          // 已退役：不该计入合计
          { id: "3", hostname: "c", ip_addresses: [], datacenter: "", supplier_id: "", vcpu: null, memory_gb: null, disk_gb: null, purpose: "", status: "retired", monthly_cost_minor_units: "100000", currency: "USD", billing_cycle: "monthly", expires_at: "", notes: "", environment: "development", created_at: "", updated_at: "" },
        ],
      }),
    );
    renderPanel();
    // (9990 + 500) 分 = $104.90
    expect(await screen.findByText("$104.90")).toBeTruthy();
  });

  it("30 天内到期的服务器/域名/证书计入警示统计", async () => {
    const now = new Date();
    const soon = new Date(now.getTime() + 5 * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
    vi.stubGlobal(
      "fetch",
      handler({
        assets: [
          { id: "1", hostname: "a", ip_addresses: [], datacenter: "", supplier_id: "", vcpu: null, memory_gb: null, disk_gb: null, purpose: "", status: "active", monthly_cost_minor_units: null, currency: "", billing_cycle: "", expires_at: soon, notes: "", environment: "development", created_at: "", updated_at: "" },
        ],
        domains: [
          { id: "1", domain_name: "x.example.com", registrar: "", dns_provider: "", expires_at: soon, cert_source: "", cert_expires_at: "", bound_service_note: "", environment: "development", created_at: "", updated_at: "" },
        ],
      }),
    );
    renderPanel();
    const tile = within((await screen.findByText("30 天内到期")).closest("div")!.parentElement as HTMLElement);
    expect(tile.getByText(/服务器续费 1/)).toBeTruthy();
    expect(tile.getByText(/域名 1/)).toBeTruthy();
  });

  it("按拍板结论写明监控与告警未接入的理由", async () => {
    vi.stubGlobal("fetch", handler());
    renderPanel();
    expect(await screen.findByText(/服务器只做记录/)).toBeTruthy();
  });
});
