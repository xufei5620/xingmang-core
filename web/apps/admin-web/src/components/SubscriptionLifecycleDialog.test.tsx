import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ProxyAssetItem, SubscriptionBatchItem } from "../api/finance";
import {
  SubscriptionLifecycleDialog,
  subjectFromBatch,
  subjectFromProxy,
  type SubscriptionLifecycleDialogProps,
} from "./SubscriptionLifecycleDialog";

function money(amount_minor: string, scale = 6) {
  return { amount_minor, currency: "USD", scale };
}

function batch(over: Partial<SubscriptionBatchItem> = {}): SubscriptionBatchItem {
  return {
    id: "33333333-3333-4333-8333-333333333333",
    upstream_account_id: "11111111-1111-4111-8111-111111111111",
    paid: money("29990000"),
    surcharge: money("0"),
    refunded: money("0"),
    cost_basis: money("29990000"),
    account_share: money("14995000"),
    daily_amortization: money("967419"),
    currency: "USD",
    starts_on: "2026-08-01",
    expires_on: "2026-08-31",
    effective_days: 31,
    refunded_on: null,
    terminated_on: null,
    account_count: 2,
    proxy_asset_id: null,
    ...over,
  };
}

function proxy(over: Partial<ProxyAssetItem> = {}): ProxyAssetItem {
  return {
    id: "22222222-2222-4222-8222-222222222222",
    paid: money("6200000"),
    surcharge: money("0"),
    refunded: money("0"),
    cost_basis: money("6200000"),
    account_share: money("3100000"),
    daily_amortization: money("100000"),
    currency: "USD",
    opened_on: "2026-08-01",
    expires_on: "2026-08-31",
    effective_days: 31,
    refunded_on: null,
    terminated_on: null,
    shared_account_count: 2,
    buy_platform: "Example",
    buy_address: "https://example.test",
    credential_ref: "",
    mounted: true,
    environment: "development",
    ...over,
  };
}

function fakeResponse(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function okFetch() {
  const fetchMock = vi.fn(() => Promise.resolve(fakeResponse({ action_run_id: "run-lc-1" })));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function failingFetch(status: number, code: string, message: string, requestId = "req-lc-1") {
  const fetchMock = vi.fn(() =>
    Promise.resolve(fakeResponse({ error: { code, message, request_id: requestId } }, status)),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

type FetchMock = ReturnType<typeof okFetch>;

function lastCall(fetchMock: FetchMock): [string, { body?: string }] {
  const call = fetchMock.mock.calls.at(-1) as unknown as [string, { body?: string }];
  if (!call) throw new Error("没有发出请求");
  return call;
}

function postedBody(fetchMock: FetchMock): unknown {
  return JSON.parse(lastCall(fetchMock)[1].body ?? "{}");
}

/** 放行微任务与一轮宏任务，给请求一个真的发出去的机会。
 *
 *  `mutationFn` 一路走到 `fetch` 之间隔着若干 `await`（useMutation 的调度、
 *  ApiClient 的异步头拼装）。断言「没发请求」而不先放行，测到的是「这一瞬间
 *  还没发出去」，不是「不会发出去」。
 *
 *  **实测说明**：今天这几条用例即使没有这个 flush 也会红——它们前面那句
 *  `await findByText(...)` 已经顺带放行了足够多的回合（把闸拆掉再去掉 flush，
 *  四条照样红，验过）。所以它现在是余量而不是那根让断言成立的柱子。留着是
 *  因为那个放行是**顺带**的：哪天有人把锚点换成同步的 getByText，缺了这一句
 *  的断言就会静悄悄变成恒真。 */
async function letRequestsFly() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

function wrap(children: ReactNode) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>{children}</MemoryRouter>
    </QueryClientProvider>
  );
}

const TRIGGER: Record<"refund" | "terminate", string> = {
  refund: "记退款",
  terminate: "终止",
};

async function openDialog(props: Partial<SubscriptionLifecycleDialogProps> = {}) {
  const merged: SubscriptionLifecycleDialogProps = {
    subject: subjectFromBatch(batch()),
    action: "refund",
    businessDayTz: "+08:00",
    onDone: () => {},
    ...props,
  };
  render(wrap(<SubscriptionLifecycleDialog {...merged} />));
  fireEvent.click(screen.getByRole("button", { name: TRIGGER[merged.action] }));
  return within(await screen.findByRole("dialog"));
}

type Dialog = Awaited<ReturnType<typeof openDialog>>;

function fillRefund(dialog: Dialog, amount = "10", day = "2026-08-15", reason = "上游多收一个月") {
  fireEvent.change(dialog.getByLabelText(/累计退款总额/), { target: { value: amount } });
  fireEvent.change(dialog.getByLabelText(/退款生效日/), { target: { value: day } });
  fireEvent.change(dialog.getByLabelText(/理由/), { target: { value: reason } });
}

function fillTerminate(dialog: Dialog, day = "2026-08-15", reason = "上游跑路，账号已不可用") {
  fireEvent.change(dialog.getByLabelText(/终止日/), { target: { value: day } });
  fireEvent.change(dialog.getByLabelText(/理由/), { target: { value: reason } });
}

describe("订阅批次与代理资产的退款 / 终止入口", () => {
  afterEach(() => vi.unstubAllGlobals());

  // --- Schema 字段逐字（白名单语义：多一个字段就是 400）---

  it("批次退款按 refund@1 的 Schema 提交，理由走 params 而不是请求体顶层", async () => {
    const fetchMock = okFetch();
    const onDone = vi.fn();
    const dialog = await openDialog({ action: "refund", onDone });

    fillRefund(dialog, "10");
    fireEvent.click(dialog.getByRole("checkbox"));
    fireEvent.click(dialog.getByRole("button", { name: "提交退款登记" }));

    await vi.waitFor(() => expect(onDone).toHaveBeenCalled());
    expect(lastCall(fetchMock)[0]).toContain(
      "/api/v1/actions/finance.subscription_batch.refund/versions/1/execute",
    );
    // toEqual 是整体比对：多一个字段、少一个字段、把 reason 放到顶层，都会红。
    // 顶层 reason 是 L2+ 的审批理由（XM-ACTION-REASON），这四个是 L1，
    // 它们的 reason 是 Schema 里的一个参数——放错地方换来的是 Schema 校验 400
    expect(postedBody(fetchMock)).toEqual({
      params: {
        subscription_batch_id: batch().id,
        refunded_minor: "10000000",
        refunded_on: "2026-08-15",
        reason: "上游多收一个月",
      },
    });
    expect(onDone).toHaveBeenCalledWith({ title: "订阅批次已登记退款", runId: "run-lc-1" });
  });

  it("批次终止按 terminate@1 的 Schema 提交，没有 refunded_minor 这一格", async () => {
    const fetchMock = okFetch();
    const onDone = vi.fn();
    const dialog = await openDialog({ action: "terminate", onDone });

    expect(dialog.queryByLabelText(/累计退款总额/)).toBeNull();
    fillTerminate(dialog);
    fireEvent.click(dialog.getByRole("checkbox"));
    fireEvent.click(dialog.getByRole("button", { name: "确认终止" }));

    await vi.waitFor(() => expect(onDone).toHaveBeenCalled());
    expect(lastCall(fetchMock)[0]).toContain(
      "/api/v1/actions/finance.subscription_batch.terminate/versions/1/execute",
    );
    expect(postedBody(fetchMock)).toEqual({
      params: {
        subscription_batch_id: batch().id,
        terminated_on: "2026-08-15",
        reason: "上游跑路，账号已不可用",
      },
    });
    expect(onDone).toHaveBeenCalledWith({
      title: "订阅批次已终止，损失已结转",
      runId: "run-lc-1",
    });
  });

  it("代理退款用 proxy_asset_id，不是 subscription_batch_id", async () => {
    const fetchMock = okFetch();
    const onDone = vi.fn();
    const dialog = await openDialog({
      subject: subjectFromProxy(proxy()),
      action: "refund",
      onDone,
    });

    fillRefund(dialog, "2.5", "2026-08-20", "机房退了一半");
    fireEvent.click(dialog.getByRole("checkbox"));
    fireEvent.click(dialog.getByRole("button", { name: "提交退款登记" }));

    await vi.waitFor(() => expect(onDone).toHaveBeenCalled());
    expect(lastCall(fetchMock)[0]).toContain(
      "/api/v1/actions/finance.proxy_asset.refund/versions/1/execute",
    );
    expect(postedBody(fetchMock)).toEqual({
      params: {
        proxy_asset_id: proxy().id,
        refunded_minor: "2500000",
        refunded_on: "2026-08-20",
        reason: "机房退了一半",
      },
    });
    expect(onDone).toHaveBeenCalledWith({ title: "代理资产已登记退款", runId: "run-lc-1" });
  });

  it("代理终止用 proxy_asset_id，回执说清损失已结转", async () => {
    const fetchMock = okFetch();
    const onDone = vi.fn();
    const dialog = await openDialog({
      subject: subjectFromProxy(proxy()),
      action: "terminate",
      onDone,
    });

    fillTerminate(dialog, "2026-08-20", "代理商跑路");
    fireEvent.click(dialog.getByRole("checkbox"));
    fireEvent.click(dialog.getByRole("button", { name: "确认终止" }));

    await vi.waitFor(() => expect(onDone).toHaveBeenCalled());
    expect(lastCall(fetchMock)[0]).toContain(
      "/api/v1/actions/finance.proxy_asset.terminate/versions/1/execute",
    );
    expect(postedBody(fetchMock)).toEqual({
      params: {
        proxy_asset_id: proxy().id,
        terminated_on: "2026-08-20",
        reason: "代理商跑路",
      },
    });
    expect(onDone).toHaveBeenCalledWith({
      title: "代理资产已终止，损失已结转",
      runId: "run-lc-1",
    });
  });

  // --- 二次确认：不勾就不发请求 ---

  it("终止没勾二次确认就提交：一个请求都不发，并说清为什么", async () => {
    const fetchMock = okFetch();
    const dialog = await openDialog({ action: "terminate" });

    fillTerminate(dialog);
    fireEvent.click(dialog.getByRole("button", { name: "确认终止" }));

    // 先 await 一个正向锚点（那句解释真的出现了），再同步断言缺席。
    // 把 not.toHaveBeenCalled 套进 waitFor 会恒真——第一次轮询时请求本来就还没发出去
    expect(
      await dialog.findByText(
        "请先勾选确认：终止只能做一次，终止日登记之后不可再改，平台没有撤销终止的 Action。",
      ),
    ).toBeTruthy();
    await letRequestsFly();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("退款没勾二次确认就提交：一个请求都不发；勾上之后同一份填写照常发得出去", async () => {
    const fetchMock = okFetch();
    const onDone = vi.fn();
    const dialog = await openDialog({ action: "refund", onDone });

    fillRefund(dialog);
    fireEvent.click(dialog.getByRole("button", { name: "提交退款登记" }));
    expect(
      await dialog.findByText(
        "请先勾选确认：累计退款额登记之后只能增不能减，服务端不接受调低。",
      ),
    ).toBeTruthy();
    await letRequestsFly();
    expect(fetchMock).not.toHaveBeenCalled();

    // 对照组：拦住的确实只是「没勾」这一件事。缺了这一段，上面那条断言
    // 也可能是因为表单里另有一处填错而通过
    fireEvent.click(dialog.getByRole("checkbox"));
    fireEvent.click(dialog.getByRole("button", { name: "提交退款登记" }));
    await vi.waitFor(() => expect(onDone).toHaveBeenCalled());
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("理由留空就提交：一个请求都不发", async () => {
    const fetchMock = okFetch();
    const dialog = await openDialog({ action: "refund" });

    fireEvent.change(dialog.getByLabelText(/累计退款总额/), { target: { value: "10" } });
    fireEvent.change(dialog.getByLabelText(/退款生效日/), { target: { value: "2026-08-15" } });
    fireEvent.change(dialog.getByLabelText(/理由/), { target: { value: "   " } });
    fireEvent.click(dialog.getByRole("checkbox"));
    fireEvent.click(dialog.getByRole("button", { name: "提交退款登记" }));

    expect(
      await dialog.findByText(
        "理由必填：它原样进审计事件，是事后唯一说得清「这笔钱为什么动」的记录。",
      ),
    ).toBeTruthy();
    await letRequestsFly();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  // --- 只增不减：服务端说不清的那一条，前端自己拦 ---

  it("累计退款额低于已登记值时前端就拦住，并把已登记的数说出来", async () => {
    const fetchMock = okFetch();
    const dialog = await openDialog({
      subject: subjectFromBatch(batch({ refunded: money("5000000"), refunded_on: "2026-08-05" })),
      action: "refund",
    });

    fillRefund(dialog, "3");
    fireEvent.click(dialog.getByRole("checkbox"));
    fireEvent.click(dialog.getByRole("button", { name: "提交退款登记" }));

    expect(
      await dialog.findByText(
        "累计退款额不得低于已登记的 $5.00：这一格填的是累计总额，不是本次新增。" +
          "服务端也会拒绝，但那条拒绝在界面上只会显示成一句「执行失败」，看不出是这个原因。",
      ),
    ).toBeTruthy();
    await letRequestsFly();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("持平已登记值不拦（只增不减，不是必须变大）", async () => {
    const fetchMock = okFetch();
    const onDone = vi.fn();
    const dialog = await openDialog({
      subject: subjectFromBatch(batch({ refunded: money("5000000"), refunded_on: "2026-08-05" })),
      action: "refund",
      onDone,
    });

    fillRefund(dialog, "5");
    fireEvent.click(dialog.getByRole("checkbox"));
    fireEvent.click(dialog.getByRole("button", { name: "提交退款登记" }));

    await vi.waitFor(() => expect(onDone).toHaveBeenCalled());
    expect(postedBody(fetchMock)).toEqual({
      params: {
        subscription_batch_id: batch().id,
        refunded_minor: "5000000",
        refunded_on: "2026-08-15",
        reason: "上游多收一个月",
      },
    });
  });

  it("DTO 的标度与表单不一致时不做比较，交给服务端判", async () => {
    const fetchMock = okFetch();
    const onDone = vi.fn();
    // scale-2 的 500000000 是 $5,000,000.00；按 scale-6 读会变成 $500.00。
    // 这个数是**挑过**的：拿掉标度这道闸，下面填的 $3 就会被误判成「低于已登记
    // 的 $500」而拦下来——用一个两种读法都拦不住的数，这条用例会恒真
    const dialog = await openDialog({
      subject: subjectFromBatch(
        batch({ refunded: money("500000000", 2), refunded_on: "2026-08-05" }),
      ),
      action: "refund",
      onDone,
    });

    fillRefund(dialog, "3");
    fireEvent.click(dialog.getByRole("checkbox"));
    fireEvent.click(dialog.getByRole("button", { name: "提交退款登记" }));

    await vi.waitFor(() => expect(onDone).toHaveBeenCalled());
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  // --- 已终止：终止入口自己收起来 ---

  it("已终止的批次不再给终止入口，同屏另一笔未终止的照常有", () => {
    render(
      wrap(
        <>
          <section aria-label="已终止那一笔">
            <SubscriptionLifecycleDialog
              subject={subjectFromBatch(batch({ terminated_on: "2026-08-10" }))}
              action="terminate"
              onDone={() => {}}
            />
          </section>
          <section aria-label="未终止那一笔">
            <SubscriptionLifecycleDialog
              subject={subjectFromBatch(batch())}
              action="terminate"
              onDone={() => {}}
            />
          </section>
        </>,
      ),
    );

    const terminated = within(screen.getByRole("region", { name: "已终止那一笔" }));
    // 正向锚点：它说出了为什么没有入口，而不是无声地少一个按钮
    expect(
      terminated.getByText("已于 2026-08-10 终止；终止只能做一次，终止日登记后不可再改。"),
    ).toBeTruthy();
    expect(terminated.queryByRole("button", { name: "终止" })).toBeNull();

    // 对照组：未终止的那一笔入口还在。少了这一段，上一条断言在
    // 「按钮名字被改错」时也会通过——那是个假绿
    expect(
      within(screen.getByRole("region", { name: "未终止那一笔" })).getByRole("button", {
        name: "终止",
      }),
    ).toBeTruthy();
  });

  it("已终止的批次仍然可以补记退款（终止之后会重算并改写损失）", () => {
    render(
      wrap(
        <section aria-label="已终止那一笔">
          <SubscriptionLifecycleDialog
            subject={subjectFromBatch(batch({ terminated_on: "2026-08-10" }))}
            action="refund"
            onDone={() => {}}
          />
        </section>,
      ),
    );
    expect(
      within(screen.getByRole("region", { name: "已终止那一笔" })).getByRole("button", {
        name: "记退款",
      }),
    ).toBeTruthy();
  });

  // --- 失败路径：后端原话逐字上屏 ---

  it("跨环境被拒（403）时显示服务端原话、错误码与 request_id", async () => {
    // 这句话逐字来自 internal/platform/finance/actions.go 的 requireSameEnvironment
    const fetchMock = failingFetch(
      403,
      "PERMISSION_DENIED",
      "不允许跨环境操作成本登记簿：调用者身份属于 development",
      "req-env-9",
    );
    const dialog = await openDialog({ action: "terminate" });

    fillTerminate(dialog);
    fireEvent.click(dialog.getByRole("checkbox"));
    fireEvent.click(dialog.getByRole("button", { name: "确认终止" }));

    expect(
      await dialog.findByText(
        "不允许跨环境操作成本登记簿：调用者身份属于 development（错误码 PERMISSION_DENIED）",
      ),
    ).toBeTruthy();
    expect(dialog.getByText("request_id: req-env-9")).toBeTruthy();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    // 表单留在原地：刚填完的内容不能因为一次 403 就消失
    expect((dialog.getByLabelText(/终止日/) as HTMLInputElement).value).toBe("2026-08-15");
  });

  it("终止日落在有效期之外（400）时，服务端那句完整的解释原样上屏", async () => {
    // 这句话逐字来自 finance/amortization.go 的 AmortizationTerm.Validate。
    // 前端**刻意不重复判**这一条：它在服务端是 INVALID_PARAMS，原话到得了界面，
    // 再写一份只会多出一份会漂开的判据。这条用例就是「到得了」的证据
    failingFetch(
      400,
      "INVALID_PARAMS",
      "终止日 2026-09-15 必须落在有效期 2026-08-01..2026-08-31 内",
      "req-range-3",
    );
    const dialog = await openDialog({ action: "terminate" });

    fillTerminate(dialog, "2026-09-15");
    fireEvent.click(dialog.getByRole("checkbox"));
    fireEvent.click(dialog.getByRole("button", { name: "确认终止" }));

    expect(
      await dialog.findByText(
        "终止日 2026-09-15 必须落在有效期 2026-08-01..2026-08-31 内（错误码 INVALID_PARAMS）",
      ),
    ).toBeTruthy();
  });

  it("冲突（409）也照原话显示，不糊成「操作失败」", async () => {
    // 这四个 Action 今天没有已知的 409 分支；这条钉的是渲染契约——
    // 服务端在 409 上说什么，界面就显示什么
    failingFetch(409, "CONFLICT", "这笔批次正在被另一处改写，请稍后重试", "req-conflict-2");
    const dialog = await openDialog({ action: "refund" });

    fillRefund(dialog);
    fireEvent.click(dialog.getByRole("checkbox"));
    fireEvent.click(dialog.getByRole("button", { name: "提交退款登记" }));

    expect(
      await dialog.findByText("这笔批次正在被另一处改写，请稍后重试（错误码 CONFLICT）"),
    ).toBeTruthy();
    expect(dialog.queryByText(/操作失败/)).toBeNull();
  });

  // --- 入口本身 ---

  it("没有维护权限时触发器不可用，点了也开不出表单", () => {
    const fetchMock = okFetch();
    render(
      wrap(
        <SubscriptionLifecycleDialog
          subject={subjectFromBatch(batch())}
          action="terminate"
          disabled
          onDone={() => {}}
        />,
      ),
    );
    const trigger = screen.getByRole("button", { name: "终止" });
    expect((trigger as HTMLButtonElement).disabled).toBe(true);
    expect(trigger.getAttribute("title")).toBe("需要 finance.subscription.manage");
    fireEvent.click(trigger);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("日期那一格不预填，并说明按业务日切时区解释", async () => {
    const dialog = await openDialog({ action: "terminate", businessDayTz: "+08:00" });
    // 预填出来的「今天」按浏览器时区算，未必是业务日上的今天
    expect((dialog.getByLabelText(/终止日/) as HTMLInputElement).value).toBe("");
    expect(
      dialog.getByText(/日期按业务日切时区 \+08:00 解释，不按浏览器时区。/),
    ).toBeTruthy();
    expect(dialog.getByText(/必须落在有效期 2026-08-01\.\.2026-08-31 内/)).toBeTruthy();
  });

  it("终止表单把三条后果写在脸上，包括「没有撤销终止的 Action」", async () => {
    const dialog = await openDialog({ action: "terminate" });
    expect(dialog.getByText("终止不可撤销。提交前先看清这三条：")).toBeTruthy();
    expect(dialog.getByText("自终止日起这笔批次不再产生摊销。")).toBeTruthy();
    expect(
      dialog.getByText(
        "剩余未摊销的部分会结转成一笔损失，记进损失科目——那是报表上单列的一个会计事件。",
      ),
    ).toBeTruthy();
    expect(
      dialog.getByText(
        "终止只能做一次：终止日登记之后不可再改，平台没有「撤销终止」的 Action。要改只能找人直接改库。",
      ),
    ).toBeTruthy();
  });
});
