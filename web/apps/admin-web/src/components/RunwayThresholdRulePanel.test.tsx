import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { RunwayThresholdRulePanel } from "./RunwayThresholdRulePanel";

function jsonResponse(body: unknown, status = 200): Response {
  return { ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function renderPanel(fetchImpl: typeof fetch) {
  vi.stubGlobal("fetch", fetchImpl);
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={queryClient}><MemoryRouter><RunwayThresholdRulePanel environment="development" /></MemoryRouter></QueryClientProvider>);
}

function currentResponse() {
  return jsonResponse({ environment: "development", critical_days: 5, warning_days: 10, serious_days: 20, revision: 8, source: "database", updated_at: "2026-08-29T01:00:00Z", updated_by: "staff:operator", reason: "initial" });
}

function historyResponse() {
  return jsonResponse({ has_more: false, items: [{ environment: "development", revision: 8, critical_days: 5, warning_days: 10, serious_days: 20, changed_at: "2026-08-29T01:00:00Z", changed_by: "staff:operator", reason: "initial", request_id: "req-8", change_source: "bootstrap" }] });
}

function previewResponse() {
  return jsonResponse({ current: { critical_days: 5, warning_days: 10, serious_days: 20 }, proposed: { critical_days: 4, warning_days: 9, serious_days: 20 }, current_revision: 8, evaluation_at: "2026-08-29T02:00:00Z", coverage: { total: 2, known: 2, unknown_reasons: {} }, counts: { would_open: 0, would_escalate: 1, would_deescalate: 0, would_resolve: 0, unchanged: 1, current_inconsistent: 0 }, items: [{ account_id: "up-1", name: "Relay A", days: 5, old_level: "critical", new_level: "warning", alert_transition: "unchanged", consistency_reason: null, observed_at: "2026-08-29T01:00:00Z" }], has_more: false });
}

afterEach(() => vi.unstubAllGlobals());

describe("RunwayThresholdRulePanel", () => {
  it("renders current snapshot, serious display-only copy, history and fixed Foundation gate", async () => {
    const fetchImpl = vi.fn(async (input: string) => input.includes("/history") ? historyResponse() : currentResponse());
    renderPanel(fetchImpl as typeof fetch);
    expect(await screen.findByText("当前阈值快照")).toBeTruthy();
    expect(screen.getByText("Serious")).toBeTruthy();
    expect(screen.getByText("仅展示关注色，不创建 R5 通知")).toBeTruthy();
    expect(screen.getByText("Foundation-B / C3c 尚未开放")).toBeTruthy();
    expect(screen.getByText("最近变更")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "提交审批" })).toBeNull();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("rejects non-increasing draft before sending preview request", async () => {
    const fetchImpl = vi.fn(async (input: string) => input.includes("/history") ? historyResponse() : currentResponse());
    renderPanel(fetchImpl as typeof fetch);
    await screen.findByText("当前阈值快照");
    fireEvent.change(screen.getByLabelText("Critical 天数"), { target: { value: "10" } });
    fireEvent.change(screen.getByLabelText("Warning 天数"), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "预览影响" }));
    expect(await screen.findByText("必须满足 0 < critical < warning < serious")).toBeTruthy();
    expect(fetchImpl.mock.calls.filter(([input]) => String(input).includes("/preview"))).toHaveLength(0);
  });

  it("shows evaluation time before impact rows and keeps observation time per account", async () => {
    const fetchImpl = vi.fn(async (input: string) => {
      if (input.includes("/history")) return historyResponse();
      if (input.includes("/preview")) return previewResponse();
      return currentResponse();
    });
    renderPanel(fetchImpl as typeof fetch);
    await screen.findByText("当前阈值快照");
    fireEvent.change(screen.getByLabelText("Critical 天数"), { target: { value: "4" } });
    fireEvent.change(screen.getByLabelText("Warning 天数"), { target: { value: "9" } });
    fireEvent.click(screen.getByRole("button", { name: "预览影响" }));
    const result = await screen.findByText("预览结果");
    expect(result).toBeTruthy();
    const section = result.closest("section") as HTMLElement;
    expect(within(section).getByText(/评估时间 2026-08-29 02:00:00 UTC/)).toBeTruthy();
    expect(within(section).getByText("2026-08-29 01:00:00 UTC")).toBeTruthy();
    await waitFor(() => expect(fetchImpl.mock.calls.some(([input]) => String(input).includes("/preview"))).toBe(true));
  });

  it("marks stale observations and gives a dedicated current-inconsistency warning", async () => {
    const fetchImpl = vi.fn(async (input: string) => {
      if (input.includes("/history")) return historyResponse();
      if (input.includes("/preview")) return jsonResponse({
        current: { critical_days: 5, warning_days: 10, serious_days: 20 },
        proposed: { critical_days: 4, warning_days: 9, serious_days: 20 },
        current_revision: 8, evaluation_at: "2026-08-29T04:00:00Z",
        coverage: { total: 1, known: 1, unknown_reasons: {} },
        counts: { would_open: 0, would_escalate: 0, would_deescalate: 0, would_resolve: 0, unchanged: 0, current_inconsistent: 1 },
        items: [{ account_id: "up-1", name: "Relay A", days: 0, old_level: "critical", new_level: "critical", alert_transition: "current_inconsistent", consistency_reason: "missing_active_alert", observed_at: "2026-08-29T01:00:00Z" }],
        has_more: false,
      });
      return currentResponse();
    });
    renderPanel(fetchImpl as typeof fetch);
    await screen.findByText("当前阈值快照");
    fireEvent.change(screen.getByLabelText("Critical 天数"), { target: { value: "4" } });
    fireEvent.click(screen.getByRole("button", { name: "预览影响" }));
    const result = await screen.findByText("预览结果");
    const section = result.closest("section") as HTMLElement;
    expect(within(section).getByText(/请先核查 Worker 评估任务/)).toBeTruthy();
    expect(within(section).getByText("观测偏旧")).toBeTruthy();
    expect(within(section).getByText(/当前分类已进入告警档/)).toBeTruthy();
  });
});
