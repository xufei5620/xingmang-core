import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ServerSuppliersPanel } from "./ServerSuppliersPanel";

function response(body: unknown, status = 200): Response {
  return { ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) } as unknown as Response;
}

const supplierRow = {
  id: "s1111111-1111-1111-1111-111111111111",
  name: "Vultr",
  website: "https://vultr.com",
  console_url: "https://my.vultr.com",
  contact_name: "运营群",
  contact_info: "@vultr-ops",
  notes: "",
  environment: "development",
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-30T00:00:00Z",
};

const assetRow = {
  id: "a1111111-1111-1111-1111-111111111111",
  hostname: "srv-sin-01",
  ip_addresses: [],
  datacenter: "",
  supplier_id: supplierRow.id,
  vcpu: null,
  memory_gb: null,
  disk_gb: null,
  purpose: "",
  status: "active",
  monthly_cost_minor_units: null,
  currency: "",
  billing_cycle: "",
  expires_at: "",
  notes: "",
  environment: "development",
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-30T00:00:00Z",
};

function renderPanel() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <ServerSuppliersPanel />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function handler({
  suppliers = [supplierRow],
  assets = [assetRow],
  setResult = { action_run_id: "run-supplier-1", result: supplierRow },
}: {
  suppliers?: unknown[];
  assets?: unknown[];
  setResult?: Record<string, unknown>;
} = {}) {
  return vi.fn((url: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      if (url.includes("server.supplier.set")) return Promise.resolve(response(setResult));
      return Promise.resolve(response({ action_run_id: "run-x", result: {} }));
    }
    if (url.startsWith("/api/v1/servers/suppliers")) return Promise.resolve(response({ items: suppliers }));
    if (url.startsWith("/api/v1/servers/assets")) return Promise.resolve(response({ items: assets }));
    return Promise.resolve(response({ items: [] }));
  });
}

function postCallTo(fetchMock: ReturnType<typeof vi.fn>, urlSubstring: string): [string, RequestInit] | undefined {
  const call = fetchMock.mock.calls.find(
    ([url, init]) => (init as RequestInit | undefined)?.method === "POST" && (url as string).includes(urlSubstring),
  );
  return call as unknown as [string, RequestInit] | undefined;
}

describe("服务器 · 供应商与采购", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("列表展示名称、官网、联系方式与关联服务器台数", async () => {
    vi.stubGlobal("fetch", handler());
    renderPanel();
    const table = within(await screen.findByRole("table"));
    expect(table.getByText("Vultr")).toBeTruthy();
    expect(table.getByText("https://vultr.com")).toBeTruthy();
    expect(table.getByText("运营群")).toBeTruthy();
    expect(table.getByText("1 台")).toBeTruthy();
  });

  it("空列表显示诚实占位", async () => {
    vi.stubGlobal("fetch", handler({ suppliers: [] }));
    renderPanel();
    expect(await screen.findByText("还没有登记任何供应商")).toBeTruthy();
  });

  it("登记供应商：非 https 的官网应被前端拦下", async () => {
    const fetchMock = handler({ suppliers: [] });
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();

    fireEvent.click(await screen.findByRole("button", { name: "登记供应商" }));
    const dialog = within(await screen.findByRole("dialog"));
    fireEvent.change(dialog.getByLabelText(/供应商名称/), { target: { value: "Vultr" } });
    fireEvent.change(dialog.getByLabelText(/官网/), { target: { value: "http://vultr.com" } });
    fireEvent.click(dialog.getByRole("button", { name: "登记" }));

    expect(await dialog.findByText("必须是 https 且不含 user:pass@ 段")).toBeTruthy();
    expect(postCallTo(fetchMock, "server.supplier.set")).toBeUndefined();
  });

  it("登记供应商：合法输入提交后调用 server.supplier.set@1", async () => {
    const fetchMock = handler({ suppliers: [] });
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();

    fireEvent.click(await screen.findByRole("button", { name: "登记供应商" }));
    const dialog = within(await screen.findByRole("dialog"));
    fireEvent.change(dialog.getByLabelText(/供应商名称/), { target: { value: "Hetzner" } });
    fireEvent.click(dialog.getByRole("button", { name: "登记" }));

    await waitFor(() => expect(postCallTo(fetchMock, "server.supplier.set")).toBeTruthy());
    const call = postCallTo(fetchMock, "server.supplier.set");
    const body = JSON.parse(String(call?.[1].body));
    expect(body.params.name).toBe("Hetzner");
    expect(await screen.findByText(/run_id run-supplier-1/)).toBeTruthy();
  });
});
