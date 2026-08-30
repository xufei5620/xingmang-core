import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ConnectorConfigPanel } from "./ConnectorConfigPanel";

function response(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

const sub2apiConfig = {
  platform: "sub2api",
  mode: "fake",
  endpoint: "",
  target_allowlist: [],
  credential_ref: "",
  version: 1,
  updated_at: "2026-08-30T08:00:00Z",
  updated_by: "bootstrap",
};

const expectedItems = [
  { credential_ref: "secret://sub2api-prod/read-token", platform: "sub2api", purpose: "Sub2API 只读令牌", configured: false },
  { credential_ref: "secret://newapi/readonly-token", platform: "newapi", purpose: "NewAPI 只读令牌", configured: false },
  { credential_ref: "secret://newapi/revenue-db", platform: "newapi", purpose: "NewAPI 收入库", configured: false },
];

function renderPanel() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <ConnectorConfigPanel />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function handler(
  actionResponse: unknown = { action_run_id: "run-conn-1" },
  configs: unknown[] = [sub2apiConfig],
) {
  return vi.fn((url: string, init?: RequestInit) => {
    if (init?.method === "POST") return Promise.resolve(response(actionResponse));
    if (url.startsWith("/api/v1/connectors/config")) return Promise.resolve(response({ items: configs }));
    if (url.startsWith("/api/v1/credentials/expected")) {
      return Promise.resolve(response({ items: expectedItems }));
    }
    return Promise.resolve(response({ items: [] }));
  });
}

function postCalls(fetchMock: ReturnType<typeof vi.fn>): Array<[string, RequestInit]> {
  return fetchMock.mock.calls.filter(
    (call) => (call[1] as RequestInit | undefined)?.method === "POST",
  ) as unknown as Array<[string, RequestInit]>;
}

function firstPost(fetchMock: ReturnType<typeof vi.fn>): [string, RequestInit] {
  const post = postCalls(fetchMock)[0];
  if (!post) throw new Error("没有发出 POST 请求");
  return post;
}

async function cardOf(label: string): Promise<HTMLElement> {
  const heading = await screen.findByRole("heading", { name: label, level: 3 });
  return heading.closest("section") as HTMLElement;
}

describe("设置 · 接入模式", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("显示数据库里当前生效的值；没有记录的平台明说没有，不拿表单初值冒充", async () => {
    vi.stubGlobal("fetch", handler());
    renderPanel();

    const sub2api = within(await cardOf("Sub2API"));
    expect(sub2api.getByText("当前 fake")).toBeTruthy();
    expect(sub2api.getByText("v1")).toBeTruthy();
    expect(sub2api.getByText(/2026-08-30 08:00:00 UTC/)).toBeTruthy();
    expect(sub2api.getByText(/bootstrap/)).toBeTruthy();

    const newapi = within(await cardOf("NewAPI"));
    expect(newapi.getByText("未配置")).toBeTruthy();
    expect(newapi.getByText("数据库里尚无这条接入配置")).toBeTruthy();

    expect(screen.getByText(/下一轮同步周期（≤5 分钟）内自动切换，无需重启/)).toBeTruthy();
  });

  it("CredentialRef 默认取该平台的预期引用，切到 real 后保存走 connector.config.set@1", async () => {
    const fetchMock = handler();
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();

    const card = await cardOf("NewAPI");
    const refInput = within(card).getByRole("textbox", { name: /CredentialRef/ }) as HTMLInputElement;
    expect(refInput.value).toBe("secret://newapi/readonly-token");

    const modeTrigger = within(card).getByRole("combobox", { name: "NewAPI 接入模式" });
    expect(modeTrigger.textContent).toContain("fake");
    fireEvent.keyDown(modeTrigger, { key: "ArrowDown" });
    fireEvent.click(await screen.findByRole("option", { name: /^real/ }));
    await waitFor(() => expect(modeTrigger.textContent).toContain("real"));

    fireEvent.change(within(card).getByRole("textbox", { name: /上游地址/ }), {
      target: { value: "https://newapi.example.com/api" },
    });
    fireEvent.change(within(card).getByRole("textbox", { name: /允许主机/ }), {
      target: { value: "newapi.example.com, db.example.com" },
    });
    fireEvent.click(within(card).getByRole("button", { name: "保存 NewAPI 接入配置" }));

    await waitFor(() => {
      expect(screen.getByText("已保存 NewAPI 接入配置（real）")).toBeTruthy();
      expect(screen.getByText("run_id run-conn-1")).toBeTruthy();
    });
    const post = firstPost(fetchMock);
    expect(post[0]).toBe("/api/v1/actions/connector.config.set/versions/1/execute");
    expect(JSON.parse(String(post[1].body))).toEqual({
      params: {
        platform: "newapi",
        mode: "real",
        endpoint: "https://newapi.example.com/api",
        target_allowlist: "newapi.example.com,db.example.com",
        credential_ref: "secret://newapi/readonly-token",
      },
    });
  });

  it("real 模式下 http 地址或不含上游主机的允许清单在提交前就被拦下，不发请求", async () => {
    const fetchMock = handler(
      { action_run_id: "run-conn-2" },
      [
        {
          ...sub2apiConfig,
          mode: "real",
          endpoint: "https://sub2api.example.com",
          target_allowlist: ["sub2api.example.com"],
          credential_ref: "secret://sub2api-prod/read-token",
          version: 2,
        },
      ],
    );
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();

    const card = await cardOf("Sub2API");
    expect(within(card).getByText("当前 real")).toBeTruthy();
    fireEvent.change(within(card).getByRole("textbox", { name: /上游地址/ }), {
      target: { value: "http://sub2api.example.com" },
    });
    fireEvent.click(within(card).getByRole("button", { name: "保存 Sub2API 接入配置" }));

    const summary = await screen.findByRole("alert", { name: /请检查 Sub2API 接入配置/ });
    expect(summary.textContent).toContain("上游地址");
    expect(within(card).getByText(/必须是 https/)).toBeTruthy();
    expect(document.activeElement).toBe(summary);

    fireEvent.change(within(card).getByRole("textbox", { name: /上游地址/ }), {
      target: { value: "https://other.example.com" },
    });
    fireEvent.click(within(card).getByRole("button", { name: "保存 Sub2API 接入配置" }));
    expect(await within(card).findByText(/允许主机里没有上游地址的主机 other\.example\.com/)).toBeTruthy();
    expect(postCalls(fetchMock)).toHaveLength(0);
  });

  it("保存失败时把错误码与 request_id 留在表单里，不丢掉已填内容", async () => {
    const fetchMock = handler({
      error: { code: "PERMISSION_DENIED", message: "缺少权限 connector.manage", request_id: "req-conn-403" },
    });
    fetchMock.mockImplementation((url: string, init?: RequestInit) => {
      if (init?.method === "POST") {
        return Promise.resolve(
          response(
            { error: { code: "PERMISSION_DENIED", message: "缺少权限 connector.manage", request_id: "req-conn-403" } },
            403,
          ),
        );
      }
      if (url.startsWith("/api/v1/connectors/config")) return Promise.resolve(response({ items: [sub2apiConfig] }));
      if (url.startsWith("/api/v1/credentials/expected")) return Promise.resolve(response({ items: expectedItems }));
      return Promise.resolve(response({ items: [] }));
    });
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();

    const card = await cardOf("Sub2API");
    const endpoint = within(card).getByRole("textbox", { name: /上游地址/ }) as HTMLInputElement;
    fireEvent.change(endpoint, { target: { value: "https://sub2api.example.com" } });
    fireEvent.click(within(card).getByRole("button", { name: "保存 Sub2API 接入配置" }));

    await waitFor(() => expect(within(card).getByText(/缺少权限 connector\.manage/)).toBeTruthy());
    expect(within(card).getByText(/request_id: req-conn-403/)).toBeTruthy();
    expect(endpoint.value).toBe("https://sub2api.example.com");
  });

  it("读取接入配置失败时显示错误态，而不是两张空表单", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) =>
        Promise.resolve(
          url.startsWith("/api/v1/connectors/config")
            ? response({ error: { code: "INTERNAL", message: "数据库不可用" } }, 500)
            : response({ items: [] }),
        ),
      ),
    );
    renderPanel();

    expect(await screen.findByText("加载失败")).toBeTruthy();
    expect(screen.getByText(/数据库不可用（错误码 INTERNAL）/)).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Sub2API", level: 3 })).toBeNull();
  });
});
