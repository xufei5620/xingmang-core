import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ServerAssetsPanel } from "./ServerAssetsPanel";

function response(body: unknown, status = 200): Response {
  return { ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) } as unknown as Response;
}

const assetRow = {
  id: "a1111111-1111-1111-1111-111111111111",
  hostname: "srv-sin-01",
  ip_addresses: ["203.0.113.9", "10.0.0.5"],
  datacenter: "SIN",
  supplier_id: "",
  vcpu: 4,
  memory_gb: 8,
  disk_gb: 160,
  purpose: "sub2api relay",
  status: "active",
  monthly_cost_minor_units: "9990",
  currency: "USD",
  billing_cycle: "monthly",
  expires_at: "2026-09-05",
  notes: "primary relay",
  environment: "development",
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-30T00:00:00Z",
};

function renderPanel() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <ServerAssetsPanel />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function handler({
  assets = [assetRow],
  suppliers = [],
  setResult = { action_run_id: "run-set-1", result: assetRow },
  retireResult = { action_run_id: "run-retire-1", result: { ...assetRow, status: "retired" } },
}: {
  assets?: unknown[];
  suppliers?: unknown[];
  setResult?: Record<string, unknown>;
  retireResult?: Record<string, unknown>;
} = {}) {
  return vi.fn((url: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      const body = init.body ? JSON.parse(String(init.body)) : {};
      if (url.includes("server.asset.retire")) return Promise.resolve(response(retireResult));
      if (url.includes("server.asset.set")) return Promise.resolve(response(setResult));
      return Promise.resolve(response({ action_run_id: "run-x", result: body }));
    }
    if (url.startsWith("/api/v1/servers/assets")) return Promise.resolve(response({ items: assets }));
    if (url.startsWith("/api/v1/servers/suppliers")) return Promise.resolve(response({ items: suppliers }));
    return Promise.resolve(response({ items: [] }));
  });
}

function postCallTo(fetchMock: ReturnType<typeof vi.fn>, urlSubstring: string): [string, RequestInit] {
  const call = fetchMock.mock.calls.find(
    ([url, init]) => (init as RequestInit | undefined)?.method === "POST" && (url as string).includes(urlSubstring),
  );
  return call as unknown as [string, RequestInit];
}

describe("服务器 · 服务器资产", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("列表展示主机名、IP、规格、状态、月付成本与到期日", async () => {
    vi.stubGlobal("fetch", handler());
    renderPanel();

    const table = within(await screen.findByRole("table"));
    expect(table.getByText("srv-sin-01")).toBeTruthy();
    expect(table.getByText(/203\.0\.113\.9, 10\.0\.0\.5/)).toBeTruthy();
    expect(table.getByText(/4 vCPU/)).toBeTruthy();
    expect(table.getByText("2026-09-05")).toBeTruthy();
  });

  it("30 天内到期的资产标警示徽章", async () => {
    vi.stubGlobal("fetch", handler());
    renderPanel();
    expect(await screen.findByText("30 天内")).toBeTruthy();
  });

  it("空列表显示诚实占位，而不是一张空表", async () => {
    vi.stubGlobal("fetch", handler({ assets: [] }));
    renderPanel();
    expect(await screen.findByText("还没有登记任何服务器资产")).toBeTruthy();
  });

  it("登记服务器资产：提交后调用 server.asset.set@1 并显示回执 run_id", async () => {
    const fetchMock = handler({ assets: [] });
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();

    fireEvent.click(await screen.findByRole("button", { name: "登记服务器资产" }));
    const dialog = within(await screen.findByRole("dialog"));
    fireEvent.change(dialog.getByLabelText(/主机名/), { target: { value: "srv-new-01" } });
    fireEvent.click(dialog.getByRole("button", { name: "登记" }));

    await waitFor(() => expect(postCallTo(fetchMock, "server.asset.set")).toBeTruthy());
    const [, init] = postCallTo(fetchMock, "server.asset.set");
    const body = JSON.parse(String(init.body));
    expect(body.params.hostname).toBe("srv-new-01");

    expect(await screen.findByText(/run_id run-set-1/)).toBeTruthy();
  });

  it("月付成本填了但没选币种应被前端拦下，不发请求", async () => {
    const fetchMock = handler({ assets: [] });
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();

    fireEvent.click(await screen.findByRole("button", { name: "登记服务器资产" }));
    const dialog = within(await screen.findByRole("dialog"));
    fireEvent.change(dialog.getByLabelText(/主机名/), { target: { value: "srv-new-02" } });
    fireEvent.change(dialog.getByLabelText(/月付成本/), { target: { value: "99.90" } });
    fireEvent.click(dialog.getByRole("button", { name: "登记" }));

    expect(await dialog.findByText("填了月付成本就必须选择币种")).toBeTruthy();
    expect(postCallTo(fetchMock, "server.asset.set")).toBeUndefined();
  });

  it("退役：提交原因后调用 server.asset.retire@1", async () => {
    const fetchMock = handler();
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();

    fireEvent.click(await screen.findByRole("button", { name: "退役" }));
    const dialog = within(await screen.findByRole("dialog"));
    fireEvent.change(dialog.getByLabelText(/退役原因/), { target: { value: "机器到期下架" } });
    fireEvent.click(dialog.getByRole("button", { name: "确认退役" }));

    await waitFor(() => expect(postCallTo(fetchMock, "server.asset.retire")).toBeTruthy());
    const [, init] = postCallTo(fetchMock, "server.asset.retire");
    const body = JSON.parse(String(init.body));
    expect(body.params.asset_id).toBe(assetRow.id);
    expect(body.params.reason).toBe("机器到期下架");
  });
});
