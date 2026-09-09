import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cpaSnapshotFreshness } from "../api/cpa";
import { cpaAssuranceSubTab } from "./CPAAssurancePanel";
import { CPAOverviewPanel } from "./CPAOverviewPanel";

const ACCOUNTS_METRIC = "cpa.accounts.health";

const accountsMetric = {
  metric_key: ACCOUNTS_METRIC,
  source: "cpa-cli-proxy-api",
  environment: "production",
  watermark: "0123456789abcdef0123456789abcdef",
  value: {
    run_id: "101",
    run_at: "2026-08-31T10:30:00Z",
    account_count: 3,
    disabled_count: 0,
    anomaly_count: 0,
    anomalies: [],
    truncated: false,
  },
  freshness: {
    state: "fresh",
    staleness_seconds: 30,
    threshold_seconds: 1800,
    is_partial: false,
    observed_at: "2026-08-31T10:31:00Z",
    last_success: "2026-08-31T10:31:00Z",
    last_error_code: "",
  },
};

function fakeResponse(body: unknown): Response {
  return { ok: true, status: 200, json: () => Promise.resolve(body) } as unknown as Response;
}

function stubMetrics(item: unknown = accountsMetric) {
  vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(fakeResponse({ items: [item] }))));
}

function renderWithQuery(ui: React.ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

describe("CPA inspection run time", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("概览卡同时显示真实 run id 与 started_at_ms 时间", async () => {
    stubMetrics();
    renderWithQuery(<CPAOverviewPanel />);
    expect(await screen.findByText("禁用 0 · 异常 0 · 巡检 101 · 2026-08-31 10:30 UTC")).toBeTruthy();
  });

  it("渠道保障页显示最近巡检批次及时间", async () => {
    stubMetrics();
    renderWithQuery(cpaAssuranceSubTab("overview"));
    expect(await screen.findByText("巡检批次 101 · 2026-08-31 10:30 UTC")).toBeTruthy();
  });

  it("没有巡检轮次时不把 0 个账号显示成正常", async () => {
    const { run_at: _ignoredRunAt, ...noRunValue } = accountsMetric.value;
    stubMetrics({ ...accountsMetric, value: { ...noRunValue, run_id: "" } });
    renderWithQuery(cpaAssuranceSubTab("overview"));
    expect(await screen.findByText("还没有巡检结果")).toBeTruthy();
    expect(screen.queryByText("正常")).toBeNull();
  });

  it("同步失败时概览不把 last-good 旧值标成正常", async () => {
    stubMetrics({
      ...accountsMetric,
      freshness: { ...accountsMetric.freshness, state: "failed", last_error_code: "bad_response" },
    });
    renderWithQuery(<CPAOverviewPanel />);
    expect(await screen.findByText("同步失败")).toBeTruthy();
    expect(screen.queryByText("正常")).toBeNull();
  });

  it("逐 Key 快照按 producer 时间计算 stale，而不是永久 fresh", () => {
    const freshness = cpaSnapshotFreshness(
      {
        observed_at: "2026-08-31T10:00:00Z",
        source: "cpa-file",
        watermark: "generation-a",
        is_partial: false,
      },
      Date.parse("2026-08-31T12:00:00Z"),
    );
    expect(freshness.state).toBe("stale");
    expect(freshness.staleness_seconds).toBe(7200);
  });
});
