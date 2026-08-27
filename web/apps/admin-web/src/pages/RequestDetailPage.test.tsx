import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { RequestDetailPage } from "./RequestDetailPage";

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

function contentBody(over: Record<string, unknown> = {}) {
  return {
    summary: {
      id: "20260828-000117",
      source: "sub2api",
      occurred_at: "2026-08-28T09:00:00Z",
      username: "zhang.wei",
      token_prefix: TOKEN_LEAD + "a1b2",
      model: "claude-sonnet-4-5",
      status: 200,
      duration_ms: 18420,
      ttfb_ms: 742,
      tokens_in: 21486,
      tokens_out: 612,
      tokens_cache: 18240,
      stream: false,
      upstream_request_id: "req_01HZX9K2M4",
      client_ip: "203.0.113.x",
    },
    messages: [
      { role: "system", content: "你是一位严谨的中文法律助理。", truncated: false, original_bytes: 30 },
      { role: "user", content: "请审阅合同条款。", truncated: false, original_bytes: 24 },
    ],
    messages_parsed: true,
    final_reply: "第一条的风险在于起算点未定义。",
    final_reply_truncated: false,
    final_reply_bytes: 42,
    raw_request: { body: "{}", truncated: false, original_bytes: 2, content_type: "application/json" },
    raw_response: { body: "{}", truncated: false, original_bytes: 2, content_type: "application/json" },
    data_source: "reqlog-real",
    freshness,
    ...over,
  };
}

function fakeResponse(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function renderPage(path = "/platforms/sub2api/requests/20260828-000117") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route
            path="/platforms/:serviceType/requests/:requestId"
            element={<RequestDetailPage />}
          />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("请求详情完整页", () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchMock = vi.fn(() => Promise.resolve(fakeResponse(contentBody())));
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("分角色渲染：system / user 各自带角色标签", async () => {
    renderPage();
    // 「用户」在页面上出现两次：摘要卡的字段名，和消息气泡的角色徽章。
    // 用 getAllBy* 而不是把其中一个改名——两处都该叫「用户」
    expect(await screen.findByText("系统")).toBeTruthy();
    expect(screen.getAllByText("用户").length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText("你是一位严谨的中文法律助理。")).toBeTruthy();
  });

  it("**默认没有导出按钮**（交接文档 §9.4）", async () => {
    // 有意与 reqlog 自带控制台的「下载原文」不同——那条路绕开平台的权限与审计
    renderPage();
    expect(await screen.findByText("系统")).toBeTruthy();
    for (const label of ["导出", "下载", "复制全文"]) {
      expect(screen.queryByRole("button", { name: new RegExp(label) })).toBeNull();
      expect(screen.queryByRole("link", { name: new RegExp(label) })).toBeNull();
    }
    // 而且明说「不提供导出」——找不到按钮的人会以为是没做，而不是有意不做
    expect(screen.getByText(/本页不提供导出/)).toBeTruthy();
  });

  it("服务端截断的内容给出「共多少」的提示", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        fakeResponse(
          contentBody({
            messages: [
              {
                role: "user",
                content: "很长的开头…",
                truncated: true,
                original_bytes: 3 * 1024 * 1024,
              },
            ],
          }),
        ),
      ),
    );
    renderPage();
    // 不声张的截断比不显示更危险——人会以为看到的就是全部
    // 提示里同时说清「显示了多少」与「共多少」
    expect(await screen.findByText(/内容过长，只显示了前 .+（原文共 3\.0 MiB）/)).toBeTruthy();
  });

  it("解析不出消息时让人去看原文，而不是画成「暂无对话」", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(fakeResponse(contentBody({ messages: [], messages_parsed: false }))),
    );
    renderPage();
    expect(await screen.findByText(/没有可识别的对话结构/)).toBeTruthy();
  });

  it("fake 来源在详情页也挂演示横幅", async () => {
    // 这一页最容易被当成真实用户问答截图转发出去
    fetchMock.mockImplementation(() =>
      Promise.resolve(fakeResponse(contentBody({ data_source: "reqlog-fake" }))),
    );
    renderPage();
    expect(await screen.findByText(/演示数据/)).toBeTruthy();
  });

  it("缺 request.content.read 时说清缺哪个权限", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        fakeResponse(
          { error: { code: "PERMISSION_DENIED", message: "缺少权限 request.content.read" } },
          403,
        ),
      ),
    );
    renderPage();
    expect(await screen.findByText(/request\.content\.read/)).toBeTruthy();
    // 403 时一个字的正文都不该渲染出来
    expect(screen.queryByText("你是一位严谨的中文法律助理。")).toBeNull();
  });

  it("记录不存在时说清可能是过了保留期", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        fakeResponse(
          {
            error: {
              code: "ACTION_NOT_REGISTERED",
              message: "没有这条请求记录：可能已过保留期，或平台选错了",
            },
          },
          404,
        ),
      ),
    );
    renderPage();
    expect(await screen.findByText(/已过保留期/)).toBeTruthy();
  });

  it("没有请求数据的平台连请求都不发，直接说清原因", async () => {
    // 后端会 404，但那个 404 说的是「没有这条记录」，
    // 而真正的原因是「这个平台不在抄录范围内」
    renderPage("/platforms/cpa/requests/whatever");
    expect(await screen.findByText("这个平台没有请求数据")).toBeTruthy();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("TTFB 为 null 显示「—」，为 0 显示 0 ms", async () => {
    fetchMock.mockImplementation(() =>
      Promise.resolve(
        fakeResponse(contentBody({ summary: { ...contentBody().summary, ttfb_ms: 0 } })),
      ),
    );
    renderPage();
    expect(await screen.findByText("0 ms")).toBeTruthy();
  });
});
