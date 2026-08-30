import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ServerServiceNotesPanel } from "./ServerServiceNotesPanel";

function response(body: unknown, status = 200): Response {
  return { ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) } as unknown as Response;
}

const assetRow = {
  id: "a1111111-1111-1111-1111-111111111111",
  hostname: "srv-sin-01",
  ip_addresses: [],
  datacenter: "",
  supplier_id: "",
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

const noteRow = {
  id: "n1111111-1111-1111-1111-111111111111",
  server_id: assetRow.id,
  service_name: "api",
  service_kind: "container",
  port: 8080,
  notes: "",
  created_at: "2026-08-01T00:00:00Z",
  updated_at: "2026-08-30T00:00:00Z",
};

function renderPanel() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <ServerServiceNotesPanel />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function handler({
  notes = [noteRow],
  assets = [assetRow],
  setResult = { action_run_id: "run-note-1", result: noteRow },
  removeResult = { action_run_id: "run-remove-1", result: { deleted: true } },
}: {
  notes?: unknown[];
  assets?: unknown[];
  setResult?: Record<string, unknown>;
  removeResult?: Record<string, unknown>;
} = {}) {
  return vi.fn((url: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      if (url.includes("server.service_note.remove")) return Promise.resolve(response(removeResult));
      if (url.includes("server.service_note.set")) return Promise.resolve(response(setResult));
      return Promise.resolve(response({ action_run_id: "run-x", result: {} }));
    }
    if (url.startsWith("/api/v1/servers/service-notes")) return Promise.resolve(response({ items: notes }));
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

/** 「登记服务」按钮在资产列表读取完成前会先渲染成禁用态（同一个可访问名），
 *  直接点击可能撞上那一帧。等到它可用了再点，避免这个时序竞态。 */
async function openCreateDialog() {
  await waitFor(() => {
    const button = screen.getByRole("button", { name: "登记服务" }) as HTMLButtonElement;
    expect(button.disabled).toBe(false);
  });
  fireEvent.click(screen.getByRole("button", { name: "登记服务" }));
  return within(await screen.findByRole("dialog"));
}

describe("服务器 · 服务与容器", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("列表展示所在服务器、服务名、类型与端口", async () => {
    vi.stubGlobal("fetch", handler());
    renderPanel();
    const table = within(await screen.findByRole("table"));
    expect(table.getByText("srv-sin-01")).toBeTruthy();
    expect(table.getByText("api")).toBeTruthy();
    // 用完整徽章文案而不是裸「容器」：表格 caption 里也含「服务与容器」这个
    // 子串，模糊匹配会撞上两处
    expect(table.getByText("容器（container）")).toBeTruthy();
    expect(table.getByText("8080")).toBeTruthy();
  });

  it("空列表显示诚实占位", async () => {
    vi.stubGlobal("fetch", handler({ notes: [] }));
    renderPanel();
    expect(await screen.findByText("还没有登记任何服务")).toBeTruthy();
  });

  it("没有任何服务器资产时，登记服务按钮禁用", async () => {
    vi.stubGlobal("fetch", handler({ notes: [], assets: [] }));
    renderPanel();
    const button = (await screen.findByRole("button", { name: "登记服务" })) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
  });

  it("登记服务：提交后调用 server.service_note.set@1，带 server_id", async () => {
    const fetchMock = handler({ notes: [] });
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();

    const dialog = await openCreateDialog();
    fireEvent.change(dialog.getByLabelText(/服务名/), { target: { value: "worker" } });
    fireEvent.click(dialog.getByRole("button", { name: "登记" }));

    await waitFor(() => expect(postCallTo(fetchMock, "server.service_note.set")).toBeTruthy());
    const call = postCallTo(fetchMock, "server.service_note.set");
    const body = JSON.parse(String(call?.[1].body));
    expect(body.params.service_name).toBe("worker");
    expect(body.params.server_id).toBe(assetRow.id);
    expect(await screen.findByText(/run_id run-note-1/)).toBeTruthy();
  });

  it("端口超出范围应被前端拦下", async () => {
    const fetchMock = handler({ notes: [] });
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();

    const dialog = await openCreateDialog();
    fireEvent.change(dialog.getByLabelText(/服务名/), { target: { value: "bad-port" } });
    fireEvent.change(dialog.getByLabelText("端口"), { target: { value: "70000" } });
    fireEvent.click(dialog.getByRole("button", { name: "登记" }));

    expect(await dialog.findByText("端口必须在 1~65535 之间")).toBeTruthy();
    expect(postCallTo(fetchMock, "server.service_note.set")).toBeUndefined();
  });

  it("删除：提交原因后调用 server.service_note.remove@1", async () => {
    const fetchMock = handler();
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();

    fireEvent.click(await screen.findByRole("button", { name: "删除" }));
    const dialog = within(await screen.findByRole("dialog"));
    fireEvent.change(dialog.getByLabelText(/删除原因/), { target: { value: "服务已下线" } });
    fireEvent.click(dialog.getByRole("button", { name: "确认删除" }));

    await waitFor(() => expect(postCallTo(fetchMock, "server.service_note.remove")).toBeTruthy());
    const call = postCallTo(fetchMock, "server.service_note.remove");
    const body = JSON.parse(String(call?.[1].body));
    expect(body.params.service_note_id).toBe(noteRow.id);
    expect(body.params.reason).toBe("服务已下线");
  });
});
