import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PlatformAlertsPanel } from "./PlatformAlertsPanel";

function response(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function alert(overrides: Record<string, unknown> = {}) {
  return {
    id: "alert-newapi-1",
    rule_key: "metric.data.stale",
    dedup_key: "newapi-channel-health",
    severity: "warning",
    status: "OPEN",
    title: "NewAPI 渠道健康数据延迟",
    detail: "最近一次同步已超过阈值",
    environment: "production",
    source_metric_key: "newapi.channels.status",
    opened_at: "2026-08-29T07:00:00Z",
    last_seen_at: "2026-08-29T08:00:00Z",
    acknowledged_at: null,
    resolved_at: null,
    fire_count: 3,
    notify_status: "delivered",
    notify_error: "",
    notified_at: "2026-08-29T08:01:00Z",
    ...overrides,
  };
}

function renderPanel(platform = "newapi") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <PlatformAlertsPanel platform={platform} />
    </QueryClientProvider>,
  );
}

describe("平台内告警", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("只按 source_metric_key 的平台前缀归属，不用标题或详情猜", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          response({
            items: [
              alert(),
              alert({
                id: "alert-sub2api-1",
                title: "Sub2API 余额不足",
                source_metric_key: "sub2api.channels.balance",
              }),
              alert({
                id: "alert-false-positive",
                title: "NewAPI 字样只是描述",
                detail: "NewAPI 也可能受影响",
                source_metric_key: "server.cpu.utilization",
              }),
              alert({
                id: "alert-global",
                title: "全局规则",
                source_metric_key: "",
              }),
            ],
          }),
        ),
      ),
    );

    renderPanel();

    expect(await screen.findByText("NewAPI 渠道健康数据延迟")).toBeTruthy();
    expect(screen.queryByText("Sub2API 余额不足")).toBeNull();
    expect(screen.queryByText("NewAPI 字样只是描述")).toBeNull();
    expect(screen.queryByText("全局规则")).toBeNull();
    expect(screen.getByText(/没有稳定来源指标的平台归属告警仍留在全局告警中心/)).toBeTruthy();
  });

  it("请求含已解决的最近 200 条，并把范围上限明确写在页面上", async () => {
    const fetchMock = vi.fn((_url: string) => Promise.resolve(response({ items: [alert()] })));
    vi.stubGlobal("fetch", fetchMock);

    renderPanel();

    await screen.findByText("NewAPI 渠道健康数据延迟");
    const calledUrl = String(fetchMock.mock.calls[0]?.[0]);
    expect(calledUrl).toContain("status=all");
    expect(calledUrl).toContain("limit=200");
    expect(screen.getByText(/最近最多 200 条/)).toBeTruthy();
  });

  it("筛选控件在短搜索之前，且平台页保持只读", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response({ items: [alert()] }))));

    renderPanel();

    await screen.findByText("NewAPI 渠道健康数据延迟");
    const severity = screen
      .getAllByLabelText("严重度")
      .find((element) => element.tagName === "SELECT") as HTMLSelectElement;
    const search = screen.getByRole("searchbox", { name: "搜索当前表格" });
    expect(severity.compareDocumentPosition(search) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(search.getAttribute("class")).toContain("w-40");
    expect(screen.queryByRole("button", { name: "确认" })).toBeNull();

    fireEvent.change(severity, { target: { value: "严重" } });
    expect(within(screen.getByRole("table")).queryByText("NewAPI 渠道健康数据延迟")).toBeNull();
    expect(screen.getByText("没有匹配的数据")).toBeTruthy();
  });

  it("当前平台没有可归属告警时说明边界，不把它说成全局零告警", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(response({ items: [alert({ source_metric_key: "sub2api.channels.balance" })] }))),
    );

    renderPanel();

    expect(await screen.findByText("没有可归属到 NewAPI 的告警")).toBeTruthy();
    expect(screen.getByText(/不代表全局没有告警/)).toBeTruthy();
  });
});
