import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AssuranceProbeKillSwitch } from "./AssuranceProbeKillSwitch";

const hasRole = vi.hoisted(() => vi.fn());

// 只替身 currentUserHasRole：角色能否读到取决于鉴权模式（见 auth/session.ts
// 的说明），这里不去搭一整套 local 登录会话，只钉住这一个判断结果，
// 与 PersistentDataTable.test.tsx 的 vi.mock 用法同一个先例。
vi.mock("../auth/session", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../auth/session")>();
  return { ...actual, currentUserHasRole: hasRole };
});

function fakeResponse(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function renderSwitch(
  currentState: string | null,
  fallbackConfig?: { mode: string; probeEnabled: boolean; probeCredentialRegistered: boolean } | null,
) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <AssuranceProbeKillSwitch platform="sub2api" currentState={currentState} fallbackConfig={fallbackConfig} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("检测 Kill Switch 入口", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    hasRole.mockReset();
  });

  it("角色判不出来/不是 assurance-probe-admin 时整个入口隐藏", () => {
    hasRole.mockReturnValue(false);
    renderSwitch("enabled");
    expect(screen.queryByRole("button", { name: /Kill Switch/ })).toBeNull();
  });

  it("持有角色时显示入口，对话框里展示当前状态", async () => {
    hasRole.mockReturnValue(true);
    renderSwitch("enabled");
    fireEvent.click(screen.getByRole("button", { name: /Kill Switch/ }));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByText("已启用")).not.toBeNull();
  });

  it("零声明且连接器配置也读不到时如实显示未知，不猜测", async () => {
    hasRole.mockReturnValue(true);
    renderSwitch(null, null);
    fireEvent.click(screen.getByRole("button", { name: /Kill Switch/ }));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByText(/未知（读不到当前状态）/)).not.toBeNull();
  });

  it("零声明但能读到连接器配置时（XM-ASSURE1-glue），用它派生当前状态", async () => {
    hasRole.mockReturnValue(true);
    renderSwitch(null, { mode: "real", probeEnabled: true, probeCredentialRegistered: true });
    fireEvent.click(screen.getByRole("button", { name: /Kill Switch/ }));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByText("已启用")).not.toBeNull();
    // 初始目标状态也要跟着派生值走，不是永远默认"启用"（与
    // PlatformUsersPanel.test.tsx 同一条纪律：选中态靠 aria-pressed，不只是颜色）
    const enableButton = within(dialog.getByRole("group", { name: "目标状态" })).getByRole("button", { name: "启用" });
    expect(enableButton.getAttribute("aria-pressed")).toBe("false");
  });

  it("零声明 + 连接器配置 mode=real 但 probe_enabled=false 时显示未启用", async () => {
    hasRole.mockReturnValue(true);
    renderSwitch(null, { mode: "real", probeEnabled: false, probeCredentialRegistered: false });
    fireEvent.click(screen.getByRole("button", { name: /Kill Switch/ }));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByText("未启用")).not.toBeNull();
  });

  it("零声明 + 连接器配置 mode=fake 时显示 fake 模式（不适用），与被动 not_applicable_fake 同一文案", async () => {
    hasRole.mockReturnValue(true);
    renderSwitch(null, { mode: "fake", probeEnabled: false, probeCredentialRegistered: false });
    fireEvent.click(screen.getByRole("button", { name: /Kill Switch/ }));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByText("fake 模式（不适用）")).not.toBeNull();
  });

  it("检测任务表本身有数据时优先用它的 kill_switch_state，忽略 fallbackConfig", async () => {
    hasRole.mockReturnValue(true);
    // currentState="enabled" 与 fallbackConfig 刻意矛盾（disabled），
    // 证明有 currentState 时 fallbackConfig 完全不参与判断。
    renderSwitch("enabled", { mode: "real", probeEnabled: false, probeCredentialRegistered: false });
    fireEvent.click(screen.getByRole("button", { name: /Kill Switch/ }));
    const dialog = within(await screen.findByRole("dialog"));
    expect(dialog.getByText("已启用")).not.toBeNull();
  });

  it("启用并填写凭据引用后提交，params 带上 probe_credential_ref，成功后显示 run_id", async () => {
    const fetchMock = vi.fn((_url: string, _init?: RequestInit) => Promise.resolve(fakeResponse({ action_run_id: "run-kill-1", result: {} })));
    vi.stubGlobal("fetch", fetchMock);
    hasRole.mockReturnValue(true);
    renderSwitch("disabled");

    fireEvent.click(screen.getByRole("button", { name: /Kill Switch/ }));
    const dialog = within(await screen.findByRole("dialog"));
    fireEvent.click(dialog.getByRole("button", { name: "启用" }));
    fireEvent.change(dialog.getByLabelText(/探测凭据引用/), {
      target: { value: "secret://sub2api/probe" },
    });
    fireEvent.click(dialog.getByRole("button", { name: "确认启用" }));

    expect(await dialog.findByText(/run_id run-kill-1/)).not.toBeNull();
    const call = fetchMock.mock.calls.find(
      ([url, init]) =>
        (init as RequestInit | undefined)?.method === "POST" &&
        (url as string).includes("assurance.probe.kill_switch.set"),
    );
    expect(call).toBeTruthy();
    const params = JSON.parse((call![1] as RequestInit).body as string).params;
    expect(params).toEqual({
      platform: "sub2api",
      probe_enabled: true,
      probe_credential_ref: "secret://sub2api/probe",
    });
  });

  it("关闭时不要求凭据引用，提交只带 probe_enabled=false", async () => {
    const fetchMock = vi.fn((_url: string, _init?: RequestInit) => Promise.resolve(fakeResponse({ action_run_id: "run-kill-2", result: {} })));
    vi.stubGlobal("fetch", fetchMock);
    hasRole.mockReturnValue(true);
    renderSwitch("enabled");

    fireEvent.click(screen.getByRole("button", { name: /Kill Switch/ }));
    const dialog = within(await screen.findByRole("dialog"));
    // "关闭"同时是对话框右上角的 X 关闭按钮（Dialog 组件的
    // aria-label="关闭"）与这里的"目标状态"切换按钮，必须限定在 group 里查,
    // 否则 getByRole 会因为撞名而报"找到多个"
    fireEvent.click(within(dialog.getByRole("group", { name: "目标状态" })).getByRole("button", { name: "关闭" }));
    fireEvent.click(dialog.getByRole("button", { name: "确认关闭" }));

    await waitFor(() => expect(dialog.queryByText(/run_id run-kill-2/)).not.toBeNull());
    const call = fetchMock.mock.calls.find(
      ([url, init]) =>
        (init as RequestInit | undefined)?.method === "POST" &&
        (url as string).includes("assurance.probe.kill_switch.set"),
    );
    const params = JSON.parse((call![1] as RequestInit).body as string).params;
    expect(params).toEqual({ platform: "sub2api", probe_enabled: false });
  });

  it("403 时给出缺哪个权限的提示，不是裸错误码", async () => {
    const fetchMock = vi.fn((_url: string, _init?: RequestInit) =>
      Promise.resolve(
        fakeResponse(
          { error: { code: "PERMISSION_DENIED", message: "缺少权限 assurance.probe.kill_switch" } },
          403,
        ),
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    hasRole.mockReturnValue(true);
    renderSwitch("enabled");

    fireEvent.click(screen.getByRole("button", { name: /Kill Switch/ }));
    const dialog = within(await screen.findByRole("dialog"));
    // "关闭"同时是对话框右上角的 X 关闭按钮（Dialog 组件的
    // aria-label="关闭"）与这里的"目标状态"切换按钮，必须限定在 group 里查,
    // 否则 getByRole 会因为撞名而报"找到多个"
    fireEvent.click(within(dialog.getByRole("group", { name: "目标状态" })).getByRole("button", { name: "关闭" }));
    fireEvent.click(dialog.getByRole("button", { name: "确认关闭" }));

    expect(await dialog.findByText(/需要权限：assurance.probe.kill_switch/)).not.toBeNull();
  });
});
