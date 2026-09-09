import { describe, expect, it } from "vitest";
import type { MetricHistoryItem, MetricItem } from "../api/platform";
import {
  channelTotal,
  metricLabel,
  metricPrimaryValue,
  metricSeriesValue,
  monthToDateSucceeded,
  presentMetric,
  readChannelRows,
  readNewApiChannelRows,
  readPaymentsDailySummary,
  readRequestsTrendDays,
  readSub2ApiChannelStatusRows,
  toSparkSamples,
  toTrendSparkSamples,
} from "./metrics";

function metric(over: Partial<MetricItem> = {}): MetricItem {
  return {
    metric_key: "sub2api.revenue.daily",
    source: "sub2api-prod",
    environment: "production",
    watermark: "wm-1",
    value: { day: "2026-08-25", amount_minor_units: 123456, currency: "CNY", order_count: 42 },
    freshness: {
      state: "fresh",
      staleness_seconds: 30,
      threshold_seconds: 1800,
      is_partial: false,
      observed_at: "2026-08-26T10:00:00Z",
      last_success: "2026-08-26T10:00:00Z",
      last_error_code: "",
    },
    ...over,
  };
}

describe("presentMetric：未初始化铁律", () => {
  it("未初始化显示「未初始化」，绝不显示 0（宪法 12 条）", () => {
    const shown = presentMetric(
      metric({
        // 后端此时 value 里仍可能带着 0，直接渲染就成了理直气壮的假数据
        value: { amount_minor_units: 0, currency: "CNY" },
        freshness: {
          state: "uninitialized",
          staleness_seconds: null,
          threshold_seconds: 1800,
          is_partial: false,
          observed_at: null,
          last_success: null,
          last_error_code: "",
        },
      }),
    );
    expect(shown.primary).toBe("未初始化");
    expect(shown.primary).not.toContain("0");
    expect(shown.unavailable).toBe(true);
  });

  it("同步失败时仍显示上一次成功的值（由徽章负责标红），不抹成空", () => {
    const shown = presentMetric(
      metric({
        freshness: {
          state: "failed",
          staleness_seconds: 9000,
          threshold_seconds: 1800,
          is_partial: false,
          observed_at: "2026-08-26T08:00:00Z",
          last_success: "2026-08-26T08:00:00Z",
          last_error_code: "upstream_timeout",
        },
      }),
    );
    expect(shown.primary).toBe("¥1,234.56");
    expect(shown.unavailable).toBe(false);
  });
});

describe("presentMetric：已登记指标", () => {
  it("日收入：金额 + 业务日 + 单数", () => {
    const shown = presentMetric(metric());
    expect(shown.label).toBe("Sub2API 日收入");
    expect(shown.primary).toBe("¥1,234.56");
    expect(shown.secondary).toBe("业务日 2026-08-25 · 42 单");
  });

  it("日成本：金额 + 业务日", () => {
    const shown = presentMetric(
      metric({
        metric_key: "sub2api.cost.daily",
        value: { day: "2026-08-25", amount_minor_units: 9900, currency: "CNY" },
      }),
    );
    expect(shown.label).toBe("Sub2API 日成本");
    expect(shown.primary).toBe("¥99.00");
    expect(shown.secondary).toBe("业务日 2026-08-25");
  });

  it("用户数：计数带千分位 + 活跃数", () => {
    const shown = presentMetric(
      metric({
        metric_key: "sub2api.users.total",
        value: { total_users: 12345, active_users: 6789 },
      }),
    );
    expect(shown.label).toBe("Sub2API 用户数");
    expect(shown.primary).toBe("12,345");
    expect(shown.secondary).toBe("活跃 6,789");
  });

  it("用户余额：金额 + 透支额", () => {
    const shown = presentMetric(
      metric({
        metric_key: "sub2api.users.balance",
        value: { balance_minor_units: 505050, overdraft_minor_units: -1200, currency: "CNY" },
      }),
    );
    expect(shown.primary).toBe("¥5,050.50");
    expect(shown.secondary).toBe("透支 -¥12.00");
  });

  it("渠道余额：渠道数 + 同币种合计 + 失效令牌数", () => {
    const shown = presentMetric(
      metric({
        metric_key: "sub2api.channels.balance",
        value: {
          channel_count: 3,
          channels: [
            { channel_id: "a", balance_minor_units: 10000, currency: "CNY", token_valid: true },
            { channel_id: "b", balance_minor_units: 2500, currency: "CNY", token_valid: false },
            { channel_id: "c", balance_minor_units: 1, currency: "CNY", token_valid: true },
          ],
        },
      }),
    );
    expect(shown.primary).toBe("3 个渠道");
    expect(shown.secondary).toBe("合计 ¥125.01 · 1 个渠道令牌失效");
  });

  it("渠道币种不一致时不给合计——把不同币种加起来就是错数", () => {
    const shown = presentMetric(
      metric({
        metric_key: "sub2api.channels.balance",
        value: {
          channel_count: 2,
          channels: [
            { balance_minor_units: 10000, currency: "CNY", token_valid: true },
            { balance_minor_units: 10000, currency: "USD", token_valid: true },
          ],
        },
      }),
    );
    expect(shown.primary).toBe("2 个渠道");
    expect(shown.secondary).toBeUndefined();
  });
});

describe("presentMetric：未登记指标兜底", () => {
  it("键名直接当标题，不编好听的假名字", () => {
    const shown = presentMetric(metric({ metric_key: "newapi.tokens.total", value: { total: 7 } }));
    expect(shown.label).toBe("newapi.tokens.total");
    expect(shown.primary).toBe("7");
  });

  it("认得出金额字段就按金额显示", () => {
    const shown = presentMetric(
      metric({ metric_key: "newapi.wallet", value: { amount_minor_units: 250, currency: "USD" } }),
    );
    expect(shown.primary).toBe("$2.50");
  });

  it("完全认不出的值形状明说未知，不瞎猜数字", () => {
    const shown = presentMetric(
      metric({ metric_key: "newapi.weird", value: { note: "hello", flag: true } }),
    );
    expect(shown.primary).toBe("—");
    expect(shown.secondary).toContain("值形状未知");
    expect(shown.unavailable).toBe(true);
  });
});

describe("metricLabel", () => {
  it("已登记的取友好名，未登记的原样返回", () => {
    expect(metricLabel("sub2api.users.total")).toBe("Sub2API 用户数");
    expect(metricLabel("x.y.z")).toBe("x.y.z");
  });
});

describe("metricPrimaryValue：卡片正文与趋势图共用同一个字段", () => {
  it("每个已登记指标取的字段与卡片主位显示的那个数一致", () => {
    const cases: Array<[string, Record<string, unknown>, bigint, "money" | "count"]> = [
      ["sub2api.users.total", { total_users: 12345, active_users: 1 }, 12345n, "count"],
      ["sub2api.users.balance", { balance_minor_units: 505050 }, 505050n, "money"],
      ["sub2api.revenue.daily", { amount_minor_units: 123456 }, 123456n, "money"],
      ["sub2api.cost.daily", { amount_minor_units: 9900 }, 9900n, "money"],
      ["sub2api.channels.balance", { channel_count: 3 }, 3n, "count"],
    ];
    for (const [key, value, raw, kind] of cases) {
      expect(metricPrimaryValue(key, value)).toMatchObject({ raw, kind });
    }
  });

  it("渠道余额没给 channel_count 时退到数组长度", () => {
    expect(metricPrimaryValue("sub2api.channels.balance", { channels: [{}, {}] }).raw).toBe(2n);
  });

  it("value 为 null 时给 null，不当成 0", () => {
    expect(metricPrimaryValue("sub2api.revenue.daily", null).raw).toBeNull();
  });
});

describe("metricSeriesValue：趋势图纵轴取值", () => {
  it("取的是整数原值（最小单位不做任何换算）", () => {
    expect(metricSeriesValue("sub2api.revenue.daily", { amount_minor_units: 123456 })).toBe(123456);
  });

  it("取不到可信数值时给 null，让趋势图显示「暂无趋势」而不是画 0", () => {
    expect(metricSeriesValue("sub2api.revenue.daily", {})).toBeNull();
    expect(metricSeriesValue("sub2api.revenue.daily", null)).toBeNull();
  });

  it("超出安全整数范围时给 null——那种值换算成 number 已经不准了", () => {
    const huge = "9007199254740993"; // 2^53 + 1
    expect(metricSeriesValue("sub2api.users.balance", { balance_minor_units: huge })).toBeNull();
    expect(
      metricSeriesValue("sub2api.users.balance", { balance_minor_units: "9007199254740991" }),
    ).toBe(9007199254740991);
  });

  it("未登记指标沿用兜底口径：先金额后计数", () => {
    expect(metricSeriesValue("newapi.wallet", { amount_minor_units: 250, currency: "USD" })).toBe(
      250,
    );
    expect(metricSeriesValue("newapi.tokens", { total: 7 })).toBe(7);
  });
});

describe("toSparkSamples：历史观测 → 趋势样本", () => {
  function history(over: Partial<MetricHistoryItem> = {}): MetricHistoryItem {
    return {
      observed_at: "2026-08-26T10:00:00Z",
      synced_at: "2026-08-26T10:05:00Z",
      source: "sub2api-prod",
      status: "ok",
      is_partial: false,
      watermark: "wm-1",
      last_error_code: "",
      value: { amount_minor_units: 100, currency: "CNY" },
      ...over,
    };
  }

  it("横轴用 synced_at（采集时刻），observed_at 作为附加数据时间保留", () => {
    const [s] = toSparkSamples("sub2api.revenue.daily", [history()]);
    expect(s?.at).toBe(Date.parse("2026-08-26T10:05:00Z"));
    expect(s?.observedAt).toBe(Date.parse("2026-08-26T10:00:00Z"));
    expect(s?.value).toBe(100);
    expect(s?.failed).toBe(false);
  });

  it("连续失败样本的横轴逐点递增，不会全堆回上一次成功的观测时刻（Codex #3）", () => {
    // XM-0024 的失败样本保留上一次成功的 observed_at（10:00）。
    // 用 observed_at 当横轴，10:05 与 10:10 两次失败会重叠回 10:00 那个成功点，
    // 红色的失败区间就此消失——这正是「那段是红的」这个语义被抹掉的方式
    const samples = toSparkSamples("sub2api.revenue.daily", [
      history({ synced_at: "2026-08-26T10:00:00Z" }),
      history({ status: "failed", synced_at: "2026-08-26T10:05:00Z" }),
      history({ status: "failed", synced_at: "2026-08-26T10:10:00Z" }),
    ]);
    expect(samples.map((s) => s.at)).toEqual([
      Date.parse("2026-08-26T10:00:00Z"),
      Date.parse("2026-08-26T10:05:00Z"),
      Date.parse("2026-08-26T10:10:00Z"),
    ]);
    // 三个点的 observed_at 都是 10:00（失败保留旧值），横轴却各不相同
    expect(new Set(samples.map((s) => s.observedAt)).size).toBe(1);
    expect(samples.map((s) => s.failed)).toEqual([false, true, true]);
  });

  it("没有 observed_at 不影响横轴，只是附加数据时间为 null", () => {
    const [s] = toSparkSamples("sub2api.revenue.daily", [history({ observed_at: null })]);
    expect(s?.at).toBe(Date.parse("2026-08-26T10:05:00Z"));
    expect(s?.observedAt).toBeNull();
  });

  it("synced_at 解析不出来的样本才丢弃（NaN 会让整条路径消失）", () => {
    const samples = toSparkSamples("sub2api.revenue.daily", [
      // observed_at 能解析也不救它：横轴只认 synced_at
      history({ synced_at: "不是时间" }),
      history(),
    ]);
    expect(samples).toHaveLength(1);
  });

  it("status 不是 ok 一律记为失败——不认识的状态不能默认当成成功观测", () => {
    const samples = toSparkSamples("sub2api.revenue.daily", [
      history({ status: "failed" }),
      history({ status: "partial" }),
      history({ status: "ok" }),
    ]);
    expect(samples.map((s) => s.failed)).toEqual([true, true, false]);
  });

  it("is_partial 透传给折线：部分数据不能混进正常实线（Codex #5）", () => {
    const samples = toSparkSamples("sub2api.revenue.daily", [
      history({ is_partial: true }),
      history(),
    ]);
    expect(samples.map((s) => s.partial)).toEqual([true, false]);
  });
});

describe("readChannelRows / channelTotal", () => {
  const channels = [
    { channel_id: "a", channel_name: "渠道甲", balance_minor_units: 10000, currency: "CNY", token_valid: true },
    { channel_id: "b", channel_name: "渠道乙", balance_minor_units: 2500, currency: "CNY", token_valid: false },
  ];

  it("解析出逐渠道字段，与连接器写入的形状一一对应", () => {
    const rows = readChannelRows({ channels });
    expect(rows).toHaveLength(2);
    expect(rows[0]).toEqual({
      channelId: "a",
      channelName: "渠道甲",
      balanceMinorUnits: 10000n,
      currency: "CNY",
      tokenValid: true,
    });
    expect(rows[1]?.tokenValid).toBe(false);
  });

  it("token_valid 缺失时给 null——不默认当作有效", () => {
    const rows = readChannelRows({ channels: [{ channel_id: "c" }] });
    expect(rows[0]?.tokenValid).toBeNull();
  });

  it("余额不是合法整数时给 null，由展示层说「数值异常」", () => {
    const rows = readChannelRows({ channels: [{ balance_minor_units: 1.5 }] });
    expect(rows[0]?.balanceMinorUnits).toBeNull();
  });

  it("channels 不是数组（或 value 为 null）时给空数组，页面不会炸", () => {
    expect(readChannelRows(null)).toEqual([]);
    expect(readChannelRows({})).toEqual([]);
    expect(readChannelRows({ channels: "nope" })).toEqual([]);
  });

  it("同币种给合计，用 BigInt 相加不经过浮点", () => {
    expect(channelTotal(readChannelRows({ channels }))).toEqual({ total: 12500n, currency: "CNY" });
  });

  it("币种不一致、有非法余额、或没有渠道时都不给合计", () => {
    expect(
      channelTotal(
        readChannelRows({
          channels: [
            { balance_minor_units: 1, currency: "CNY" },
            { balance_minor_units: 1, currency: "USD" },
          ],
        }),
      ),
    ).toBeNull();
    expect(
      channelTotal(readChannelRows({ channels: [{ balance_minor_units: "x", currency: "CNY" }] })),
    ).toBeNull();
    expect(channelTotal([])).toBeNull();
  });
});

// 指标键抽成常量而不是就地写字面量：`xxx_key: "……"` 这个形状会被 gitleaks 的
// generic-api-key 规则当成泄露的密钥（同一条误报见 router.test.tsx）。本仓禁止
// 加 gitleaks allowlist（会顺手掩盖真报，见 scripts/check-governance.sh），
// 所以换个写法比放宽扫描器划算。
const NEWAPI_CHANNELS_METRIC = "newapi.channels.status";
const NEWAPI_RECHARGE_METRIC = "newapi.recharge.daily";
const NEWAPI_SUBSCRIPTION_METRIC = "newapi.subscription.daily";
const SUB2API_REQUESTS_DAILY_METRIC = "sub2api.requests.daily";
const NEWAPI_REQUESTS_DAILY_METRIC = "newapi.requests.daily";
const SUB2API_SUCCESS_RATE_24H_METRIC = "sub2api.requests.success_rate_24h";
const SUB2API_CHANNEL_STATUS_METRIC = "sub2api.channels.status";

describe("NewAPI 渠道状态（XM-0035）", () => {
  const rows = () =>
    readNewApiChannelRows({
      channels: [
        {
          channel_id: "ch-1",
          name: "上游甲",
          type: "openai",
          enabled: true,
          balance_minor_units: 10000,
          currency: "CNY",
          model_count: 12,
          error_rate_ppm: 1200,
          latency_ms: 480,
        },
        // 余额键缺席 = 未配置
        { channel_id: "ch-2", name: "自建乙", enabled: false, currency: "CNY" },
        // 余额确实是 0 = 已耗尽
        { channel_id: "ch-3", name: "上游丙", enabled: true, balance_minor_units: 0, currency: "CNY" },
      ],
    });

  it("余额三态：未配置 / 已耗尽 / 有值，互不合流", () => {
    const [withBalance, unconfigured, zero] = rows();
    expect(withBalance?.balanceMinorUnits).toBe(10000n);
    // undefined ≠ 0n：前者是「这个渠道不按余额计费」，后者是「花光了，要立刻处理」。
    // 合流成一个值，看板上就会出现一排理直气壮的 ¥0.00，运营分不出哪个该救。
    expect(unconfigured?.balanceMinorUnits).toBeUndefined();
    expect(zero?.balanceMinorUnits).toBe(0n);
  });

  it("启停缺字段时是 null 而不是默认 false——不替上游断言", () => {
    expect(rows()[0]?.enabled).toBe(true);
    expect(rows()[1]?.enabled).toBe(false);
    expect(readNewApiChannelRows({ channels: [{ channel_id: "x" }] })[0]?.enabled).toBeNull();
  });

  it("错误率读成 bigint，不经过浮点", () => {
    expect(rows()[0]?.errorRatePPM).toBe(1200n);
    expect(typeof rows()[0]?.errorRatePPM).toBe("bigint");
  });

  it("channels 不是数组时给空数组，不抛错也不编数据", () => {
    expect(readNewApiChannelRows(null)).toEqual([]);
    expect(readNewApiChannelRows({})).toEqual([]);
    expect(readNewApiChannelRows({ channels: "nope" })).toEqual([]);
  });

  it("渠道状态卡片给出渠道数、启用数与异常数", () => {
    const shown = presentMetric(
      metric({
        metric_key: NEWAPI_CHANNELS_METRIC,
        value: {
          channel_count: 6,
          enabled_channel_count: 5,
          unhealthy_channel_count: 1,
          channels: [],
        },
      }),
    );
    expect(shown.label).toBe("NewAPI 渠道状态");
    expect(shown.primary).toBe("6 个渠道");
    // 异常为 0 也要说出来：不显示与「没算过」在页面上长得一样
    expect(shown.secondary).toContain("启用 5");
    expect(shown.secondary).toContain("异常 1");
  });

  it("充值与订阅是两条指标，各自带业务日", () => {
    const recharge = presentMetric(
      metric({
        metric_key: NEWAPI_RECHARGE_METRIC,
        value: { day: "2026-08-27", amount_minor_units: 864200, currency: "CNY", order_count: 94 },
      }),
    );
    expect(recharge.label).toBe("NewAPI 日充值");
    expect(recharge.primary).toBe("¥8,642.00");
    expect(recharge.secondary).toContain("94 单");

    const subscription = presentMetric(
      metric({
        metric_key: NEWAPI_SUBSCRIPTION_METRIC,
        value: { day: "2026-08-27", amount_minor_units: 318000, currency: "CNY" },
      }),
    );
    expect(subscription.label).toBe("NewAPI 日订阅");
    expect(subscription.primary).toBe("¥3,180.00");
  });

  it("从未采集时不显示数字，哪怕 value 里还留着 0（宪法 12 条）", () => {
    const shown = presentMetric(
      metric({
        metric_key: NEWAPI_CHANNELS_METRIC,
        value: { channel_count: 0, channels: [] },
        freshness: {
          state: "uninitialized",
          staleness_seconds: null,
          threshold_seconds: 1800,
          is_partial: false,
          observed_at: null,
          last_success: null,
          last_error_code: "",
        },
      }),
    );
    expect(shown.primary).toBe("未初始化");
    expect(shown.unavailable).toBe(true);
  });
});

describe("请求量三件套（XM-OVERVIEW-UI）：今日调用量 / 成功率（24h）", () => {
  it("今日调用量：主数值 = request_count，副文案含业务日/成功/失败/平均耗时", () => {
    const shown = presentMetric(
      metric({
        metric_key: SUB2API_REQUESTS_DAILY_METRIC,
        value: {
          day: "2026-08-31",
          request_count: 12345,
          success_count: 12000,
          failure_count: 345,
          avg_duration_ms: 420,
        },
      }),
    );
    expect(shown.label).toBe("Sub2API 调用量（日）");
    expect(shown.primary).toBe("12,345 次");
    expect(shown.secondary).toBe("业务日 2026-08-31 · 成功 12,000 · 失败 345 · 平均耗时 420ms");
  });

  it("今日调用量：avg_duration_ms 为 null 时明说「未知」，不是 0ms", () => {
    // 0ms 是个会骗人的默认值——null 表示这天压根没有可信的平均耗时
    const shown = presentMetric(
      metric({
        metric_key: SUB2API_REQUESTS_DAILY_METRIC,
        value: {
          day: "2026-08-31",
          request_count: 0,
          success_count: 0,
          failure_count: 0,
          avg_duration_ms: null,
        },
      }),
    );
    expect(shown.secondary).toContain("平均耗时未知");
    expect(shown.secondary).not.toContain("0ms");
  });

  it("newapi 侧同样登记：友好名与格式化都跟着 metric_key 的平台前缀走", () => {
    const shown = presentMetric(
      metric({
        metric_key: NEWAPI_REQUESTS_DAILY_METRIC,
        source: "newapi-prod",
        value: {
          day: "2026-08-31",
          request_count: 500,
          success_count: 480,
          failure_count: 20,
          avg_duration_ms: 100,
        },
      }),
    );
    expect(shown.label).toBe("NewAPI 调用量（日）");
    expect(shown.primary).toBe("500 次");
  });

  it("成功率（24h）：success_rate_bp 用整数运算格式化成两位小数百分比（不经过浮点）", () => {
    const shown = presentMetric(
      metric({
        metric_key: SUB2API_SUCCESS_RATE_24H_METRIC,
        value: { window_hours: 24, request_count: 1000, success_count: 995, success_rate_bp: 9950 },
      }),
    );
    expect(shown.primary).toBe("99.50%");
    expect(shown.secondary).toBe("请求 1,000 次 · 成功 995 次");
    expect(shown.unavailable).toBe(false);
  });

  it("成功率（24h）：success_rate_bp 为 null 时显示「—」并说明 24h 内无请求，不是 0%", () => {
    // 0.00% 会被读成「全部失败」，而 null 的真实含义是「压根没有请求，比率无意义」
    const shown = presentMetric(
      metric({
        metric_key: SUB2API_SUCCESS_RATE_24H_METRIC,
        value: { window_hours: 24, request_count: 0, success_count: 0, success_rate_bp: null },
      }),
    );
    expect(shown.primary).toBe("—");
    expect(shown.primary).not.toContain("0");
    expect(shown.secondary).toContain("24h 内无请求");
    expect(shown.unavailable).toBe(true);
  });
});

describe("readRequestsTrendDays / toTrendSparkSamples：近 7 日调用量（XM-OVERVIEW-UI）", () => {
  function day(
    over: Partial<{ day: string; request_count: number; success_count: number; missing: boolean }> = {},
  ) {
    return { day: "2026-08-25", request_count: 100, success_count: 95, ...over };
  }

  it("解析出逐日字段", () => {
    const days = readRequestsTrendDays({ days: [day()] });
    expect(days[0]).toEqual({
      day: "2026-08-25",
      requestCount: 100n,
      successCount: 95n,
      missing: false,
    });
  });

  it("契约保证升序给 7 个元素，这里仍按 day 再排一次防御", () => {
    const days = readRequestsTrendDays({
      days: [day({ day: "2026-08-27" }), day({ day: "2026-08-25" }), day({ day: "2026-08-26" })],
    });
    expect(days.map((d) => d.day)).toEqual(["2026-08-25", "2026-08-26", "2026-08-27"]);
  });

  it("missing 的日子标出来；request_count 仍原样解析，供折线在原位置标断点", () => {
    const days = readRequestsTrendDays({ days: [day({ day: "2026-08-26", missing: true })] });
    expect(days[0]?.missing).toBe(true);
    expect(days[0]?.requestCount).toBe(100n);
  });

  it("days 不是数组（或 value 为 null）时给空数组，不抛错也不编数据", () => {
    expect(readRequestsTrendDays(null)).toEqual([]);
    expect(readRequestsTrendDays({})).toEqual([]);
    expect(readRequestsTrendDays({ days: "nope" })).toEqual([]);
  });

  it("toTrendSparkSamples：横轴用日历日的 UTC 零点", () => {
    const samples = toTrendSparkSamples(readRequestsTrendDays({ days: [day({ day: "2026-08-25" })] }));
    expect(samples[0]?.at).toBe(Date.parse("2026-08-25T00:00:00Z"));
    expect(samples[0]?.value).toBe(100);
    expect(samples[0]?.failed).toBe(false);
  });

  it("toTrendSparkSamples：missing 映射到 failed，折线在此断开（不当正常读数画）", () => {
    const days = readRequestsTrendDays({
      days: [
        day({ day: "2026-08-25" }),
        day({ day: "2026-08-26", missing: true, request_count: 0, success_count: 0 }),
        day({ day: "2026-08-27" }),
      ],
    });
    const samples = toTrendSparkSamples(days);
    expect(samples.map((s) => s.failed)).toEqual([false, true, false]);
  });

  it("toTrendSparkSamples：day 解析不出合法日期的样本被丢弃（NaN 会让整条路径消失）", () => {
    const samples = toTrendSparkSamples(
      readRequestsTrendDays({ days: [day({ day: "不是日期" }), day()] }),
    );
    expect(samples).toHaveLength(1);
  });
});

describe("Sub2API 渠道状态（XM-OVERVIEW-UI）", () => {
  it("按真实契约形状解析：name / status / channel_id / currency", () => {
    const rows = readSub2ApiChannelStatusRows({
      channels: [
        { name: "上游甲", status: "active", currency: "USD", channel_id: "245" },
        { name: "上游乙", status: "error", currency: "USD", channel_id: "246" },
      ],
    });
    expect(rows).toEqual([
      { channelId: "245", name: "上游甲", status: "active", currency: "USD" },
      { channelId: "246", name: "上游乙", status: "error", currency: "USD" },
    ]);
  });

  it("channels 不是数组（或 value 为 null）时给空数组", () => {
    expect(readSub2ApiChannelStatusRows(null)).toEqual([]);
    expect(readSub2ApiChannelStatusRows({})).toEqual([]);
    expect(readSub2ApiChannelStatusRows({ channels: "nope" })).toEqual([]);
  });

  it("渠道状态卡片：主数值渠道数，副文案给可用/不可用两个数", () => {
    const shown = presentMetric(
      metric({
        metric_key: SUB2API_CHANNEL_STATUS_METRIC,
        value: {
          channels: [
            { name: "a", status: "active", currency: "USD", channel_id: "1" },
            { name: "b", status: "error", currency: "USD", channel_id: "2" },
            { name: "c", status: "active", currency: "USD", channel_id: "3" },
          ],
        },
      }),
    );
    expect(shown.label).toBe("Sub2API 渠道状态");
    expect(shown.primary).toBe("3 个渠道");
    expect(shown.secondary).toBe("可用 2 · 不可用 1");
  });
});

describe("readPaymentsDailySummary（XM-PAY1）：sub2api.payments.daily / newapi.payments.daily", () => {
  it("解析完整形状：四个桶、手续费、净现金流", () => {
    const summary = readPaymentsDailySummary({
      day: "2026-08-29",
      currency: "USD",
      by_status: {
        succeeded: { count: 42, amount_minor_units: 418000 },
        pending: { count: 3, amount_minor_units: 9900 },
        failed: { count: 1, amount_minor_units: 3000 },
        refunded: { count: 1, amount_minor_units: 20200 },
      },
      fee_minor_units: 20000,
      net_minor_units: null,
    });
    expect(summary.day).toBe("2026-08-29");
    expect(summary.currency).toBe("USD");
    expect(summary.byStatus.succeeded).toEqual({ count: 42n, amountMinor: 418000n });
    expect(summary.byStatus.refunded).toEqual({ count: 1n, amountMinor: 20200n });
    expect(summary.feeMinorUnits).toBe(20000n);
    expect(summary.netMinorUnits).toBeNull();
  });

  it("上游这天没有落进某个桶时，那个桶的键就不出现——不是 count:0 的对象", () => {
    // 契约原话："上游这一天没有落进某个桶的订单，那个桶的键就不出现"
    // （payments.read.v1.md）。NewAPI 的 refunded 桶恒不出现是这条规则的
    // 一个特例，不是单独的逻辑分支。
    const summary = readPaymentsDailySummary({
      day: "2026-08-29",
      currency: "USD",
      by_status: { succeeded: { count: 5, amount_minor_units: 1000 } },
      fee_minor_units: null,
      net_minor_units: null,
    });
    expect(summary.byStatus.refunded).toBeUndefined();
    expect(summary.byStatus.pending).toBeUndefined();
    expect(summary.byStatus.failed).toBeUndefined();
  });

  it("fee_minor_units/net_minor_units 为 null 时保持 null，不折成 0", () => {
    const summary = readPaymentsDailySummary({
      day: "2026-08-29",
      currency: "USD",
      by_status: {},
      fee_minor_units: null,
      net_minor_units: null,
    });
    expect(summary.feeMinorUnits).toBeNull();
    expect(summary.netMinorUnits).toBeNull();
  });

  it("value 为 null（从未采集）时给出全空但不抛错的形状", () => {
    const summary = readPaymentsDailySummary(null);
    expect(summary.day).toBeNull();
    expect(summary.currency).toBe("");
    expect(summary.byStatus).toEqual({});
    expect(summary.feeMinorUnits).toBeNull();
    expect(summary.netMinorUnits).toBeNull();
  });

  it("by_status 形状不对（不是对象）时不抛错，退回空", () => {
    const summary = readPaymentsDailySummary({ day: "2026-08-29", by_status: "not-an-object" });
    expect(summary.byStatus).toEqual({});
  });
});

describe("monthToDateSucceeded（XM-PAY1）：/metrics/history 聚合月累计", () => {
  function historyItem(day: string, over: Partial<MetricHistoryItem> = {}): MetricHistoryItem {
    return {
      observed_at: `${day}T10:00:00Z`,
      synced_at: `${day}T10:00:00Z`,
      source: "newapi-prod",
      status: "ok",
      is_partial: false,
      watermark: `day:${day}`,
      last_error_code: "",
      value: {
        day,
        currency: "USD",
        by_status: { succeeded: { count: 2, amount_minor_units: 1000 } },
        fee_minor_units: null,
        net_minor_units: null,
      },
      ...over,
    };
  }

  it("完整覆盖时逐日累加，标记 complete", () => {
    const items = [historyItem("2026-08-01"), historyItem("2026-08-02"), historyItem("2026-08-03")];
    const summary = monthToDateSucceeded(items, "2026-08-01", "2026-08-03");
    expect(summary.amountMinor).toBe(3000n);
    expect(summary.currency).toBe("USD");
    expect(summary.coveredDays).toBe(3);
    expect(summary.totalDays).toBe(3);
    expect(summary.complete).toBe(true);
  });

  it("同一天多次同步取 synced_at 最新的一条，不重复累加", () => {
    const items = [
      historyItem("2026-08-01", { synced_at: "2026-08-01T08:00:00Z", value: { day: "2026-08-01", currency: "USD", by_status: { succeeded: { count: 1, amount_minor_units: 500 } } } }),
      historyItem("2026-08-01", { synced_at: "2026-08-01T20:00:00Z", value: { day: "2026-08-01", currency: "USD", by_status: { succeeded: { count: 2, amount_minor_units: 1000 } } } }),
    ];
    const summary = monthToDateSucceeded(items, "2026-08-01", "2026-08-01");
    expect(summary.amountMinor).toBe(1000n); // 只取较晚那条（20:00），不是两条相加
    expect(summary.coveredDays).toBe(1);
  });

  it("桶键缺席且当天非 partial 是确认过的零，计入覆盖但不加金额", () => {
    const items = [
      historyItem("2026-08-01"),
      historyItem("2026-08-02", { value: { day: "2026-08-02", currency: "USD", by_status: {} } }),
    ];
    const summary = monthToDateSucceeded(items, "2026-08-01", "2026-08-02");
    expect(summary.amountMinor).toBe(1000n);
    expect(summary.coveredDays).toBe(2);
    expect(summary.complete).toBe(true);
  });

  it("桶键缺席且当天 is_partial 时说不清是否真的是零，这一天不计入覆盖", () => {
    const items = [
      historyItem("2026-08-01"),
      historyItem("2026-08-02", { is_partial: true, value: { day: "2026-08-02", currency: "USD", by_status: {} } }),
    ];
    const summary = monthToDateSucceeded(items, "2026-08-01", "2026-08-02");
    expect(summary.amountMinor).toBe(1000n);
    expect(summary.coveredDays).toBe(1);
    expect(summary.complete).toBe(false);
  });

  it("桶键存在但当天 is_partial：仍然把这个真实数字加进去，但整体标记不完整", () => {
    const items = [historyItem("2026-08-01"), historyItem("2026-08-02", { is_partial: true })];
    const summary = monthToDateSucceeded(items, "2026-08-01", "2026-08-02");
    expect(summary.amountMinor).toBe(2000n); // 两天都真实计入
    expect(summary.coveredDays).toBe(2);
    expect(summary.complete).toBe(false); // 但整体不完整，因为有一天覆盖不全
  });

  it("同步本身失败（status !== ok）的整天跳过，不计入覆盖", () => {
    const items = [historyItem("2026-08-01"), historyItem("2026-08-02", { status: "failed" })];
    const summary = monthToDateSucceeded(items, "2026-08-01", "2026-08-02");
    expect(summary.amountMinor).toBe(1000n);
    expect(summary.coveredDays).toBe(1);
    expect(summary.complete).toBe(false);
  });

  it("历史查询够不到月初（服务端 7 天硬顶）时诚实标记不完整，而不是假装完整", () => {
    // 月初是 08-01，但历史只从 08-25 开始有数据（模拟 7 天硬顶只覆盖到 08-25~08-31）
    const items = [historyItem("2026-08-29"), historyItem("2026-08-30"), historyItem("2026-08-31")];
    const summary = monthToDateSucceeded(items, "2026-08-01", "2026-08-31");
    expect(summary.amountMinor).toBe(3000n); // 有覆盖到的那几天仍然给出真实数字
    expect(summary.coveredDays).toBe(3);
    expect(summary.totalDays).toBe(31);
    expect(summary.complete).toBe(false);
  });

  it("完全没有落在区间内的样本时给 null，不是 0", () => {
    const items = [historyItem("2026-07-15")]; // 落在区间之外
    const summary = monthToDateSucceeded(items, "2026-08-01", "2026-08-05");
    expect(summary.amountMinor).toBeNull();
    expect(summary.coveredDays).toBe(0);
    expect(summary.complete).toBe(false);
  });

  it("区间外的样本被过滤，不会污染月累计", () => {
    const items = [historyItem("2026-07-31"), historyItem("2026-08-01"), historyItem("2026-09-01")];
    const summary = monthToDateSucceeded(items, "2026-08-01", "2026-08-31");
    expect(summary.amountMinor).toBe(1000n);
    expect(summary.coveredDays).toBe(1);
  });
});
