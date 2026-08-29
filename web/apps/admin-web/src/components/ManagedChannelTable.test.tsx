import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ManagedChannelTable } from "./ManagedChannelTable";

function response(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function page() {
  return {
    service: { id: "svc-1", service_type: "newapi", instance_id: "newapi-a", environment: "development" },
    inventory: { state: "ok", source: "newapi-a", observed_at: "2026-08-29T01:00:00Z", complete: true, truncated: false, reported_count: 2, fetched_count: 2, coverage_partial: false, evidence: "reported_count" },
    from: "2026-08-29", to: "2026-08-29",
    items: [
      { channel_ref: { service_id: "svc-1", external_channel_id: "1" }, name: "OpenAI A", binding: { id: "b1", upstream_account_id: "up-1", valid_from: "2026-08-28T00:00:00Z", reason: "人工确认" }, candidate: { state: "candidate", evidence_status: "sufficient", upstream_account_ids: ["up-1"], reason_codes: [], platform_assignment_missing: false, inventory_unknown: false }, economics: null, economics_state: "binding_pending_economics", conflicts: [], health: { state: "observed" }, models: { count: 4 }, assurance: null, runway: { days: 12 }, observed: { source: "newapi-a", observed_at: "2026-08-29T01:00:00Z", is_stale: false } },
      { channel_ref: { service_id: "svc-1", external_channel_id: "2" }, name: "Claude B", binding: { id: "b2", upstream_account_id: "up-1", valid_from: "2026-08-28T00:00:00Z", reason: "人工确认" }, candidate: { state: "candidate", evidence_status: "sufficient", upstream_account_ids: ["up-1"], reason_codes: [], platform_assignment_missing: false, inventory_unknown: false }, economics: null, economics_state: "binding_pending_economics", conflicts: [], health: { state: "observed" }, models: { count: 2 }, assurance: null, runway: { days: 12 }, observed: { source: "newapi-a", observed_at: "2026-08-29T01:00:00Z", is_stale: false } },
    ],
    runway_coverage: { total: 1, known: 1, reasons: {} }, next_cursor: null,
  };
}

function renderPanel() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><MemoryRouter><ManagedChannelTable platform="newapi" serviceId="svc-1" /></MemoryRouter></QueryClientProvider>);
}

describe("ChannelRef 粒度渠道表", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("同一上游的两个渠道仍是两行，且共享的模型/余额事实不合计", async () => {
    vi.stubGlobal("fetch", vi.fn((url: string) => {
      if (url.includes("/platforms/newapi/channels")) return Promise.resolve(response(page()));
      return Promise.resolve(response({ items: [] }));
    }));
    renderPanel();
    const table = within(await screen.findByRole("table"));
    expect(table.getByText("OpenAI A")).toBeTruthy();
    expect(table.getByText("Claude B")).toBeTruthy();
    expect(screen.getByText("已确认映射").closest("article")?.textContent).toContain("2");
    expect(screen.getByText(/共享余额只作为引用/)).toBeTruthy();
  });

  it("502/读取失败显示页级错误，不保留旧渠道行", async () => {
    vi.stubGlobal("fetch", vi.fn((url: string) => {
      if (url.includes("/platforms/newapi/channels")) return Promise.resolve(response({ error: { code: "EXECUTION_FAILED", message: "目录读取失败" } }, 502));
      return Promise.resolve(response({ items: [] }));
    }));
    renderPanel();
    expect(await screen.findByRole("button", { name: /重试/ })).toBeTruthy();
    expect(screen.queryByText("OpenAI A")).toBeNull();
  });
});
