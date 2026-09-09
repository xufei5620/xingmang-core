import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { UpstreamAccountItem } from "../api/finance";
import { UpstreamAccountDialog } from "./UpstreamAccountDialog";

const REF_SCHEME = "secret://";
const SAMPLE_REF = `${REF_SCHEME}sub2api/prod-key`;

function account(over: Partial<UpstreamAccountItem> = {}): UpstreamAccountItem {
  return {
    id: "11111111-1111-4111-8111-111111111111",
    system_type: "sub2api",
    access_method: "upstream_key",
    base_url: "https://relay-a.example.com",
    upstream_name: "Relay A",
    upstream_contact: "运营群 @relay-a",
    upstream_group: "gpt-main",
    credential_ref: SAMPLE_REF,
    recharge_ratio: "1.15",
    group_rate: "1.25",
    recharge_cost_rate: "0.869565217",
    currency: "USD",
    business_day_tz: "+08:00",
    platform_id: "sub2api",
    status: "active",
    environment: "development",
    metered: true,
    token_mappings: [],
    created_at: "2026-08-01T00:00:00Z",
    updated_at: "2026-08-28T09:00:00Z",
    ...over,
  };
}

function fakeResponse(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function stubPost(body: unknown, status = 200) {
  const fetchMock = vi.fn(() => Promise.resolve(fakeResponse(body, status)));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

async function openDialog(props: { account?: UpstreamAccountItem } = {}) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <UpstreamAccountDialog
          platform="sub2api"
          {...(props.account ? { account: props.account } : {})}
          onDone={() => {}}
        />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  fireEvent.click(screen.getByRole("button", { name: props.account ? "修改" : "＋ 添加上游" }));
  return within(await screen.findByRole("dialog"));
}

/** 取出这次 POST 的 params。 */
function postedParams(fetchMock: ReturnType<typeof vi.fn>): Record<string, unknown> {
  const call = fetchMock.mock.calls.at(-1) as unknown as [string, { body?: string }];
  if (!call) throw new Error("没有发出任何请求");
  return (JSON.parse(call[1].body ?? "{}") as { params: Record<string, unknown> }).params;
}

describe("登记 / 修改上游账号（走 Action 内核）", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("修改模式锁死系统类型与接入方式，并说明为什么", async () => {
    // 它们是成本口径的分叉点：改了会让同一个账号的历史成本前后用两套算法算出来，
    // 而台账里毫无痕迹。后端会拒，这里做成只读是为了让人在填之前就看见这条规则
    const dialog = await openDialog({ account: account() });
    const systemType = dialog.getByLabelText(/系统类型/) as HTMLInputElement;
    expect(systemType.readOnly).toBe(true);
    expect(dialog.getByText(/成本口径的分叉点/)).toBeTruthy();
  });

  it("新登记时两项是可选的下拉，不是只读", async () => {
    const dialog = await openDialog();
    expect(dialog.getByRole("combobox", { name: "系统类型" })).toBeTruthy();
    expect(dialog.getByRole("combobox", { name: "接入方式" })).toBeTruthy();
  });

  it("新登记预填当前平台——登记完忘了配归属，这条账号的成本就归不到任何平台", async () => {
    const dialog = await openDialog();
    expect((dialog.getByLabelText(/接入平台/) as HTMLInputElement).value).toBe("sub2api");
  });

  it("修改模式预填全部字段：这个 Action 是**整行替换**，漏预填等于保存时清空", async () => {
    const fetchMock = stubPost({ action_run_id: "run-1" });
    const dialog = await openDialog({ account: account() });
    fireEvent.click(dialog.getByRole("button", { name: "保存" }));

    await screen.findByRole("dialog").catch(() => null);
    const params = postedParams(fetchMock);
    expect(params.base_url).toBe("https://relay-a.example.com");
    expect(params.currency).toBe("USD");
    expect(params.business_day_tz).toBe("+08:00");
    expect(params.recharge_ratio).toBe("1.15");
    expect(params.group_rate).toBe("1.25");
    expect(params.upstream_name).toBe("Relay A");
    expect(params.upstream_contact).toBe("运营群 @relay-a");
    expect(params.upstream_group).toBe("gpt-main");
    // 有 id = 改这一条，不是新建
    expect(params.upstream_account_id).toBe("11111111-1111-4111-8111-111111111111");
  });

  it("元数据和分组倍率都有显式标签与完整预填", async () => {
    const dialog = await openDialog({ account: account() });
    expect((dialog.getByLabelText("上游名称") as HTMLInputElement).value).toBe("Relay A");
    expect((dialog.getByLabelText("上游联系人") as HTMLInputElement).value).toBe("运营群 @relay-a");
    expect((dialog.getByLabelText("上游分组") as HTMLInputElement).value).toBe("gpt-main");
    expect((dialog.getByLabelText("分组倍率") as HTMLInputElement).value).toBe("1.25");
  });

  it("前端校验不过就不发请求，就地报错", async () => {
    const fetchMock = stubPost({ action_run_id: "run-1" });
    const dialog = await openDialog({ account: account({ credential_ref: "" }) });
    fireEvent.click(dialog.getByRole("button", { name: "保存" }));
    expect(await dialog.findByText(/凭据引用必填/)).toBeTruthy();
    expect(fetchMock.mock.calls.length).toBe(0);
  });

  it("后端错误**原样透传**，不包一层「操作失败」", async () => {
    // 后端已保证不回显敏感值；自己再包一层只会把「倍率必须为正」这类
    // 可操作的话盖掉，让人去查一个并不存在的系统故障
    const message = "recharge_ratio=\"0\" 必须为正：非正倍率会被静默当成 1";
    stubPost(
      { error: { code: "INVALID_PARAMS", message, request_id: "req-9" } },
      400,
    );
    const dialog = await openDialog({ account: account() });
    fireEvent.click(dialog.getByRole("button", { name: "保存" }));

    const alert = await dialog.findByRole("alert");
    expect(alert.textContent).toContain("必须为正");
    expect(dialog.getByText(/req-9/)).toBeTruthy();
  });

  it("403 时说清缺哪个权限", async () => {
    stubPost(
      {
        error: {
          code: "PERMISSION_DENIED",
          message: "缺少权限 finance.upstream_account.manage",
          request_id: "req-10",
        },
      },
      403,
    );
    const dialog = await openDialog({ account: account() });
    fireEvent.click(dialog.getByRole("button", { name: "保存" }));
    // 两处都说得出这个 scope：后端原文一条、「需要权限」补充一条
    expect((await dialog.findAllByText(/finance\.upstream_account\.manage/)).length).toBe(2);
  });

  it("失败之后表单还在——刚填完的内容不能因为一次 403 就消失", async () => {
    stubPost({ error: { code: "PERMISSION_DENIED", message: "缺少权限" } }, 403);
    const dialog = await openDialog({ account: account() });
    fireEvent.click(dialog.getByRole("button", { name: "保存" }));
    await dialog.findByText(/缺少权限/);
    expect((dialog.getByLabelText(/上游网址/) as HTMLInputElement).value).toBe(
      "https://relay-a.example.com",
    );
  });
});
