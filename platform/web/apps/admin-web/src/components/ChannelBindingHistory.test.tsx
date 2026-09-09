import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChannelBindingHistory } from "./ChannelBindingHistory";

function fakeResponse(body: unknown, status = 200): Response {
  return { ok: status < 400, status, json: () => Promise.resolve(body) } as unknown as Response;
}

/** 用全局 fetch 替身而不是注入 ApiClient：这一条链路要连**真实的 URL 拼装**
 *  一起验，`include_history=true` 少写一个字母，整块历史就永远是空的，而屏幕
 *  上看起来与「这条渠道没绑过」一模一样。 */
function stubBindings(body: unknown) {
  // 形参写出来是为了让 mock.calls 带上参数类型——断言请求 URL 时要取 calls[0][0]
  const fetchMock = vi.fn((url: string) => {
    void url;
    return Promise.resolve(fakeResponse(body));
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function renderHistory(externalChannelId = "channel-a") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <ChannelBindingHistory serviceId="svc-1" externalChannelId={externalChannelId} />
    </QueryClientProvider>,
  );
}

function itemWithHistory(history: unknown[], binding: unknown) {
  return {
    items: [
      {
        channel_ref: { service_id: "svc-1", external_channel_id: "channel-a" },
        channel_name: "OpenAI A",
        binding,
        history,
      },
    ],
    next_cursor: null,
  };
}

const NEWER = {
  id: "bind-2",
  upstream_account_id: "up-新绑定",
  valid_from: "2026-09-01T02:03:04Z",
  provenance: "manual",
  reason: "甲供应商降价，改绑过去",
  created_by: "staff:alice",
};

const OLDER = {
  id: "bind-1",
  upstream_account_id: "up-旧绑定",
  valid_from: "2026-08-20T01:00:00Z",
  provenance: "token_map_backfill",
  reason: "按令牌映射首次确认",
  created_by: "system:backfill",
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("绑定历史区块（后端 include_history 此前零调用）", () => {
  it("两条历史都渲染出来、最新的在前，且只有当前生效那条带标记", async () => {
    const fetchMock = stubBindings(itemWithHistory([NEWER, OLDER], NEWER));
    renderHistory();

    const rows = await screen.findAllByRole("listitem");
    expect(rows).toHaveLength(2);

    // 顺序：服务端 ORDER BY valid_from DESC，前端原样保留。断言逐行内容而不是
    // 「页面上有这两个字符串」——后者在顺序反过来的实现上照样绿
    expect(rows[0]?.textContent).toContain("up-新绑定");
    expect(rows[0]?.textContent).toContain("甲供应商降价，改绑过去");
    expect(rows[1]?.textContent).toContain("up-旧绑定");
    expect(rows[1]?.textContent).toContain("按令牌映射首次确认");

    // 每一行的四个字段都要落到屏幕上
    expect(within(rows[0]!).getByText("2026-09-01 02:03:04 UTC")).not.toBeNull();
    expect(within(rows[0]!).getByText("人工确认")).not.toBeNull();
    expect(within(rows[0]!).getByText("staff:alice")).not.toBeNull();
    expect(within(rows[1]!).getByText("2026-08-20 01:00:00 UTC")).not.toBeNull();
    expect(within(rows[1]!).getByText("令牌映射回填")).not.toBeNull();
    expect(within(rows[1]!).getByText("system:backfill")).not.toBeNull();

    // 「当前生效」只标在与 binding.id 相同的那一条上
    expect(within(rows[0]!).getByText("当前生效")).not.toBeNull();
    expect(within(rows[1]!).queryByText("当前生效")).toBeNull();

    // 请求确实带了 include_history=true——少这一个参数，history 键根本不下发
    const url = String(fetchMock.mock.calls[0]?.[0]);
    expect(url).toContain("/api/v1/finance/platform-channel-bindings");
    expect(url).toContain("include_history=true");
    expect(url).toContain("service_id=svc-1");
  });

  it("解绑之后（binding 为 null）历史照常列出，但没有任何一行标「当前生效」", async () => {
    stubBindings(itemWithHistory([NEWER, OLDER], null));
    renderHistory();

    const rows = await screen.findAllByRole("listitem");
    expect(rows).toHaveLength(2);
    // 缺席型断言：先确认列表真的渲染出来了（上一行），再断言标记不在——
    // 否则「什么都没渲染」也会让这一条恒绿
    expect(screen.queryByText("当前生效")).toBeNull();
  });

  it("从来没绑过：说「没有绑定记录」，而不是「未接入」", async () => {
    stubBindings(itemWithHistory([], null));
    renderHistory();

    expect(await screen.findByText("这条渠道没有绑定记录")).not.toBeNull();
    expect(
      screen.getByText("从来没有确认过上游映射。这是查出来的事实，不是尚未接入。"),
    ).not.toBeNull();
    expect(screen.queryByRole("listitem")).toBeNull();
  });

  it("候选清单里没有这条渠道：说没取到，且指名道姓说是哪条渠道", async () => {
    stubBindings({ items: [], next_cursor: null });
    renderHistory("channel-missing");

    expect(await screen.findByText("没有取到这条渠道的绑定历史")).not.toBeNull();
    expect(
      screen.getByText(/绑定候选清单里没有渠道 channel-missing/),
    ).not.toBeNull();
    // 这一条不能显示成「没有绑定记录」：那是在替服务端下一个我们没查到的结论
    expect(screen.queryByText("这条渠道没有绑定记录")).toBeNull();
  });

  it("说明段写清这份数据的两个盲区：没有失效时间、不含解绑", async () => {
    stubBindings(itemWithHistory([NEWER], NEWER));
    renderHistory();
    await screen.findAllByRole("listitem");

    const note = screen.getByText(/每一行是/);
    expect(note.textContent).toContain("不包含解绑");
    expect(note.textContent).toContain("不要用下一行的生效时间当作上一行的结束时间");
  });

  it("provenance 出现没见过的值时原样显示，不吞成一个看起来正常的中文标签", async () => {
    stubBindings(itemWithHistory([{ ...NEWER, provenance: "imported_v2" }], NEWER));
    renderHistory();

    const rows = await screen.findAllByRole("listitem");
    expect(within(rows[0]!).getByText("imported_v2")).not.toBeNull();
  });
});
