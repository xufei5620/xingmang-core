import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { SilenceItem, SilenceState } from "../api/alerts";
import { AlertSilences } from "./AlertSilences";

/** 服务端判定三态所用的时刻。窗口的起止都相对它写，读起来不用心算。 */
const AS_OF = "2026-09-08T12:00:00Z";

function fakeResponse(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function silence(
  id: string,
  state: SilenceState,
  overrides: Partial<SilenceItem> = {},
): SilenceItem {
  return {
    id,
    rule_key: "metric.sync.failed",
    environment: "development",
    reason: `理由-${id}`,
    starts_at: "2026-09-08T11:30:00Z",
    ends_at: "2026-09-08T12:30:00Z",
    created_by: "staff_alice",
    created_at: "2026-09-08T11:29:00Z",
    state,
    ...overrides,
  };
}

/** active = state 参数缺省那次请求的结果；all = state=all 那次。 */
function stubSilences(active: SilenceItem[], all: SilenceItem[], envelope: Record<string, unknown> = {}) {
  const fetchMock = vi.fn((url: string) => {
    if (url.startsWith("/api/v1/alerts/silences")) {
      const items = url.includes("state=all") ? all : active;
      return Promise.resolve(
        fakeResponse({ items, limit: 200, truncated: false, as_of: AS_OF, ...envelope }),
      );
    }
    return Promise.resolve(fakeResponse({ error: { code: "NOT_REGISTERED", message: "未知路径" } }, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderSilences() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <AlertSilences />
    </QueryClientProvider>,
  );
}

/** 找到某条记录所在的表格行（按理由定位，理由在这些用例里唯一）。 */
function rowByReason(reason: string): HTMLElement {
  return screen.getByText(reason).closest("tr") as HTMLElement;
}

afterEach(() => vi.unstubAllGlobals());

describe("暂停告警子页（XM-SILENCE-LIST）", () => {
  it("生效中与已过期分得开：同一张表里各自标对了态", async () => {
    // 真数据：一条正在压着、一条早就过了、一条还没开始。
    const active = silence("s-active", "active", { reason: "上游维护中" });
    const expired = silence("s-expired", "expired", {
      reason: "上周那次",
      starts_at: "2026-09-01T00:00:00Z",
      ends_at: "2026-09-01T02:00:00Z",
    });
    const scheduled = silence("s-scheduled", "scheduled", {
      reason: "今晚要发版",
      starts_at: "2026-09-08T20:00:00Z",
      ends_at: "2026-09-08T22:00:00Z",
    });
    stubSilences([active], [active, expired, scheduled]);
    renderSilences();

    // 正向锚点：三行都渲染出来了，再逐行断言各自的态。
    expect(await screen.findByText("上游维护中")).not.toBeNull();
    expect(within(rowByReason("上游维护中")).getByText("生效中")).not.toBeNull();
    expect(within(rowByReason("上周那次")).getByText("已过期")).not.toBeNull();
    expect(within(rowByReason("今晚要发版")).getByText("未开始")).not.toBeNull();

    // 「已过期」那行不能同时挂着「生效中」——两个徽章都在的话，
    // 一眼扫过去分不出哪条在压着告警，而那是这一页的全部用途。
    expect(within(rowByReason("上周那次")).queryByText("生效中")).toBeNull();
  });

  it("横幅只数生效中的那些，已过期的不进计数", async () => {
    const active = silence("s-1", "active", { reason: "正在压着" });
    const expired = silence("s-2", "expired", { reason: "已经过了" });
    const alsoExpired = silence("s-3", "expired", { reason: "更早那条" });
    // 生效中那次请求只返回 1 条；含历史那次返回 3 条。
    stubSilences([active], [active, expired, alsoExpired]);
    renderSilences();

    expect(await screen.findByText(/现在有 1 个静默窗口正在压着告警/)).not.toBeNull();
    // 断言「不是 3」：横幅若错用了含历史那份数据，数字会变成 3。
    expect(screen.queryByText(/现在有 3 个静默窗口正在压着告警/)).toBeNull();
  });

  it("一条都不生效时说清楚「所有规则都会正常投递」", async () => {
    // 库里有历史窗口但没有生效中的——这是最容易被误读成「有静默」的情形。
    const expired = silence("s-old", "expired", { reason: "很久以前" });
    stubSilences([], [expired]);
    renderSilences();

    expect(await screen.findByText(/当前没有静默窗口生效/)).not.toBeNull();
    expect(screen.queryByText(/正在压着告警/)).toBeNull();
    // 历史窗口仍然要列出来：它是「上次谁压过、压了什么」的唯一记录。
    expect(screen.getByText("很久以前")).not.toBeNull();
  });

  it("发两次请求：生效中单独查，不跟含历史那份共用", async () => {
    // 这一条钉住的是「生效中计数不会被条数上限挤掉」这个设计。
    // 改成只发一次 state=all 再在前端分组，它立刻变红。
    const fetchMock = stubSilences([], []);
    renderSilences();
    await screen.findByText(/当前没有静默窗口生效/);

    const urls = fetchMock.mock.calls.map(([url]) => String(url));
    const silenceUrls = urls.filter((u) => u.startsWith("/api/v1/alerts/silences"));
    expect(silenceUrls.length).toBe(2);
    // 一次带 state=all（含历史），一次不带（服务端默认只给生效中的）。
    expect(silenceUrls.filter((u) => u.includes("state=all")).length).toBe(1);
    expect(silenceUrls.filter((u) => !u.includes("state=")).length).toBe(1);
  });

  it("「是谁按的、什么时候到期」都在行里", async () => {
    // 这一页存在的理由就是回答这两个问题，少一个就白加了这条端点。
    const active = silence("s-1", "active", { reason: "上游维护中", created_by: "staff_bob" });
    stubSilences([active], [active]);
    renderSilences();

    await screen.findByText("上游维护中");
    const row = within(rowByReason("上游维护中"));
    expect(row.getByText("staff_bob")).not.toBeNull();
    expect(row.getByText(/止 .*2026-09-08/)).not.toBeNull();
    expect(row.getByText(/起 .*2026-09-08/)).not.toBeNull();
    // 还要压多久：由服务端给的 ends_at 与 as_of 算，不碰浏览器时钟。
    expect(row.getByText("还剩 30 分钟")).not.toBeNull();
  });

  it("全局窗口标成「全部规则」，不显示成一条普通的单规则窗口", async () => {
    // rule_key 空串 = 压住该环境**所有**规则，是爆炸半径最大的一种。
    const global = silence("s-global", "active", { rule_key: "", reason: "全线维护" });
    stubSilences([global], [global]);
    renderSilences();

    expect(await screen.findByText("全线维护")).not.toBeNull();
    const row = within(rowByReason("全线维护"));
    expect(row.getByText("全部规则（全局）")).not.toBeNull();
    expect(row.getByText("生效中")).not.toBeNull();
  });

  it("说清楚窗口没法取消——只会自己到期", async () => {
    // 平台今天确实没有撤销静默的能力。不说的话，运营会一直找那个按钮。
    stubSilences([], []);
    renderSilences();
    await screen.findByText(/当前没有静默窗口生效/);

    expect(screen.getByText(/窗口只会自己到期，没有「取消」/)).not.toBeNull();
  });

  it("含历史那份被截断时说出来，并声明计数不受影响", async () => {
    const active = silence("s-1", "active", { reason: "正在压着" });
    stubSilences([active], [active], { truncated: true, limit: 200 });
    renderSilences();

    expect(await screen.findByText(/这一页可能不是全部/)).not.toBeNull();
    expect(screen.getByText(/「生效中」计数不受这条影响/)).not.toBeNull();
  });
});
