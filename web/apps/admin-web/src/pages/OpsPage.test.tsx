import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { OpsOverview } from "../api/ops";
import { OpsPage } from "./OpsPage";

function jsonResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

const FRESHNESS = {
  state: "fresh" as const,
  staleness_seconds: 5,
  threshold_seconds: 120,
  is_partial: false,
  observed_at: "2026-08-31T03:04:05Z",
  last_success: "2026-08-31T03:04:05Z",
  last_error_code: "",
};

const FIXTURE: OpsOverview = {
  build: { version: "1.2.3", commit: "abcdef1", environment: "development" },
  worker_heartbeat: {
    metric_key: "platform.heartbeat",
    source: "platform-worker",
    value: { job_id: 123, attempt: 1 },
    freshness: FRESHNESS,
  },
  sync_pipelines: [
    {
      kind: "sub2api_sync",
      platform: "sub2api",
      config_available: true,
      effective_mode: "real",
      config_updated_at: "2026-08-31T03:00:00Z",
      sample_metric_key: "sub2api.channels.status",
      source: "sub2api",
      freshness: FRESHNESS,
    },
    {
      kind: "newapi_sync",
      platform: "newapi",
      config_available: true,
      effective_mode: "fake",
      config_updated_at: null,
      sample_metric_key: "newapi.channels.status",
      source: "newapi",
      freshness: { ...FRESHNESS, state: "stale", staleness_seconds: 400 },
    },
  ],
  connector_health: [
    {
      metric_key: "sub2api.connector.health",
      source: "sub2api",
      value: {
        version: "0.1.152",
        supported: true,
        healthy: true,
        kind: "",
        latency_ms: 12,
        checked_at: "2026-08-31T03:04:05Z",
      },
      freshness: FRESHNESS,
    },
    {
      metric_key: "newapi.connector.health",
      source: "newapi",
      value: {
        version: "0.2.0",
        supported: true,
        healthy: false,
        kind: "auth",
        latency_ms: 20,
        checked_at: "2026-08-31T03:04:05Z",
      },
      freshness: { ...FRESHNESS, state: "failed", last_error_code: "AUTH_FAILED" },
    },
  ],
  alert_delivery: { telegram_configured: true, webhook_configured: false },
  retention: {
    metric_key: "platform.retention.last_run",
    source: "platform-worker",
    value: {
      metric_samples_deleted: 0,
      resolved_alerts_deleted: 0,
      sample_retention_days: 90,
      alert_retention_days: 180,
    },
    freshness: FRESHNESS,
  },
  database: { connected: true },
  failed_jobs_by_kind: [],
  failed_jobs_status: "ok",
  failed_jobs_window_hours: 24,
};

function renderOps(initialEntry = "/ops", body: unknown = FIXTURE) {
  const fetchImpl = vi.fn(async () => jsonResponse(body));
  vi.stubGlobal("fetch", fetchImpl);
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <OpsPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return fetchImpl;
}

afterEach(() => vi.unstubAllGlobals());

describe("OpsPage 子页", () => {
  it("默认进入控制平面健康、请求真实数据，且六格页签条都在", async () => {
    const fetchImpl = renderOps();

    for (const label of [
      "控制平面健康",
      "稳定性与外部监控",
      "备份与恢复",
      "故障处理手册（Runbook）",
      "迁移与数据对比",
      "模型质量保障",
    ]) {
      expect(screen.getByRole("tab", { name: label })).toBeTruthy();
    }

    expect(await screen.findByText("worker 心跳")).toBeTruthy();
    await waitFor(() => expect(fetchImpl).toHaveBeenCalled());
    expect(JSON.stringify(fetchImpl.mock.calls).includes("/api/v1/ops/overview")).toBe(true);

    for (const component of [
      "sub2api 采集链路",
      "newapi 采集链路",
      "sub2api 连接器健康",
      "newapi 连接器健康",
      "保留期清理",
    ]) {
      expect(screen.getByText(component)).toBeTruthy();
    }

    // 摘要行：build 信息 + 两个投递渠道 + 数据库连通性
    expect(screen.getByText("1.2.3")).toBeTruthy();
    expect(screen.getByText("Telegram 已配置")).toBeTruthy();
    expect(screen.getByText("Webhook 未配置")).toBeTruthy();
    expect(screen.getByText("数据库：已连接")).toBeTruthy();

    // newapi 连接器健康那一行：kind="auth" 进依赖列，last_error_code 进最近错误列
    expect(screen.getByText("auth")).toBeTruthy();
    expect(screen.getByText("AUTH_FAILED")).toBeTruthy();
  });

  it("?sub=health 与不带 sub 行为一致", async () => {
    const fetchImpl = renderOps("/ops?sub=health");
    expect(await screen.findByText("worker 心跳")).toBeTruthy();
    await waitFor(() => expect(fetchImpl).toHaveBeenCalled());
  });

  it("稳定性子页诚实显示未接入，且不发起请求", async () => {
    const fetchImpl = renderOps("/ops?sub=stability");
    expect(await screen.findByText("「稳定性与外部监控」尚未接入")).toBeTruthy();
    // 同一句说明在页头与 PageState 里各出现一次（两处刻意复用同一段已核准文案）
    expect(screen.getAllByText(/Uptime Kuma/).length).toBeGreaterThan(0);
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("备份与恢复子页诚实显示未接入，且不发起请求", async () => {
    const fetchImpl = renderOps("/ops?sub=backup");
    expect(await screen.findByText("「备份与恢复」尚未接入")).toBeTruthy();
    expect(screen.getAllByText(/Platform Lifecycle Operation/).length).toBeGreaterThan(0);
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("未知子页不静默回落到控制平面健康", async () => {
    const fetchImpl = renderOps("/ops?sub=not-a-real-tab");
    expect(await screen.findByText("「not-a-real-tab」子页尚未接入")).toBeTruthy();
    expect(screen.getByRole("link", { name: "返回控制平面健康" }).getAttribute("href")).toBe(
      "/ops?sub=health",
    );
    expect(fetchImpl).not.toHaveBeenCalled();
  });
});

// XM-WORKBENCH-WIRE-OPS：纯函数那一层已经把三条分支钉过了，这一组只回答
// 「那句话真的被渲染到格子上了吗」——判据算得对而没人把它挂上去，单测全绿。
describe("采集链路的「模式未知」（XM-WORKBENCH-WIRE-OPS）", () => {
  function withPipelines(pipelines: unknown[]) {
    return { ...FIXTURE, sync_pipelines: pipelines };
  }
  const sub2api = FIXTURE.sync_pipelines[0];
  const newapi = FIXTURE.sync_pipelines[1];

  it("source=unknown 的那一行显示「模式未知」，悬浮里写着去哪配", async () => {
    renderOps(
      "/ops",
      withPipelines([
        { ...sub2api, effective_mode: "", effective_mode_source: "unknown" },
        { ...newapi, effective_mode: "real", effective_mode_source: "database" },
      ]),
    );
    const badge = await screen.findByText("模式未知");
    expect(badge.getAttribute("title")).toContain("未在后台配置接入模式");
    expect(badge.getAttribute("title")).toContain("设置 → 凭据 → 接入模式");
    // 「不知道」没有被显示成一个具体的模式：这一屏上不该出现「模拟数据」
    // ——后端此前正是在这种情况下硬答 fake 的。
    expect(screen.queryByText("模拟数据")).toBeNull();
  });

  it("source=database 的行照常显示模式，且不挂那句悬浮说明", async () => {
    renderOps(
      "/ops",
      withPipelines([
        { ...sub2api, effective_mode: "real", effective_mode_source: "database" },
        { ...newapi, effective_mode: "fake", effective_mode_source: "database" },
      ]),
    );
    expect(await screen.findByText("真实对接")).toBeTruthy();
    expect(screen.getByText("真实对接").getAttribute("title")).toBeNull();
    expect(screen.getByText("模拟数据").getAttribute("title")).toBeNull();
    expect(screen.queryByText("模式未知")).toBeNull();
  });

  it("老后端没有这一列时照旧显示模式，不因为字段缺席就退化成「模式未知」", async () => {
    // FIXTURE 里那两条本来就不带 effective_mode_source。
    renderOps();
    expect(await screen.findByText("真实对接")).toBeTruthy();
    expect(screen.getByText("模拟数据")).toBeTruthy();
    expect(screen.queryByText("模式未知")).toBeNull();
  });
});
