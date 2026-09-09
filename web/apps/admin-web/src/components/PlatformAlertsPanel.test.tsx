import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
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

function renderPanel(platform = "newapi", initialEntry = "/") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <PlatformAlertsPanel platform={platform} />
      </MemoryRouter>
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

describe("持续时长与时间范围（XM-ALERTS-TAB-DURATION）", () => {
  // 取数时刻固定：面板用 query.dataUpdatedAt 而不是 Date.now()，否则每次重绘
  // 都往前走一秒，排序会在人眼前跳动、测试变成碰运气。
  function withNow(iso: string, alerts: unknown[]) {
    vi.setSystemTime(new Date(iso));
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response({ items: alerts }))));
  }

  afterEach(() => vi.useRealTimers());

  it("未恢复的告警按「到此刻」显示持续时长", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    withNow("2026-08-29T09:07:00Z", [alert()]); // opened 07:00 -> 2 小时 7 分
    renderPanel();

    expect(await screen.findByText("2 小时 7 分")).toBeTruthy();
  });

  it("已确认的行多一行「确认 <时刻>」，与告警中心同一列同一写法", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    withNow("2026-08-29T09:07:00Z", [
      alert({
        id: "alert-acked",
        title: "已确认的那条",
        status: "ACKNOWLEDGED",
        acknowledged_at: "2026-08-29T07:30:00Z",
      }),
      alert(),
    ]);
    renderPanel();

    const acked = (await screen.findByText("已确认的那条")).closest("tr") as HTMLElement;
    const open = screen.getByText("NewAPI 渠道健康数据延迟").closest("tr") as HTMLElement;
    expect(within(acked).getByText("确认 2026-08-29 07:30:00 UTC")).toBeTruthy();
    expect(within(open).queryByText(/^确认 /)).toBeNull();
  });

  it("已恢复的告警算到恢复时刻，不再继续增长", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    withNow("2026-08-30T00:00:00Z", [
      alert({ id: "alert-resolved", status: "RESOLVED", resolved_at: "2026-08-29T07:30:00Z" }),
    ]);
    renderPanel();

    expect(await screen.findByText("30 分")).toBeTruthy();
  });

  it("时间范围按最近一次发现收窄，长期未恢复的告警不会被藏起来", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    withNow("2026-08-29T09:00:00Z", [
      // 两个月前开的，但五分钟前还在响：它正是此刻要处理的那条，必须留下。
      alert({ id: "alert-long", opened_at: "2026-06-29T07:00:00Z", last_seen_at: "2026-08-29T08:55:00Z" }),
      // 十天前最后一次出现：24 小时窗口里应当被收走。
      alert({ id: "alert-old", title: "十天前的告警", opened_at: "2026-08-19T07:00:00Z", last_seen_at: "2026-08-19T08:00:00Z" }),
    ]);
    renderPanel("newapi", "/?alert_window=24h");

    expect(await screen.findByText("NewAPI 渠道健康数据延迟")).toBeTruthy();
    expect(screen.queryByText("十天前的告警")).toBeNull();
    expect(screen.getByText(/当前隐藏 1 条/)).toBeTruthy();
  });

  it("改时间范围写进 URL，可分享可恢复", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    withNow("2026-08-29T09:00:00Z", [alert()]);
    renderPanel();

    const select = await screen.findByLabelText("按时间范围收窄");
    expect((select as HTMLSelectElement).value).toBe("all");
    fireEvent.change(select, { target: { value: "7d" } });

    await waitFor(() => {
      expect((screen.getByLabelText("按时间范围收窄") as HTMLSelectElement).value).toBe("7d");
    });
  });

  it("编号显示告警 ID 前八位，完整 ID 在提示里；不编造带序号的告警号", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    withNow("2026-08-29T09:00:00Z", [
      alert({ id: "0119ab26-0000-4000-8000-000000000001" }),
    ]);
    renderPanel();

    const code = await screen.findByText("0119AB26");
    expect(code.getAttribute("title")).toBe("0119ab26-0000-4000-8000-000000000001");
  });

  it("提供到全局告警中心的链接，处置仍不在平台页", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    withNow("2026-08-29T09:00:00Z", [alert()]);
    renderPanel();

    const link = await screen.findByRole("link", { name: "全局告警中心" });
    expect(link.getAttribute("href")).toBe("/alerts");
  });

  // XM-WORKBENCH-TRUTH 评审回合三：与告警中心那一列同一写法——数字列里只放数字，
  // 整句留给悬停与表格说明。
  it("「评估轮次」列的格子是纯数字，整句退到悬停里", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    withNow("2026-08-29T09:00:00Z", [alert({ fire_count: 3 })]);
    renderPanel();
    await screen.findByText("NewAPI 渠道健康数据延迟");

    const table = screen.getByRole("table");
    const headers = within(table).getAllByRole("columnheader");
    const index = headers.findIndex((h) => (h.textContent ?? "").includes("评估轮次"));
    expect(index).toBeGreaterThanOrEqual(0);
    const row = within(table)
      .getAllByRole("row")
      .find((r) => within(r).queryAllByRole("cell").length > 0)!;
    const cell = within(row).getAllByRole("cell")[index]!;
    expect(cell.textContent).toBe("3");
    expect(cell.querySelector("[title]")?.getAttribute("title")).toContain("评估 3 轮");
  });
});
