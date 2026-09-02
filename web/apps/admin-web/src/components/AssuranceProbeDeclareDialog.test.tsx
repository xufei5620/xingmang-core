import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AssuranceProbeDeclareDialog } from "./AssuranceProbeDeclareDialog";

function fakeResponse(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

const channelsBody = {
  service: { id: "svc-1", service_type: "sub2api", instance_id: "sub2api-dev", environment: "development" },
  inventory: { state: "complete", source: "sub2api", complete: true },
  from: "2026-08-31",
  to: "2026-08-31",
  items: [
    {
      channel_ref: { service_id: "svc-1", external_channel_id: "chn-1" },
      name: "Claude 官方 API",
      models: { count: 3 },
    },
    {
      channel_ref: { service_id: "svc-1", external_channel_id: "chn-2" },
      name: "Azure 东亚",
    },
  ],
};

const connectorConfigsBody = {
  items: [
    {
      platform: "sub2api",
      mode: "real",
      endpoint: "https://sub2api.example.com",
      target_allowlist: ["api.example.com"],
      credential_ref: "secret://sub2api/admin",
      version: 3,
      updated_at: "2026-08-01T00:00:00Z",
      updated_by: "ops",
    },
  ],
};

function handler({
  declareResult = { action_run_id: "run-declare-1", result: { declaration_id: "decl-new-1" } },
  runResult = { action_run_id: "run-run-1", result: { run_id: "run-run-1", declaration_id: "decl-new-1", status: "pending" } },
}: { declareResult?: Record<string, unknown>; runResult?: Record<string, unknown> } = {}) {
  return vi.fn((url: string, init?: RequestInit) => {
    if (init?.method === "POST") {
      if (url.includes("assurance.probe.declare")) return Promise.resolve(fakeResponse(declareResult));
      if (url.includes("assurance.probe.run")) return Promise.resolve(fakeResponse(runResult));
      return Promise.resolve(fakeResponse({ action_run_id: "run-x", result: {} }));
    }
    if (url.startsWith("/api/v1/connectors/config")) return Promise.resolve(fakeResponse(connectorConfigsBody));
    if (/\/api\/v1\/platforms\/[^/]+\/channels/.test(url)) return Promise.resolve(fakeResponse(channelsBody));
    return Promise.resolve(fakeResponse({ items: [] }));
  });
}

function postedParams(fetchMock: ReturnType<typeof vi.fn>, urlSubstring: string): Record<string, unknown> {
  const call = fetchMock.mock.calls.find(
    ([url, init]) => (init as RequestInit | undefined)?.method === "POST" && (url as string).includes(urlSubstring),
  );
  if (!call) throw new Error(`没有找到对 ${urlSubstring} 的 POST 请求`);
  return (JSON.parse((call[1] as RequestInit).body as string) as { params: Record<string, unknown> }).params;
}

async function openDialog(onDone: (summary: unknown) => void = () => {}) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <AssuranceProbeDeclareDialog platform="sub2api" serviceId="svc-1" onDone={onDone} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  fireEvent.click(screen.getByRole("button", { name: "发起检测" }));
  return within(await screen.findByRole("dialog"));
}

describe("发起检测对话框", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("渠道来自真实目录（仅给数量，不给模型名清单），目标主机预填自接入配置的白名单", async () => {
    vi.stubGlobal("fetch", handler());
    const dialog = await openDialog();

    expect(await dialog.findByText("Claude 官方 API")).not.toBeNull();
    expect(dialog.getByText("Azure 东亚")).not.toBeNull();
    expect(dialog.getByText("3 个模型（仅数量）")).not.toBeNull();
    await waitFor(() => {
      expect((dialog.getByLabelText(/探测目标主机/) as HTMLInputElement).value).toBe("api.example.com");
    });
  });

  it("提交后依次调用 declare@1 与 run@1，用 declare 返回的 declaration_id 触发 run", async () => {
    const fetchMock = handler();
    vi.stubGlobal("fetch", fetchMock);
    const onDone = vi.fn();
    const dialog = await openDialog(onDone);

    fireEvent.change(dialog.getByLabelText(/任务名称/), { target: { value: "模型指纹" } });
    await dialog.findByText("Claude 官方 API");
    fireEvent.change(dialog.getByLabelText(/探测目标主机/), { target: { value: "api.example.com" } });
    fireEvent.click(dialog.getByText("Claude 官方 API"));
    fireEvent.change(dialog.getByLabelText(/目标模型/), { target: { value: "claude-sonnet-4" } });
    fireEvent.click(dialog.getByRole("button", { name: "声明并触发检测" }));

    await waitFor(() =>
      expect(onDone).toHaveBeenCalledWith({
        declareRunId: "run-declare-1",
        runResult: { run_id: "run-run-1", declaration_id: "decl-new-1", status: "pending" },
      }),
    );

    const declareParams = postedParams(fetchMock, "assurance.probe.declare");
    expect(declareParams.platform).toBe("sub2api");
    expect(declareParams.name).toBe("模型指纹");
    expect(declareParams.prompt_template_key).toBe("model_fingerprint");
    expect(declareParams.max_tokens).toBe(64);
    expect(JSON.parse(declareParams.targets as string)).toEqual([
      { channel_id: "chn-1", external_channel_id: "chn-1", model: "claude-sonnet-4" },
    ]);
    expect(postedParams(fetchMock, "assurance.probe.run")).toEqual({ declaration_id: "decl-new-1" });
  });

  it("触发的批次被拒绝执行时，回调里带上拒绝原因而不是冒充成功", async () => {
    const fetchMock = handler({
      runResult: {
        action_run_id: "run-run-2",
        result: { run_id: "run-run-2", declaration_id: "decl-new-1", status: "refused", refusal_reason: "platform_kill_switch_off" },
      },
    });
    vi.stubGlobal("fetch", fetchMock);
    const onDone = vi.fn();
    const dialog = await openDialog(onDone);

    fireEvent.change(dialog.getByLabelText(/任务名称/), { target: { value: "模型指纹" } });
    await dialog.findByText("Claude 官方 API");
    fireEvent.change(dialog.getByLabelText(/探测目标主机/), { target: { value: "api.example.com" } });
    fireEvent.click(dialog.getByText("Claude 官方 API"));
    fireEvent.change(dialog.getByLabelText(/目标模型/), { target: { value: "claude-sonnet-4" } });
    fireEvent.click(dialog.getByRole("button", { name: "声明并触发检测" }));

    await waitFor(() =>
      expect(onDone).toHaveBeenCalledWith({
        declareRunId: "run-declare-1",
        runResult: { run_id: "run-run-2", declaration_id: "decl-new-1", status: "refused", refusal_reason: "platform_kill_switch_off" },
      }),
    );
  });

  it("字段校验：未填必填项时不提交，显示具体缺什么", async () => {
    const fetchMock = handler();
    vi.stubGlobal("fetch", fetchMock);
    const dialog = await openDialog();
    await dialog.findByText("Claude 官方 API");

    fireEvent.click(dialog.getByRole("button", { name: "声明并触发检测" }));

    expect(dialog.getByRole("alert")).not.toBeNull();
    expect(dialog.getByText("请填写任务名称")).not.toBeNull();
    expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === "POST")).toBe(
      false,
    );
  });

  it("没有唯一已登记 service 时不猜渠道，明确说明原因", async () => {
    vi.stubGlobal("fetch", handler());
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <AssuranceProbeDeclareDialog platform="sub2api" onDone={() => {}} />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "发起检测" }));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByText(/需要恰好一个已登记的 service/)).not.toBeNull();
  });
});
