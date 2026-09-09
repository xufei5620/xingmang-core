import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ServerDomainsPanel } from "./ServerDomainsPanel";

function response(body: unknown, status = 200): Response {
  return { ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) } as unknown as Response;
}

const domainRow = {
  id: "d1111111-1111-1111-1111-111111111111",
  domain_name: "console.example.com",
  registrar: "GoDaddy",
  dns_provider: "Cloudflare",
  expires_at: "2027-01-15",
  cert_source: "acme",
  cert_expires_at: "2026-09-10",
  bound_service_note: "control-plane nginx",
  environment: "development",
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-30T00:00:00Z",
};

function renderPanel() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <ServerDomainsPanel />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function handler({
  domains = [domainRow],
  setResult = { action_run_id: "run-domain-1", result: domainRow },
}: {
  domains?: unknown[];
  setResult?: Record<string, unknown>;
} = {}) {
  return vi.fn((url: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      if (url.includes("server.domain.set")) return Promise.resolve(response(setResult));
      return Promise.resolve(response({ action_run_id: "run-x", result: {} }));
    }
    if (url.startsWith("/api/v1/servers/domains")) return Promise.resolve(response({ items: domains }));
    return Promise.resolve(response({ items: [] }));
  });
}

function postCallTo(fetchMock: ReturnType<typeof vi.fn>, urlSubstring: string): [string, RequestInit] | undefined {
  const call = fetchMock.mock.calls.find(
    ([url, init]) => (init as RequestInit | undefined)?.method === "POST" && (url as string).includes(urlSubstring),
  );
  return call as unknown as [string, RequestInit] | undefined;
}

describe("服务器 · 域名与证书", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("列表展示域名、注册商、到期日与证书来源", async () => {
    vi.stubGlobal("fetch", handler());
    renderPanel();
    const table = within(await screen.findByRole("table"));
    expect(table.getByText("console.example.com")).toBeTruthy();
    expect(table.getByText("GoDaddy")).toBeTruthy();
    expect(table.getByText("2027-01-15")).toBeTruthy();
    expect(table.getByText(/ACME 自动续期/)).toBeTruthy();
  });

  it("证书 30 天内到期的行标警示徽章", async () => {
    vi.stubGlobal("fetch", handler());
    renderPanel();
    expect(await screen.findByText("30 天内")).toBeTruthy();
  });

  it("空列表显示诚实占位", async () => {
    vi.stubGlobal("fetch", handler({ domains: [] }));
    renderPanel();
    expect(await screen.findByText("还没有登记任何域名")).toBeTruthy();
  });

  it("登记域名：提交后调用 server.domain.set@1", async () => {
    const fetchMock = handler({ domains: [] });
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();

    fireEvent.click(await screen.findByRole("button", { name: "登记域名" }));
    const dialog = within(await screen.findByRole("dialog"));
    fireEvent.change(dialog.getByLabelText(/域名/), { target: { value: "new.example.com" } });
    fireEvent.click(dialog.getByRole("button", { name: "登记" }));

    await waitFor(() => expect(postCallTo(fetchMock, "server.domain.set")).toBeTruthy());
    const call = postCallTo(fetchMock, "server.domain.set");
    const body = JSON.parse(String(call?.[1].body));
    expect(body.params.domain_name).toBe("new.example.com");
    expect(await screen.findByText(/run_id run-domain-1/)).toBeTruthy();
  });

  it("到期日格式非法应被前端拦下", async () => {
    const fetchMock = handler({ domains: [] });
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();

    fireEvent.click(await screen.findByRole("button", { name: "登记域名" }));
    const dialog = within(await screen.findByRole("dialog"));
    fireEvent.change(dialog.getByLabelText(/域名/), { target: { value: "bad-date.example.com" } });
    fireEvent.change(dialog.getByLabelText("到期日"), { target: { value: "2026/09/30" } });
    fireEvent.click(dialog.getByRole("button", { name: "登记" }));

    expect(await dialog.findByText("必须形如 2026-09-30")).toBeTruthy();
    expect(postCallTo(fetchMock, "server.domain.set")).toBeUndefined();
  });
});
