import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  FIRE_COUNT_HEADER,
  type AlertItem,
  type AlertNotifyStatus,
  type AlertStatus,
} from "../api/alerts";
import { AlertsPage } from "./AlertsPage";

const ACK_URL = "/api/v1/actions/alerts.alert.acknowledge/versions/1/execute";

function fakeResponse(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

function alert(
  id: string,
  status: AlertStatus,
  title: string,
  notify: AlertNotifyStatus = "delivered",
): AlertItem {
  return {
    id,
    rule_key: "metric.sync.failed",
    dedup_key: `metric.sync.failed:development:${id}`,
    severity: "critical",
    status,
    title,
    detail: "",
    environment: "development",
    source_metric_key: "",
    opened_at: "2026-09-01T10:00:00Z",
    last_seen_at: "2026-09-01T10:05:00Z",
    acknowledged_at: null,
    resolved_at: null,
    fire_count: 3,
    notify_status: notify,
    notify_error: notify === "failed" ? "telegram: HTTP 502" : "",
    notified_at: notify === "delivered" ? "2026-09-01T10:00:05Z" : null,
  };
}

/** 三条 OPEN + 一条 SILENCED：批量确认的每一种结局都要有对应的行。 */
const ALERTS = [
  alert("id-1", "OPEN", "甲告警"),
  alert("id-2", "OPEN", "乙告警"),
  alert("id-3", "OPEN", "丙告警"),
  alert("id-4", "SILENCED", "丁告警（已静默）"),
];

/** 让 `failing` 里的 alert_id 返回 403，其余成功。
 *
 *  按 alert_id 而不是按调用次序区分：批量是串行发的，钉次序等于顺带钉住了
 *  实现的循环顺序，那不是这条用例要测的东西。 */
function stubAlerts(items: AlertItem[], failing: Record<string, unknown> = {}) {
  const fetchMock = vi.fn((url: string, init?: RequestInit) => {
    if (init?.method === "POST" && url === ACK_URL) {
      const alertId = JSON.parse(String(init.body)).params.alert_id as string;
      const failure = failing[alertId];
      if (failure) return Promise.resolve(fakeResponse(failure, 403));
      return Promise.resolve(fakeResponse({ action_run_id: `run-${alertId}` }));
    }
    if (url.startsWith("/api/v1/alerts")) {
      return Promise.resolve(fakeResponse({ items }));
    }
    return Promise.resolve(fakeResponse({ error: { code: "NOT_REGISTERED", message: "未知路径" } }, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

/** 静默端点的 stub。
 *
 *  必须先于 `/api/v1/alerts` 判断：后者是前者的前缀，顺序反了的话静默请求会
 *  拿到一份告警列表，而这一页会把它渲染成一张全是空格子的表。 */
function stubSilences(active: unknown[], all: unknown[]) {
  const fetchMock = vi.fn((url: string) => {
    if (url.startsWith("/api/v1/alerts/silences")) {
      const items = url.includes("state=all") ? all : active;
      return Promise.resolve(
        fakeResponse({ items, limit: 200, truncated: false, as_of: "2026-09-08T12:00:00Z" }),
      );
    }
    if (url.startsWith("/api/v1/alerts")) {
      return Promise.resolve(fakeResponse({ items: [] }));
    }
    return Promise.resolve(fakeResponse({ error: { code: "NOT_REGISTERED", message: "未知路径" } }, 404));
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderAlerts(path = "/alerts") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <AlertsPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

/** 勾上若干行（复选框的可及名是「选择 <行键>」，行键 = alert.id）。 */
function select(...ids: string[]) {
  for (const id of ids) {
    fireEvent.click(screen.getByRole("checkbox", { name: `选择 ${id}` }));
  }
}

function ackedIds(fetchMock: ReturnType<typeof stubAlerts>): string[] {
  return fetchMock.mock.calls
    .filter(([url, init]) => init?.method === "POST" && url === ACK_URL)
    .map(([, init]) => JSON.parse(String(init?.body)).params.alert_id as string);
}

afterEach(() => vi.unstubAllGlobals());

/** 「评估轮次」那一列每一行的格子文本，按表头定位列，不按文本猜。 */
function fireCountCells(): { text: string; title: string | null }[] {
  const table = screen.getByRole("table");
  const headers = within(table).getAllByRole("columnheader");
  const index = headers.findIndex((h) => (h.textContent ?? "").includes(FIRE_COUNT_HEADER));
  expect(index).toBeGreaterThanOrEqual(0);
  return within(table)
    .getAllByRole("row")
    .filter((row) => within(row).queryAllByRole("cell").length > 0)
    .map((row) => {
      const cell = within(row).getAllByRole("cell")[index]!;
      return { text: cell.textContent ?? "", title: cell.querySelector("[title]")?.getAttribute("title") ?? null };
    });
}

// --- XM-WORKBENCH-TRUTH 评审回合三：数字列里只放数字 --------------------------

describe("「评估轮次」列", () => {
  it("格子里是纯数字：口径由表头与悬停整句承担，数字列里再写整句既重复又对不齐", async () => {
    stubAlerts(ALERTS);
    renderAlerts();
    await screen.findByText("甲告警");

    const cells = fireCountCells();
    expect(cells.length).toBe(ALERTS.length);
    for (const cell of cells) {
      expect(cell.text).toBe("3");
      expect(cell.text).toMatch(/^\d+$/);
      // 整句没有丢，只是退到了悬停里
      expect(cell.title).toContain("评估 3 轮");
      expect(cell.title).toContain("这是评估轮数，不是发生次数");
    }
  });

  it("trigger_count 到位后格子写「M / N」，悬停说清哪个是触发、哪个是评估", async () => {
    stubAlerts([{ ...alert("id-9", "OPEN", "己告警"), trigger_count: 5 } as AlertItem]);
    renderAlerts();
    await screen.findByText("己告警");

    const [cell] = fireCountCells();
    expect(cell!.text).toBe("5 / 3");
    expect(cell!.title).toContain("触发 5 次 · 评估 3 轮");
  });
});

describe("「首次 / 最近」列的确认时刻", () => {
  it("已确认的行多一行「确认 <时刻>」，没人确认过的行没有这一行", async () => {
    stubAlerts([
      { ...alert("id-a", "ACKNOWLEDGED", "戊告警"), acknowledged_at: "2026-09-01T10:03:00Z" },
      alert("id-b", "OPEN", "己告警"),
    ]);
    renderAlerts();
    const acked = (await screen.findByText("戊告警")).closest("tr") as HTMLElement;
    const open = screen.getByText("己告警").closest("tr") as HTMLElement;
    expect(within(acked).getByText("确认 2026-09-01 10:03:00 UTC")).not.toBeNull();
    // 「最近」晚于「确认」才读得出「确认之后还在响」，两行都要在
    expect(within(acked).getByText("最近 2026-09-01 10:05:00 UTC")).not.toBeNull();
    expect(within(open).queryByText(/^确认 /)).toBeNull();
  });
});

// --- 缺口 1：批量确认 -------------------------------------------------------

describe("批量确认（XM-ALERTS-GAPS）", () => {
  it("真的对每一条选中的告警调一次 Action，回执带上各自的 run_id", async () => {
    const fetchMock = stubAlerts(ALERTS);
    renderAlerts();
    await screen.findByText("甲告警");

    select("id-1", "id-2");
    fireEvent.click(screen.getByRole("button", { name: "批量确认 2 条" }));

    await screen.findByText("已确认 2 条；审计事件通常几秒内出现在审计页");
    // 走的是同一个 Action，一条一次（宪法 2 条：没有"批量确认"端点）
    expect(ackedIds(fetchMock)).toEqual(["id-1", "id-2"]);
    // 规格 §5.8：每一次写的 action_run_id 都要显示出来，批量不是例外
    expect(screen.getByText("甲告警：run_id=run-id-1")).not.toBeNull();
    expect(screen.getByText("乙告警：run_id=run-id-2")).not.toBeNull();
  });

  it("部分失败时逐条说清是哪一条、为什么，而不是一句「操作成功」", async () => {
    // 三条里第二条 403：这是批量写最常见的真实形态
    const fetchMock = stubAlerts(ALERTS, {
      "id-2": {
        error: {
          code: "PERMISSION_DENIED",
          message: "缺少权限 alerts.alert.manage",
          request_id: "req-9",
        },
      },
    });
    renderAlerts();
    await screen.findByText("甲告警");

    select("id-1", "id-2", "id-3");
    fireEvent.click(screen.getByRole("button", { name: "批量确认 3 条" }));

    await screen.findByText("已确认 2 条，1 条失败；审计事件通常几秒内出现在审计页");
    // 失败的那条要能被指认出来，且原因逐字可读
    expect(
      screen.getByText(
        "乙告警：缺少权限 alerts.alert.manage（权限不足，错误码 PERMISSION_DENIED），需要权限 alerts.alert.manage，request_id=req-9",
      ),
    ).not.toBeNull();
    // 一条失败不中断后面的：第三条仍然发了、也成了
    expect(ackedIds(fetchMock)).toEqual(["id-1", "id-2", "id-3"]);
    expect(screen.getByText("丙告警：run_id=run-id-3")).not.toBeNull();
    // 缺席断言（已变异验证）：失败的那条不该出现在 run_id 名单里
    expect(screen.queryByText(/乙告警：run_id=/)).toBeNull();
  });

  it("全部失败时明说「0 条确认成功」", async () => {
    const failure = { error: { code: "INTERNAL", message: "服务端错误" } };
    stubAlerts(ALERTS, { "id-1": failure, "id-2": failure });
    renderAlerts();
    await screen.findByText("甲告警");

    select("id-1", "id-2");
    fireEvent.click(screen.getByRole("button", { name: "批量确认 2 条" }));

    await screen.findByText("0 条确认成功，2 条失败");
    // 缺席断言（已变异验证）：一次都没写成，就别把人支去审计页找记录
    expect(screen.queryByText(/审计事件通常几秒内出现在审计页/)).toBeNull();
  });

  it("不可确认的行在点之前就说会跳过，点完之后也不发它的请求", async () => {
    const fetchMock = stubAlerts(ALERTS);
    renderAlerts();
    await screen.findByText("甲告警");

    select("id-1", "id-4");
    // 点之前就说清只会发 1 条——回执里才第一次出现「其实只发了 1 条」已经晚了
    expect(screen.getByRole("button", { name: "批量确认 1 条" })).not.toBeNull();
    expect(screen.getByText("另有 1 条不可确认，将跳过")).not.toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "批量确认 1 条" }));

    await screen.findByText("已确认 1 条，1 条跳过；审计事件通常几秒内出现在审计页");
    expect(
      screen.getByText("丁告警（已静默）：当前状态「已静默」不可确认，只有未处理与复发可以"),
    ).not.toBeNull();
    // 明知会被后端拒的那一条不发出去：既不刷审计，也不在回执里多两条读不懂的错误
    expect(ackedIds(fetchMock)).toEqual(["id-1"]);
  });

  it("选中的全都不可确认时按钮禁用，一个请求都不发", async () => {
    const fetchMock = stubAlerts(ALERTS);
    renderAlerts();
    await screen.findByText("丁告警（已静默）");

    select("id-4");
    const button = screen.getByRole("button", { name: "批量确认 0 条" });
    expect((button as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("选中的 1 条都不可确认（只有未处理与复发可以）")).not.toBeNull();

    fireEvent.click(button);
    await waitFor(() => expect(screen.getByText("已选择 1 条")).not.toBeNull());
    expect(ackedIds(fetchMock)).toEqual([]);
  });

  it("不再说「批量确认随 Foundation-B 上线」——那句话从写下那天起就是错的", async () => {
    // 确认动作声明的是 L0（internal/platform/alerts/actions.go 的 acknowledgeDef），
    // L0 不进审批通道，所以它从来不等 Foundation-B。
    // 缺席断言（已变异验证：把旧那段 span 放回去，本条转红）。
    stubAlerts(ALERTS);
    renderAlerts();
    await screen.findByText("甲告警");

    select("id-1");
    expect(screen.queryByText(/批量确认随 Foundation-B/)).toBeNull();
    expect(screen.queryByText(/这里不执行任何真实操作/)).toBeNull();
  });

  it("单条确认的回执与批量回执互斥，不会同时挂两张", async () => {
    stubAlerts(ALERTS);
    renderAlerts();
    await screen.findByText("甲告警");

    select("id-1");
    fireEvent.click(screen.getByRole("button", { name: "批量确认 1 条" }));
    await screen.findByText("已确认 1 条；审计事件通常几秒内出现在审计页");

    fireEvent.click(screen.getAllByRole("button", { name: "确认" })[0] as HTMLElement);
    await screen.findByText(/已确认，run_id=run-id-1；审计事件通常几秒内出现在审计页/);
    // 缺席断言（已变异验证）：上一张批量名单要让位，否则分不清刚才那一下做了什么
    expect(screen.queryByText("已确认 1 条；审计事件通常几秒内出现在审计页")).toBeNull();
  });
});

// --- 缺口 2：通知子页 -------------------------------------------------------

describe("通知子页（XM-ALERTS-GAPS）", () => {
  const NOTIFY_ALERTS = [
    alert("n-1", "OPEN", "甲告警", "failed"),
    alert("n-2", "SILENCED", "乙告警", "pending"),
    alert("n-3", "RESOLVED", "丙告警", "delivered"),
  ];

  it("接上 /alerts 的投递字段，不再落到通用占位", async () => {
    stubAlerts(NOTIFY_ALERTS);
    renderAlerts("/alerts?sub=notifications");

    expect(await screen.findByRole("heading", { name: "通知", level: 2 })).not.toBeNull();
    // 缺席断言（已变异验证：把 notifications 从三元里去掉，本条转红）
    expect(screen.queryByText("「通知」尚未接入")).toBeNull();
    expect(screen.queryByText("该子页尚未接入稳定的数据源。")).toBeNull();

    // 投递失败的原因原样显示——「为什么没投出去」是这一页唯一要给出的答案
    expect(await screen.findByText("telegram: HTTP 502")).not.toBeNull();
    const table = within(screen.getByRole("table"));
    expect(table.getByText("投递失败")).not.toBeNull();
    expect(table.getByText("未投递")).not.toBeNull();
    expect(table.getByText("已投递")).not.toBeNull();
  });

  it("按投递状态分别计数", async () => {
    stubAlerts(NOTIFY_ALERTS);
    renderAlerts("/alerts?sub=notifications");
    await screen.findByText("telegram: HTTP 502");

    // 三条样本正好一种状态一条，三格都得是 1（少数一条就会有一格是 0）
    for (const label of ["已投递", "投递失败", "未投递"]) {
      expect(within(screen.getByRole("group", { name: label })).getByText("1")).not.toBeNull();
    }
    // 认得出的三种之外没有第四格
    expect(screen.queryByRole("group", { name: "未知状态" })).toBeNull();
  });

  it("认不出的投递状态单独占一格，不并进「未投递」", async () => {
    stubAlerts([
      alert("n-1", "OPEN", "甲告警", "throttled" as AlertNotifyStatus),
      alert("n-2", "OPEN", "乙告警", "pending"),
    ]);
    renderAlerts("/alerts?sub=notifications");
    await screen.findByRole("heading", { name: "通知", level: 2 });

    expect(
      within(await screen.findByRole("group", { name: "未知状态" })).getByText("1"),
    ).not.toBeNull();
    expect(within(screen.getByRole("group", { name: "未投递" })).getByText("1")).not.toBeNull();
  });

  it("把「还没解决又没送达」单独报出来——这是本模块最危险的组合", async () => {
    stubAlerts(NOTIFY_ALERTS);
    renderAlerts("/alerts?sub=notifications");

    // n-1（OPEN/failed）与 n-2（SILENCED/pending）算，n-3（RESOLVED）不算
    expect(await screen.findByText(/有 2 条还没解决的告警没有送达任何人/)).not.toBeNull();
  });

  it("全部送达时不报警", async () => {
    stubAlerts([alert("n-9", "OPEN", "甲告警", "delivered")]);
    renderAlerts("/alerts?sub=notifications");
    // 先等数据真的渲染出来再断言那句话不在。这里原本写的是
    // `await waitFor(() => expect(queryByText(...)).toBeNull())`，而它**恒绿**：
    // waitFor 第一次回调就通过了——那时页面还停在「加载中…」，什么都没渲染。
    // 变异验证（把 unreachedActive 的判断改成恒真）当场抓到了这一点。
    expect(
      within(await screen.findByRole("group", { name: "已投递" })).getByText("1"),
    ).not.toBeNull();

    // 缺席断言（已变异验证）
    expect(screen.queryByText(/条还没解决的告警没有送达任何人/)).toBeNull();
  });

  it("说清数据来自告警本身，以及「已投递」只代表至少一个渠道收下了", async () => {
    // 这一页最容易被当成通知流水来读，而平台今天没有那张表；
    // 「至少一个渠道」是 MultiNotifier.Notify 的取舍（部分失败不进 notify_error）
    stubAlerts(NOTIFY_ALERTS);
    renderAlerts("/alerts?sub=notifications");
    await screen.findByRole("heading", { name: "通知", level: 2 });

    expect(screen.getByText(/不是一份按渠道记账的通知流水/)).not.toBeNull();
    expect(screen.getByText(/至少一个渠道/)).not.toBeNull();
    expect(screen.getByText("notify_status")).not.toBeNull();
  });

  it("从未投递成功的行不留空白——空着会读成「刚刚投的」", async () => {
    stubAlerts([alert("n-1", "OPEN", "甲告警", "failed")]);
    renderAlerts("/alerts?sub=notifications");

    expect(await screen.findByText("从未投递成功")).not.toBeNull();
  });

  it("故障事件仍是占位，不被顺手接上别的数据", async () => {
    // 通知与暂停告警都做出来了，因为它们各自有真实的数据源；故障事件没有，
    // 拿告警凑数就是把活跃告警误当成故障事件。
    // 「暂停告警」为什么不再在这条清单里：见 AlertSilences.test.tsx。
    stubAlerts(ALERTS);
    renderPlaceholder("incidents");
    expect(await screen.findByText("「故障事件」尚未接入")).not.toBeNull();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("暂停告警不再是占位——它有了自己的端点", async () => {
    // 这条与上面那条是一对：同一个渲染入口、同一批 stub，只换子页签。
    // 它防的是「把静默页删掉、退回占位」而上面那条仍然全绿。
    stubSilences([], []);
    renderPlaceholder("silences");

    // 先等一个正向锚点，再同步断言占位不在——占位的缺席若套在 waitFor 里，
    // 第一次回调会在「加载中…」时就通过，那条断言等于恒真。
    expect(await screen.findByRole("heading", { name: "暂停告警", level: 2 })).not.toBeNull();
    expect(screen.queryByText("「暂停告警」尚未接入")).toBeNull();
  });
});

function renderPlaceholder(sub: string) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[`/alerts?sub=${sub}`]}>
        <AlertsPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}
