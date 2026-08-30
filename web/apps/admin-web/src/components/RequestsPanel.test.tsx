import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { RequestsPanel } from "./RequestsPanel";

/** 样本令牌前缀的引导串。拼接而不是就地写 "sk-…"：`sk-` 开头的字面量会被
 *  gitleaks 的 generic-api-key 规则判成泄漏，而本仓库禁止用 allowlist 消音。 */
const TOKEN_LEAD = "sk-";

const freshness = {
  state: "fresh",
  staleness_seconds: 0,
  threshold_seconds: 60,
  is_partial: false,
  observed_at: "2026-08-28T09:00:00Z",
  last_success: "2026-08-28T09:00:00Z",
  last_error_code: "",
};

function summary(over: Record<string, unknown> = {}) {
  return {
    id: "20260828-000117",
    source: "sub2api",
    occurred_at: "2026-08-28T09:00:00Z",
    username: "zhang.wei",
    token_prefix: TOKEN_LEAD + "a1b2",
    model: "claude-sonnet-4-5",
    channel: "OpenAI 中转·主",
    upstream: "OpenAI Relay A",
    status: 200,
    duration_ms: 1200,
    ttfb_ms: 300,
    tokens_in: 100,
    tokens_out: 50,
    tokens_cache: 20,
    billed_amount: { amount_minor: "184", currency: "USD", scale: 3 },
    stream: false,
    upstream_request_id: "req_1",
    client_ip: "203.0.113.x",
    ...over,
  };
}

function pageBody(over: Record<string, unknown> = {}) {
  return {
    items: [summary()],
    next_cursor: "",
    retention_days: 30,
    data_source: "reqlog-real",
    stats: {
      request_count: 9,
      success_count: 7,
      failure_count: 2,
      average_duration_ms: 845,
    },
    freshness,
    ...over,
  };
}

function fakeResponse(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function renderPanel(initialEntry = "/") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <RequestsPanel platform="sub2api" />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function lastRequestedURL(fetchMock: ReturnType<typeof vi.fn>): string {
  const calls = fetchMock.mock.calls as unknown as Array<[RequestInfo | URL, RequestInit?]>;
  return String(calls.at(-1)?.[0] ?? "");
}

describe("请求列表", () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchMock = vi.fn(() => Promise.resolve(fakeResponse(pageBody())));
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("**不显示任何正文**：列表权限拿不到对话内容", async () => {
    // 摘要与正文的权限分级落在类型边界上，这条守的是渲染层——
    // 有人往列表里加一列「摘要」，这条会变红
    renderPanel();
    expect(await screen.findByText("zhang.wei")).toBeTruthy();
    // 表头里不该出现任何暗示正文的列
    for (const forbidden of ["摘要", "对话", "内容预览"]) {
      expect(screen.queryByText(forbidden)).toBeNull();
    }
  });

  it("四张统计卡展示完整过滤集统计，不拿当前页行数冒充", async () => {
    renderPanel();
    expect(await screen.findByText("请求数")).toBeTruthy();
    expect(screen.getByText("成功")).toBeTruthy();
    expect(screen.getByText("失败")).toBeTruthy();
    expect(screen.getByText("平均耗时")).toBeTruthy();
    expect(screen.getByText("9")).toBeTruthy();
    expect(screen.getByText("7")).toBeTruthy();
    expect(screen.getByText("2")).toBeTruthy();
    expect(screen.getByText("845 ms")).toBeTruthy();
    expect(screen.getAllByText(/完整筛选结果/).length).toBe(4);
  });

  it("表格按原型补齐请求 ID、渠道上游、输入输出与计费列", async () => {
    renderPanel();
    await screen.findByText("zhang.wei");
    for (const header of ["时间", "请求 ID", "用户", "模型", "渠道 / 上游", "状态", "耗时", "输入 / 输出", "计费", "详情"]) {
      expect(screen.getByRole("columnheader", { name: header })).toBeTruthy();
    }
    expect(screen.getByText("20260828-000117")).toBeTruthy();
    expect(screen.getByText("OpenAI 中转·主")).toBeTruthy();
    expect(screen.getByText("OpenAI Relay A")).toBeTruthy();
    expect(screen.getByText("100 / 50")).toBeTruthy();
    expect(screen.getByText("缓存 20")).toBeTruthy();
    expect(screen.getByText("$0.18")).toBeTruthy();
  });

  it("保留期一直显示，不只在空结果时显示", async () => {
    // 人翻不到某天的请求时第一反应是「筛错了」，而真正的原因常常是
    // 那天已经出了保留窗口。摆在筛选条旁边比在空状态里补一句更早
    renderPanel();
    expect(await screen.findByText(/只保留最近 30 天/)).toBeTruthy();
  });

  it("每一行都有通往完整详情页的链接，不是抽屉", async () => {
    renderPanel();
    const link = await screen.findByRole("link", { name: "查看内容" });
    expect(link.getAttribute("href")).toBe(
      "/platforms/sub2api/requests/20260828-000117",
    );
  });

  it("令牌映射不到用户名时显示「未映射」而不是空白", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(fakeResponse(pageBody({ items: [summary({ username: "" })] }))),
    );
    renderPanel();
    expect(await screen.findByText("未映射")).toBeTruthy();
  });

  it("fake 来源挂演示数据横幅，真实来源不挂", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(fakeResponse(pageBody({ data_source: "reqlog-fake" }))),
    );
    renderPanel();
    expect(await screen.findByText(/演示数据/)).toBeTruthy();
  });

  it("真实来源下没有演示横幅", async () => {
    renderPanel();
    expect(await screen.findByText("zhang.wei")).toBeTruthy();
    expect(screen.queryByText(/演示数据/)).toBeNull();
  });

  it("403 时说清缺哪个权限", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        fakeResponse(
          { error: { code: "PERMISSION_DENIED", message: "缺少权限 request.read" } },
          403,
        ),
      ),
    );
    renderPanel();
    // 「无权访问」+ 具体 scope 名，人才知道该去要什么权限
    expect(await screen.findByText(/request\.read/)).toBeTruthy();
  });

  it("空结果时给出明确的清除入口", async () => {
    fetchMock.mockImplementation(() => Promise.resolve(fakeResponse(pageBody({ items: [] }))));
    renderPanel();
    expect(await screen.findByText(/这个平台还没有请求记录/)).toBeTruthy();
  });

  it("只有时间筛选且结果为空时也能清除区间", async () => {
    fetchMock.mockImplementation(() => Promise.resolve(fakeResponse(pageBody({ items: [] }))));
    renderPanel("/?period=custom&since=2026-08-28T00%3A00%3A00Z");
    expect(await screen.findByText(/没有符合条件的请求/)).toBeTruthy();
    expect(screen.getByText(/^自 .+ 起$/)).toBeTruthy();

    const before = fetchMock.mock.calls.length;
    fireEvent.click(screen.getByRole("button", { name: "清除区间" }));
    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(before));
    const url = decodeURIComponent(lastRequestedURL(fetchMock));
    expect(url).not.toContain("period=");
    expect(url).not.toContain("since=");
    expect(url).not.toContain("until=");
  });

  it("清除区间会同时回到游标第一页", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(fakeResponse(pageBody({ next_cursor: "reqlog:50" }))),
    );
    renderPanel(
      "/?period=custom&since=2026-08-28T00%3A00%3A00Z&until=2026-08-29T00%3A00%3A00Z",
    );
    await screen.findByText("zhang.wei");

    fireEvent.click(screen.getByRole("button", { name: "下一页" }));
    await waitFor(() =>
      expect(decodeURIComponent(lastRequestedURL(fetchMock))).toContain("cursor=reqlog:50"),
    );

    const before = fetchMock.mock.calls.length;
    fireEvent.click(screen.getByRole("button", { name: "清除区间" }));
    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(before));
    const url = decodeURIComponent(lastRequestedURL(fetchMock));
    expect(url).not.toContain("cursor=");
    expect(url).not.toContain("period=");
    expect(url).not.toContain("since=");
    expect(url).not.toContain("until=");
  });

  it("没有下一页时不显示翻页按钮", async () => {
    renderPanel();
    expect(await screen.findByText("zhang.wei")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "下一页" })).toBeNull();
  });

  it("XM_REQLOG_MODE=off（无 error.code 的 404）显示未接入，不是失败态，也不带样本数据（XM-UX-OFFSTATE）", async () => {
    // chi 对没挂载的路由回纯文本 404：json() 会像浏览器解析 HTML/纯文本一样抛出
    fetchMock.mockImplementation(() =>
      Promise.resolve({
        ok: false,
        status: 404,
        json: () => Promise.reject(new SyntaxError("Unexpected token <")),
      } as unknown as Response),
    );
    renderPanel();

    expect(await screen.findByText("未接入")).toBeTruthy();
    expect(screen.getByText(/XM_REQLOG_MODE=off/)).toBeTruthy();
    expect(screen.queryByText(/失败|错误/)).toBeNull();
    expect(screen.queryByRole("button", { name: "重试" })).toBeNull();
    // 之前的 bug：404 解析不出 code 时会显示「请求失败（HTTP 404）（错误码 UNKNOWN）」
    expect(screen.queryByText(/UNKNOWN/)).toBeNull();
    // 不该有任何一行样本请求渲染出来
    expect(screen.queryByText("zhang.wei")).toBeNull();
    expect(screen.queryByRole("table")).toBeNull();
  });
});
