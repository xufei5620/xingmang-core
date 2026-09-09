import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PublishingPage } from "./PublishingPage";

function fakeResponse(body: unknown, status = 200): Response {
  return {
    ok: status < 400,
    status,
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(typeof body === "string" ? body : JSON.stringify(body)),
  } as unknown as Response;
}

const CHANNEL_ID = "11111111-1111-4111-8111-111111111111";
const DRAFT_ID = "22222222-2222-4222-8222-222222222222";

/** 那句解释逐字照抄后端常量（internal/platform/publishing/model.go 的
 *  NotDeliveredReason）。**在夹具里写死是刻意的**：这条文案是跨前后端的契约，
 *  fixture 与后端一起改才说明两边同意了同一句话；从前端常量里取会让它自证。 */
const NOT_DELIVERED_REASON =
  "未投递：平台尚无出站投递器，排期与审批是真的，内容没有发送到任何外部平台。";

function channelsBody(canDeliver = false) {
  return {
    items: [
      {
        id: CHANNEL_ID,
        platform: "x",
        handle: "@xingmang",
        display_name: "星芒官方",
        purpose: "产品公告",
        credential_ref: "secret://publishing-x/mkt-token",
        credential_ref_present: true,
        status: "ACTIVE",
        note: "",
        can_deliver: canDeliver,
        updated_at: "2026-09-08T03:00:00Z",
      },
    ],
    limit: 200,
    truncated: false,
    platforms_with_deliverer: canDeliver ? ["x"] : [],
  };
}

function draftsBody() {
  return {
    items: [
      {
        id: DRAFT_ID,
        title: "十月产品更新",
        body: "本月上线了三项能力。",
        status: "SCHEDULED",
        current_version: 2,
        scheduled_at: "2026-10-01T02:00:00Z",
        asset_ids: [],
        created_by: "editor-1",
        updated_at: "2026-09-08T03:00:00Z",
      },
    ],
    limit: 200,
    truncated: false,
  };
}

function recordsBody() {
  return {
    items: [
      {
        id: "33333333-3333-4333-8333-333333333333",
        draft_id: DRAFT_ID,
        draft_version: 2,
        channel_id: CHANNEL_ID,
        scheduled_at: "2026-10-01T02:00:00Z",
        requested_by: "publisher-1",
        result: "NOT_DELIVERED",
        delivered: false,
        external_ref: "",
        detail: NOT_DELIVERED_REASON,
        created_at: "2026-09-08T03:05:00Z",
      },
    ],
    limit: 200,
    truncated: false,
  };
}

function emptyList() {
  return { items: [], limit: 200, truncated: false };
}

function stubFetch(handler: (url: string) => Response) {
  const fetchImpl = vi.fn((input: string) => Promise.resolve(handler(input)));
  vi.stubGlobal("fetch", fetchImpl);
  return fetchImpl;
}

/** 默认桩。canDeliver 是**这一组用例的变异开关**——把它拨到 true，
 *  「没有出站投递器」那几条断言必须变红。 */
function makeHandler(canDeliver = false) {
  return (url: string): Response => {
    if (url.includes("/api/v1/publishing/channels")) return fakeResponse(channelsBody(canDeliver));
    if (url.includes("/api/v1/publishing/drafts")) return fakeResponse(draftsBody());
    if (url.includes("/api/v1/publishing/records")) return fakeResponse(recordsBody());
    if (url.includes("/api/v1/publishing/assets")) return fakeResponse(emptyList());
    if (url.includes("/api/v1/approvals")) return fakeResponse(emptyList());
    throw new Error(`测试没有为这个地址准备响应：${url}`);
  };
}

function renderPublishing(initialEntry = "/ext/publishing") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[initialEntry]}>
        <PublishingPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("内容发布页", () => {
  it("五格页签都在，默认落在内容日历", async () => {
    stubFetch(makeHandler());
    renderPublishing();

    for (const label of ["内容日历", "草稿与素材", "审批队列", "渠道与账号", "发布记录"]) {
      expect(screen.getByRole("tab", { name: label })).toBeTruthy();
    }
    expect(screen.getByRole("tab", { name: "内容日历", selected: true })).toBeTruthy();
  });

  it("页头常驻门禁：说清排期与审批是真的、内容发不出去、站内公告为什么不在这里", async () => {
    stubFetch(makeHandler());
    renderPublishing();

    // 先 await 正向锚点：门禁那条横幅确实渲染出来了。
    const gate = await screen.findByText(/但平台还没有任何出站投递器/);
    expect(gate).toBeTruthy();
    // 只断「有这么一句」不够，几件事要逐条说到：
    expect(screen.getByText(/内容不会被发送到 X 或任何外部社交平台/)).toBeTruthy();
    expect(screen.getByText(/结果为「未投递」的发布记录/)).toBeTruthy();
    // 站内公告那条线不通，且说得出**为什么**不通（不是「以后再说」）。
    expect(screen.getByText(/ADR-018/)).toBeTruthy();
    expect(screen.getByText(/需要产品负责人另立 ADR/)).toBeTruthy();
  });

  it("渠道那一格逐行写「未接投递器」，而不是只在页头挂一句", async () => {
    stubFetch(makeHandler());
    renderPublishing("/ext/publishing?sub=channels");

    // 正向锚点：这一行确实渲染出来了，凭据引用也原样显示（它不是秘密）。
    const handle = await screen.findByText("@xingmang");
    expect(handle).toBeTruthy();
    expect(screen.getByText("secret://publishing-x/mkt-token")).toBeTruthy();

    // 锚点站住了，下面才是这条用例要证明的事。
    expect(screen.getByText("未接投递器")).toBeTruthy();
    expect(screen.queryByText("可投递")).toBeNull();
  });

  it("发布记录如实说未投递，并原样带出那句解释；平台返回编号与互动数据留列不留数", async () => {
    stubFetch(makeHandler());
    renderPublishing("/ext/publishing?sub=records");

    // 正向锚点：这一条记录真的渲染出来了，而且钉住了版本号。
    expect(await screen.findByText(/十月产品更新（v2）/)).toBeTruthy();
    expect(screen.getByText("publisher-1")).toBeTruthy();

    expect(screen.getByText("未投递")).toBeTruthy();
    // 文案逐字：只断「未投递」这三个字的话，把解释改软成「投递中，请稍后」
    // 不会被发现。
    expect(screen.getByText(NOT_DELIVERED_REASON)).toBeTruthy();
    expect(screen.queryByText("已投递")).toBeNull();
    expect(screen.getByText(/「互动数据」一列留列不留数/)).toBeTruthy();
  });

  it("变异验证：后端说某个平台能投递时，上面那几条「发不出去」的断言不再成立", async () => {
    // 这条用例把变异做成常驻代码而不是一次性手工操作：手工验过一次，下一个人
    // 重写页面时不会再验一次。靶心是同一份夹具的 can_deliver /
    // platforms_with_deliverer 两个字段——它们正是那几条缺席断言的来源。
    stubFetch(makeHandler(true));
    renderPublishing("/ext/publishing?sub=channels");

    // 靶心 1：渠道行改口。
    expect(await screen.findByText("可投递")).toBeTruthy();
    expect(screen.queryByText("未接投递器")).toBeNull();
    // 靶心 2：页头横幅换成另一条，不再是那条警告。
    await waitFor(() =>
      expect(screen.getByText(/出站投递：已接入 x/)).toBeTruthy(),
    );
    expect(screen.queryByText(/但平台还没有任何出站投递器/)).toBeNull();
  });

  it("对照组：同一份夹具不拨那个开关时，仍然是「发不出去」", async () => {
    // 没有这一段，上面那条可能只是因为「这个用例怎么写都过」。
    stubFetch(makeHandler(false));
    renderPublishing("/ext/publishing?sub=channels");

    expect(await screen.findByText("未接投递器")).toBeTruthy();
    expect(screen.queryByText("可投递")).toBeNull();
    expect(screen.getByText(/但平台还没有任何出站投递器/)).toBeTruthy();
  });

  it("内容日历按排期日期分组，且拉的是 status=SCHEDULED", async () => {
    const fetchImpl = stubFetch(makeHandler());
    renderPublishing("/ext/publishing?sub=calendar");

    expect(await screen.findByText("十月产品更新")).toBeTruthy();
    expect(
      JSON.stringify(fetchImpl.mock.calls).includes("status=SCHEDULED"),
    ).toBe(true);
    // 日历不承诺「到点自动发」——那需要一个本片没有的定时投递任务。
    expect(screen.getByText(/到点不会自动发出去/)).toBeTruthy();
  });

  it("审批队列只列本页发起的单，且说清投票与执行不在这里做", async () => {
    stubFetch((url) => {
      if (url.includes("/api/v1/approvals")) {
        return fakeResponse({
          items: [
            {
              id: "appr-1",
              action_id: "publishing.publish.submit",
              action_version: "1",
              risk_level: "L3",
              params: {},
              params_hash: "",
              requester_id: "publisher-1",
              requester_type: "HUMAN",
              reason: "十月产品更新，市场部已确认文案",
              status: "PENDING",
              created_at: "2026-09-08T03:00:00Z",
              expires_at: "2026-09-09T03:00:00Z",
              decisions: [],
              votes_required: 2,
              votes_cast: 0,
              privileged_vote_required: false,
              privileged_vote_cast: false,
            },
            {
              // 别的域的单不该出现在这一格里。
              id: "appr-2",
              action_id: "cards.withdraw.execute",
              action_version: "1",
              risk_level: "L3",
              params: {},
              params_hash: "",
              requester_id: "ops-1",
              requester_type: "HUMAN",
              reason: "把结余提回公司钱包",
              status: "PENDING",
              created_at: "2026-09-08T03:00:00Z",
              expires_at: "2026-09-09T03:00:00Z",
              decisions: [],
              votes_required: 2,
              votes_cast: 0,
              privileged_vote_required: false,
              privileged_vote_cast: false,
            },
          ],
          limit: 200,
          truncated: false,
        });
      }
      return makeHandler()(url);
    });
    renderPublishing("/ext/publishing?sub=approvals");

    // 正向锚点：本页那张单真的列出来了，票数按后端给的显示（不让界面自己数）。
    const table = await screen.findByRole("table");
    expect(within(table).getByText("appr-1")).toBeTruthy();
    expect(within(table).getByText("十月产品更新，市场部已确认文案")).toBeTruthy();
    expect(within(table).getByText("0 / 2")).toBeTruthy();
    // 锚点站住了，才有资格断言另一张单不在这里。
    expect(within(table).queryByText("appr-2")).toBeNull();
    expect(screen.getByText(/投票与触发执行在审批中心页面完成/)).toBeTruthy();
  });

  it("认不出来的 ?sub= 不静默回落到第一格", async () => {
    stubFetch(makeHandler());
    renderPublishing("/ext/publishing?sub=nope");

    expect(await screen.findByText(/「nope」子页尚未接入/)).toBeTruthy();
    // 回落的话这里会渲染出内容日历的页签条——那正是这条要防的事。
    expect(screen.queryByRole("tab", { name: "内容日历" })).toBeNull();
  });
});
