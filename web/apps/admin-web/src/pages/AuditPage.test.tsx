import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AuditPage } from "./AuditPage";

function jsonResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

function renderAudit(initialEntry = "/audit") {
  const fetchImpl = vi.fn(async () => jsonResponse({ items: [], next_before: 0 }));
  vi.stubGlobal("fetch", fetchImpl);
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <AuditPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return fetchImpl;
}

afterEach(() => vi.unstubAllGlobals());

describe("AuditPage 子页", () => {
  it("默认进入审计记录并保留原有事件查询", async () => {
    const fetchImpl = renderAudit();

    expect(screen.getByRole("tab", { name: "审计记录" })).toBeTruthy();
    expect(screen.getByRole("tab", { name: "操作证据" })).toBeTruthy();
    expect(screen.getByRole("tab", { name: "审计链验证" })).toBeTruthy();
    expect(await screen.findByText("还没有审计事件")).toBeTruthy();
    await waitFor(() => expect(fetchImpl).toHaveBeenCalled());
    expect(fetchImpl.mock.calls.some(([input]) => String(input).includes("/api/v1/audit/events"))).toBe(true);
  });

  it("操作证据子页诚实显示未接入，且不读取事件或对象存储", async () => {
    const fetchImpl = renderAudit("/audit?sub=evidence");

    expect(await screen.findByText("操作证据尚未接入")).toBeTruthy();
    expect(screen.getByText(/不连接对象存储/)).toBeTruthy();
    expect(screen.getByText(/对象、manifest 与冷读接口尚未建立/)).toBeTruthy();
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("审计链验证子页明确为 CLI-only，且不触发事件查询", async () => {
    const fetchImpl = renderAudit("/audit?sub=chain");

    expect(await screen.findByText("审计链验证仅支持 CLI")).toBeTruthy();
    expect(screen.getAllByText(/audit-verify/).length).toBeGreaterThan(0);
    expect(screen.getByText(/CLI-only/)).toBeTruthy();
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("未知子页不静默回落到事件列表", async () => {
    const fetchImpl = renderAudit("/audit?sub=not-a-real-tab");

    expect(await screen.findByText("「not-a-real-tab」子页尚未接入")).toBeTruthy();
    expect(screen.getByRole("link", { name: "返回审计记录" }).getAttribute("href")).toBe(
      "/audit?sub=events",
    );
    expect(fetchImpl).not.toHaveBeenCalled();
  });
});
