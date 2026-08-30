import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ExpectedCredentialsPanel } from "./ExpectedCredentialsPanel";

function response(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

const expectedItems = [
  { credential_ref: "secret://sub2api-prod/read-token", platform: "sub2api", purpose: "Sub2API 只读 API 令牌", configured: true },
  { credential_ref: "secret://newapi/readonly-token", platform: "newapi", purpose: "NewAPI 只读令牌", configured: false },
  { credential_ref: "secret://newapi/revenue-db", platform: "newapi", purpose: "NewAPI 收入库只读 DSN", configured: false },
  { credential_ref: "secret://archive/minio-runtime", platform: "archive", purpose: "归档 MinIO 运行时凭据", configured: false },
  { credential_ref: "secret://archive/minio-kms", platform: "archive", purpose: "归档 MinIO KMS 密钥", configured: false },
];

function renderPanel() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <ExpectedCredentialsPanel />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function handler(actionResponse: unknown = { action_run_id: "run-exp-1" }, actionStatus = 200) {
  return vi.fn((url: string, init?: RequestInit) => {
    if (init?.method === "POST") return Promise.resolve(response(actionResponse, actionStatus));
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

function rowOf(credentialRef: string): HTMLElement {
  return screen.getByText(credentialRef).closest("tr") as HTMLElement;
}

describe("设置 · 平台需要的凭据", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("列出后端声明的引用，并按 configured 显示已配置/缺失", async () => {
    vi.stubGlobal("fetch", handler());
    renderPanel();

    const table = within(await screen.findByRole("table"));
    for (const item of expectedItems) {
      expect(table.getByText(item.credential_ref)).toBeTruthy();
    }
    expect(table.getAllByText("已配置")).toHaveLength(1);
    expect(table.getAllByText("缺失")).toHaveLength(4);
    const revenue = within(rowOf("secret://newapi/revenue-db"));
    expect(revenue.getByText("newapi")).toBeTruthy();
    expect(revenue.getByText("NewAPI 收入库只读 DSN")).toBeTruthy();
    expect(revenue.getByText("缺失")).toBeTruthy();
  });

  it("缺失的引用粘贴后走 upsert，成功后清空密码框并显示 run_id", async () => {
    const fetchMock = handler();
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();
    await screen.findByRole("table");

    const row = rowOf("secret://newapi/readonly-token");
    const input = within(row).getByLabelText("secret://newapi/readonly-token 的凭据值") as HTMLInputElement;
    expect(input.type).toBe("password");
    const transientValue = ["paste", "once"].join("-");
    fireEvent.change(input, { target: { value: transientValue } });
    fireEvent.click(within(row).getByRole("button", { name: /粘贴并保存/ }));

    await waitFor(() => {
      expect(screen.getByText("已保存 secret://newapi/readonly-token")).toBeTruthy();
      expect(screen.getByText("run_id run-exp-1")).toBeTruthy();
    });
    expect(input.value).toBe("");
    expect(screen.queryByText(transientValue)).toBeNull();

    const post = firstPost(fetchMock);
    expect(post[0]).toBe("/api/v1/actions/credential.secret.upsert/versions/1/execute");
    expect(JSON.parse(String(post[1].body))).toEqual({
      params: { credential_ref: "secret://newapi/readonly-token", secret_value: transientValue },
    });
  });

  it("已配置的引用再粘贴一次走 rotate", async () => {
    const fetchMock = handler({ action_run_id: "run-exp-rotate" });
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();
    await screen.findByRole("table");

    const row = rowOf("secret://sub2api-prod/read-token");
    fireEvent.change(within(row).getByLabelText(/的凭据值/), { target: { value: "next-token" } });
    fireEvent.click(within(row).getByRole("button", { name: /粘贴并保存/ }));

    await waitFor(() => {
      expect(screen.getByText("已轮换 secret://sub2api-prod/read-token")).toBeTruthy();
      expect(screen.getByText("run_id run-exp-rotate")).toBeTruthy();
    });
    const post = firstPost(fetchMock);
    expect(post[0]).toBe("/api/v1/actions/credential.secret.rotate/versions/1/execute");
    expect(JSON.parse(String(post[1].body))).toEqual({
      params: { credential_ref: "secret://sub2api-prod/read-token", secret_value: "next-token" },
    });
  });

  it("空值不发请求，就地提示", async () => {
    const fetchMock = handler();
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();
    await screen.findByRole("table");

    const row = rowOf("secret://archive/minio-kms");
    fireEvent.click(within(row).getByRole("button", { name: /粘贴并保存/ }));

    expect(await within(row).findByText(/请粘贴凭据值/)).toBeTruthy();
    expect(postCalls(fetchMock)).toHaveLength(0);
  });

  it("Action 失败时脱敏错误并清空输入，行内显示错误码与 request_id", async () => {
    const transientValue = ["do", "not", "echo"].join("-");
    const fetchMock = handler(
      { error: { code: "INVALID_PARAMS", message: `provider rejected ${transientValue}`, request_id: "req-exp-err" } },
      400,
    );
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();
    await screen.findByRole("table");

    const row = rowOf("secret://archive/minio-runtime");
    const input = within(row).getByLabelText(/的凭据值/) as HTMLInputElement;
    fireEvent.change(input, { target: { value: transientValue } });
    fireEvent.click(within(row).getByRole("button", { name: /粘贴并保存/ }));

    await waitFor(() => expect(within(row).getByText(/凭据 Action 失败/)).toBeTruthy());
    expect(within(row).getByText(/request_id: req-exp-err/)).toBeTruthy();
    expect(input.value).toBe("");
    expect(screen.queryByText(transientValue)).toBeNull();
  });

  it("清单为空时明说，不伪装成全部已配置", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response({ items: [] }))));
    renderPanel();

    expect(await screen.findByText("后端没有声明需要的凭据")).toBeTruthy();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("缺权限时提示缺哪个 scope，而不是空白", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          response({ error: { code: "PERMISSION_DENIED", message: "缺少权限 credential.manage" } }, 403),
        ),
      ),
    );
    renderPanel();

    expect(await screen.findByText("无权访问")).toBeTruthy();
    expect(screen.getByText(/credential\.manage/)).toBeTruthy();
  });
});
