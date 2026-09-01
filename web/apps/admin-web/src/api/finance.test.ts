import { describe, expect, it, vi } from "vitest";
import { ApiError, FeatureNotMountedError } from "./client";
import type { ApiClient } from "./client";
import type { PlatformApiConfig } from "./config";
import {
  describeAccessMethod,
  describeCredential,
  describePaymentStatus,
  getPlatformOrder,
  listChannelSummaries,
  listPlatformOrders,
  listProxyAssets,
  listSubscriptionBatches,
  listUpstreamAccounts,
  listUpstreamSummaries,
  platformHasRefunds,
  platformHasUpstreamRegistry,
  REFUND_LIFECYCLE_STATUSES,
  registerSubscriptionBatch,
  removeTokenMapping,
  setProxyAsset,
  setRechargeRatio,
  setTokenMapping,
  setUpstreamAccount,
} from "./finance";

function fakeClient(body: unknown): ApiClient {
  return {
    get: vi.fn().mockResolvedValue(body),
    post: vi.fn().mockResolvedValue(body),
  } as unknown as ApiClient;
}

const config: PlatformApiConfig = {
  baseUrl: "",
  principalId: "staff_alice",
  principalType: "HUMAN",
  scopes: ["finance.read"],
  environment: "development",
};

describe("listChannelSummaries：null 是「给不出」，不是 0", () => {
  it("三个金额与毛利率的 null 原样保留", async () => {
    const page = await listChannelSummaries(
      {},
      fakeClient({
        items: [
          {
            id: "c1",
            system_type: "sub2api",
            usage_revenue: null,
            supply_cost: null,
            gross_profit: null,
            gross_margin: null,
          },
        ],
        from: "2026-08-28",
        to: "2026-08-28",
      }),
      config,
    );
    const item = page.items[0]!;
    // 折成 0 会让「今天还没入账」看起来像「今天没赚钱」
    expect(item.usageRevenue).toBeNull();
    expect(item.supplyCost).toBeNull();
    expect(item.grossProfit).toBeNull();
    expect(item.grossMargin).toBeNull();
  });

  it("金额带 scale 一起映射——前端不硬编码 6", async () => {
    const page = await listChannelSummaries(
      {},
      fakeClient({
        items: [
          {
            id: "c1",
            supply_cost: { amount_minor: "29990000", currency: "USD", scale: 6 },
          },
        ],
      }),
      config,
    );
    expect(page.items[0]!.supplyCost).toEqual({
      amountMinor: "29990000",
      currency: "USD",
      scale: 6,
    });
  });

  it("后端漏了 scale 时不补一个默认值——补 6 正是这个字段要避免的事", async () => {
    const page = await listChannelSummaries(
      {},
      fakeClient({
        items: [{ id: "c1", supply_cost: { amount_minor: "1", currency: "USD" } }],
      }),
      config,
    );
    expect(Number.isNaN(page.items[0]!.supplyCost!.scale)).toBe(true);
  });

  it("覆盖率缺省按「不完整」处理，页面因此显示提示而不是一个笃定的数", async () => {
    const page = await listChannelSummaries(
      {},
      fakeClient({ items: [{ id: "c1" }] }),
      config,
    );
    expect(page.items[0]!.coverage.complete).toBe(false);
  });

  it("分组倍率缺席时保持 undefined，不折成空串（XM-0049）", async () => {
    // 后端刻意用「不出这个字段」表达「这条渠道没有分组倍率」——
    // 折成 "" 会让它看起来像一个被清空的值，而下一步就是有人写
    // 「空串当 1」，那正是 §10.2 禁止的重复乘算。
    const page = await listChannelSummaries(
      {},
      fakeClient({ items: [{ id: "c1", recharge_ratio: "1.5" }] }),
      config,
    );
    expect(page.items[0]!.groupRate).toBeUndefined();
    expect("groupRate" in page.items[0]!).toBe(false);
  });

  it("分组倍率有值时原样带出（定点字符串，不经浮点）", async () => {
    const page = await listChannelSummaries(
      {},
      fakeClient({ items: [{ id: "c1", group_rate: "1.15", recharge_ratio: "1.5" }] }),
      config,
    );
    expect(page.items[0]!.groupRate).toBe("1.15");
    // 与充值倍率是两个独立的量
    expect(page.items[0]!.rechargeRatio).toBe("1.5");
  });

  it("items 为 null 时按空数组处理，页面不会炸", async () => {
    const page = await listChannelSummaries({}, fakeClient({ items: null }), config);
    expect(page.items).toEqual([]);
  });

  it("不传区间时不往 URL 里塞 from/to（后端据此只取今天）", async () => {
    const client = fakeClient({ items: [] });
    await listChannelSummaries({}, client, config);
    expect(client.get).toHaveBeenCalledWith("/api/v1/finance/channels/summary", {
      searchParams: { environment: "development", from: undefined, to: undefined },
    });
  });
});

describe("listUpstreamSummaries：可用天数与覆盖率", () => {
  it("days 为 null 时 reason 一并带出——没有解释的空位会被读成 bug", async () => {
    const page = await listUpstreamSummaries(
      {},
      fakeClient({
        items: [
          {
            id: "u1",
            supplier_key: "sub2api|https://a.test",
            runway: {
              days: null,
              reason: "no_balance",
              window_days: 7,
              covered_days: 0,
              balance: null,
              balance_observed_at: null,
            },
          },
        ],
        runway_coverage: { total: 1, known: 0, reasons: { no_balance: 1 } },
        runway_thresholds: { critical_days: 5, warning_days: 10, serious_days: 20 },
      }),
      config,
    );
    const item = page.items[0]!;
    expect(item.runway.days).toBeNull();
    expect(item.runway.reason).toBe("no_balance");
    expect(item.supplierKey).toBe("sub2api|https://a.test");
    // 覆盖率与阈值原样带出：前端不硬编码那三个数
    expect(page.runwayCoverage).toEqual({ total: 1, known: 0, reasons: { no_balance: 1 } });
    expect(page.runwayThresholds).toEqual({
      criticalDays: 5,
      warningDays: 10,
      seriousDays: 20,
    });
  });

  it("算得出天数时余额与观测时刻一并带出（§10.4 要求显示观测时间）", async () => {
    const page = await listUpstreamSummaries(
      {},
      fakeClient({
        items: [
          {
            id: "u1",
            runway: {
              days: 105,
              level: "healthy",
              reason: "",
              window_days: 7,
              covered_days: 7,
              daily_average: { amount_minor: "4000000", currency: "USD", scale: 6 },
              balance: { amount_minor: "420000000", currency: "USD", scale: 6 },
              balance_observed_at: "2026-08-28T05:59:00Z",
            },
          },
        ],
      }),
      config,
    );
    const runway = page.items[0]!.runway;
    expect(runway.days).toBe(105);
    expect(runway.level).toBe("healthy");
    expect(runway.balance?.amountMinor).toBe("420000000");
    expect(runway.balanceObservedAt).toBe("2026-08-28T05:59:00Z");
    expect(runway.dailyAverage?.scale).toBe(6);
  });
});

// --- XM-0048 成本登记簿（读 + 四个写 Action）---

/** 取出第一次 post 的实参（noUncheckedIndexedAccess 下 calls[0] 带 undefined）。 */
function firstPost(client: ApiClient): [string, { params: Record<string, unknown> }] {
  const call = (client.post as ReturnType<typeof vi.fn>).mock.calls[0];
  if (!call) throw new Error("没有发出任何 POST 请求");
  return call as [string, { params: Record<string, unknown> }];
}

describe("登记簿读取", () => {
  it("items 为 null 时按空数组处理，页面不会炸", async () => {
    await expect(listUpstreamAccounts({}, fakeClient({ items: null }))).resolves.toEqual([]);
    await expect(
      listProxyAssets(
        {},
        fakeClient({ items: null, truncated: false, limit: 200, as_of: "2026-08-28" }),
      ),
    ).resolves.toEqual({ items: [], truncated: false, limit: 200, as_of: "2026-08-28" });
    await expect(
      listSubscriptionBatches(
        "acc-1",
        {},
        fakeClient({ items: null, truncated: false, limit: 200, as_of: "2026-08-28" }),
      ),
    ).resolves.toEqual({ items: [], truncated: false, limit: 200, as_of: "2026-08-28" });
  });

  it("批次与代理列表保留 truncated / limit / as_of，不把截断页伪装成完整列表", async () => {
    const page = { items: [{ id: "row-1" }], truncated: true, limit: 1, as_of: "2026-08-28" };
    await expect(listSubscriptionBatches("acc-1", {}, fakeClient(page))).resolves.toEqual(page);
    await expect(listProxyAssets({}, fakeClient(page))).resolves.toEqual(page);
  });

  it("批次按 upstream_account_id 过滤——登记簿是跨平台的一张表", async () => {
    const client = fakeClient({ items: [] });
    await listSubscriptionBatches("acc-1", {}, client);
    expect(client.get).toHaveBeenCalledWith("/api/v1/finance/subscription-batches", {
      searchParams: { upstream_account_id: "acc-1" },
    });
  });

  it("**不传 environment**：跨环境读取由后端硬拒，前端猜一个只会换来 403", async () => {
    const client = fakeClient({ items: [] });
    await listUpstreamAccounts({}, client);
    expect(client.get).toHaveBeenCalledWith("/api/v1/finance/upstream-accounts", {});
  });
});

describe("写路径全部走 Action 内核", () => {
  it("四个写操作打的都是 /actions/{id}/versions/1/execute", async () => {
    const cases: Array<[string, (c: ApiClient) => Promise<unknown>]> = [
      ["finance.upstream_account.set", (c) => setUpstreamAccount({ system_type: "sub2api" }, {}, c)],
      [
        "finance.recharge_ratio.set",
        (c) =>
          setRechargeRatio(
            { upstream_account_id: "a", recharge_ratio: "1.2", reason: "上游调价" },
            {},
            c,
          ),
      ],
      [
        "finance.token_map.set",
        (c) => setTokenMapping({ upstream_account_id: "a", upstream_token_id: "t" }, {}, c),
      ],
      [
        "finance.token_map.remove",
        (c) =>
          removeTokenMapping(
            { upstream_account_id: "a", upstream_token_id: "t", reason: "映射写错了" },
            {},
            c,
          ),
      ],
    ];

    for (const [actionId, run] of cases) {
      const client = fakeClient({ action_run_id: "run-1" });
      await run(client);
      const [path] = firstPost(client);
      expect(path).toBe(`/api/v1/actions/${actionId}/versions/1/execute`);
    }
  });

  it("倍率与移除都把 reason 带上——没有理由的改动事后与手滑不可区分", async () => {
    const ratioClient = fakeClient({ action_run_id: "run-1" });
    await setRechargeRatio(
      { upstream_account_id: "a", recharge_ratio: "1.2", reason: "上游 8/26 调价" },
      {},
      ratioClient,
    );
    expect(firstPost(ratioClient)[1].params.reason).toBe("上游 8/26 调价");

    const removeClient = fakeClient({ action_run_id: "run-2" });
    await removeTokenMapping(
      { upstream_account_id: "a", upstream_token_id: "t", reason: "记错渠道" },
      {},
      removeClient,
    );
    expect(firstPost(removeClient)[1].params.reason).toBe("记错渠道");
  });

  it("请求体只有 params——后端 DisallowUnknownFields，多塞一个键会 400", async () => {
    const client = fakeClient({ action_run_id: "run-1" });
    await setUpstreamAccount({ system_type: "sub2api" }, {}, client);
    expect(Object.keys(firstPost(client)[1])).toEqual(["params"]);
  });

  it("订阅批次只调用 register@1，并对白名单外字段 fail closed", async () => {
    const client = fakeClient({});
    const run = await registerSubscriptionBatch(
      {
        upstream_account_id: "acc-1",
        paid_minor: "29990000",
        surcharge_minor: "0",
        currency: "USD",
        starts_on: "2026-08-01",
        expires_on: "2026-08-31",
        account_count: 2,
        proxy_asset_id: "proxy-1",
        // 运行时即使有人绕过 TS 塞入这些键，wrapper 也不能把它们送到 Action。
        environment: "production",
        refunded_minor: "1",
        terminated_on: "2026-08-10",
      } as Parameters<typeof registerSubscriptionBatch>[0] & Record<string, unknown>,
      {},
      client,
    );

    const [path, body] = firstPost(client);
    expect(path).toBe(
      "/api/v1/actions/finance.subscription_batch.register/versions/1/execute",
    );
    expect(body.params).toEqual({
      upstream_account_id: "acc-1",
      paid_minor: "29990000",
      surcharge_minor: "0",
      currency: "USD",
      starts_on: "2026-08-01",
      expires_on: "2026-08-31",
      account_count: 2,
      proxy_asset_id: "proxy-1",
    });
    // 响应没给 run id 就诚实保留空串，不编一个成功回执。
    expect(run.runId).toBe("");
  });

  it("代理新建只调用 set@1，且不传 environment / 退款 / 终止字段", async () => {
    const client = fakeClient({ action_run_id: "run-proxy-create" });
    await setProxyAsset(
      {
        paid_minor: "6200000",
        surcharge_minor: "0",
        currency: "USD",
        opened_on: "2026-08-01",
        expires_on: "2026-08-31",
        shared_account_count: 2,
        buy_platform: "Example",
        buy_address: "https://example.test",
        credential_ref: "secret://finance/proxy-a",
        mounted: false,
        environment: "production",
        refunded_minor: "1",
        terminated_on: "2026-08-10",
      } as Parameters<typeof setProxyAsset>[0] & Record<string, unknown>,
      {},
      client,
    );

    const [path, body] = firstPost(client);
    expect(path).toBe("/api/v1/actions/finance.proxy_asset.set/versions/1/execute");
    expect(body.params).toEqual({
      paid_minor: "6200000",
      surcharge_minor: "0",
      currency: "USD",
      opened_on: "2026-08-01",
      expires_on: "2026-08-31",
      shared_account_count: 2,
      buy_platform: "Example",
      buy_address: "https://example.test",
      credential_ref: "secret://finance/proxy-a",
      mounted: false,
    });
  });

  it("代理编辑只提交可编辑购买字段、CredentialRef、mounted 与 id", async () => {
    const client = fakeClient({ action_run_id: "run-proxy-edit" });
    await setProxyAsset(
      {
        proxy_asset_id: "proxy-1",
        buy_platform: "Example 2",
        buy_address: "manual purchase",
        credential_ref: "secret://finance/proxy-b",
        mounted: false,
        paid_minor: "999999999",
        currency: "CNY",
        opened_on: "2099-01-01",
        shared_account_count: 99,
      } as Parameters<typeof setProxyAsset>[0] & Record<string, unknown>,
      {},
      client,
    );

    expect(firstPost(client)[1].params).toEqual({
      proxy_asset_id: "proxy-1",
      buy_platform: "Example 2",
      buy_address: "manual purchase",
      credential_ref: "secret://finance/proxy-b",
      mounted: false,
    });
  });
});

describe("接入方式的展示口径", () => {
  it("三种方式各有各的成本口径说明，不是一个英文枚举值让人自己猜", () => {
    expect(describeAccessMethod("upstream_key").label).toBe("上游中转");
    expect(describeAccessMethod("official_api").label).toBe("官方 API");
    expect(describeAccessMethod("subscription_account").label).toBe("订阅账号");
    for (const raw of ["upstream_key", "official_api", "subscription_account"]) {
      expect(describeAccessMethod(raw).hint).toBeTruthy();
    }
  });

  it("不认识的枚举值原样显示 + 标注，不猜：前端不认识不等于配置错了", () => {
    const shown = describeAccessMethod("resale_pool");
    expect(shown.label).toBe("resale_pool");
    expect(shown.hint).toContain("resale_pool");
  });
});

describe("凭据的展示口径", () => {
  it("**永远只说状态，绝不显示值**", () => {
    const configured = describeCredential("secret://sub2api/prod-key");
    expect(configured.label).toBe("已配置");
    expect(configured.configured).toBe(true);
  });

  it("没有引用是**正常状态**（newapi 走账号级会话），文案里说清楚", () => {
    const missing = describeCredential("");
    expect(missing.label).toBe("未配置");
    expect(missing.configured).toBe(false);
    expect(missing.hint).toContain("正常状态");
  });
});

describe("哪些平台有「上游管理」这一格", () => {
  it("只有 sub2api / newapi", () => {
    expect(platformHasUpstreamRegistry("sub2api")).toBe(true);
    expect(platformHasUpstreamRegistry("newapi")).toBe(true);
  });

  it("**服务器不算**——它那一格叫「供应商与采购」，说的是机器与机房", () => {
    // 两格的 tab.value 恰好都是 suppliers。少了这道判定，服务器页会渲染出
    // 一张 API 成本登记簿，而且看起来完全正常
    for (const p of ["server", "cpa", ""]) {
      expect(platformHasUpstreamRegistry(p)).toBe(false);
    }
  });
});

const fresh = {
  state: "fresh",
  staleness_seconds: 5,
  threshold_seconds: 60,
  is_partial: false,
  observed_at: "2026-08-27T10:00:00Z",
  last_success: "2026-08-27T10:00:00Z",
  last_error_code: "",
};

function orderItemBody(overrides: Record<string, unknown> = {}) {
  return {
    order_id: "9001",
    created_at: "2026-08-27T10:00:00Z",
    status: "PAID",
    amount: { minor_units: "10000", currency: "USD" },
    method: "alipay",
    user_ref: "a***@example.test",
    upstream_order_ref: "OUT-9001",
    fee: { minor_units: null, currency: "" },
    refund_amount: { minor_units: "0", currency: "USD" },
    ...overrides,
  };
}

describe("listPlatformOrders（XM-PAY1）：逐笔订单查询", () => {
  it("原样透传 items/next_cursor/stats_by_status/freshness，fee 缺席时不是 0", async () => {
    const client = fakeClient({
      items: [orderItemBody()],
      next_cursor: "MTAw",
      stats_by_status: { PAID: { count: 1, amount: { minor_units: "10000", currency: "USD" } } },
      from: "2026-08-27",
      to: "2026-08-27",
      data_source: "sub2api-fake",
      freshness: fresh,
    });
    const page = await listPlatformOrders("sub2api", {}, client);
    expect(page.items).toHaveLength(1);
    expect(page.items[0]!.fee.minor_units).toBeNull();
    expect(page.items[0]!.refund_amount.minor_units).toBe("0");
    expect(page.next_cursor).toBe("MTAw");
    expect(page.stats_by_status["PAID"]).toEqual({ count: 1, amount: { minor_units: "10000", currency: "USD" } });
    expect(page.freshness).toEqual(fresh);
    expect(page.data_source).toBe("sub2api-fake");
  });

  it("缺字段时给出安全缺省，而不是抛错或裸 undefined", async () => {
    const client = fakeClient({});
    const page = await listPlatformOrders("sub2api", {}, client);
    expect(page.items).toEqual([]);
    expect(page.next_cursor).toBe("");
    expect(page.stats_by_status).toEqual({});
    expect(page.from).toBe("");
    expect(page.to).toBe("");
    expect(page.data_source).toBe("");
  });

  it("窗口/状态/游标/limit 原样转成查询参数", async () => {
    const get = vi.fn().mockResolvedValue({ items: [] });
    const client: ApiClient = { get, post: vi.fn() };
    await listPlatformOrders(
      "newapi",
      { day: "2026-08-27", status: "success", cursor: "MTAw", limit: 25 },
      client,
    );
    const [path, options] = get.mock.calls[0]!;
    expect(path).toBe("/api/v1/platforms/newapi/orders");
    expect(options.searchParams).toMatchObject({
      day: "2026-08-27",
      status: "success",
      cursor: "MTAw",
      limit: "25",
    });
  });

  it("XM_PLATFORM_PAYMENTS_MODE=off 的裸 404 转成 FeatureNotMountedError", async () => {
    const client: ApiClient = {
      get: vi.fn().mockRejectedValue(new ApiError(404, "UNKNOWN", "请求失败（HTTP 404）")),
      post: vi.fn(),
    };
    const error = await listPlatformOrders("sub2api", {}, client).then(
      () => null,
      (e: unknown) => e,
    );
    expect(error).toBeInstanceOf(FeatureNotMountedError);
    expect((error as FeatureNotMountedError).description).toContain("XM_PLATFORM_PAYMENTS_MODE=off");
  });

  it("其它错误原样透传，不吞", async () => {
    const client: ApiClient = {
      get: vi.fn().mockRejectedValue(new ApiError(403, "PERMISSION_DENIED", "缺少权限 finance.read")),
      post: vi.fn(),
    };
    await expect(listPlatformOrders("sub2api", {}, client)).rejects.toBeInstanceOf(ApiError);
  });
});

describe("getPlatformOrder（XM-PAY1）：订单详情，找不到不回退到别的订单", () => {
  it("找到时返回 kind:found 与完整详情", async () => {
    const client = fakeClient({
      order: orderItemBody(),
      from: "2026-08-27",
      to: "2026-08-27",
      data_source: "sub2api-fake",
      freshness: fresh,
    });
    const result = await getPlatformOrder("sub2api", "9001", { day: "2026-08-27" }, client);
    expect(result.kind).toBe("found");
    if (result.kind === "found") {
      expect(result.detail.order.order_id).toBe("9001");
      expect(result.detail.from).toBe("2026-08-27");
    }
  });

  it("结构化 404（ACTION_NOT_REGISTERED）转成 kind:notFound，带上后端的安全文案", async () => {
    const client: ApiClient = {
      get: vi.fn().mockRejectedValue(
        new ApiError(404, "ACTION_NOT_REGISTERED", "订单 bogus 在 2026-08-27 至 2026-08-27 窗口内未找到"),
      ),
      post: vi.fn(),
    };
    const result = await getPlatformOrder("sub2api", "bogus", { day: "2026-08-27" }, client);
    expect(result.kind).toBe("notFound");
    if (result.kind === "notFound") {
      expect(result.message).toContain("窗口内未找到");
    }
  });

  it("裸 404（未挂载）转成 FeatureNotMountedError，不是 notFound——两者是完全不同的两件事", async () => {
    const client: ApiClient = {
      get: vi.fn().mockRejectedValue(new ApiError(404, "UNKNOWN", "请求失败（HTTP 404）")),
      post: vi.fn(),
    };
    const error = await getPlatformOrder("sub2api", "9001", {}, client).then(
      () => null,
      (e: unknown) => e,
    );
    expect(error).toBeInstanceOf(FeatureNotMountedError);
  });

  it("非 404 错误原样透传", async () => {
    const client: ApiClient = {
      get: vi.fn().mockRejectedValue(new ApiError(502, "EXECUTION_FAILED", "上游超时")),
      post: vi.fn(),
    };
    await expect(getPlatformOrder("sub2api", "9001", {}, client)).rejects.toBeInstanceOf(ApiError);
  });
});

describe("describePaymentStatus：上游原始状态 → 展示口径", () => {
  it("Sub2API 的状态枚举逐条给出中文与色调", () => {
    expect(describePaymentStatus("PAID")).toMatchObject({ label: "已支付", tone: "success" });
    expect(describePaymentStatus("pending")).toMatchObject({ label: "待处理", tone: "warning" });
    expect(describePaymentStatus("REFUNDED")).toMatchObject({ label: "已退款", tone: "neutral" });
    expect(describePaymentStatus("REFUND_FAILED")).toMatchObject({ label: "退款失败", tone: "danger" });
  });

  it("NewAPI 的状态枚举也认识（小写）", () => {
    expect(describePaymentStatus("success")).toMatchObject({ label: "已到账", tone: "success" });
    expect(describePaymentStatus("expired")).toMatchObject({ label: "已过期", tone: "neutral" });
  });

  it("认不出的枚举值原样显示 + 中性色，不猜", () => {
    expect(describePaymentStatus("SOME_NEW_STATUS")).toEqual({ label: "SOME_NEW_STATUS", tone: "neutral" });
    expect(describePaymentStatus("")).toMatchObject({ label: "状态未知", tone: "neutral" });
  });
});

describe("platformHasRefunds：只有 Sub2API", () => {
  it("sub2api 是 true，newapi 与其它一律 false", () => {
    expect(platformHasRefunds("sub2api")).toBe(true);
    expect(platformHasRefunds("newapi")).toBe(false);
    expect(platformHasRefunds("cpa")).toBe(false);
    expect(platformHasRefunds("")).toBe(false);
  });
});

describe("REFUND_LIFECYCLE_STATUSES：与 paymentStatusBucket 的 refunded 桶判据一致", () => {
  it("六个退款生命周期状态都在集合里，非退款状态不在", () => {
    for (const status of [
      "REFUND_REQUESTED",
      "REFUNDING",
      "REFUND_PENDING",
      "REFUND_FAILED",
      "PARTIALLY_REFUNDED",
      "REFUNDED",
    ]) {
      expect(REFUND_LIFECYCLE_STATUSES.has(status)).toBe(true);
    }
    for (const status of ["PAID", "PENDING", "FAILED", "COMPLETED"]) {
      expect(REFUND_LIFECYCLE_STATUSES.has(status)).toBe(false);
    }
  });
});

describe("渠道汇总与登记簿的 id 是两个空间", () => {
  it("汇总行的 id 是**上游账号**的 UUID，不是被管平台自己的渠道 id", async () => {
    // 这条钉的是一个不能靠类型发现的事实：/finance/channels/summary 一行 =
    // 一个上游账号（后端 §8.5：两个端点同粒度），而渠道管理表的一行来自
    // sub2api/newapi 自己的渠道指标，两边的 id 互不认识。
    // 按 id join 不会报错，只会一条都匹配不上——毛利列全空且看着像「后端没数据」。
    const client = fakeClient({
      items: [{ id: "11111111-1111-4111-8111-111111111111", system_type: "sub2api" }],
    });
    const page = await listChannelSummaries({}, client, config);
    expect(page.items[0]!.id).toMatch(/^[0-9a-f-]{36}$/);
  });
});
