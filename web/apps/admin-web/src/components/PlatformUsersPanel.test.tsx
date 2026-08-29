import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PlatformUsersPanel } from "./PlatformUsersPanel";

/** 桩的是**线上 snake_case 原始形状**，不是映射后的模型——
 *  这样 api/users.ts 的映射层也一起被测到。 */
function userItem(over: Record<string, unknown> = {}) {
  return {
    id: "u_10241",
    username: "张伟",
    email_masked: "zh***@example.com",
    status: "active",
    balance: { minor_units: "1284500", currency: "CNY" },
    period_recharge: { minor_units: "120000", currency: "CNY" },
    period_consumed: { minor_units: "31200", currency: "CNY" },
    last_30d_consumed: { minor_units: "812000", currency: "CNY" },
    last_active_at: "2026-08-27T09:12:00Z",
    token_prefix: "sk-a1b2",
    ...over,
  };
}

function pageBody(over: Record<string, unknown> = {}) {
  return {
    items: [
      userItem(),
      // 流水缺席的那一条：v1 契约给不出（原型 warnbar）
      userItem({
        id: "u_10221",
        username: "Studio X",
        status: "limited",
        period_recharge: { minor_units: null, currency: "" },
        period_consumed: { minor_units: null, currency: "" },
        last_30d_consumed: { minor_units: null, currency: "" },
        last_active_at: null,
      }),
    ],
    next_cursor: "",
    total_count: { value: 8 },
    total_balance: { minor_units: "18423600", currency: "CNY" },
    active_today: { value: 3 },
    period_totals: {
      recharge: { minor_units: "120000", currency: "CNY" },
      consumed: { minor_units: "31200", currency: "CNY" },
      covered_users: 5,
      total_users: 8,
      complete: false,
    },
    period: { day: "2026-08-27", granularity: "day", from: "2026-08-27", to: "2026-08-27" },
    data_source: "sub2api-fake",
    freshness: {
      state: "fresh",
      staleness_seconds: 3,
      threshold_seconds: 60,
      observed_at: "2026-08-27T09:20:00Z",
      last_success: "2026-08-27T09:20:00Z",
    },
    ...over,
  };
}

/** 记录每次请求的 URL，供断言查询参数。 */
function stubFetch(body: unknown = pageBody()) {
  const urls: string[] = [];
  const fetchMock = vi.fn((input: unknown) => {
    urls.push(String(input));
    return Promise.resolve({
      ok: true,
      status: 200,
      json: () => Promise.resolve(body),
    } as unknown as Response);
  });
  vi.stubGlobal("fetch", fetchMock);
  return urls;
}

function renderPanel(platform = "sub2api", initialEntry = "/") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <PlatformUsersPanel platform={platform} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("统计区间控件（原型 periodControls）", () => {
  it("渲染日期框与日/周/月三个按钮，默认选中「日」", async () => {
    stubFetch();
    renderPanel();

    expect(screen.getByLabelText("统计区间的日期")).toBeTruthy();
    const group = screen.getByRole("group", { name: "统计粒度" });
    const buttons = within(group).getAllByRole("button");
    expect(buttons.map((b) => b.textContent)).toEqual(["日", "周", "月"]);
    // 选中态靠 aria-pressed 而不只是颜色，读屏用户要能听出来
    expect(buttons[0]?.getAttribute("aria-pressed")).toBe("true");
    expect(buttons[1]?.getAttribute("aria-pressed")).toBe("false");
  });

  it("显示的是**服务端回显**的区间，不是本地拼的", async () => {
    stubFetch(
      pageBody({
        period: { day: "2026-08-27", granularity: "week", from: "2026-08-24", to: "2026-08-30" },
      }),
    );
    renderPanel();
    // 周粒度要说出到底是哪七天——跨月那一周最容易被读错
    expect(await screen.findByText("2026-08-24 ~ 2026-08-30 · 按周查看")).toBeTruthy();
  });

  it("切换粒度会带着 granularity 重新取数", async () => {
    const urls = stubFetch();
    renderPanel();
    await screen.findByText("用户总数");

    fireEvent.click(screen.getByRole("button", { name: "月" }));

    await waitFor(() => {
      expect(urls[urls.length - 1] ?? "").toContain("granularity=month");
    });
  });

  it("不选日期时**不传** day —— 「今天」由服务端按 CST 解释", async () => {
    const urls = stubFetch();
    renderPanel();
    await screen.findByText("用户总数");

    expect(urls[0]).not.toContain("day=");
  });

  it("URL 里带 day 时会传给后端", async () => {
    const urls = stubFetch();
    renderPanel("sub2api", "/?day=2026-08-20&granularity=week");
    await screen.findByText("用户总数");

    expect(urls[0]).toContain("day=2026-08-20");
    expect(urls[0]).toContain("granularity=week");
  });

  it("URL 里的坏日期被忽略，不把整页搞崩", async () => {
    // URL 是人能手改的；2026-02-30 若被 new Date 悄悄接受会变成 3 月 2 日
    const urls = stubFetch();
    renderPanel("sub2api", "/?day=2026-02-30&granularity=weekly");
    await screen.findByText("用户总数");

    expect(urls[0]).not.toContain("day=");
    expect(urls[0]).toContain("granularity=day");
  });
});

describe("顶部四格（Sub2API 原型）", () => {
  it("四格逐格对齐原型，用户总数带「今日活跃」副行", async () => {
    stubFetch();
    renderPanel();

    for (const label of ["用户总数", "所有用户总余额", "区间充值", "区间消费（计费额）"]) {
      expect(await screen.findByRole("heading", { name: label, level: 3 })).toBeTruthy();
    }
    expect(screen.getByText("今日活跃 3")).toBeTruthy();
    expect(screen.getByText("含可用余额，不含上游余额")).toBeTruthy();
  });

  it("区间合计覆盖不全时说明它是下界", async () => {
    // 这是本页最要紧的一条：一个只覆盖 5/8 的合计，和真的合计长得一模一样
    stubFetch();
    renderPanel();

    expect(
      await screen.findAllByText("只覆盖 5/8 位用户，实际金额不低于这个数"),
    ).toHaveLength(2);
    expect(screen.getAllByText("合计不全")).toHaveLength(2);
  });

  it("覆盖全时不再挂「合计不全」", async () => {
    stubFetch(
      pageBody({
        period_totals: {
          recharge: { minor_units: "120000", currency: "CNY" },
          consumed: { minor_units: "31200", currency: "CNY" },
          covered_users: 8,
          total_users: 8,
          complete: true,
        },
      }),
    );
    renderPanel();

    expect(await screen.findAllByText("覆盖全部 8 位用户")).toHaveLength(2);
    expect(screen.queryByText("合计不全")).toBeNull();
  });

  it("今日活跃缺席时说「上游没给」，不显示 0", async () => {
    // 「没人活跃」和「不知道」是相反的两件事
    stubFetch(pageBody({ active_today: { value: null } }));
    renderPanel();

    expect(await screen.findByText("上游没给今日活跃数")).toBeTruthy();
  });
});

describe("表格列（原型逐列）", () => {
  it("Sub2API 的列与原型一致，含近30天消费与行尾箭头", async () => {
    stubFetch();
    renderPanel();
    await screen.findByText("用户总数");

    const headers = screen.getAllByRole("columnheader").map((h) => h.textContent?.trim());
    expect(headers).toEqual(
      expect.arrayContaining([
        "用户",
        "邮箱",
        "可用余额",
        "区间充值",
        "区间消费",
        "近30天消费",
        "状态",
        "最后活跃",
      ]),
    );
  });

  it("NewAPI 照它自己的原型：没有「区间充值」列，第四格是「需关注」", async () => {
    // 做成两页的并集会让 NewAPI 多出一列永远是「—」的充值，
    // 那看起来像上游坏了，而它的原型压根没要这一列
    stubFetch();
    renderPanel("newapi");
    await screen.findByText("用户总数");

    const headers = screen.getAllByRole("columnheader").map((h) => h.textContent?.trim());
    expect(headers).not.toContain("区间充值");
    expect(headers).toContain("近30天消费");
    expect(screen.getByRole("heading", { name: "需关注", level: 3 })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "区间充值", level: 3 })).toBeNull();
  });

  it("缺席的流水显示「—」而不是 ¥0.00", async () => {
    // 显示 0 会被读成「这个月一分钱没充」，而事实是我们不知道
    stubFetch();
    renderPanel();
    await screen.findByText("Studio X");

    const row = screen.getByText("Studio X").closest("tr");
    expect(row).toBeTruthy();
    expect(within(row as HTMLElement).getAllByText("—").length).toBeGreaterThanOrEqual(3);
  });

  it("行尾详情动作是有可访问名称的 Link，整行本身不接管点击", async () => {
    stubFetch();
    renderPanel();
    await screen.findByText("张伟");

    const row = screen.getByText("张伟").closest("tr") as HTMLElement;
    const link = within(row).getByRole("link", { name: "查看 张伟 的用户详情" });
    expect(link.getAttribute("href")).toBe("/platforms/sub2api/users/u-755f3130323431");
    // 不把整行做成隐式点击区：键盘与读屏只遇到真正的 Link
    expect(row.onclick).toBeNull();
    expect(row.getAttribute("role")).not.toBe("link");
  });

  it("不透明用户 ID 在详情链接中只占一个 URL 编码段", async () => {
    const opaqueId = "tenant/a?slot=#1% ready";
    stubFetch(pageBody({ items: [userItem({ id: opaqueId, username: "Opaque" })] }));
    renderPanel("newapi");

    const link = await screen.findByRole("link", { name: "查看 Opaque 的用户详情" });
    expect(link.getAttribute("href")).toBe(
      "/platforms/newapi/users/u-74656e616e742f613f736c6f743d233125207265616479",
    );
  });

  it("点段 ID 的 canonical link 不会被 URL 解析器折叠", async () => {
    stubFetch(
      pageBody({
        items: [
          userItem({ id: ".", username: "Dot" }),
          userItem({ id: "..", username: "DotDot" }),
        ],
      }),
    );
    renderPanel();

    const dot = (await screen.findByRole("link", {
      name: "查看 Dot 的用户详情",
    })) as HTMLAnchorElement;
    const dotDot = screen.getByRole("link", {
      name: "查看 DotDot 的用户详情",
    }) as HTMLAnchorElement;
    expect(dot.getAttribute("href")).toBe("/platforms/sub2api/users/u-2e");
    expect(dotDot.getAttribute("href")).toBe("/platforms/sub2api/users/u-2e2e");
    expect(new URL(dot.href).pathname).toBe("/platforms/sub2api/users/u-2e");
    expect(new URL(dotDot.href).pathname).toBe("/platforms/sub2api/users/u-2e2e");
  });

  it("状态文案是原型的「正常 · 注意 · 停用」", async () => {
    stubFetch();
    renderPanel();
    await screen.findByText("张伟");

    // 限定在表格里找：状态筛选下拉里也有同名选项
    const table = screen.getByRole("table");
    expect(within(table).getByText("正常")).toBeTruthy();
    expect(within(table).getByText("注意")).toBeTruthy();
    // 「受限」是改文案之前的说法，不该再出现
    expect(within(table).queryByText("受限")).toBeNull();
  });
});

describe("契约缺口提示条", () => {
  it("照抄原型那句话，并说清逐用户流水来自样本", async () => {
    stubFetch();
    renderPanel();

    // 先等数据落地：加载态的 spinner 也是 role="status"
    await screen.findByText("用户总数");
    const banner = screen
      .getAllByRole("status")
      .find((el) => (el.textContent ?? "").includes("read contract v2"));
    expect(banner).toBeTruthy();
    // JSX 里换行缩进会进 textContent，比对前先把空白压平
    const text = (banner?.textContent ?? "").replace(/\s+/g, "");
    expect(text).toContain(
      "当前只读契约v1仅提供用户总数与总余额；逐用户今日充值、今日消费和消费明细是目标界面，接真实数据前需扩展readcontractv2。".replace(
        /\s+/g,
        "",
      ),
    );
    // fake 供出了这几列，提示条必须说明它们不是真实上游数据
    expect(text).toContain("样本数据源");
  });
});
