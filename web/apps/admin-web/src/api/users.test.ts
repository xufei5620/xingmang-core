import { describe, expect, it, vi } from "vitest";
import { ApiError, FeatureNotMountedError } from "./client";
import goldenText from "../../../../../contracts/testdata/platform-user-ref-v1.json?raw";
import type { ApiClient } from "./client";
import {
  decodePlatformUserIdSegment,
  describeMaskedEmail,
  describeUserStatus,
  encodePlatformUserIdSegment,
  getPlatformUser,
  lookupPlatformUserExact,
  listPlatformUserDailyUsage,
  listPlatformUserKeys,
  listPlatformUsers,
  platformHasUsers,
  UNPARSED_CONTACT,
  type PlatformUserItem,
  type PlatformUserPage,
} from "./users";

describe("用户详情扩展只读查询", () => {
  it("每日消费使用 canonical 用户路径与 day/days 参数", async () => {
    const client: ApiClient = { get: vi.fn().mockResolvedValue({}), post: vi.fn() };
    await listPlatformUserDailyUsage("sub2api", "u_10241", { day: "2026-08-29", days: 7 }, client);
    expect(client.get).toHaveBeenCalledWith(
      "/api/v1/platforms/sub2api/users/u-755f3130323431/daily-usage",
      expect.objectContaining({ searchParams: { day: "2026-08-29", days: "7" } }),
    );
  });

  it("Key 查询只发送 limit/cursor，不构造或返回完整 Key", async () => {
    const client: ApiClient = { get: vi.fn().mockResolvedValue({ items: [] }), post: vi.fn() };
    await listPlatformUserKeys("sub2api", "u_10241", { limit: 50, cursor: "next-1" }, client);
    expect(client.get).toHaveBeenCalledWith(
      "/api/v1/platforms/sub2api/users/u-755f3130323431/keys",
      expect.objectContaining({ searchParams: { limit: "50", cursor: "next-1" } }),
    );
    expect(JSON.stringify((client.get as ReturnType<typeof vi.fn>).mock.calls[0])).not.toMatch(/secret|plaintext|complete.?key/i);
  });

  it("每日消费：XM_PLATFORM_USERS_MODE=off 时的 404（没有 error.code）转成 FeatureNotMountedError", async () => {
    const notMounted: ApiClient = {
      get: vi.fn().mockRejectedValue(new ApiError(404, "UNKNOWN", "请求失败（HTTP 404）")),
      post: vi.fn(),
    };
    const error = await listPlatformUserDailyUsage("sub2api", "u_10241", {}, notMounted).then(
      () => null,
      (e: unknown) => e,
    );
    expect(error).toBeInstanceOf(FeatureNotMountedError);
    expect((error as FeatureNotMountedError).description).toContain("XM_PLATFORM_USERS_MODE=off");
  });

  it("每日消费：具体用户没有记录的结构化 404（带 error.code）原样抛出，不被误判成未接入", async () => {
    const notFound: ApiClient = {
      get: vi.fn().mockRejectedValue(new ApiError(404, "ACTION_NOT_REGISTERED", "没有这条用户记录")),
      post: vi.fn(),
    };
    const error = await listPlatformUserDailyUsage("sub2api", "u_10241", {}, notFound).then(
      () => null,
      (e: unknown) => e,
    );
    expect(error).toBeInstanceOf(ApiError);
    expect(error).not.toBeInstanceOf(FeatureNotMountedError);
  });

  it("Key 元数据：XM_PLATFORM_USERS_MODE=off 时的 404（没有 error.code）转成 FeatureNotMountedError", async () => {
    const notMounted: ApiClient = {
      get: vi.fn().mockRejectedValue(new ApiError(404, "UNKNOWN", "请求失败（HTTP 404）")),
      post: vi.fn(),
    };
    const error = await listPlatformUserKeys("sub2api", "u_10241", {}, notMounted).then(
      () => null,
      (e: unknown) => e,
    );
    expect(error).toBeInstanceOf(FeatureNotMountedError);
    expect((error as FeatureNotMountedError).description).toContain("XM_PLATFORM_USERS_MODE=off");
  });

  it("Key 元数据：具体用户没有记录的结构化 404（带 error.code）原样抛出，不被误判成未接入", async () => {
    const notFound: ApiClient = {
      get: vi.fn().mockRejectedValue(new ApiError(404, "ACTION_NOT_REGISTERED", "没有这条用户记录")),
      post: vi.fn(),
    };
    const error = await listPlatformUserKeys("sub2api", "u_10241", {}, notFound).then(
      () => null,
      (e: unknown) => e,
    );
    expect(error).toBeInstanceOf(ApiError);
    expect(error).not.toBeInstanceOf(FeatureNotMountedError);
  });
});

describe("不透明用户 ID 的 URL 路径段 codec", () => {
  const golden = JSON.parse(goldenText) as Array<{ id: string; segment: string }>;

  it.each(golden)("与 Go 共用 golden：%j", ({ id, segment }) => {
    expect(encodePlatformUserIdSegment(id)).toBe(segment);
    expect(decodePlatformUserIdSegment(segment)).toBe(id);
  });
  it.each([
    ".",
    "..",
    "u-2e",
    "u-2e2e",
    "%2E",
    "~..",
    "/",
    "?",
    "#",
    "%",
    "中文",
    "含 空格",
    "tenant/a?#% 中文",
  ])(
    "%j 做 UTF-8 round-trip，且浏览器不会把路径段归一化掉",
    (id) => {
      const segment = encodePlatformUserIdSegment(id);
      expect(segment).toMatch(/^u-[0-9a-f]+$/);
      expect(decodePlatformUserIdSegment(segment)).toBe(id);

      const url = new URL(`/platforms/sub2api/users/${segment}`, "https://ops.example.test");
      expect(url.pathname).toBe(`/platforms/sub2api/users/${segment}`);
    },
  );

  it("空 ID 与非法/非规范编码 fail closed", () => {
    expect(() => encodePlatformUserIdSegment("")).toThrow(/不能为空/);
    for (const segment of ["", "u-", "raw-id", "u-0", "u-gg", "u-C2A0", "u-c0af"]) {
      expect(decodePlatformUserIdSegment(segment)).toBeNull();
    }
    const oversized = "a".repeat(513);
    expect(() => encodePlatformUserIdSegment(oversized)).toThrow(/512/);
    expect(decodePlatformUserIdSegment(`u-${"61".repeat(513)}`)).toBeNull();
  });
});

describe("v2 精确用户详情 Query", () => {
  it("只调用 canonical detail endpoint，不扫描用户列表", async () => {
    const user: PlatformUserItem = {
      id: "u_10241", username: "张伟", email_masked: "张***@example.com", status: "active",
      balance: { minor_units: "1284500", currency: "CNY" },
      period_recharge: { minor_units: "120000", currency: "CNY" },
      period_consumed: { minor_units: "31200", currency: "CNY" },
      last_30d_consumed: { minor_units: "812000", currency: "CNY" },
      last_active_at: "2026-08-28T08:00:00Z", token_prefix: "sk-a1b2",
    };
    const client: ApiClient = {
      get: vi.fn().mockResolvedValue({
        ref: { platform: "sub2api", id: "u_10241" }, user, registered_at: null,
        period: { day: "2026-08-28", granularity: "day", from: "2026-08-28", to: "2026-08-28" },
        snapshot: { observed_at: "2026-08-28T09:00:00Z", source: "sub2api-fake", watermark: "wm", is_partial: false },
        capabilities: ["platformusers.user.detail_read"],
      }),
      post: vi.fn(),
    };
    const result = await getPlatformUser("sub2api", "u_10241", {}, client);
    expect(result.kind).toBe("found");
    expect(client.get).toHaveBeenCalledTimes(1);
    expect(String((client.get as ReturnType<typeof vi.fn>).mock.calls[0]?.[0])).toBe(
      "/api/v1/platforms/sub2api/users/u-755f3130323431",
    );
    expect(String((client.get as ReturnType<typeof vi.fn>).mock.calls[0]?.[0])).not.toContain("?q=");
  });

  it("404 转成确定的 notFound，502 保留给页面错误态", async () => {
    const notFound: ApiClient = { get: vi.fn().mockRejectedValue(new ApiError(404, "ACTION_NOT_REGISTERED", "没有这条用户记录")), post: vi.fn() };
    expect((await getPlatformUser("newapi", "20031", {}, notFound)).kind).toBe("notFound");
    const failed: ApiClient = { get: vi.fn().mockRejectedValue(new ApiError(502, "EXECUTION_FAILED", "用户精确查找未完成，请重试")), post: vi.fn() };
    await expect(getPlatformUser("newapi", "20031", {}, failed)).rejects.toBeInstanceOf(ApiError);
  });

  it("XM_PLATFORM_USERS_MODE=off 时的 404（没有 error.code）转成 FeatureNotMountedError，不是 notFound", async () => {
    // chi 对没挂载的路由回纯文本 404，客户端解析不出 JSON，code 落回 UNKNOWN——
    // 这与「具体这个用户不存在」的 ACTION_NOT_REGISTERED 404 结构不同
    // （见上一个用例），不能把两者都判成「没有这个用户」
    const notMounted: ApiClient = {
      get: vi.fn().mockRejectedValue(new ApiError(404, "UNKNOWN", "请求失败（HTTP 404）")),
      post: vi.fn(),
    };
    const error = await getPlatformUser("newapi", "20031", {}, notMounted).then(
      () => null,
      (e: unknown) => e,
    );
    expect(error).toBeInstanceOf(FeatureNotMountedError);
    expect((error as FeatureNotMountedError).description).toContain("XM_PLATFORM_USERS_MODE=off");
  });
});

describe("listPlatformUsers", () => {
  it("XM_PLATFORM_USERS_MODE=off 时的 404（没有 error.code）转成 FeatureNotMountedError", async () => {
    const client: ApiClient = {
      get: vi.fn().mockRejectedValue(new ApiError(404, "UNKNOWN", "请求失败（HTTP 404）")),
      post: vi.fn(),
    };
    const error = await listPlatformUsers("sub2api", {}, client).then(
      () => null,
      (e: unknown) => e,
    );
    expect(error).toBeInstanceOf(FeatureNotMountedError);
    expect((error as FeatureNotMountedError).description).toContain("XM_PLATFORM_USERS_MODE=off");
  });

  it("其它错误原样抛出，不被误判成未接入", async () => {
    const client: ApiClient = {
      get: vi.fn().mockRejectedValue(new ApiError(403, "PERMISSION_DENIED", "缺少权限 platform.users.read")),
      post: vi.fn(),
    };
    const error = await listPlatformUsers("sub2api", {}, client).then(
      () => null,
      (e: unknown) => e,
    );
    expect(error).toBeInstanceOf(ApiError);
    expect(error).not.toBeInstanceOf(FeatureNotMountedError);
  });

  it("items 为 null 时按空数组处理，页面不会炸", async () => {
    const client: ApiClient = {
      get: vi.fn().mockResolvedValue({ items: null, next_cursor: "" }),
      post: vi.fn(),
    };
    const page = (await listPlatformUsers("sub2api", {}, client)) as PlatformUserPage;
    expect(page.items).toEqual([]);
  });
});

function user(id: string): PlatformUserItem {
  return {
    id,
    username: `user ${id}`,
    email_masked: "u***@example.com",
    status: "active",
    balance: { minor_units: "0", currency: "CNY" },
    period_recharge: { minor_units: null, currency: "" },
    period_consumed: { minor_units: null, currency: "" },
    last_30d_consumed: { minor_units: null, currency: "" },
    last_active_at: null,
    token_prefix: "",
  };
}

function page(items: PlatformUserItem[], nextCursor = ""): PlatformUserPage {
  return {
    items,
    next_cursor: nextCursor,
    total_count: { value: items.length },
    total_balance: { minor_units: "0", currency: "CNY" },
    active_today: { value: null },
    period_totals: {
      recharge: { minor_units: null, currency: "" },
      consumed: { minor_units: null, currency: "" },
      covered_users: 0,
      total_users: items.length,
      complete: false,
    },
    period: {
      day: "2026-08-28",
      granularity: "day",
      from: "2026-08-28",
      to: "2026-08-28",
    },
    data_source: "sub2api-fake",
    freshness: {
      state: "fresh",
      staleness_seconds: 0,
      threshold_seconds: 60,
      is_partial: false,
      observed_at: "2026-08-28T09:00:00Z",
      last_success: "2026-08-28T09:00:00Z",
      last_error_code: "",
    },
  };
}

function clientReturning(...pages: PlatformUserPage[]) {
  const get = vi.fn<ApiClient["get"]>();
  for (const body of pages) get.mockResolvedValueOnce(body);
  return { client: { get, post: vi.fn() } as unknown as ApiClient, get };
}

describe("按不透明 ID 精确查找用户", () => {
  it("模糊结果排在前面时仍只接受 ID 全等的那条", async () => {
    const { client, get } = clientReturning(page([user("acct-42-copy"), user("acct-42")]));

    const result = await lookupPlatformUserExact("sub2api", "acct-42", {}, client);

    expect(result.kind).toBe("found");
    if (result.kind === "found") expect(result.user.id).toBe("acct-42");
    expect(get).toHaveBeenCalledWith(
      "/api/v1/platforms/sub2api/users",
      expect.objectContaining({
        searchParams: expect.objectContaining({ q: "acct-42", limit: "200" }),
      }),
    );
  });

  it("只有过滤结果彻底耗尽后才返回 definite not-found", async () => {
    const { client } = clientReturning(page([user("acct-42-copy")]));

    const result = await lookupPlatformUserExact("sub2api", "acct-42", {}, client);

    expect(result).toMatchObject({ kind: "notFound", pagesScanned: 1 });
  });

  it("把包含路径符号与查询符号的 ID 当不透明查询值，不拼进 API 路径", async () => {
    const opaqueId = "tenant/a?slot=#1% ready";
    const { client, get } = clientReturning(page([user(opaqueId)]));

    const result = await lookupPlatformUserExact("newapi", opaqueId, {}, client);

    expect(result.kind).toBe("found");
    expect(get.mock.calls[0]?.[0]).toBe("/api/v1/platforms/newapi/users");
    expect(get.mock.calls[0]?.[1]?.searchParams?.q).toBe(opaqueId);
  });

  it("继续扫描后续游标并能在第二页找到精确 ID", async () => {
    const { client, get } = clientReturning(
      page([user("acct-42-copy")], "cursor-2"),
      page([user("acct-42")]),
    );

    const result = await lookupPlatformUserExact("sub2api", "acct-42", {}, client);

    expect(result).toMatchObject({ kind: "found", pagesScanned: 2 });
    expect(get.mock.calls[1]?.[1]?.searchParams?.cursor).toBe("cursor-2");
  });

  it("五页后仍有 next_cursor 时返回 incomplete，绝不误报 404", async () => {
    const pages = Array.from({ length: 5 }, (_, index) =>
      page([user(`acct-42-copy-${index}`)], `cursor-${index + 2}`),
    );
    const { client, get } = clientReturning(...pages);

    const result = await lookupPlatformUserExact("sub2api", "acct-42", {}, client);

    expect(result).toMatchObject({ kind: "incomplete", pagesScanned: 5, reason: "pageLimit" });
    expect(get).toHaveBeenCalledTimes(5);
  });

  it("上游重复游标时停止并返回 incomplete，不死循环也不误报 404", async () => {
    const { client, get } = clientReturning(
      page([user("acct-42-copy")], "same-cursor"),
      page([user("acct-42-copy-2")], "same-cursor"),
    );

    const result = await lookupPlatformUserExact("sub2api", "acct-42", {}, client);

    expect(result).toMatchObject({ kind: "incomplete", pagesScanned: 2, reason: "cursorCycle" });
    expect(get).toHaveBeenCalledTimes(2);
  });

  it("同一个 AbortSignal 传播到游标扫描的每一页", async () => {
    const controller = new AbortController();
    const { client, get } = clientReturning(
      page([user("acct-42-copy")], "cursor-2"),
      page([user("acct-42")]),
    );

    await lookupPlatformUserExact("sub2api", "acct-42", { signal: controller.signal }, client);

    expect(get).toHaveBeenCalledTimes(2);
    for (const call of get.mock.calls) expect(call[1]?.signal).toBe(controller.signal);
  });
});

describe("账号状态的展示口径", () => {
  it("三种已知状态各有各的语气", () => {
    expect(describeUserStatus("active")).toMatchObject({ label: "正常", tone: "success" });
    // 原型逐格写的是「注意」（XM-0053 对齐）
    expect(describeUserStatus("limited")).toMatchObject({ label: "注意", tone: "warning" });
    expect(describeUserStatus("disabled")).toMatchObject({ label: "停用", tone: "neutral" });
  });

  it("不认识的状态按需关注处理，**不回落成正常**", () => {
    // 把一个被停用的账号显示成绿色「正常」，人会据此排除掉真正的故障原因
    const shown = describeUserStatus("shadow_banned");
    expect(shown.label).toBe("未知");
    expect(shown.tone).not.toBe("success");
    // 原始值进悬停：前端不认识不等于运维不认识
    expect(shown.hint).toContain("shadow_banned");
  });
});

describe("打码邮箱的展示口径", () => {
  it("有值就原样显示——二次处理会让「脱敏在哪一层做」变成两个答案", () => {
    expect(describeMaskedEmail("zh***@example.com").text).toBe("zh***@example.com");
  });

  it("「上游没记」与「记了但形状不认识」分开说", () => {
    // 压成一个「—」会把「我们没解析出来」这条要有人看一眼的信号盖掉
    expect(describeMaskedEmail("").text).toBe("—");
    expect(describeMaskedEmail(UNPARSED_CONTACT).text).toBe("格式不认识");
    expect(describeMaskedEmail("").text).not.toBe(describeMaskedEmail(UNPARSED_CONTACT).text);
  });

  it("每一种都带一句悬停说明", () => {
    for (const raw of ["zh***@example.com", "", UNPARSED_CONTACT]) {
      expect(describeMaskedEmail(raw).hint).toBeTruthy();
    }
  });
});

describe("哪些平台有终端用户", () => {
  it("只有 Sub2API 与 NewAPI", () => {
    expect(platformHasUsers("sub2api")).toBe(true);
    expect(platformHasUsers("newapi")).toBe(true);
  });

  it("CPA 与服务器没有——给它们挂一个永远空的用户页签等于说「这个平台没有用户」", () => {
    // CPA 是渠道代理，它的「用户」是代理商（语义不同）；服务器根本没有终端用户
    for (const p of ["cpa", "server", "invoice", ""]) {
      expect(platformHasUsers(p)).toBe(false);
    }
  });
});
