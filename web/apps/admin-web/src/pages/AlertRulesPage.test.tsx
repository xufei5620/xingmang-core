import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AlertRulesPage } from "./AlertRulesPage";

afterEach(() => vi.unstubAllGlobals());

it("规则页提供内联入口并保留只读门禁", async () => {
  vi.stubGlobal("fetch", vi.fn(async (input: string) => {
    if (input.includes("/history")) return { ok: true, status: 200, json: () => Promise.resolve({ items: [], has_more: false }) } as unknown as Response;
    return { ok: true, status: 200, json: () => Promise.resolve({ environment: "development", critical_days: 5, warning_days: 10, serious_days: 20, revision: 1, source: "database", updated_at: "2026-08-29T00:00:00Z", updated_by: "bootstrap", reason: "initial" }) } as unknown as Response;
  }));
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={queryClient}><MemoryRouter><AlertRulesPage /></MemoryRouter></QueryClientProvider>);
  expect(await screen.findByRole("heading", { name: "告警与故障", level: 2 })).toBeTruthy();
  expect(await screen.findByText("Foundation-B / C3c 尚未开放")).toBeTruthy();
  expect(screen.queryByRole("dialog")).toBeNull();
});
