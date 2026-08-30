import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CredentialManagementPanel } from "./CredentialManagementPanel";

function response(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

const row = {
  credential_ref: "secret://sub2api/readonly",
  scope: "sub2api",
  updated_at: "2026-08-30T09:00:00Z",
  fingerprint: "sha256:abcdef0123456789",
};

function renderPanel() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <CredentialManagementPanel />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function listAndActionHandler(
  actionResponse: Record<string, unknown> = { action_run_id: "run-cred-1" },
) {
  return vi.fn((url: string, init?: RequestInit) => {
    if (init?.method === "POST") return Promise.resolve(response(actionResponse));
    if (url.startsWith("/api/v1/credentials")) return Promise.resolve(response({ items: [row] }));
    return Promise.resolve(response({ items: [] }));
  });
}

describe("设置 · 凭据管理", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("只展示 CredentialRef、scope、更新时间和指纹前缀，不展示响应里的秘密字段", async () => {
    const hiddenValue = ["never", "render", "this"].join("-");
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          response({ items: [{ ...row, secret_value: hiddenValue }] }),
        ),
      ),
    );

    renderPanel();

    const table = within(await screen.findByRole("table"));
    expect(table.getByRole("columnheader", { name: "CredentialRef" })).toBeTruthy();
    expect(table.getByRole("columnheader", { name: "scope" })).toBeTruthy();
    expect(table.getByRole("columnheader", { name: "更新时间" })).toBeTruthy();
    expect(table.getByRole("columnheader", { name: "指纹" })).toBeTruthy();
    expect(table.getByText(row.credential_ref)).toBeTruthy();
    expect(table.getByText("sub2api")).toBeTruthy();
    expect(table.getByText(/2026-08-30 09:00:00 UTC/)).toBeTruthy();
    expect(table.getByText("sha256:abcdef01…")).toBeTruthy();
    expect(screen.queryByText(hiddenValue)).toBeNull();
    expect(screen.getByText(/保存后不会再次显示/)).toBeTruthy();
  });

  it("没有凭据列表时明确显示未接入/空态，不凭空填充样例行", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response({ items: [] }))));

    renderPanel();

    expect(await screen.findByText("暂无凭据引用")).toBeTruthy();
    expect(screen.getByText(/当前环境没有已登记的 CredentialRef/)).toBeTruthy();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("粘贴后通过 credential.secret.upsert Action 保存，并在回执后清空输入框", async () => {
    const fetchMock = listAndActionHandler();
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();

    const form = (await screen.findByRole("heading", { name: "添加凭据" })).closest("section") as HTMLElement;
    const refInput = within(form).getByRole("textbox", { name: /CredentialRef/ });
    const valueInput = within(form).getByLabelText(/凭据值/);
    const transientValue = ["paste", "once", "then", "clear"].join("-");
    fireEvent.change(refInput, { target: { value: "secret://newapi/session" } });
    fireEvent.change(valueInput, { target: { value: transientValue } });
    fireEvent.click(screen.getByRole("button", { name: "保存凭据" }));

    await waitFor(() => {
      expect(screen.getByText("已保存凭据")).toBeTruthy();
      expect(screen.getByText("run_id run-cred-1")).toBeTruthy();
    });
    expect((valueInput as HTMLInputElement).value).toBe("");
    expect(screen.queryByText(transientValue)).toBeNull();

    const post = fetchMock.mock.calls.find(
      (call) => (call[1] as RequestInit | undefined)?.method === "POST",
    ) as unknown as [string, RequestInit];
    expect(post[0]).toBe("/api/v1/actions/credential.secret.upsert/versions/1/execute");
    expect(JSON.parse(String(post[1].body))).toEqual({
      params: { credential_ref: "secret://newapi/session", secret_value: transientValue },
    });
  });

  it("编辑已有引用时使用 rotate，引用只读且不会把旧值预填回表单", async () => {
    const fetchMock = listAndActionHandler({ action_run_id: "run-rotate-1" });
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();

    fireEvent.click(await screen.findByRole("button", { name: "修改凭据" }));
    const form = screen.getByRole("heading", { name: "轮换凭据" }).closest("section") as HTMLElement;
    const refInput = within(form).getByRole("textbox", { name: /CredentialRef/ }) as HTMLInputElement;
    const valueInput = within(form).getByLabelText(/凭据值/) as HTMLInputElement;
    expect(refInput.readOnly).toBe(true);
    expect(refInput.value).toBe(row.credential_ref);
    expect(valueInput.value).toBe("");

    const nextValue = ["rotated", "once"].join("-");
    fireEvent.change(valueInput, { target: { value: nextValue } });
    fireEvent.click(screen.getByRole("button", { name: "轮换并保存" }));
    await waitFor(() => {
      expect(screen.getByText("已轮换凭据")).toBeTruthy();
      expect(screen.getByText("run_id run-rotate-1")).toBeTruthy();
    });

    const post = fetchMock.mock.calls.find(
      (call) => (call[1] as RequestInit | undefined)?.method === "POST",
    ) as unknown as [string, RequestInit];
    expect(post[0]).toBe("/api/v1/actions/credential.secret.rotate/versions/1/execute");
    expect(JSON.parse(String(post[1].body))).toEqual({
      params: { credential_ref: row.credential_ref, secret_value: nextValue },
    });
  });

  it("吊销前要求理由，并只发送引用与理由", async () => {
    const fetchMock = listAndActionHandler({ action_run_id: "run-revoke-1" });
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();

    fireEvent.click(await screen.findByRole("button", { name: "吊销凭据" }));
    const dialog = within(await screen.findByRole("dialog"));
    fireEvent.click(dialog.getByRole("button", { name: "确认吊销" }));
    expect(await screen.findByText(/请填写吊销原因/)).toBeTruthy();
    expect(fetchMock.mock.calls.filter((call) => (call[1] as RequestInit | undefined)?.method === "POST")).toHaveLength(0);

    fireEvent.change(dialog.getByRole("textbox", { name: "吊销原因" }), { target: { value: "planned-retirement" } });
    fireEvent.click(dialog.getByRole("button", { name: "确认吊销" }));
    await waitFor(() => {
      expect(screen.getByText("已吊销凭据")).toBeTruthy();
      expect(screen.getByText("run_id run-revoke-1")).toBeTruthy();
    });

    const post = fetchMock.mock.calls.find(
      (call) => (call[1] as RequestInit | undefined)?.method === "POST",
    ) as unknown as [string, RequestInit];
    expect(post[0]).toBe("/api/v1/actions/credential.secret.revoke/versions/1/execute");
    expect(JSON.parse(String(post[1].body))).toEqual({
      params: { credential_ref: row.credential_ref, reason: "planned-retirement" },
    });
  });

  it("Query 尚未接入时显示明确的未接入状态，但保留粘贴表单作为预览边界", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          response(
            { error: { code: "ACTION_NOT_REGISTERED", message: "凭据列表尚未接入" } },
            404,
          ),
        ),
      ),
    );

    renderPanel();

    expect(await screen.findByText("凭据登记簿尚未接入")).toBeTruthy();
    const form = screen.getByRole("heading", { name: "添加凭据" }).closest("section") as HTMLElement;
    expect(within(form).getByRole("textbox", { name: /CredentialRef/ })).toBeTruthy();
    expect(within(form).getByLabelText(/凭据值/)).toBeTruthy();
  });

  it("Action 错误偶然带回粘贴值时，页面也会脱敏并清空输入", async () => {
    const transientValue = ["do", "not", "echo"].join("-");
    const fetchMock = vi.fn((url: string, init?: RequestInit) => {
      if (init?.method === "POST") {
        return Promise.resolve(
          response(
            {
              error: {
                code: "INVALID_PARAMS",
                message: `provider rejected ${transientValue}`,
                request_id: "req-cred-error",
              },
            },
            400,
          ),
        );
      }
      if (url.startsWith("/api/v1/credentials")) return Promise.resolve(response({ items: [] }));
      return Promise.resolve(response({ items: [] }));
    });
    vi.stubGlobal("fetch", fetchMock);
    renderPanel();

    const form = (await screen.findByRole("heading", { name: "添加凭据" })).closest("section") as HTMLElement;
    const refInput = within(form).getByRole("textbox", { name: /CredentialRef/ });
    const valueInput = within(form).getByLabelText(/凭据值/) as HTMLInputElement;
    fireEvent.change(refInput, { target: { value: "secret://sub2api/readonly" } });
    fireEvent.change(valueInput, { target: { value: transientValue } });
    fireEvent.click(screen.getByRole("button", { name: "保存凭据" }));

    await waitFor(() => expect(screen.getByText(/凭据 Action 失败/)).toBeTruthy());
    expect(screen.queryByText(transientValue)).toBeNull();
    expect(valueInput.value).toBe("");
    expect(screen.getByText(/request_id: req-cred-error/)).toBeTruthy();
    expect(screen.getByRole("alert", { name: /请检查凭据表单/ })).toBeTruthy();
  });

  it("提交空表单时显示可聚焦的错误摘要并保留逐字段提示", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response({ items: [] }))));
    renderPanel();

    fireEvent.click(screen.getByRole("button", { name: "保存凭据" }));
    const summary = await screen.findByRole("alert", { name: /请检查凭据表单/ });
    expect(summary.textContent).toContain("CredentialRef");
    expect(summary.textContent).toContain("凭据值");
    expect(screen.getByText(/凭据引用必须是/)).toBeTruthy();
    expect(screen.getByText(/请粘贴凭据值/)).toBeTruthy();
    expect(document.activeElement).toBe(summary);
  });
});
