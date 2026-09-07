import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactElement } from "react";
import { RouterProvider, createMemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { devLogin, devLogout } from "./auth";
import { routes } from "./router";

// 指标键抽成常量而不是就地写字面量：`xxx_key: "……"` 这个形状会被 gitleaks 的
// generic-api-key 规则当成泄露的密钥（同一条误报见 pages/OverviewPage.test.tsx
// 与 api/platform.test.ts）。本仓禁止加 gitleaks allowlist（会顺手掩盖真报，
// 见 scripts/check-governance.sh），所以换个写法比放宽扫描器划算。
const REVENUE_METRIC = "sub2api.revenue.daily";
const CHANNEL_BALANCE_METRIC = "sub2api.channels.balance";
const NEWAPI_CHANNELS_METRIC = "newapi.channels.status";
const COST_METRIC = "sub2api.cost.daily";

const metricsBody = {
  items: [
    {
      metric_key: REVENUE_METRIC,
      source: "sub2api-prod",
      environment: "development",
      watermark: "wm-1",
      value: { day: "2026-08-25", amount_minor_units: 123456, currency: "CNY", order_count: 42 },
      freshness: {
        state: "stale",
        staleness_seconds: 7200,
        threshold_seconds: 1800,
        is_partial: false,
        observed_at: "2026-08-26T10:00:00Z",
        last_success: "2026-08-26T10:00:00Z",
        last_error_code: "",
      },
    },
    {
      metric_key: "sub2api.users.balance",
      source: "sub2api-prod",
      environment: "development",
      watermark: "",
      value: { balance_minor_units: 0, overdraft_minor_units: 0, currency: "CNY" },
      freshness: {
        state: "uninitialized",
        staleness_seconds: null,
        threshold_seconds: 1800,
        is_partial: false,
        observed_at: null,
        last_success: null,
        last_error_code: "",
      },
    },
  ],
};

const servicesBody = {
  items: [
    {
      id: "11111111-1111-1111-1111-111111111111",
      service_type: "sub2api",
      instance_id: "sub2api-dev",
      environment: "development",
      endpoint: "https://sub2api.example.com",
      owner: "平台组",
      status: "degraded",
      source_watermark: "wm-1",
      observed_at: "2026-08-26T10:00:00Z",
      stale_seconds: 120,
    },
  ],
};

const ASSURANCE_CHANNEL_REASON =
  "请求审计落盘格式不采集渠道/上游字段，无法按渠道拆分——不是这一片没接，是这条数据源从写入那一刻起就不产出这个维度。";

const assuranceFreshness = {
  state: "fresh",
  staleness_seconds: 0,
  threshold_seconds: 60,
  is_partial: false,
  observed_at: "2026-08-31T10:00:00Z",
  last_success: "2026-08-31T10:00:00Z",
  last_error_code: "",
};

/** 一个空窗口/空业务日的保障聚合——okHandler 的默认兜底用它，不在共用兜底
 *  里编样例数据（与本文件 servers/upstream-accounts 默认空列表同一条规矩），
 *  需要非空断言的用例自己覆盖 stubFetch。 */
function emptyAssuranceAggregate(day?: string) {
  // 显式放宽为 number | null：字面量推断成 null 会让后面按天覆盖的
  // 有样本数据（数字百分位）赋不进去（tsc 报 TS2322）。
  const zeroPercentiles: {
    sample_count: number;
    p50_ms: number | null;
    p95_ms: number | null;
    p99_ms: number | null;
  } = { sample_count: 0, p50_ms: null, p95_ms: null, p99_ms: null };
  return {
    ...(day ? { day } : {}),
    since: "2026-08-31T09:00:00Z",
    until: "2026-08-31T10:00:00Z",
    request_count: 0,
    status_classes: { success: 0, client_error: 0, server_error: 0, disconnected: 0, other: 0 },
    duration_ms: zeroPercentiles,
    ttfb_ms: zeroPercentiles,
    // 同理：空数组会推断成 never[]，按天覆盖时塞不进模型行
    models: [] as Array<Record<string, unknown>>,
    models_truncated: false,
    coverage: { spanned_days: 1, missing_days: 0, bad_lines: 0 },
  };
}

function emptyAssuranceOverviewBody(window: string) {
  return {
    source: "sub2api",
    window,
    ...emptyAssuranceAggregate(),
    channel_breakdown_supported: false,
    channel_breakdown_reason: ASSURANCE_CHANNEL_REASON,
    retention_days: 30,
    freshness: assuranceFreshness,
  };
}

function emptyAssuranceHistoryBody() {
  return {
    source: "sub2api",
    days: Array.from({ length: 7 }, (_, i) => ({
      ...emptyAssuranceAggregate(`2026-08-${25 + i}`),
      missing: false,
    })),
    channel_breakdown_supported: false,
    channel_breakdown_reason: ASSURANCE_CHANNEL_REASON,
    retention_days: 30,
    freshness: assuranceFreshness,
  };
}

/** 非空的保障概览响应，覆盖成功/两类失败/按模型拆分（含一个空模型名）,
 *  供「显示真实数据」这条用例断言。 */
function richAssuranceOverviewBody(window: string) {
  const bigModel = {
    model: "claude-3-opus",
    request_count: 9,
    status_classes: { success: 8, client_error: 1, server_error: 0, disconnected: 0, other: 0 },
    duration_ms: { sample_count: 9, p50_ms: 110, p95_ms: 400, p99_ms: 800 },
    ttfb_ms: { sample_count: 6, p50_ms: 35, p95_ms: 80, p99_ms: 120 },
  };
  const unknownModel = {
    model: "",
    request_count: 3,
    status_classes: { success: 2, client_error: 0, server_error: 1, disconnected: 0, other: 0 },
    duration_ms: { sample_count: 3, p50_ms: 200, p95_ms: 600, p99_ms: 900 },
    ttfb_ms: { sample_count: 2, p50_ms: 60, p95_ms: 100, p99_ms: 140 },
  };
  return {
    source: "sub2api",
    window,
    since: "2026-08-31T09:00:00Z",
    until: "2026-08-31T10:00:00Z",
    request_count: 12,
    status_classes: { success: 10, client_error: 1, server_error: 1, disconnected: 0, other: 0 },
    duration_ms: { sample_count: 12, p50_ms: 120, p95_ms: 480, p99_ms: 900 },
    ttfb_ms: { sample_count: 8, p50_ms: 40, p95_ms: 90, p99_ms: 150 },
    models: [bigModel, unknownModel],
    models_truncated: false,
    channel_breakdown_supported: false,
    channel_breakdown_reason: ASSURANCE_CHANNEL_REASON,
    coverage: { spanned_days: 1, missing_days: 0, bad_lines: 0 },
    retention_days: 30,
    freshness: assuranceFreshness,
  };
}

/** 近 7 天里只有最后一天（今天）有数据，其余 6 天目录缺失——覆盖历史记录
 *  「missing 标记」与「涉及模型数」两条断言。 */
function richAssuranceHistoryBody() {
  const days = Array.from({ length: 7 }, (_, i) => ({
    ...emptyAssuranceAggregate(`2026-08-${25 + i}`),
    missing: true,
  }));
  days[6] = {
    ...emptyAssuranceAggregate("2026-08-31"),
    request_count: 40,
    status_classes: { success: 38, client_error: 1, server_error: 1, disconnected: 0, other: 0 },
    duration_ms: { sample_count: 40, p50_ms: 100, p95_ms: 300, p99_ms: 500 },
    ttfb_ms: { sample_count: 20, p50_ms: 30, p95_ms: 70, p99_ms: 110 },
    models: [
      {
        model: "gpt-4o",
        request_count: 40,
        status_classes: { success: 38, client_error: 1, server_error: 1, disconnected: 0, other: 0 },
        duration_ms: { sample_count: 40, p50_ms: 100, p95_ms: 300, p99_ms: 500 },
        ttfb_ms: { sample_count: 20, p50_ms: 30, p95_ms: 70, p99_ms: 110 },
      },
    ],
    missing: false,
  };
  return {
    source: "sub2api",
    days,
    channel_breakdown_supported: false,
    channel_breakdown_reason: ASSURANCE_CHANNEL_REASON,
    retention_days: 30,
    freshness: assuranceFreshness,
  };
}

/** XM-ASSURE1-ui：检测任务表 / 主动检测历史的空响应，与被动指标同一条
 *  "共用兜底给空、需要非空数据的用例自己覆盖 stubFetch"规矩。 */
function emptyProbeListBody() {
  return {
    platform: "sub2api",
    probes: [] as Array<Record<string, unknown>>,
    freshness: assuranceFreshness,
    assertion_disclaimer: "检测结果为形状与延迟检测，非语义正确性保证。",
    channel_breakdown_supported: true,
  };
}

function emptyProbeHistoryBody() {
  return {
    platform: "sub2api",
    entries: [] as Array<Record<string, unknown>>,
    next_cursor: null,
    freshness: assuranceFreshness,
    channel_breakdown_supported: true,
  };
}

/** 非空的检测任务表：一行 fake 模式已声明、`can_run_now=true`；一行
 *  real 模式因平台 Kill Switch 关闭而 `can_run_now=false`——覆盖"运行
 *  按钮在各 cannot_run_reason 下禁用+tooltip"（设计稿 §8.4）与"未启用
 *  pill 与结果 pill 视觉可区分"两条断言。 */
function richProbeListBody() {
  return {
    platform: "sub2api",
    probes: [
      {
        declaration_id: "decl-1",
        name: "模型指纹",
        channel_ids: ["chn-1"],
        channel_names: ["Claude 官方 API"],
        target_models: ["claude-sonnet-4"],
        policy_text: "按需 · 无定时",
        last_run_at: "2026-08-31T02:30:00Z",
        last_run_status: "ok",
        last_run_verdict: "模型自称与声明一致",
        kill_switch_state: "not_applicable_fake",
        can_run_now: true,
        cannot_run_reason: "",
        cannot_run_reason_text: "",
      },
      {
        declaration_id: "decl-2",
        name: "基准题集",
        channel_ids: ["chn-2"],
        channel_names: ["Azure 东亚"],
        target_models: ["gpt-4o"],
        policy_text: "按需 · 无定时",
        last_run_at: null,
        last_run_status: "never_run",
        last_run_verdict: null,
        kill_switch_state: "disabled",
        can_run_now: false,
        cannot_run_reason: "platform_kill_switch_off",
        cannot_run_reason_text: "该平台检测未启用，请在治理段打开开关",
      },
    ],
    freshness: assuranceFreshness,
    assertion_disclaimer: "检测结果为形状与延迟检测，非语义正确性保证。",
    channel_breakdown_supported: true,
  };
}

function richProbeHistoryBody() {
  return {
    platform: "sub2api",
    entries: [
      {
        observed_at: "2026-08-31T02:30:00Z",
        channel_id: "chn-1",
        external_channel_id: "",
        model: "claude-sonnet-4",
        declaration_name: "模型指纹",
        prompt_template_key: "model_fingerprint",
        status: "ok",
        verdict: "模型自称与声明一致",
        evidence_ref: "probe-8f3a1c2d",
        latency_ms: 12,
        first_token_ms: 5,
        measured_first_token: true,
        tokens_used: 8,
        http_status: 200,
        error_kind: "",
      },
    ],
    next_cursor: null,
    freshness: assuranceFreshness,
    channel_breakdown_supported: true,
  };
}

/** 只实现客户端用到的 ok/status/json 三样，不依赖 jsdom 是否提供 Response。 */
function fakeResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

function stubFetch(handler: (url: string, init?: RequestInit) => Response) {
  const fn = vi.fn((input: string, init?: RequestInit) => Promise.resolve(handler(input, init)));
  vi.stubGlobal("fetch", fn);
  return fn;
}

const channelsBody = {
  items: [
    {
      metric_key: CHANNEL_BALANCE_METRIC,
      source: "sub2api-prod",
      environment: "development",
      watermark: "wm-9",
      value: {
        channel_count: 2,
        channels: [
          {
            channel_id: "ch-a",
            channel_name: "渠道甲",
            balance_minor_units: 10000,
            currency: "CNY",
            token_valid: true,
          },
          {
            channel_id: "ch-b",
            channel_name: "渠道乙",
            balance_minor_units: 2500,
            currency: "CNY",
            token_valid: false,
          },
        ],
      },
      freshness: {
        state: "fresh",
        staleness_seconds: 30,
        threshold_seconds: 1800,
        is_partial: false,
        observed_at: "2026-08-26T10:00:00Z",
        last_success: "2026-08-26T10:00:00Z",
        last_error_code: "",
      },
    },
  ],
};

/** NewAPI 渠道状态指标（XM-0035）。三条渠道刻意覆盖三种边界：
 *  停用、余额未配置、错误率越过判据。 */
const newapiChannelsBody = {
  items: [
    {
      metric_key: NEWAPI_CHANNELS_METRIC,
      source: "newapi-staging",
      environment: "development",
      watermark: "wm-n1",
      value: {
        channel_count: 3,
        enabled_channel_count: 2,
        unhealthy_channel_count: 1,
        unhealthy_threshold_ppm: 50000,
        channels: [
          {
            channel_id: "ch-ok",
            name: "上游甲",
            type: "openai",
            enabled: true,
            balance_minor_units: 10000,
            currency: "CNY",
            model_count: 12,
            error_rate_ppm: 1200,
            latency_ms: 480,
          },
          {
            channel_id: "ch-off",
            name: "上游乙",
            type: "gemini",
            enabled: false,
            balance_minor_units: 2500,
            currency: "CNY",
            model_count: 5,
            error_rate_ppm: 0,
            latency_ms: 0,
          },
          {
            // 余额键**缺席** = 未配置余额（与 balance_minor_units: 0 相反）
            channel_id: "ch-nobal",
            name: "自建丙",
            type: "ollama",
            enabled: true,
            currency: "CNY",
            model_count: 3,
            error_rate_ppm: 187500,
            latency_ms: 2340,
          },
        ],
      },
      freshness: {
        state: "fresh",
        staleness_seconds: 30,
        threshold_seconds: 1800,
        is_partial: false,
        observed_at: "2026-08-26T10:00:00Z",
        last_success: "2026-08-26T10:00:00Z",
        last_error_code: "",
      },
    },
  ],
};

const historyBody = {
  items: [0, 1, 2, 3].map((i) => ({
    observed_at: `2026-08-26T0${i}:00:00Z`,
    synced_at: `2026-08-26T0${i}:05:00Z`,
    source: "sub2api-prod",
    status: i === 2 ? "failed" : "ok",
    is_partial: false,
    watermark: `wm-${i}`,
    last_error_code: i === 2 ? "upstream_timeout" : "",
    value: { amount_minor_units: 1000 + i * 100, currency: "CNY" },
  })),
};

const auditEvent = {
  sequence: 2,
  occurred_at: "2026-08-26T10:00:00Z",
  principal_id: "dev-operator",
  principal_type: "HUMAN",
  action_id: "registry.service.create",
  action_version: "1",
  action_run_id: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
  resource_type: "core.service",
  resource_id: "svc-1",
  environment: "development",
  request_id: "req-7",
  result: "succeeded",
  error_code: "",
  before_summary: null,
  after_summary: { instance_id: "sub2api-dev", status: "active" },
  event_hash: "a".repeat(64),
  prev_hash: "b".repeat(64),
};

/** XM-0033：一条 OPEN 未投递的严重告警 + 一条已静默的警告。
 *
 *  刻意配成这两条：前者是「已触发但没人被通知到」，后者是「静默不是解决」——
 *  两个最容易在界面上被显示错的状态。 */
/** 后台任务概览（XM-JOBS0 / XM-OPS-TAILS0，GET /api/v1/jobs/overview）。
 *  只挂一个 schedule 条目——这里只是验证路由真的挂了 JobsPage，不是
 *  JobsPage.test.tsx 那种逐字段覆盖。 */
const jobsOverviewBody = {
  environment: "development",
  generated_at: "2026-08-31T10:00:00Z",
  schedules: [
    {
      id: "platform_heartbeat", kind: "platform_heartbeat", queue: "maintenance",
      schedule_config_env: "HEARTBEAT_INTERVAL", side_effect_class: "audit_append",
      last_run: null, observed_interval_seconds: null, next_run_estimated_at: null,
      activity: "never_observed",
      configured_enabled: null, configured_interval_seconds: null, configured_source: null,
      configured_mode: null, configured_mode_source: null,
    },
  ],
  queue_backlog: [
    { queue: "default", available: 0, running: 0, retryable: 0, scheduled: 0, completed_24h: 0, discarded: 0 },
    { queue: "maintenance", available: 0, running: 0, retryable: 0, scheduled: 0, completed_24h: 0, discarded: 0 },
  ],
  worker_heartbeat: { last_seen_at: null, seconds_ago: null, state: "", environment: "platform", known: false },
};

const alertsBody = {
  items: [
    {
      id: "aaaa1111-2222-3333-4444-555555555555",
      rule_key: "metric.sync.failed",
      dedup_key: `metric.sync.failed:development:${REVENUE_METRIC}`,
      severity: "critical",
      status: "OPEN",
      title: "指标 sub2api.revenue.daily 同步失败",
      detail: "来源 sub2api-prod，错误码 upstream_timeout",
      environment: "development",
      source_metric_key: REVENUE_METRIC,
      opened_at: "2026-08-26T10:00:00Z",
      last_seen_at: "2026-08-26T10:05:00Z",
      acknowledged_at: null,
      resolved_at: null,
      fire_count: 6,
      notify_status: "failed",
      notify_error: "telegram: HTTP 502",
      notified_at: null,
    },
    {
      id: "bbbb1111-2222-3333-4444-555555555555",
      rule_key: "channel.balance.low",
      dedup_key: "channel.balance.low:development:ch-b",
      severity: "warning",
      status: "SILENCED",
      title: "渠道乙 余额不足",
      detail: "余额 2500 低于阈值 500000（均为最小货币单位，CNY）",
      environment: "development",
      source_metric_key: CHANNEL_BALANCE_METRIC,
      opened_at: "2026-08-26T09:00:00Z",
      last_seen_at: "2026-08-26T10:05:00Z",
      acknowledged_at: null,
      resolved_at: null,
      fire_count: 12,
      notify_status: "pending",
      notify_error: "",
      notified_at: null,
    },
  ],
};

/** 成本看板供数的最小响应体（XM-0037d）。
 *
 *  平台概览页从 XM-0037d 起会拉这两个端点（成本三卡）。给它们一个**空但合法**
 *  的响应，而不是让它们落到 404 分支：那会让「趋势图挂了」这类用例看到一个
 *  与它无关的错误态，然后有人会去改断言而不是去查真正的原因。 */
const financeChannelsBody = { items: [], from: "2026-08-28", to: "2026-08-28" };

/** 渠道管理页的一行 = 一个上游账号（XM-0052）。
 *
 *  两条：一条计量型（有余额与可用天数），一条订阅型（没有余额这个概念）。
 *  这两种在页面上必须长得不一样——订阅型显示 ¥0.00 余额会被当成「花光了」。 */
function financeChannelRow(over: Record<string, unknown> = {}) {
  return {
    id: "acc-metered",
    name: "上游甲",
    system_type: "sub2api",
    access_method: "upstream_key",
    metered: true,
    base_url: "https://relay-a.example.test",
    platform_id: "sub2api",
    credential_ref: "secret://xm/upstream/a",
    recharge_ratio: "1.5",
    recharge_cost_rate: "0.666667",
    business_day_tz: "+08:00",
    status: "active",
    token_count: 1,
    usage_revenue: { amount_minor: "100000000", currency: "CNY", scale: 6 },
    supply_cost: { amount_minor: "70000000", currency: "CNY", scale: 6 },
    gross_profit: { amount_minor: "30000000", currency: "CNY", scale: 6 },
    gross_margin: "0.3",
    coverage: {
      row_count: 1,
      revenue_known_rows: 1,
      cost_known_rows: 1,
      account_grain_rows: 0,
      mixed_currency: false,
      complete: true,
    },
    observed: { source: "test" },
    runway: {
      days: 12,
      level: "serious",
      reason: "",
      window_days: 7,
      covered_days: 7,
      balance: { amount_minor: "3000000000", currency: "CNY", scale: 6 },
      balance_observed_at: "2026-08-28T02:00:00Z",
    },
    ...over,
  };
}

function financeChannelsWith(items: unknown[]) {
  return { items, from: "2026-08-28", to: "2026-08-28" };
}
const financeUpstreamsBody = {
  items: [],
  from: "2026-08-28",
  to: "2026-08-28",
  runway_coverage: { total: 0, known: 0, reasons: {} },
  runway_thresholds: { critical_days: 5, warning_days: 10, serious_days: 20 },
};

/** 用户清单样本。第二条刻意让逐用户流水缺席（minor_units: null）——
 *  原型的 warnbar 说 v1 只读契约给不出，这件事必须能在界面上看见。 */
const usersBody = {
  items: [
    {
      id: "u_10241",
      username: "张伟",
      email_masked: "zh***@example.com",
      status: "active",
      balance: { minor_units: "1284500", currency: "CNY" },
      period_recharge: { minor_units: "120000", currency: "CNY" },
      period_consumed: { minor_units: "31200", currency: "CNY" },
      last_30d_consumed: { minor_units: "812000", currency: "CNY" },
      last_active_at: "2026-08-28T09:00:00Z",
      token_prefix: "sk-a1b2",
    },
    {
      id: "u_10207",
      username: "试用账号 07",
      email_masked: "",
      status: "limited",
      // 已知的零余额：必须显示成 ¥0.00，不能和「上游没给」混同
      balance: { minor_units: "0", currency: "CNY" },
      period_recharge: { minor_units: null, currency: "" },
      period_consumed: { minor_units: null, currency: "" },
      last_30d_consumed: { minor_units: null, currency: "" },
      last_active_at: null,
      token_prefix: "",
    },
  ],
  next_cursor: "",
  total_count: { value: 2 },
  total_balance: { minor_units: "1284500", currency: "CNY" },
  active_today: { value: 1 },
  // 两条里只有一条给得出流水 → 合计是下界（XM-0053）
  period_totals: {
    recharge: { minor_units: "120000", currency: "CNY" },
    consumed: { minor_units: "31200", currency: "CNY" },
    covered_users: 1,
    total_users: 2,
    complete: false,
  },
  period: { day: "2026-08-28", granularity: "day", from: "2026-08-28", to: "2026-08-28" },
  data_source: "sub2api-fake",
  freshness: {
    state: "fresh",
    staleness_seconds: 5,
    threshold_seconds: 60,
    is_partial: false,
    observed_at: "2026-08-28T10:00:00Z",
    last_success: "2026-08-28T10:00:00Z",
    last_error_code: "",
  },
};

function userDetailBody(platform = "sub2api") {
  const user = usersBody.items[0]!;
  return {
    ref: { platform, id: user.id },
    user,
    registered_at: null,
    period: usersBody.period,
    snapshot: {
      observed_at: usersBody.freshness.observed_at,
      source: usersBody.data_source,
      watermark: "wm-detail",
      is_partial: usersBody.freshness.is_partial,
    },
    capabilities: ["platformusers.user.detail_read"],
  };
}

/** Action 目录（XM-ACTIONS0，GET /api/v1/actions）。一条 L1 可执行、一条
 *  L2 不可执行——覆盖「操作目录」与「风险与启用条件」两个子页签都要用到的
 *  executable/blocked_reason 分支。 */
const actionsDirectoryBody = {
  items: [
    {
      id: "registry.service.create",
      version: "1",
      risk_level: "L1",
      permission: "registry.service.manage",
      environments: ["development"],
      principal_types: ["HUMAN"],
      executable: true,
    },
    {
      id: "finance.upstream_account.bulk_disable",
      version: "1",
      risk_level: "L2",
      permission: "finance.upstream_account.manage",
      environments: ["development"],
      principal_types: ["HUMAN"],
      executable: false,
      blocked_reason: "需要 Action Advanced Controls（Foundation-B）",
    },
  ],
};

/** 跨 Action 执行记录一页（XM-ACTIONS0，GET /api/v1/actions/runs）。 */
const actionRunsBody = {
  items: [
    {
      id: "22222222-2222-2222-2222-222222222222",
      action_id: "registry.service.create",
      action_version: "1",
      principal_id: "staff_alice",
      principal_type: "HUMAN",
      environment: "development",
      request_id: "req-1",
      risk_level: "L1",
      status: "succeeded",
      error_code: "",
      duration_ms: 12,
      started_at: "2026-08-29T10:00:00.000000Z",
      finished_at: "2026-08-29T10:00:00.012000Z",
    },
  ],
  next_cursor: "",
};

function okHandler(url: string): Response {
  // 精确匹配在前：/api/v1/actions/runs 与 /api/v1/actions/{id}/versions/{v}/execute
  // 都以 /api/v1/actions 开头，必须先分流，不能让下面任何一条 startsWith
  // 意外吞掉另一条
  if (url === "/api/v1/actions") return fakeResponse(200, actionsDirectoryBody);
  if (url.startsWith("/api/v1/actions/runs")) return fakeResponse(200, actionRunsBody);
  // history 必须排在 metrics 前面：两者的前缀是包含关系
  if (url.startsWith("/api/v1/metrics/history")) return fakeResponse(200, historyBody);
  if (url.startsWith("/api/v1/finance/channels/summary"))
    return fakeResponse(200, financeChannelsBody);
  if (url.startsWith("/api/v1/finance/upstreams/summary"))
    return fakeResponse(200, financeUpstreamsBody);
  // 渠道管理页 2026-09-02 起把登记簿字段直接并入渠道表的行与详情页
  // （XM-CHAN-MERGE0），默认空登记簿——需要具体账号数据的用例自己覆盖
  // stubFetch，不在这个共用兜底里编样例行（与服务器登记簿四个查询同一条
  // 注释里的规矩）。
  if (url.startsWith("/api/v1/finance/upstream-accounts"))
    return fakeResponse(200, { items: [] });
  if (url.startsWith("/api/v1/finance/runway-thresholds/history"))
    return fakeResponse(200, { items: [{ environment: "development", revision: 1, critical_days: 5, warning_days: 10, serious_days: 20, changed_at: "2026-08-28T10:00:00Z", changed_by: "bootstrap", reason: "initial", request_id: "req-1", change_source: "bootstrap" }], has_more: false });
  if (url.startsWith("/api/v1/finance/runway-thresholds/preview"))
    return fakeResponse(200, { current: { critical_days: 5, warning_days: 10, serious_days: 20 }, proposed: { critical_days: 5, warning_days: 10, serious_days: 20 }, current_revision: 1, evaluation_at: "2026-08-28T10:00:00Z", coverage: { total: 0, known: 0, unknown_reasons: {} }, counts: { would_open: 0, would_escalate: 0, would_deescalate: 0, would_resolve: 0, unchanged: 0, current_inconsistent: 0 }, items: [], has_more: false });
  if (url.startsWith("/api/v1/finance/runway-thresholds"))
    return fakeResponse(200, { environment: "development", critical_days: 5, warning_days: 10, serious_days: 20, revision: 1, source: "database", updated_at: "2026-08-28T10:00:00Z", updated_by: "bootstrap", reason: "initial" });
  if (url.startsWith("/api/v1/metrics")) return fakeResponse(200, metricsBody);
  if (url.startsWith("/api/v1/services")) return fakeResponse(200, servicesBody);
  if (/\/api\/v1\/platforms\/[^/]+\/users\/u-/.test(url)) {
    return fakeResponse(200, userDetailBody(url.includes("/platforms/newapi/") ? "newapi" : "sub2api"));
  }
  if (url.includes("/users")) return fakeResponse(200, usersBody);
  // XM-PAY1：逐笔订单，默认空页——单条详情先判（路径比列表多一段），
  // 否则列表分支会先吞掉详情请求。
  if (/\/api\/v1\/platforms\/[^/]+\/orders\/[^/?]+/.test(url)) {
    return fakeResponse(404, { error: { code: "ACTION_NOT_REGISTERED", message: "未找到订单" } });
  }
  if (/\/api\/v1\/platforms\/[^/]+\/orders/.test(url)) {
    return fakeResponse(200, {
      items: [],
      next_cursor: "",
      stats_by_status: {},
      from: "2026-08-28",
      to: "2026-08-28",
      data_source: "fake",
      freshness: {
        state: "fresh",
        staleness_seconds: 5,
        threshold_seconds: 60,
        is_partial: false,
        observed_at: "2026-08-28T10:00:00Z",
        last_success: "2026-08-28T10:00:00Z",
        last_error_code: "",
      },
    });
  }
  // 后台任务：runs 必须排在 overview 前面判断吗？不必——两条路径互不是
  // 彼此前缀（/jobs/overview 与 /jobs/runs），谁在前都不影响匹配。
  if (url.startsWith("/api/v1/jobs/overview")) return fakeResponse(200, jobsOverviewBody);
  if (url.startsWith("/api/v1/jobs/runs")) return fakeResponse(200, { items: [], next_before: 0 });
  if (url.startsWith("/api/v1/alerts")) return fakeResponse(200, alertsBody);
  if (url.startsWith("/api/v1/audit/events"))
    return fakeResponse(200, { items: [auditEvent], next_before: 0 });
  // XM-SERVER0：服务器登记簿四个只读查询，默认空列表——各测试用例需要
  // 具体数据时自己覆盖 stubFetch，不在这个共用兜底里编样例行。
  if (url.startsWith("/api/v1/servers/")) return fakeResponse(200, { items: [] });
  // XM-ASSURE0：渠道保障被动指标，默认空窗口/空历史——同上，需要非空数据的
  // 用例自己覆盖 stubFetch。history 必须排在 overview 前面：两者的路径是
  // 包含关系（.../assurance/overview 不会匹配 .../assurance/history 的正则，
  // 反过来也一样，这里其实互不包含，但保持与 metrics/history 那条注释一致
  // 的排列习惯，以防将来任一路径改名产生真正的前缀重叠）。
  if (/\/api\/v1\/platforms\/[^/]+\/assurance\/history/.test(url))
    return fakeResponse(200, emptyAssuranceHistoryBody());
  if (/\/api\/v1\/platforms\/[^/]+\/assurance\/overview/.test(url)) {
    const window = new URL(url, "http://localhost").searchParams.get("window") ?? "1h";
    return fakeResponse(200, emptyAssuranceOverviewBody(window));
  }
  // XM-ASSURE1-ui：检测任务 / 主动检测历史，默认空——同上，需要非空数据
  // 的用例自己覆盖 stubFetch。probe-history 必须排在 probes 前面判断吗？
  // 不必——两条路径互不是彼此前缀（.../assurance/probes 与
  // .../assurance/probe-history），谁在前都不影响匹配。
  if (/\/api\/v1\/platforms\/[^/]+\/assurance\/probe-history/.test(url))
    return fakeResponse(200, emptyProbeHistoryBody());
  if (/\/api\/v1\/platforms\/[^/]+\/assurance\/probes/.test(url))
    return fakeResponse(200, emptyProbeListBody());
  return fakeResponse(404, { error: { code: "NOT_REGISTERED", message: "未知路径" } });
}

function renderRoute(path: string): ReactElement {
  // 每个用例一个全新的 QueryClient：否则上一个用例的缓存会让断言看起来通过
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const router = createMemoryRouter(routes, { initialEntries: [path] });
  const tree = (
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  );
  render(tree);
  return tree;
}

describe("admin-web 路由（登录前/后壳）", () => {
  beforeEach(() => {
    devLogout();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("未登录访问 /dashboard 重定向到登录壳", async () => {
    renderRoute("/dashboard");
    expect(await screen.findByText("开发模式进入")).not.toBeNull();
  });

  it("已登录访问 /dashboard 显示四分组导航（ADMIN-IA v3）", async () => {
    devLogin();
    renderRoute("/dashboard");
    expect(await screen.findByRole("heading", { name: "运营工作台", level: 2 })).not.toBeNull();

    const nav = await screen.findByRole("navigation", { name: "主导航" });
    for (const section of ["全局", "平台", "平台治理", "扩展能力"]) {
      expect(within(nav).getByRole("heading", { name: section })).not.toBeNull();
    }
    for (const name of ["运营工作台", "告警与故障", "审计记录", "资源目录", "设置"]) {
      expect(within(nav).getByRole("link", { name })).not.toBeNull();
    }
    // 平台段由 /api/v1/services 驱动，要等这一次请求回来
    expect(await within(nav).findByRole("link", { name: "Sub2API" })).not.toBeNull();
  });

  it("提示条给出当前位置与环境口径", async () => {
    devLogin();
    renderRoute("/dashboard");
    const crumbs = await screen.findByRole("navigation", { name: "面包屑" });
    expect(within(crumbs).getByText("全局")).not.toBeNull();
    expect(within(crumbs).getByText("运营工作台").getAttribute("aria-current")).toBe("page");
    // 测试环境没有 VITE_XM_ENVIRONMENT：显示的是「由服务端解析」这条口径，
    // 而不是猜一个环境名（见 lib/breadcrumbs）
    expect(screen.getByText(/环境 由服务端解析/)).not.toBeNull();
  });

  it("换页时提示条跟着换位置", async () => {
    devLogin();
    renderRoute("/registry");
    const crumbs = await screen.findByRole("navigation", { name: "面包屑" });
    expect(within(crumbs).getByText("平台治理")).not.toBeNull();
    expect(within(crumbs).getByText("资源目录").getAttribute("aria-current")).toBe("page");
  });
});

describe("运营工作台（ADMIN-IA v3 §一 分组 1，原型 #/g/overview）", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("版式照原型：四格计数 + 我的待处理 + 运营焦点 + 最近活动 + 平台状态矩阵", async () => {
    renderRoute("/dashboard");
    expect(await screen.findByRole("heading", { name: "运营工作台", level: 2 })).not.toBeNull();
    // 四格在 ApiStateView 里面，要等告警回来才渲染
    await screen.findByRole("heading", { name: "紧急", level: 3 });
    for (const block of ["我的待处理", "运营焦点", "最近活动", "平台状态矩阵"]) {
      expect(screen.getByRole("heading", { name: block, level: 3 })).not.toBeNull();
    }
    for (const tile of ["紧急", "今日到期", "阻塞", "最近恢复"]) {
      expect(screen.getByRole("heading", { name: tile, level: 3 })).not.toBeNull();
    }
  });

  it("**不合成总健康分**：运营焦点三行分开，屏幕上没有任何一个综合评分", async () => {
    // 交接文档 §9.1 明令禁止。一个 87 分的看板没法回答「我现在该去修哪个」，
    // 而任何一条恶化都会被另外两条稀释掉
    renderRoute("/dashboard");
    await screen.findByText("可靠性");
    for (const domain of ["可靠性", "财务", "安全"]) {
      expect(screen.getByText(domain)).not.toBeNull();
    }
    // 判据是「屏幕上有没有一个把三条信号揉成一个数的评分」，不是搜关键词——
    // 页头那句「不合成总健康分」本身就含「总健康分」四个字
    expect(screen.queryByText(/^\s*\d+\s*分\s*$/)).toBeNull();
    expect(screen.queryByText(/\d+\s*\/\s*100/)).toBeNull();
    // 三个领域各有各的状态徽章，没有被合并成一格
    expect(screen.getAllByText("未接入").length).toBeGreaterThanOrEqual(2);
  });

  it("「紧急」数的是未解决的严重告警", async () => {
    // alertsBody：1 条 critical（OPEN）+ 1 条 warning（SILENCED）
    renderRoute("/dashboard");
    const urgent = (await screen.findByRole("heading", { name: "紧急", level: 3 })).closest(
      "article",
    );
    expect(within(urgent as HTMLElement).getByText("1")).not.toBeNull();
    expect(within(urgent as HTMLElement).getByRole("link", { name: /查看全部告警/ })).not.toBeNull();
  });

  it("没有数据源的两格显示「—」并标「未接入」，不显示 0", async () => {
    // 0 会被读成「今天没有到期项」，而事实是这条线还没接
    renderRoute("/dashboard");
    const due = (await screen.findByRole("heading", { name: "今日到期", level: 3 })).closest(
      "article",
    ) as HTMLElement;
    expect(within(due).getByText("—")).not.toBeNull();
    expect(within(due).getByText("未接入")).not.toBeNull();
    expect(within(due).queryByText("0")).toBeNull();
  });

  it("我的待处理列出活跃告警，并说明其余分类为什么是空的", async () => {
    renderRoute("/dashboard");
    expect(await screen.findByText("指标 sub2api.revenue.daily 同步失败")).not.toBeNull();
    // 今天「故障」一类里躺的其实是活跃告警，Incident 对象还没建——
    // 不说的话，人会以为这些已经是收敛过的故障单
    expect(screen.getByText(/其余各类的空是「还没接」，不是「没有问题」/)).not.toBeNull();
  });

  // XM-WORKBENCH-JOBS：这一类原来写着"后台任务页与 River 查询端点尚未建"，
  // 而那两样在 XM-JOBS0 就交付了——失败的后台任务在首屏彻底不可见。
  it("已放弃的后台任务进「失败任务」，并直达后台任务页对应页签", async () => {
    stubFetch((url) => {
      if (url.startsWith("/api/v1/jobs/runs")) {
        // 只有 state=discarded 那一次查询有内容：工作台不查重试中的任务。
        if (!url.includes("state=discarded")) return fakeResponse(200, { items: [] });
        return fakeResponse(200, {
          items: [
            {
              id: 77,
              kind: "newapi_sync",
              queue: "default",
              state: "discarded",
              attempt: 3,
              max_attempts: 3,
              created_at: "2026-08-26T08:00:00Z",
              scheduled_at: "2026-08-26T08:00:00Z",
              attempted_at: "2026-08-26T09:00:00Z",
              finalized_at: "2026-08-26T09:30:00Z",
              duration_ms: 90,
              error_count: 3,
              last_error: null,
              args: {},
            },
          ],
          next_before: 0,
        });
      }
      return okHandler(url);
    });
    renderRoute("/dashboard?work=jobs");
    const item = await screen.findByText("NewAPI 同步 重试 3 次后放弃");
    expect(item).not.toBeNull();
    expect(screen.queryByText("「失败任务」还没有数据源")).toBeNull();
    const link = item.closest("a") as HTMLAnchorElement;
    expect(link.getAttribute("href")).toBe("/jobs?sub=repeated");
  });

  // XM-WORKBENCH-TRUNCATION / XM-ALERTS-LIST-TRUNCATED：被截断时要说出来。
  // 一张"没有待处理事项"的清单如果其实被截断了，人会据此收工。
  //
  // **判据是服务端给的游标，不是"取满了 20 条"**：取满 20 条而没有下一页
  // 游标时，那就真的只有 20 条，说"可能不是全部"是无中生有。
  it("还有下一页时说明这一屏可能不是全部", async () => {
    stubFetch((url) => {
      if (url.startsWith("/api/v1/jobs/runs")) {
        if (!url.includes("state=discarded")) return fakeResponse(200, { items: [] });
        return fakeResponse(200, {
          items: Array.from({ length: 20 }, (_, index) => ({
            id: 100 + index,
            kind: "newapi_sync",
            queue: "default",
            state: "discarded",
            attempt: 3,
            max_attempts: 3,
            created_at: "2026-08-26T08:00:00Z",
            scheduled_at: "2026-08-26T08:00:00Z",
            attempted_at: "2026-08-26T09:00:00Z",
            finalized_at: "2026-08-26T09:30:00Z",
            duration_ms: 90,
            error_count: 3,
            last_error: null,
            args: {},
          })),
          // 还有下一页：这才是"没显示全"的权威信号。
          next_before: 99,
        });
      }
      return okHandler(url);
    });
    renderRoute("/dashboard?work=jobs");
    expect(await screen.findByText(/这一屏可能不是全部/)).not.toBeNull();
    expect(screen.getByText(/已放弃的后台任务只取了 20 条/)).not.toBeNull();
  });

  // 权威判据比"数个数"准的地方正在这里：取满 20 条但没有下一页 = 就是 20 条。
  it("取满 20 条但没有下一页时不提示——那就是全部", async () => {
    stubFetch((url) => {
      if (url.startsWith("/api/v1/jobs/runs")) {
        if (!url.includes("state=discarded")) return fakeResponse(200, { items: [] });
        return fakeResponse(200, {
          items: Array.from({ length: 20 }, (_, index) => ({
            id: 200 + index,
            kind: "newapi_sync",
            queue: "default",
            state: "discarded",
            attempt: 3,
            max_attempts: 3,
            created_at: "2026-08-26T08:00:00Z",
            scheduled_at: "2026-08-26T08:00:00Z",
            attempted_at: "2026-08-26T09:00:00Z",
            finalized_at: "2026-08-26T09:30:00Z",
            duration_ms: 90,
            error_count: 3,
            last_error: null,
            args: {},
          })),
          next_before: 0,
        });
      }
      return okHandler(url);
    });
    renderRoute("/dashboard?work=jobs");
    await screen.findAllByText(/重试 3 次后放弃/);
    expect(screen.queryByText(/这一屏可能不是全部/)).toBeNull();
  });

  it("没取满时不显示截断提示——显示一句「没有截断」是噪声", async () => {
    renderRoute("/dashboard?work=jobs");
    await screen.findByText("没有待处理事项");
    expect(screen.queryByText(/这一屏可能不是全部/)).toBeNull();
  });

  it("没有已放弃的任务时说清重试中的不计入，不显示成「还没接」", async () => {
    renderRoute("/dashboard?work=jobs");
    // okHandler 的 /jobs/runs 返回空列表
    expect(await screen.findByText("没有待处理事项")).not.toBeNull();
    expect(screen.getByText(/重试中的任务不计入这里/)).not.toBeNull();
    expect(screen.queryByText("「失败任务」还没有数据源")).toBeNull();
  });

  // XM-ALERTS-LIST-TRUNCATED：全局告警页的职责就是"看全部"，被截断时必须说。
  it("告警页在服务端说截断时提示，并给出生效上限", async () => {
    stubFetch((url) => {
      if (url.startsWith("/api/v1/alerts")) {
        return fakeResponse(200, { items: [], limit: 500, truncated: true });
      }
      return okHandler(url);
    });
    renderRoute("/alerts");
    expect(await screen.findByText(/这一页可能不是全部/)).not.toBeNull();
    expect(screen.getByText(/最多返回 500 条/)).not.toBeNull();
  });

  // 老后端没有 truncated 字段时按"没截断"处理：那时它确实回答不了这个问题，
  // 假装"可能截断"是平白吓人。
  it("服务端没给 truncated 时不提示", async () => {
    renderRoute("/alerts");
    // **必须等表格里的数据落地再断言"没有提示"**：页头在 ApiStateView 外面，
    // 等到页头就断言的话，这条断言在查询还没回来时永远成立（恒真）。
    await screen.findByText("指标 sub2api.revenue.daily 同步失败");
    expect(screen.queryByText(/这一页可能不是全部/)).toBeNull();
  });

  it("筛选进 ?work=，选到没有数据源的分类时说清楚被什么挡着", async () => {
    // XM-WORKBENCH-APPROVALS：这一条原来用的是 `?work=approvals`。审批接上真实
    // 数据之后那一类不再有 blockedBy，拿它就测不到「说清楚被什么挡着」这件事
    // 了——换成一个今天仍然没有源的分类。「即将到期」的缺口比页面更深一层：
    // 凭据模型里根本没有到期时间这个字段。
    renderRoute("/dashboard?work=expiring");
    const empty = await screen.findByText("「即将到期」还没有数据源");
    const box = empty.closest("div") as HTMLElement;
    // 说明必须指到真正的缺口上。含糊成「随人员与权限页上线」会让人去等一个
    // 早就建成的页面，而缺的其实是 CredentialRef 上的到期元数据。
    expect(within(box).getByText(/凭据模型里还没有到期时间/)).not.toBeNull();
    expect(within(box).getByText(/CredentialRef/)).not.toBeNull();
    // 一个筛过的工作台是可以贴给同事的地址（交接文档 §8）
    expect(screen.getByRole("button", { name: "即将到期" }).getAttribute("aria-pressed")).toBe(
      "true",
    );
  });

  it("没有数据源的分类，后端挂了也照样说「还没接」，不显示成加载失败", async () => {
    // 这一类的空与这次请求成不成功无关。套进 ApiStateView 的话，后端一挂就
    // 显示「加载失败」——把「还没接」说成一次网络故障，人会去点重试，
    // 而重试一万次也不会有内容
    stubFetch(() =>
      fakeResponse(503, { error: { code: "UNAVAILABLE", message: "上游不可用" } }),
    );
    renderRoute("/dashboard?work=finance");
    expect(await screen.findByText("「财务异常」还没有数据源")).not.toBeNull();
  });

  it("平台状态矩阵按平台给出阶段、状态、新鲜度与活动事件", async () => {
    renderRoute("/dashboard");
    // 矩阵在 ApiStateView 里面，等注册表与指标都回来
    const link = await screen.findByRole("link", { name: "Sub2API" });
    const matrix = link.closest("section") as HTMLElement;
    // servicesBody 只登记了 sub2api（degraded）
    expect(within(matrix).getByText("降级")).not.toBeNull();
    // 没登记的平台说「未登记 / 未接入·Mx」，不说「正常」
    expect(within(matrix).getByText("未接入·M4")).not.toBeNull();
    // 开票系统在矩阵里，但入口落在治理段而不是一个平台页
    expect(within(matrix).getByRole("link", { name: "开票系统" }).getAttribute("href")).toBe(
      "/finance?sub=invoicing",
    );
  });

  it("五条 query 各自独立：指标端点挂掉时告警那几格照常显示", async () => {
    // 规格 §9.2 把告警列为工作台的固定一项：指标挂了正是最需要看见告警的时候
    stubFetch((url) =>
      url.startsWith("/api/v1/metrics")
        ? fakeResponse(500, { error: { code: "INTERNAL", message: "服务内部错误" } })
        : okHandler(url),
    );
    renderRoute("/dashboard");
    expect(await screen.findByText("指标 sub2api.revenue.daily 同步失败")).not.toBeNull();
    expect(screen.getAllByText("加载失败").length).toBeGreaterThan(0);
  });

  it("403 时提示缺少的权限名", async () => {
    stubFetch(() =>
      fakeResponse(403, {
        error: { code: "PERMISSION_DENIED", message: "缺少权限 ops.read", request_id: "req-7" },
      }),
    );
    renderRoute("/dashboard");
    expect((await screen.findAllByText("无权访问")).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/ops\.read/).length).toBeGreaterThan(0);
  });

  it("网络失败时显示可重试的错误态", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.reject(new TypeError("Failed to fetch"))));
    renderRoute("/dashboard");
    expect((await screen.findAllByText("加载失败")).length).toBeGreaterThan(0);
    expect(screen.getAllByRole("button", { name: "重试" }).length).toBeGreaterThan(0);
  });

  it("请求带上开发期身份头", async () => {
    const fetchMock = stubFetch(okHandler);
    renderRoute("/dashboard");
    await screen.findByRole("heading", { name: "我的待处理", level: 3 });
    const init = (fetchMock.mock.calls[0] as unknown as [string, RequestInit])[1];
    expect(init.headers).toMatchObject({
      "X-Dev-Principal-ID": "dev-operator",
      "X-Dev-Principal-Type": "HUMAN",
      // 逐字断言而不是「包含 finance.read」：这一行是开发身份到底带了哪些权限的
      // 唯一现场，多带一个 scope 要在 diff 里看得见——本地随手加一个用完忘了删,
      // 正是「在我机器上好好的」那类问题的来源
      "X-Dev-Scopes":
        "registry.read,ops.read,audit.read,registry.service.manage,alerts.alert.manage,alerts.silence.manage,finance.read,request.read,request.content.read,platform.users.read,platform.user_keys.read,finance.upstream_account.manage,finance.recharge_ratio.manage,finance.token_map.manage,ui.saved_view.manage,credential.manage,connector.manage,staff.manage",
    });
  });

  it("最近活动来自审计记录，并给出通往审计页的入口", async () => {
    renderRoute("/dashboard");
    const event = await screen.findByText("registry.service.create@1");
    const activity = event.closest("section") as HTMLElement;
    expect(within(activity).getByRole("link", { name: /查看审计记录/ })).not.toBeNull();
  });

  it("零告警时说「没有待处理事项」，并提醒这一屏不代表全部待办", async () => {
    stubFetch((url) =>
      url.startsWith("/api/v1/alerts") ? fakeResponse(200, { items: [] }) : okHandler(url),
    );
    renderRoute("/dashboard");
    expect(await screen.findByText("没有待处理事项")).not.toBeNull();
    // 零不等于「都处理完了」：还有四类根本没接
    expect(screen.getByText(/并不代表全部待办/)).not.toBeNull();
    expect(screen.getByText("当前没有未解决的严重告警")).not.toBeNull();
  });
});

describe("演示数据横幅", () => {
  beforeEach(() => devLogin());
  afterEach(() => vi.unstubAllGlobals());

  it("指标 source 命中已知演示实例时挂出横幅，且没有关闭按钮（Codex #8）", async () => {
    const demo = { items: metricsBody.items.map((m) => ({ ...m, source: "sub2api-staging" })) };
    stubFetch((url) =>
      url.startsWith("/api/v1/metrics") && !url.startsWith("/api/v1/metrics/history")
        ? fakeResponse(200, demo)
        : okHandler(url),
    );
    renderRoute("/dashboard");
    expect(await screen.findByText(/当前展示的是演示数据（Fake 连接器）/)).not.toBeNull();
    // 可关闭的警告等于「点一次就永远看不见的警告」
    expect(screen.queryByRole("button", { name: /关闭|知道了|不再提示/ })).toBeNull();
  });

  it("source 没命中就不挂——误报会把横幅变成人人无视的噪音", async () => {
    stubFetch(okHandler); // metricsBody 里的 source 是 sub2api-prod
    renderRoute("/dashboard");
    await screen.findByRole("heading", { name: "我的待处理", level: 3 });
    expect(screen.queryByText(/当前展示的是演示数据/)).toBeNull();
  });
});

describe("注册表页（原服务清单）", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("表格列出实例、状态徽章与采集新鲜度", async () => {
    renderRoute("/registry");
    expect(await screen.findByText("sub2api-dev")).not.toBeNull();
    expect(screen.getByText("降级")).not.toBeNull();
    expect(screen.getByText("数据新鲜")).not.toBeNull();
    expect(screen.getByText(/落后 2 分钟/)).not.toBeNull();
    expect(screen.getByText(/数据时间 2026-08-26 10:00:00 UTC/)).not.toBeNull();
  });
});

describe("平台概览的迷你趋势图", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  // 折线随 XM-0043 从工作台挪到了平台概览页：裁定 #3 砍掉「指标趋势」页签之后，
  // 历史曲线的落点就是各平台概览的指标卡（ADMIN-IA §8.1）
  it("卡片渲染之后才去拉历史，拿到后画出折线", async () => {
    const fetchMock = stubFetch(okHandler);
    renderRoute("/platforms/sub2api");

    // 首屏那一批请求里没有 history：它是挂载之后才发的
    await screen.findByText("今日充值");
    expect(String(fetchMock.mock.calls[0]?.[0])).not.toContain("/metrics/history");

    // XM-0051 起概览的折线看七天（原型的 tile 内嵌 sparkline 是 7 个点）
    expect(
      await screen.findByRole("img", { name: /Sub2API 日收入 近 168 小时趋势/ }),
    ).not.toBeNull();
  });

  it("历史接口挂了只影响趋势那一小块，卡片主体照常显示", async () => {
    stubFetch((url) =>
      url.startsWith("/api/v1/metrics/history")
        ? fakeResponse(500, { error: { code: "INTERNAL", message: "服务内部错误" } })
        : okHandler(url),
    );
    renderRoute("/platforms/sub2api");

    expect(await screen.findByText("¥1,234.56")).not.toBeNull();
    expect((await screen.findAllByText("趋势不可用")).length).toBeGreaterThan(0);
    // 整页没有被换成错误态
    expect(screen.queryByText("加载失败")).toBeNull();
  });

  it("统计卡用原型的格名，金额与新鲜度徽章都在", async () => {
    // 原型这一格叫「今日充值」；契约说这条指标是当天支付订单的累加，
    // 与指标注册表里那个「日收入」的名字冲突，按契约口径走
    renderRoute("/platforms/sub2api");
    expect(await screen.findByText("今日充值")).not.toBeNull();
    expect(screen.getByText("¥1,234.56")).not.toBeNull();
    // 原型那一格画的是环比涨跌，平台没有环比这个数——那个位置换成新鲜度，
    // 但**不能没有**（规格 §9.1）
    expect(screen.getAllByText("数据延迟").length).toBeGreaterThan(0);
    expect(
      screen.getAllByText(/数据时间 2026-08-26 10:00:00 UTC · 落后 2 小时/).length,
    ).toBeGreaterThan(0);
  });

  it("未初始化的指标显示「未初始化」而不是 ¥0.00（宪法 12 条）", async () => {
    // 换成概览页上真的有的那一格：从未成功采集时后端 value 里仍带着 0，
    // 直接渲染就会变成理直气壮的「今日成本 ¥0.00」
    stubFetch((url) =>
      url.startsWith("/api/v1/metrics") && !url.startsWith("/api/v1/metrics/history")
        ? fakeResponse(200, {
            items: [
              {
                metric_key: COST_METRIC,
                source: "sub2api-prod",
                environment: "development",
                watermark: "",
                value: { day: "2026-08-26", amount_minor_units: 0, currency: "CNY" },
                freshness: {
                  state: "uninitialized",
                  staleness_seconds: null,
                  threshold_seconds: 1800,
                  is_partial: false,
                  observed_at: null,
                  last_success: null,
                  last_error_code: "",
                },
              },
            ],
          })
        : okHandler(url),
    );
    renderRoute("/platforms/sub2api");
    await screen.findByText("今日成本");
    expect(screen.getAllByText("未初始化").length).toBeGreaterThan(0);
    expect(screen.queryByText("¥0.00")).toBeNull();
  });

  it("头部显示最后刷新时刻（HH:MM:SS）", async () => {
    renderRoute("/platforms/sub2api");
    await screen.findByText("今日充值");
    expect(screen.getByText(/最后刷新 \d{2}:\d{2}:\d{2}/)).not.toBeNull();
  });
});

describe("Sub2API 平台详情·渠道管理页签（XM-0052 逐格对齐原型）", () => {
  // 行数据从「渠道余额指标」换成了 finance 汇总端点：一行 = 一个上游账号。
  // 换粒度的理由见 ChannelTable 的文件头——指标那一行是平台自己的渠道，
  // 与上游账号的 id 互不认识，按 id join 一条都对不上。
  beforeEach(() => {
    devLogin();
    stubFetch((url) =>
      url.startsWith("/api/v1/finance/channels/summary")
        ? fakeResponse(
            200,
            financeChannelsWith([
              financeChannelRow(),
              financeChannelRow({
                id: "acc-sub",
                name: "订阅乙",
                access_method: "subscription_account",
                metered: false,
                runway: {
                  days: null,
                  level: "",
                  reason: "not_applicable",
                  window_days: 7,
                  covered_days: 0,
                },
              }),
            ]),
          )
        : okHandler(url),
    );
  });
  afterEach(() => vi.unstubAllGlobals());

  it("逐个上游账号列出供给成本、我方计费消耗与毛利", async () => {
    renderRoute("/platforms/sub2api?tab=upstream");
    expect(await screen.findByText("上游甲")).not.toBeNull();
    const table = within(screen.getByRole("table"));
    expect(table.getByText("订阅乙")).not.toBeNull();
    // scale-6 微单位按标度降到分，不是差一万倍的那个数
    expect(table.getAllByText("¥70.00").length).toBeGreaterThan(0);
    expect(table.getAllByText("¥30.00").length).toBeGreaterThan(0);
    // 毛利率是后端给的，前端只格式化
    expect(table.getAllByText("30%").length).toBeGreaterThan(0);
  });

  it("计量型显示余额与可用天数，订阅型说明它没有余额这个概念", async () => {
    renderRoute("/platforms/sub2api?tab=upstream");
    expect(await screen.findByText("上游甲")).not.toBeNull();
    const table = within(screen.getByRole("table"));
    expect(table.getByText(/约 12 天/)).not.toBeNull();
    expect(table.getByText(/余额观测/)).not.toBeNull();
    // 订阅型不显示 ¥0.00 余额——那会被读成「花光了」
    expect(table.getByText(/订阅型渠道没有余额，可用天数对它无意义/)).not.toBeNull();
  });

  it("口径声明照抄原型；07:20 补充裁定后不再指向任何独立区块的链接", async () => {
    // 2026-09-02 07:20 补充裁定：登记簿字段直接并入渠道表的行与详情页，
    // 不再有独立页签、也不再有独立区块可跳
    renderRoute("/platforms/sub2api?tab=upstream");
    expect(
      await screen.findByText(/渠道管理只做单账号 \/ 单 Key 核算，不做跨账号汇总/),
    ).not.toBeNull();
    expect(screen.queryByRole("link", { name: /上游管理/ })).toBeNull();
  });

  it("该环境没有上游账号时给空态而不是空表", async () => {
    stubFetch((url) =>
      url.startsWith("/api/v1/finance/channels/summary")
        ? fakeResponse(200, financeChannelsWith([]))
        : okHandler(url),
    );
    renderRoute("/platforms/sub2api?tab=upstream");
    expect(await screen.findByText("还没有上游账号")).not.toBeNull();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("汇总端点读失败时给错误态，而不是一张空表", async () => {
    stubFetch((url) =>
      url.startsWith("/api/v1/finance/channels/summary")
        ? fakeResponse(500, { error: { code: "boom", message: "读取失败" } })
        : okHandler(url),
    );
    renderRoute("/platforms/sub2api?tab=upstream");
    expect(await screen.findByRole("button", { name: /重试/ })).not.toBeNull();
    expect(screen.queryByRole("table")).toBeNull();
  });
});

describe("NewAPI 平台详情（XM-0035）", () => {
  beforeEach(() => {
    devLogin();
    stubFetch((url) =>
      url.startsWith("/api/v1/metrics/history")
        ? okHandler(url)
        : url.startsWith("/api/v1/metrics")
          ? fakeResponse(200, newapiChannelsBody)
          : okHandler(url),
    );
  });
  afterEach(() => vi.unstubAllGlobals());

  it("注册表里没有 newapi 也照样展开全部页签，而不是一屏「未接入」", async () => {
    renderRoute("/platforms/newapi");
    // servicesBody 里没有 newapi——页面靠指标活着，不靠登记
    expect(await screen.findByRole("tab", { name: "概览" })).not.toBeNull();
    // 八格：2026-09-02 裁定「上游管理」并入渠道管理页内区块后，
    // ADMIN-IA v3 §2.1 的 7 格 + 裁定 #1 恢复的「渠道保障」
    expect(screen.getAllByRole("tab")).toHaveLength(8);
    // 断言的是**页头上**没挂「未登记」徽章，而不是全屏搜「未接入」——
    // 导航上 CPA / 服务器确实还挂着「未接入·Mx」，那是对的。
    expect(screen.queryByText("未登记")).toBeNull();
  });

  // XM-0052：渠道管理页签换成 finance 汇总端点驱动（一行 = 一个上游账号）。
  //
  // ⚠️ 原来这里有三列来自 `newapi.channels.status` 指标（启停 / 错误率 / 延迟），
  // 本片按原型把它们从这张表上去掉了——那条指标一行是 NewAPI 自己的渠道，
  // 与上游账号对不上号。**逐渠道的错误率因此在 UI 上暂时没有去处**
  // （概览卡片只给「3 个渠道 · 启用 2 · 异常 1」这种聚合），
  // 它应该跟着渠道保障（M1.5）一起回来。下面那条概览用例是它仅剩的守卫。
  it("渠道管理页签按上游账号出行，并显示经营三列", async () => {
    stubFetch((url) =>
      url.startsWith("/api/v1/finance/channels/summary")
        ? fakeResponse(
            200,
            financeChannelsWith([
              financeChannelRow({ id: "np-1", name: "上游甲", system_type: "newapi", platform_id: "newapi" }),
            ]),
          )
        : okHandler(url),
    );
    renderRoute("/platforms/newapi?tab=upstream");
    expect(await screen.findByText("上游甲")).not.toBeNull();
    const table = within(screen.getByRole("table"));
    expect(table.getByText("供给成本")).not.toBeNull();
    expect(table.getByText("我方计费消耗")).not.toBeNull();
    expect(table.getByText("毛利")).not.toBeNull();
    // 原型的 NewAPI 表没有「成功率」这一列，Sub2API 才有
    expect(table.queryByText("成功率")).toBeNull();
  });

  it("说明与 Sub2API 共用上游目录但各自核算", async () => {
    stubFetch((url) =>
      url.startsWith("/api/v1/finance/channels/summary")
        ? fakeResponse(200, financeChannelsWith([]))
        : okHandler(url),
    );
    renderRoute("/platforms/newapi?tab=upstream");
    expect(
      await screen.findByText(/NewAPI 与 Sub2API 共用上游目录和充值成本率/),
    ).not.toBeNull();
  });

  it("该环境没有上游账号时给空态而不是空表", async () => {
    stubFetch((url) =>
      url.startsWith("/api/v1/finance/channels/summary")
        ? fakeResponse(200, financeChannelsWith([]))
        : okHandler(url),
    );
    renderRoute("/platforms/newapi?tab=upstream");
    expect(await screen.findByText("还没有上游账号")).not.toBeNull();
  });

  it("概览按 NewAPI 自己的原型页排：用户总数 + 渠道健康表", async () => {
    // 原型给两个平台画的概览**结构不同**，不共用一套模板
    renderRoute("/platforms/newapi");
    expect(await screen.findByText("用户总数")).not.toBeNull();
    expect(screen.getByText("渠道健康")).not.toBeNull();
    // 渠道逐条进表，不再是一张「3 个渠道」的聚合卡
    expect(screen.getByRole("table", { name: /NewAPI 渠道启停与错误率/ })).not.toBeNull();
  });

  it("演示来源 newapi-staging 会挂出演示横幅（Fake 数据不得冒充真实运营数据）", async () => {
    renderRoute("/platforms/newapi");
    expect(
      await screen.findByText(/当前展示的是演示数据（Fake 连接器）/),
    ).not.toBeNull();
  });

  it("连接与凭据页签进入真实结构，未登记时给行动指向而不是通用占位", async () => {
    renderRoute("/platforms/newapi?tab=creds");
    expect(await screen.findByText("连接事实与凭据边界")).not.toBeNull();
    expect(screen.getByText("NewAPI 还没有登记实例")).not.toBeNull();
    expect(screen.getByRole("heading", { name: "凭据引用", level: 3 })).not.toBeNull();
    expect(screen.queryByText(/「连接与凭据」尚未实现/)).toBeNull();
  });

  it("平台告警页签进入只读归属视图，不再落到通用占位", async () => {
    renderRoute("/platforms/newapi?tab=alerts");
    // 页头改成冻结 IA 第 7 格的逐字标题（XM-ALERTS-TAB-DURATION）。
    expect(await screen.findByRole("heading", { name: "NewAPI · 告警", level: 2 })).not.toBeNull();
    // 默认样本只有 sub2api.* 告警，所以 NewAPI 是可归属空态，不是全局零告警。
    expect(await screen.findByText("没有可归属到 NewAPI 的告警")).not.toBeNull();
    expect(screen.getByText(/不代表全局没有告警/)).not.toBeNull();
    expect(screen.queryByText(/「告警」尚未实现/)).toBeNull();
  });

});

describe("审计事件页", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("默认审计路由仍能挂载并显示事件表", async () => {
    renderRoute("/audit");
    const panels = await screen.findAllByRole("tabpanel");
    expect(panels.length).toBeGreaterThan(0);
    const panel = panels[0]!;
    expect(screen.getByRole("tab", { name: "审计记录", selected: true })).not.toBeNull();
    expect(await within(panel).findByText("registry.service.create@1")).not.toBeNull();
    expect(within(panel).getByText("core.service/svc-1")).not.toBeNull();
    expect(within(panel).getAllByText("2").length).toBeGreaterThan(0);
    expect(within(panel).getByRole("table").textContent).toContain("成功");
    expect(within(panel).getByText("aaaaaaaa")).not.toBeNull();
    expect(within(panel).getByText(/完整性校验以 audit-verify 工具与链根签名为准/)).not.toBeNull();
  });
});

describe("登记服务（写路径）", () => {
  beforeEach(() => {
    devLogin();
  });
  afterEach(() => vi.unstubAllGlobals());

  function fillRequired() {
    fireEvent.change(screen.getByLabelText(/服务类型/), { target: { value: "sub2api" } });
    fireEvent.change(screen.getByLabelText(/实例标识/), { target: { value: "sub2api-new" } });
    fireEvent.change(screen.getByLabelText(/对外地址/), {
      target: { value: "https://new.example.com" },
    });
    fireEvent.change(screen.getByLabelText(/负责人/), { target: { value: "平台组" } });
  }

  async function openDialog() {
    renderRoute("/registry");
    fireEvent.click(await screen.findByRole("button", { name: "登记服务" }));
    return within(await screen.findByRole("dialog"));
  }

  it("环境取自身份且只读，不做成可选下拉", async () => {
    stubFetch(okHandler);
    await openDialog();
    const env = screen.getByLabelText("环境") as HTMLInputElement;
    // servicesBody 里那条记录是 development
    expect(env.value).toBe("development");
    expect(env.readOnly).toBe(true);
  });

  it("必填项没填就不发请求，就地给出报错", async () => {
    const fetchMock = stubFetch(okHandler);
    const dialog = await openDialog();
    const before = fetchMock.mock.calls.length;

    fireEvent.click(dialog.getByRole("button", { name: "登记" }));
    expect(await screen.findByText("服务类型必填")).not.toBeNull();
    expect(fetchMock.mock.calls.length).toBe(before);
  });

  it("提交失败时焦点移到错误摘要（Codex #9：不然读屏用户只听到一片沉默）", async () => {
    stubFetch(okHandler);
    const dialog = await openDialog();
    fireEvent.click(dialog.getByRole("button", { name: "登记" }));

    const summary = await screen.findByText(/还有 4 处需要修正/);
    const focusTarget = summary.closest("[tabindex]");
    await waitFor(() => expect(document.activeElement).toBe(focusTarget));
  });

  it("地址里带凭据时就地拒绝，不把 token 送进审计摘要（Codex #7）", async () => {
    const fetchMock = stubFetch(okHandler);
    const dialog = await openDialog();
    fillRequired();
    fireEvent.change(screen.getByLabelText(/对外地址/), {
      target: { value: "https://u:p@new.example.com/?token=abc" },
    });
    const before = fetchMock.mock.calls.length;

    fireEvent.click(dialog.getByRole("button", { name: "登记" }));
    expect(await screen.findByText(/用户名\/密码/)).not.toBeNull();
    expect(fetchMock.mock.calls.length).toBe(before);
  });

  it("标识符非法时按后端同一条正则给出提示", async () => {
    stubFetch(okHandler);
    const dialog = await openDialog();
    fillRequired();
    fireEvent.change(screen.getByLabelText(/实例标识/), { target: { value: "Sub2API_New" } });
    fireEvent.click(dialog.getByRole("button", { name: "登记" }));
    expect(await screen.findByText(/只能用小写字母、数字与短横线/)).not.toBeNull();
  });

  it("提交走 Action 执行入口：只带 params，request_id 在头里", async () => {
    const fetchMock = stubFetch((url, init) => {
      if (init?.method === "POST") return fakeResponse(200, { action_run_id: "run-42" });
      return okHandler(url);
    });
    const dialog = await openDialog();
    fillRequired();
    fireEvent.click(dialog.getByRole("button", { name: "登记" }));

    await waitFor(() => expect(screen.getByText(/已登记，run_id=run-42/)).not.toBeNull());

    const post = fetchMock.mock.calls.find((c) => (c[1] as RequestInit)?.method === "POST");
    expect(post?.[0]).toBe("/api/v1/actions/registry.service.create/versions/1/execute");
    const init = post?.[1] as RequestInit;
    expect(JSON.parse(String(init.body))).toEqual({
      params: {
        service_type: "sub2api",
        instance_id: "sub2api-new",
        environment: "development",
        endpoint: "https://new.example.com",
        owner: "平台组",
      },
    });
    expect((init.headers as Record<string, string>)["X-Request-ID"]).toMatch(
      /^[A-Za-z0-9._:-]{1,128}$/,
    );
    // 回执把 run_id 指回审计页，但**不承诺**那里一定有这条事件：
    // 业务写/ActionRun/审计三段非原子且 fail-open（Codex #10）
    expect(screen.getByText(/审计事件通常几秒内出现在审计页/)).not.toBeNull();
    expect(screen.queryByText(/可在审计页查看这条事件/)).toBeNull();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("403 时弹窗留在原地，显示错误码与缺少的权限", async () => {
    stubFetch((url, init) => {
      if (init?.method === "POST") {
        return fakeResponse(403, {
          error: {
            code: "PERMISSION_DENIED",
            message: "缺少权限 registry.service.manage",
            request_id: "req-9",
          },
        });
      }
      return okHandler(url);
    });
    const dialog = await openDialog();
    fillRequired();
    fireEvent.click(dialog.getByRole("button", { name: "登记" }));

    expect(await screen.findByText(/错误码 PERMISSION_DENIED/)).not.toBeNull();
    expect(screen.getByText(/需要权限：registry\.service\.manage/)).not.toBeNull();
    expect(screen.getByText(/req-9/)).not.toBeNull();
    // 表单没被清掉，人不用重填
    expect((screen.getByLabelText(/实例标识/) as HTMLInputElement).value).toBe("sub2api-new");
    expect(screen.getByRole("dialog")).not.toBeNull();
  });
});

describe("上报观测（写路径）", () => {
  beforeEach(() => devLogin());
  afterEach(() => vi.unstubAllGlobals());

  it("提交后回执带 run_id，并重新拉一次服务列表", async () => {
    const fetchMock = stubFetch((url, init) => {
      if (init?.method === "POST") return fakeResponse(200, { action_run_id: "run-7" });
      return okHandler(url);
    });
    renderRoute("/registry");
    fireEvent.click(await screen.findByRole("button", { name: "上报观测" }));

    const dialog = within(await screen.findByRole("dialog"));
    const watermark = screen.getByLabelText(/数据水位/) as HTMLInputElement;
    expect(watermark.value).toMatch(/^wm-\d{8}T\d{6}Z$/);

    fireEvent.click(dialog.getByRole("button", { name: "上报" }));
    await waitFor(() => expect(screen.getByText(/已上报观测，run_id=run-7/)).not.toBeNull());

    const post = fetchMock.mock.calls.find((c) => (c[1] as RequestInit)?.method === "POST");
    expect(post?.[0]).toBe("/api/v1/actions/registry.service.observe/versions/1/execute");
    expect(JSON.parse(String((post?.[1] as RequestInit).body))).toMatchObject({
      params: { service_type: "sub2api", instance_id: "sub2api-dev", status: "degraded" },
    });
  });
});

// --- XM-0042 导航重构（ADMIN-IA v3，严格对齐 UI 原型）---

describe("四分组侧栏：分组与条目逐字对齐 ADMIN-IA v3 §一", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("四个分组标题逐字：全局 / 平台 / 平台治理 / 扩展能力", async () => {
    renderRoute("/dashboard");
    const nav = await screen.findByRole("navigation", { name: "主导航" });
    // v2 的「被管平台」在原型里写作「平台」，侧栏渲染代码写死的就是这两个字
    for (const section of ["全局", "平台", "平台治理", "扩展能力"]) {
      expect(within(nav).getByRole("heading", { name: section })).not.toBeNull();
    }
    expect(within(nav).queryByRole("heading", { name: "被管平台" })).toBeNull();
  });

  it("全局段五条，顺序与命名逐字", async () => {
    renderRoute("/dashboard");
    const nav = await screen.findByRole("navigation", { name: "主导航" });
    await within(nav).findByRole("link", { name: /Sub2API/ });
    const expected = ["运营工作台", "告警与故障", "操作与审批", "后台任务", "审计记录"];
    const found = within(nav)
      .getAllByRole("link")
      .map((el) => el.textContent ?? "")
      .filter((text) => expected.some((label) => text.startsWith(label)));
    expect(found.map((text) => expected.find((label) => text.startsWith(label)))).toEqual(expected);
  });

  it("治理段七条，顺序与命名逐字（「注册表」已改名「资源目录」）", async () => {
    renderRoute("/dashboard");
    const nav = await screen.findByRole("navigation", { name: "主导航" });
    const expected = [
      "资源目录",
      "人员与权限",
      "跨平台财务",
      "运行保障",
      "版本与发布",
      "界面规范",
      "设置",
    ];
    for (const label of expected) {
      expect(within(nav).getByRole("link", { name: new RegExp(label) })).not.toBeNull();
    }
    // 旧名字一个都不许留：同一页在侧栏与页头上有两个名字，坐标就没用了
    for (const gone of ["注册表", "财务中心", "变更与审批"]) {
      expect(within(nav).queryByText(gone)).toBeNull();
    }
  });

  it("未实装的页仍然是可点的链接，右侧标着「未建·<阶段>」", async () => {
    // 灰掉它只能表达「不能点」，表达不了「能看结构与命名、还没有内容」，
    // 而后者才是这几页现在的状态（§12 惯例要的是别把没接的说成接了）
    renderRoute("/dashboard");
    const nav = await screen.findByRole("navigation", { name: "主导航" });
    const publishing = within(nav).getByRole("link", { name: /内容发布/ });
    expect(publishing.getAttribute("href")).toBe("/ext/publishing");
    // 2026-09-07 起 F-B 一条不剩：跨平台财务 / 版本与发布 / 界面规范三页建成
    // （XM-FINANCE-GLOBAL0 / XM-CHANGES0 / XM-DESIGN0），操作与审批更早在
    // XM-ACTIONS0 毕业。现在挂标签的只剩扩展能力段那四条「后置」——按
    // ADMIN-IA §5.4 它们是刻意的只读蓝图，不是待补的缺口
    expect(within(nav).queryAllByText("未建·F-B").length).toBe(0);
    expect(within(nav).getAllByText("未建·后置").length).toBe(4);
    const changes = within(nav).getByRole("link", { name: /版本与发布/ });
    expect(changes.getAttribute("href")).toBe("/changes");
    const actions = within(nav).getByRole("link", { name: /操作与审批/ });
    expect(actions.getAttribute("href")).toBe("/actions");
    // 已实装的页不挂标签：一个写着「未建」的标签贴在正常工作的页面旁边什么也没说
    expect(actions.textContent).not.toMatch(/未建/);
    expect(changes.textContent).not.toMatch(/未建/);
    expect(within(nav).getByRole("link", { name: "运营工作台" })).not.toBeNull();
  });

  it("面包屑跟着新命名走", async () => {
    renderRoute("/registry");
    const crumbs = await screen.findByRole("navigation", { name: "面包屑" });
    expect(within(crumbs).getByText("平台治理")).not.toBeNull();
    expect(within(crumbs).getByText("资源目录").getAttribute("aria-current")).toBe("page");
  });
});

describe("扩展能力段：可折叠、默认收起、进入时自动展开", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  function extDetails(nav: HTMLElement): HTMLDetailsElement {
    const details = nav.querySelector("details");
    if (!details) throw new Error("扩展能力段不是 <details>");
    return details as HTMLDetailsElement;
  }

  it("在别的页面上默认收起，但标题与「后置」标签始终在场", async () => {
    renderRoute("/dashboard");
    const nav = await screen.findByRole("navigation", { name: "主导航" });
    expect(extDetails(nav).open).toBe(false);
    // 收起不是隐藏：人要能看出还有这么一段
    expect(within(nav).getByRole("heading", { name: "扩展能力" })).not.toBeNull();
    expect(within(nav).getByText("后置")).not.toBeNull();
  });

  it("进入 /ext/* 时自动展开（原型行为）", async () => {
    renderRoute("/ext/ai");
    const nav = await screen.findByRole("navigation", { name: "主导航" });
    expect(extDetails(nav).open).toBe(true);
  });

  it("四条命名逐字，注意「AI能力管理」没有空格", async () => {
    renderRoute("/ext/app");
    const nav = await screen.findByRole("navigation", { name: "主导航" });
    for (const label of ["应用与配置", "接口与自动化", "内容发布", "AI能力管理"]) {
      expect(within(nav).getByRole("link", { name: new RegExp(label) })).not.toBeNull();
    }
  });
});

describe("平台段：Registry 驱动 + 显式排除名单", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("只有 4 个平台，且都可点", async () => {
    renderRoute("/dashboard");
    const nav = await screen.findByRole("navigation", { name: "主导航" });
    for (const label of ["Sub2API", "NewAPI", "CPA", "服务器"]) {
      expect(await within(nav).findByRole("link", { name: new RegExp(label) })).not.toBeNull();
    }
  });

  it("开票系统 / 支付 / 模型保障不再出现在平台段", async () => {
    renderRoute("/dashboard");
    const nav = await screen.findByRole("navigation", { name: "主导航" });
    await within(nav).findByRole("link", { name: /Sub2API/ });
    for (const gone of ["开票系统", "支付", "模型保障"]) {
      expect(within(nav).queryByText(gone)).toBeNull();
    }
  });

  it("Registry 里登记了 payment/invoice 也不会爬回平台段", async () => {
    // 后端 registry 认得这两个 service_type。只从目录里删掉的话，
    // 「登记即出现」那条规则会把它们以原始键名重新塞回侧栏
    stubFetch((url) =>
      url.startsWith("/api/v1/services")
        ? fakeResponse(200, {
            items: [
              { ...servicesBody.items[0], service_type: "payment", instance_id: "payment-dev" },
              {
                ...servicesBody.items[0],
                id: "33333333-3333-3333-3333-333333333333",
                service_type: "invoice",
                instance_id: "invoice-dev",
              },
            ],
          })
        : okHandler(url),
    );
    renderRoute("/dashboard");
    const nav = await screen.findByRole("navigation", { name: "主导航" });
    await within(nav).findByRole("link", { name: /Sub2API/ });
    expect(within(nav).queryByText("payment")).toBeNull();
    expect(within(nav).queryByText("invoice")).toBeNull();
  });

  it("未接入的平台照样可点，右侧挂里程碑标签", async () => {
    renderRoute("/dashboard");
    const nav = await screen.findByRole("navigation", { name: "主导航" });
    const cpa = await within(nav).findByRole("link", { name: /CPA/ });
    expect(cpa.getAttribute("href")).toBe("/platforms/cpa");
    expect(within(nav).getByText("未接入·M4")).not.toBeNull();
    // 服务器从「未接入·M2 且点不动」提为可进入：原型把它画成了完整的资产中心
    expect(within(nav).getByRole("link", { name: /服务器/ })).not.toBeNull();
    expect(within(nav).getByText("未接入·M2")).not.toBeNull();
  });

  it("NewAPI 没登记也不挂标签：它的内容来自指标，不依赖注册表（XM-0035）", async () => {
    renderRoute("/dashboard");
    const nav = await screen.findByRole("navigation", { name: "主导航" });
    expect(await within(nav).findByRole("link", { name: "NewAPI" })).not.toBeNull();
    expect(within(nav).queryByText("未接入·M1")).toBeNull();
  });

  it("读不到注册表时说「读取失败」，不说「未接入」", async () => {
    stubFetch((url) =>
      url.startsWith("/api/v1/services")
        ? fakeResponse(403, {
            error: { code: "FORBIDDEN", message: "缺少 scope: registry.read" },
          })
        : okHandler(url),
    );
    renderRoute("/dashboard");
    const nav = await screen.findByRole("navigation", { name: "主导航" });
    // 拿一次 403 去断言「这个平台没接入」，与新鲜度铁律禁止的是同一类事
    expect((await within(nav).findAllByText("读取失败")).length).toBeGreaterThan(0);
    expect(within(nav).queryByText("未接入·M4")).toBeNull();
    expect(within(nav).queryByRole("link", { name: /NewAPI/ })).toBeNull();
  });
});

describe("详情深链整合（服务器 / 渠道 / 上游）", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("服务器详情使用静态 fixture ID，未知 ID 进入 404", async () => {
    renderRoute("/platforms/server/detail/srv_sin_01");
    expect(await screen.findByRole("heading", { name: "服务器资产详情", level: 2 })).not.toBeNull();
  });

  it("服务器未知 fixture 不回落到详情空壳", async () => {
    renderRoute("/platforms/server/detail/not-real");
    expect(await screen.findByRole("heading", { name: "页面不存在", level: 2 })).not.toBeNull();
  });

  it("平台渠道与上游详情都保留平台上下文", async () => {
    renderRoute("/platforms/sub2api/upstream/detail/ch_demo_01");
    expect(await screen.findByRole("heading", { name: "渠道详情", level: 2 })).not.toBeNull();
    expect(screen.getAllByText("Sub2API").length).toBeGreaterThan(0);
  });

  it("suppliers/new 只读评审蓝图已下线（产品负责人 2026-09-03 裁定），落回动态 upstreamId 路由自带的 not-found 兜底", async () => {
    renderRoute("/platforms/newapi/suppliers/new");
    expect(await screen.findByRole("heading", { name: "页面不存在", level: 2 })).not.toBeNull();
    expect(screen.getByText(/新增上游/)).not.toBeNull();
    expect(screen.queryByRole("heading", { name: "添加上游", level: 2 })).toBeNull();
  });
});

describe("旧路径 redirect 全表（ADMIN-IA v3 §4.1，逐条断言）", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("/platforms/invoice → Sub2API 支付与财务 · 开票", async () => {
    renderRoute("/platforms/invoice");
    expect(await screen.findByRole("tab", { name: "支付与财务", selected: true })).not.toBeNull();
    expect(await screen.findByRole("tab", { name: "开票", selected: true })).not.toBeNull();
  });

  it("/platforms/payment → 治理段的跨平台财务", async () => {
    renderRoute("/platforms/payment");
    expect(await screen.findByRole("heading", { name: "跨平台财务", level: 2 })).not.toBeNull();
  });

  it("/platforms/model-assurance → Sub2API 渠道保障（裁定 #1）", async () => {
    renderRoute("/platforms/model-assurance");
    expect(await screen.findByRole("tab", { name: "渠道保障", selected: true })).not.toBeNull();
  });

  it("?tab=resources → ?tab=upstream（渠道/资源拆成渠道管理 + 上游管理）", async () => {
    renderRoute("/platforms/sub2api?tab=upstream");
    expect(await screen.findByRole("tab", { name: "渠道管理", selected: true })).not.toBeNull();
  });

  it("?tab=suppliers → ?tab=upstream（2026-09-02 裁定：上游管理并入渠道管理页）", async () => {
    renderRoute("/platforms/sub2api?tab=suppliers");
    expect(await screen.findByRole("tab", { name: "渠道管理", selected: true })).not.toBeNull();
    // 落地之后渠道管理页本身正常渲染，不带锚点：07:20 补充裁定把登记簿字段
    // 直接并入了渠道表的行与详情页，已经没有独立区块要滚过去。这里的
    // servicesBody 只登记了一个 degraded 的 sub2api 实例（不满足"恰好一个
    // active"），走的是账号粒度回落分支，口径声明两条分支共用同一句话
    expect(
      await screen.findByText(/渠道管理只做单账号 \/ 单 Key 核算，不做跨账号汇总/),
    ).not.toBeNull();
  });

  it("?tab=requests → ?tab=usage（「请求」改名「请求详情」）", async () => {
    renderRoute("/platforms/sub2api?tab=requests");
    expect(await screen.findByRole("tab", { name: "请求详情", selected: true })).not.toBeNull();
  });

  it("?tab=connection → ?tab=creds", async () => {
    renderRoute("/platforms/sub2api?tab=connection");
    expect(await screen.findByRole("tab", { name: "连接与凭据", selected: true })).not.toBeNull();
  });

  it("?tab=trends → ?tab=overview（裁定 #3：趋势并进各页卡片）", async () => {
    renderRoute("/platforms/sub2api?tab=trends");
    expect(await screen.findByRole("tab", { name: "概览", selected: true })).not.toBeNull();
  });

  it("?tab=operations → /actions（裁定 #4：Action 收进全局「操作与审批」）", async () => {
    renderRoute("/platforms/sub2api?tab=operations");
    expect(await screen.findByRole("heading", { name: "操作与审批", level: 2 })).not.toBeNull();
  });

  it("原型自带的三条服务器别名：containers/certs/logs", async () => {
    renderRoute("/platforms/server?tab=containers");
    expect(await screen.findByRole("tab", { name: "服务与容器", selected: true })).not.toBeNull();
  });

  it("/services → 资源目录（v1 旧路径，保留）", async () => {
    renderRoute("/services");
    expect(await screen.findByRole("heading", { name: "资源目录", level: 2 })).not.toBeNull();
    expect(await screen.findByText("sub2api-dev")).not.toBeNull();
  });

  it("/channels → Sub2API 的渠道管理，而不是概览", async () => {
    renderRoute("/channels");
    expect(await screen.findByRole("heading", { name: "Sub2API", level: 2 })).not.toBeNull();
    expect(await screen.findByRole("tab", { name: "渠道管理", selected: true })).not.toBeNull();
  });
});

describe("未知页签与未知路径：Not Found，不静默回落", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("拼错的 ?tab= 给 Not Found，不再悄悄显示概览", async () => {
    // 旧行为是回落 overview：贴一个拼错的地址过去，屏幕上显示的是概览，
    // 而人以为自己看的是刚贴进去的那一格（交接文档 §8 明令禁止）
    renderRoute("/platforms/sub2api?tab=拼错了");
    expect(await screen.findByRole("heading", { name: "页面不存在", level: 2 })).not.toBeNull();
    expect(screen.getByText(/没有名为 拼错了 的页签/)).not.toBeNull();
  });

  it("别名在这个平台上不存在时也是 Not Found，不硬跳", async () => {
    // ?tab=logs 是服务器的历史别名，Sub2API 没有「监控与告警」这一格
    renderRoute("/platforms/sub2api?tab=logs");
    expect(await screen.findByRole("heading", { name: "页面不存在", level: 2 })).not.toBeNull();
  });

  it("拼错的 ?sub= 就地说清楚，不回落第一格", async () => {
    renderRoute("/platforms/sub2api?tab=finance&sub=拼错了");
    expect(await screen.findByText("没有这个子页签")).not.toBeNull();
    // 平台页头还在：错的是子页签，不是整个平台
    expect(screen.getByRole("heading", { name: "Sub2API", level: 2 })).not.toBeNull();
  });

  it("没匹配上的路径落到 404 页，而不是框架的英文报错页", async () => {
    renderRoute("/查无此页");
    expect(await screen.findByRole("heading", { name: "页面不存在", level: 2 })).not.toBeNull();
    expect(screen.getByText(/查无此页/)).not.toBeNull();
    // 404 渲染在壳内部：导航还在，人有地方可去
    expect(screen.getByRole("navigation", { name: "主导航" })).not.toBeNull();
    expect(screen.getByRole("link", { name: "回到运营工作台" })).not.toBeNull();
  });

  it("目录与注册表都不认识的平台明说未知，不是白屏", async () => {
    renderRoute("/platforms/查无此平台");
    expect(await screen.findByText("未知平台")).not.toBeNull();
  });
});

describe("平台详情：按平台各自的页签集合（ADMIN-IA v3 §2.1）", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("Sub2API 八格，顺序与命名逐字（末格是裁定 #1 恢复的渠道保障）", async () => {
    // 2026-09-02 裁定：「上游管理」并入渠道管理页内区块，不再单独占页签
    renderRoute("/platforms/sub2api");
    const tabs = await screen.findAllByRole("tab");
    expect(tabs.map((tab) => tab.textContent)).toEqual([
      "概览",
      "用户管理",
      "渠道管理",
      "支付与财务",
      "请求详情",
      "连接与凭据",
      "告警",
      "渠道保障",
    ]);
  });

  it("服务器七格：原型把它画成了完整的资产中心，不是统一模板", async () => {
    renderRoute("/platforms/server");
    const tabs = await screen.findAllByRole("tab");
    expect(tabs.map((tab) => tab.textContent)).toEqual([
      "概览",
      "服务器资产",
      "供应商与采购",
      "服务与容器",
      "域名与证书",
      "监控与告警",
      "连接与凭据",
    ]);
  });

  it("`suppliers` 在 Sub2API 上改跳渠道管理页，不是服务器的供应商登记簿", async () => {
    renderRoute("/platforms/sub2api?tab=suppliers");
    expect(
      await screen.findByText(/渠道管理只做单账号 \/ 单 Key 核算，不做跨账号汇总/),
    ).not.toBeNull();
    expect(screen.queryByRole("button", { name: "登记供应商" })).toBeNull();
  });

  it("`overview` 在服务器上是登记簿汇总（XM-SERVER0），不是一屏通用指标卡或蓝图", async () => {
    // 与 suppliers 同一类错误：概览那个 case 若不判平台就直接接管，
    // 服务器的登记簿汇总（XM-SERVER0）会被悄悄换掉。这里同时确认三件事都
    // 没发生：没落到 sub2api/newapi 的「暂无本平台指标」，也没落到旧蓝图
    // （蓝图数字全是「—」，真汇总即使空表也会给出「0」）
    renderRoute("/platforms/server?tab=overview");
    expect(await screen.findByText("月成本合计")).not.toBeNull();
    expect(screen.queryByText("暂无本平台指标")).toBeNull();
  });

  it("`suppliers` 在服务器上是 XM-SERVER0 供应商登记簿——**同一个 value，两种语义**", async () => {
    // 上游管理那一格若不判平台就直接接管，服务器的「供应商与采购」登记簿
    // 会被悄悄换掉：页面看起来完全正常，只是内容变成了别的平台的东西
    renderRoute("/platforms/server?tab=suppliers");
    // 断言真实面板的内容，而不是旧蓝图占位（XM-SERVER0 已把这一格接成登记簿）
    expect(await screen.findByRole("button", { name: "登记供应商" })).not.toBeNull();
    expect(screen.queryByText(/渠道管理只做单账号/)).toBeNull();
    expect(screen.queryByText("「供应商与采购」尚未实现")).toBeNull();
  });

  it("CPA 五格，「渠道保障」按原型字面排在第 4 格", async () => {
    renderRoute("/platforms/cpa");
    const tabs = await screen.findAllByRole("tab");
    expect(tabs.map((tab) => tab.textContent)).toEqual([
      "概览",
      "用户管理",
      "渠道管理",
      "渠道保障",
      "支付与财务",
    ]);
  });

  it("未接入的平台展开页签骨架 + 页头挂状态徽章，而不是一整屏占位", async () => {
    // 摆出页签在这里是**有内容**的：命名、顺序与地址就是本片的产出，
    // 「还没有数据」由页头徽章与每一格里的占位说清楚
    renderRoute("/platforms/cpa");
    expect((await screen.findAllByRole("tab")).length).toBe(5);
    expect(screen.getByRole("heading", { name: "CPA", level: 2 })).not.toBeNull();
    // 侧栏一处、页头徽章一处——两处说的是同一件事，都该在场
    expect(screen.getAllByText("未接入·M4").length).toBe(2);
  });

  it("默认落在概览，且只显示本平台的指标卡", async () => {
    renderRoute("/platforms/sub2api");
    expect(await screen.findByRole("tab", { name: "概览", selected: true })).not.toBeNull();
    // 裁定 #3 砍掉「指标趋势」页签后，历史曲线的落点就是概览这些统计卡
    expect(await screen.findByText("今日充值")).not.toBeNull();
    expect(screen.getAllByText("数据延迟").length).toBeGreaterThan(0);
  });

  it("点「渠道管理」能切过去，渲染的是迁移进来的渠道面板", async () => {
    renderRoute("/platforms/sub2api");
    await screen.findAllByRole("tab");
    // Radix 的页签用 mousedown 激活（不是 click）——fireEvent.click 不带 mousedown，
    // 点了不会切
    fireEvent.mouseDown(screen.getByRole("tab", { name: "渠道管理" }), { button: 0 });
    expect(await screen.findByRole("tab", { name: "渠道管理", selected: true })).not.toBeNull();
    // okHandler 的 finance 汇总是空的，面板据此给空态而不是一张空表
    expect(await screen.findByText("还没有上游账号")).not.toBeNull();
  });

  it("有子页签的格子展开子页签条，逐字对齐 §2.2", async () => {
    renderRoute("/platforms/sub2api?tab=finance");
    expect(await screen.findByRole("tab", { name: "资金概览" })).not.toBeNull();
    for (const label of ["充值订单", "退款与冲正", "利润核算", "开票"]) {
      expect(screen.getByRole("tab", { name: label })).not.toBeNull();
    }
  });

  it("NewAPI 的支付与财务现在三格，末位是「开票」（CR-0005 推翻裁定 #2）", async () => {
    renderRoute("/platforms/newapi?tab=finance");
    expect(await screen.findByRole("tab", { name: "资金与订单" })).not.toBeNull();
    expect(screen.getByRole("tab", { name: "利润核算" })).not.toBeNull();
    expect(screen.getByRole("tab", { name: "开票" })).not.toBeNull();
  });

  it("CPA 的支付与财务保持未接入，但写明具体缺什么（XM-CPA0）", async () => {
    // 从通用的「只重构了导航与路由」占位换成了写明缺口的说明——
    // usage.sqlite 没有支付/充值数据，这件事必须说清楚，不能只说「阶段未到」
    renderRoute("/platforms/cpa?tab=finance");
    expect(await screen.findByText("支付与财务尚未接入")).not.toBeNull();
    expect(screen.getByText(/usage\.sqlite 只记录用量与折算成本/)).not.toBeNull();
  });
});

describe("用户管理页签（交接文档 §9.3、原型 V[\"s2/users\"]）", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("列出用户，并把原型那句 v1 契约边界原样说出来", async () => {
    renderRoute("/platforms/sub2api?tab=users");
    expect(await screen.findByText("张伟")).not.toBeNull();
    // 这一页的边界由**上游契约**决定，不是我们没做
    expect(screen.getByText(/仅提供用户总数与总余额/)).not.toBeNull();
    // fake 供出了逐用户流水（XM-0053），提示条必须说明它们不是真实上游数据
    expect(screen.getByText(/样本数据源/)).not.toBeNull();
  });

  it("缺席的逐用户流水显示「—」，而已知的零余额显示 ¥0.00", async () => {
    // 两者压成一个，人就分不出「这个月没充值」和「我们不知道」
    renderRoute("/platforms/sub2api?tab=users");
    await screen.findByText("试用账号 07");
    const table = within(screen.getByRole("table"));
    expect(table.getAllByText("—").length).toBeGreaterThan(0);
    expect(table.getByText("¥0.00")).not.toBeNull();
  });

  it("邮箱是打过码的，明文一个字都不出现", async () => {
    renderRoute("/platforms/sub2api?tab=users");
    await screen.findByText("张伟");
    expect(screen.getByText("zh***@example.com")).not.toBeNull();
    expect(screen.queryByText(/zhangwei@/)).toBeNull();
  });

  it("上游没记联系方式与从未活跃，各有各的说法", async () => {
    renderRoute("/platforms/sub2api?tab=users");
    await screen.findByText("试用账号 07");
    // 从未活跃 ≠ 很久以前活跃过
    expect(screen.getByText("从未活跃")).not.toBeNull();
  });

  it("区间合计覆盖不全时说明它是下界，而不是把下界当全量", async () => {
    // XM-0053：这两格现在有真数了（fake 供出流水），但样本里有一条给不出——
    // 一个只覆盖 1/2 的合计，和一个真的合计长得一模一样（宪法 12 条）
    renderRoute("/platforms/sub2api?tab=users");
    await screen.findByText("张伟");
    const recharge = (await screen.findByRole("heading", { name: "区间充值", level: 3 })).closest(
      "article",
    ) as HTMLElement;
    expect(within(recharge).getByText("¥1,200.00")).not.toBeNull();
    expect(within(recharge).getByText(/只覆盖 1\/2 位用户/)).not.toBeNull();
    expect(within(recharge).getByText("合计不全")).not.toBeNull();
  });

  it("统计区间控件在场，日期 + 日/周/月（原型 periodControls）", async () => {
    renderRoute("/platforms/sub2api?tab=users");
    await screen.findByText("张伟");
    expect(screen.getByLabelText("统计区间的日期")).not.toBeNull();
    const group = screen.getByRole("group", { name: "统计粒度" });
    expect(within(group).getAllByRole("button").map((b) => b.textContent)).toEqual([
      "日",
      "周",
      "月",
    ]);
    // 区间描述来自服务端回显，不是本地拼的
    expect(screen.getByText("2026-08-28 · 按日查看")).not.toBeNull();
  });

  it("总余额与新鲜度都在场", async () => {
    renderRoute("/platforms/sub2api?tab=users");
    await screen.findByText("张伟");
    // 样本里张伟的余额恰好等于总余额，所以要限定在那一格里找
    const total = screen.getByRole("heading", { name: "所有用户总余额", level: 3 })
      .closest("article") as HTMLElement;
    expect(within(total).getByText("¥12,845.00")).not.toBeNull();
    expect(screen.getAllByText("数据新鲜").length).toBeGreaterThan(0);
  });

  it("CPA 的「用户管理」读的是 API Key 用量，不是终端用户清单（XM-CPA0）", async () => {
    // CPA 的「用户」其实是 API Key，不是终端用户身份——platformusers 域依旧
    // 不认 "cpa"（platformHasUsers 恒为 false），但这一格不再是永远空的占位，
    // 而是走 connectors/cpa 的逐 key 用量（CPAKeysPanel），与 Sub2API/NewAPI
    // 那套终端用户清单完全是两条数据源
    renderRoute("/platforms/cpa?tab=users");
    expect(await screen.findByText(/按 API Key 哈希聚合的当日用量/)).not.toBeNull();
  });
});

describe("平台用户详情完整页（XM-B001）", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("Sub2API 深链进入完整详情页，返回用户管理的可分享地址", async () => {
    renderRoute("/platforms/sub2api/users/u-755f3130323431");

    expect(await screen.findByRole("heading", { name: "张伟", level: 2 })).not.toBeNull();
    const back = screen.getByRole("link", { name: "返回 Sub2API 用户管理" });
    expect(back.getAttribute("href")).toBe("/platforms/sub2api?tab=users");
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("NewAPI 使用自己的详情布局与返回地址", async () => {
    renderRoute("/platforms/newapi/users/u-755f3130323431");

    expect(await screen.findByRole("heading", { name: "张伟", level: 2 })).not.toBeNull();
    expect(screen.getByRole("heading", { name: "区间请求", level: 3 })).not.toBeNull();
    expect(screen.queryByRole("heading", { name: "开票记录", level: 3 })).toBeNull();
    const back = screen.getByRole("link", { name: "返回 NewAPI 用户管理" });
    expect(back.getAttribute("href")).toBe("/platforms/newapi?tab=users");
  });

  it("不支持终端用户的平台走既有 Not Found 边界，且不发送 users Query", async () => {
    const fetchMock = stubFetch(okHandler);
    renderRoute("/platforms/cpa/users/agent-1");

    expect(await screen.findByRole("heading", { name: "页面不存在", level: 2 })).not.toBeNull();
    expect(screen.getByText(/cpa 不支持终端用户详情/)).not.toBeNull();
    expect(fetchMock.mock.calls.some((call) => String(call[0]).includes("/users"))).toBe(false);
  });

  it.each(["u-", "u-c0af", "raw-id"])(
    "非法用户 ID segment %s 走 Not Found，且不发送 users Query",
    async (segment) => {
      const fetchMock = stubFetch(okHandler);
      renderRoute(`/platforms/sub2api/users/${segment}`);
      expect(await screen.findByRole("heading", { name: "页面不存在", level: 2 })).not.toBeNull();
      expect(screen.getByText(/用户 ID 编码无效/)).not.toBeNull();
      expect(fetchMock.mock.calls.some((call) => String(call[0]).includes("/users"))).toBe(false);
    },
  );
});

describe("渠道保障页签（裁定 #1 的 A 落地；XM-ASSURE0 起「保障概览」「历史记录」接被动指标）", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("三个子页签逐字，默认落在保障概览且带窗口选择", async () => {
    renderRoute("/platforms/sub2api?tab=model");
    for (const label of ["保障概览", "检测任务", "历史记录"]) {
      expect(await screen.findByRole("tab", { name: label })).not.toBeNull();
    }
    expect(await screen.findByRole("group", { name: "保障概览统计窗口" })).not.toBeNull();
    expect(screen.getByRole("button", { name: "1 小时", pressed: true })).not.toBeNull();
  });

  it("保障概览显示真实的请求量/成功率/延迟与按模型明细，并原样转述渠道拆分限制", async () => {
    stubFetch((url) =>
      /\/assurance\/overview/.test(url)
        ? fakeResponse(
            200,
            richAssuranceOverviewBody(new URL(url, "http://localhost").searchParams.get("window") ?? "1h"),
          )
        : okHandler(url),
    );
    renderRoute("/platforms/sub2api?tab=model");

    const requestsTile = (await screen.findByRole("heading", { name: "请求量", level: 3 })).closest(
      "article",
    ) as HTMLElement;
    expect(within(requestsTile).getByText("12")).not.toBeNull();

    expect(await screen.findByText("claude-3-opus")).not.toBeNull();
    expect(screen.getByText("(未知模型)")).not.toBeNull();
    // 服务端给出的原因原样出现，前端不重新编一份措辞
    expect(screen.getByText(ASSURANCE_CHANNEL_REASON)).not.toBeNull();
  });

  it("切换窗口会带上新的 window 参数重新请求保障概览", async () => {
    const fetchMock = stubFetch(okHandler);
    renderRoute("/platforms/sub2api?tab=model");
    await screen.findByRole("button", { name: "1 小时", pressed: true });

    fireEvent.click(screen.getByRole("button", { name: "24 小时" }));

    await waitFor(() => {
      expect(
        fetchMock.mock.calls.some(
          (call) => String(call[0]).includes("/assurance/overview") && String(call[0]).includes("window=24h"),
        ),
      ).toBe(true);
    });
    expect(await screen.findByRole("button", { name: "24 小时", pressed: true })).not.toBeNull();
  });

  it("检测任务：空表时显示发起检测入口与空态说明，不冒充有数据", async () => {
    renderRoute("/platforms/sub2api?tab=model&sub=probes");
    expect(await screen.findByRole("button", { name: "发起检测" })).not.toBeNull();
    // dev-header 模式读不到角色声明，Kill Switch 入口默认隐藏（见
    // auth/session.ts currentUserHasRole 的说明）
    expect(screen.queryByRole("button", { name: /Kill Switch/ })).toBeNull();
    expect(await screen.findByText("还没有检测任务")).not.toBeNull();
    // 空表时 DataTableV2 只渲染 emptyState，不渲染表头（ui-admin 既有行为：
    // `rows.length === 0` 直接返回 emptyState）——因此列结构断言放在下面
    // 「显示真实声明」这条有数据的用例里
  });

  it("检测任务：显示真实声明与结果 pill，未启用的行禁用运行按钮并给出人话原因", async () => {
    stubFetch((url) =>
      /\/assurance\/probes/.test(url) ? fakeResponse(200, richProbeListBody()) : okHandler(url),
    );
    renderRoute("/platforms/sub2api?tab=model&sub=probes");

    for (const col of ["任务", "渠道", "目标模型", "策略", "最近一次", "结果", "操作"]) {
      expect(await screen.findByRole("columnheader", { name: col })).not.toBeNull();
    }
    expect(screen.getByText("模型指纹")).not.toBeNull();
    expect(screen.getByText("模型自称与声明一致")).not.toBeNull();
    expect(screen.getByText("从未运行")).not.toBeNull();
    expect(screen.getByText("未启用")).not.toBeNull();

    const runButtons = screen.getAllByRole("button", { name: /^运行/ });
    expect((runButtons[0] as HTMLButtonElement).disabled).toBe(false);
    expect((runButtons[1] as HTMLButtonElement).disabled).toBe(true);
    expect(runButtons[1]!.getAttribute("title")).toBe("该平台检测未启用，请在治理段打开开关");
    // 检测结果为形状/延迟检测这条常驻说明必须出现，替换掉原型的
    // 「这是目标布局」warnbar
    expect(screen.getByText("检测结果为形状与延迟检测，非语义正确性保证。")).not.toBeNull();
  });

  it("历史记录显示真实的近 7 天聚合，缺目录的天数标记覆盖不全", async () => {
    stubFetch((url) =>
      /\/assurance\/history/.test(url) ? fakeResponse(200, richAssuranceHistoryBody()) : okHandler(url),
    );
    renderRoute("/platforms/sub2api?tab=model&sub=history");

    const todayCell = await screen.findByText("2026-08-31");
    expect(screen.getAllByText("目录缺失")).toHaveLength(6);
    expect(screen.getByText("完整")).not.toBeNull();
    // 「涉及模型数」列：今天这一行是 1，历史记录本身不逐个列出模型名——
    // 那属于「保障概览」的按模型明细表，两张表分工不同
    const todayRow = todayCell.closest("tr") as HTMLElement;
    expect(within(todayRow).getByText("1")).not.toBeNull();
    expect(screen.getByText(/近 7 天中有 6 天索引目录缺失/)).not.toBeNull();
    // 历史记录页渲染两张独立卡片，不合并成一张看起来是同一份数据的表
    // （设计稿 §6.2）：被动卡片有自己的标题，主动检测历史卡片另起一段
    expect(screen.getByRole("heading", { name: "被动聚合（近 7 天）", level: 3 })).not.toBeNull();
    expect(screen.getByRole("heading", { name: "主动检测历史", level: 3 })).not.toBeNull();
  });

  it("历史记录：主动检测历史卡片独立请求、独立渲染，与被动聚合互不影响", async () => {
    stubFetch((url) =>
      /\/assurance\/probe-history/.test(url) ? fakeResponse(200, richProbeHistoryBody()) : okHandler(url),
    );
    renderRoute("/platforms/sub2api?tab=model&sub=history");

    expect(await screen.findByText("probe-8f3a1c2d")).not.toBeNull();
    expect(screen.getByText("模型自称与声明一致")).not.toBeNull();
    // 被动卡片仍按 okHandler 的默认空聚合（7 天各 0 请求）正常渲染，不受
    // 主动卡片有数据影响——两条 stubFetch 分支互不干扰
    expect(screen.getByText(/近 7 天索引目录均完整/)).not.toBeNull();
  });
});

describe("支付与财务页签（框架）", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("Sub2API 五个子页签逐字（IA v3 §2.2）", async () => {
    renderRoute("/platforms/sub2api?tab=finance");
    for (const label of ["资金概览", "充值订单", "退款与冲正", "利润核算", "开票"]) {
      expect(await screen.findByRole("tab", { name: label })).not.toBeNull();
    }
  });

  it("默认 Sub2API 资金概览仍由真实路由渲染统计区间与八卡", async () => {
    renderRoute("/platforms/sub2api?tab=finance&sub=overview");
    expect(await screen.findByRole("region", { name: "统计区间" })).not.toBeNull();
    for (const label of ["区间成功到账", "区间待处理", "区间失败", "退款与冲正", "支付手续费", "净现金流入", "使用收入", "渠道毛利"]) {
      expect(await screen.findByRole("heading", { name: label, level: 3 })).not.toBeNull();
    }
  });

  it("NewAPI 现在三格，含「开票」（CR-0005 推翻裁定 #2：不再刻意不补齐）", async () => {
    renderRoute("/platforms/newapi?tab=finance");
    expect(await screen.findByRole("tab", { name: "资金与订单" })).not.toBeNull();
    expect(screen.getByRole("tab", { name: "利润核算" })).not.toBeNull();
    expect(screen.getByRole("tab", { name: "开票" })).not.toBeNull();
  });

  it("NewAPI 资金与订单保留原型的四格布局（区间到账/区间退款/月累计/支付失败），不是 Sub2API 的六卡", async () => {
    renderRoute("/platforms/newapi?tab=finance&sub=orders");
    for (const label of ["区间到账", "区间退款", "月累计", "支付失败"]) {
      expect(await screen.findByRole("heading", { name: label, level: 3 })).not.toBeNull();
    }
    // Sub2API 六卡独有的标签不该出现在 NewAPI 上
    for (const label of ["区间成功到账", "退款与冲正", "支付手续费", "净现金流入"]) {
      expect(screen.queryByRole("heading", { name: label, level: 3 })).toBeNull();
    }
    const refundHeading = screen.getByRole("heading", { name: "区间退款", level: 3 });
    expect(within(refundHeading.closest("article") as HTMLElement).getByText("不适用")).not.toBeNull();
    // Sub2API 独有的「充值订单/退款与冲正」两个独立页签不该出现在 NewAPI 上——
    // NewAPI 的等价内容都在这一个「资金与订单」页签里。
    expect(screen.queryByRole("tab", { name: "充值订单" })).toBeNull();
  });

  it("§9.8 的硬口径印在界面上：充值不是收入", async () => {
    // 把充值当收入，整条利润线从第一步就错了
    renderRoute("/platforms/sub2api?tab=finance");
    expect(await screen.findByText(/「用户充值」不是当期收入/)).not.toBeNull();
  });

  it("开票格是嵌入式开票控制台，未配置来源时诚实显示未接入（CR-0005 第一阶段）", async () => {
    // CR-0005 之前，这一格说的是「阻塞点是契约（CR-0002）」；现在阻塞点已经
    // 不是契约——第一阶段直接嵌入开票系统管理端，不等 CR-0002。路由测试没有
    // 注入 window.__XM_CONFIG__，所以看到的是「未配置」态，不是 iframe；
    // iframe 本身的行为见 ui-admin/EmbeddedConsoleFrame.test.tsx 与
    // components/InvoiceConsolePanel.test.tsx
    renderRoute("/platforms/sub2api?tab=finance&sub=invoices");
    expect(await screen.findByRole("tab", { name: "开票", selected: true })).not.toBeNull();
    expect(screen.getByText(/未配置开票控制台来源（XM_INVOICE_CONSOLE_ORIGIN）/)).not.toBeNull();
    expect(screen.queryByText(/CR-0002/)).toBeNull();
  });

  it("退款格明说上线后也不会有「直接退款」按钮", async () => {
    renderRoute("/platforms/sub2api?tab=finance&sub=refunds");
    expect(await screen.findByText(/不会有「直接退款」的按钮/)).not.toBeNull();
  });
});

describe("未实装页的诚实占位与门禁", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("占位页有页头与「未建」徽章", async () => {
    // 样本第三次搬家：/jobs（XM-JOBS0）→ /finance（XM-FINANCE-GLOBAL0）→
    // /ext/app。**治理段已经一页不剩是占位页了**，只有扩展能力四页仍走
    // PlaceholderPage——而按 ADMIN-IA §5.4 它们是刻意的只读蓝图，会长期留在
    // 这条路径上，所以这个样本不会再被建成真实页而搬走。
    renderRoute("/ext/app");
    expect(await screen.findByRole("heading", { name: "应用与配置", level: 2 })).not.toBeNull();
    expect(within(screen.getByRole("main")).getByText("未建·后置")).not.toBeNull();
  });

  // 「操作与审批页显示 F-B 门禁」原来在这里断言，作为占位页的一个特例。
  // XM-ACTIONS0 把操作目录/执行记录接上真实数据后，/actions 不再是占位页，
  // 断言挪到下面的「操作与审批」独立 describe 块（门禁本身仍然存在并被断言）。

  it("扩展能力四页标注「仅预览、不保存、不发布、不执行」", async () => {
    renderRoute("/ext/publishing");
    expect(await screen.findByText(/仅预览、不保存、不发布、不执行/)).not.toBeNull();
    expect(screen.getByRole("heading", { name: "内容发布", level: 2 })).not.toBeNull();
  });

  it("子页签进 ?sub=，可分享可恢复", async () => {
    // 同上搬到 /ext/app：这一条测的是 PlaceholderPage 自己的
    // 「?sub= 可分享可恢复」，路径必须真的还走 PlaceholderPage 才算数——
    // 用一个已建成的页会让它测成那一页的实现，绿得毫无意义。
    renderRoute("/ext/app?sub=pages");
    expect(await screen.findByRole("tab", { name: "页面配置", selected: true })).not.toBeNull();
  });

  it("占位页拼错的 ?sub= 给 Not Found，不回落第一格", async () => {
    renderRoute("/ext/app?sub=拼错了");
    expect(await screen.findByRole("heading", { name: "页面不存在", level: 2 })).not.toBeNull();
  });

  // 建成页的同一行为长什么样：它们不给 Not Found，而是留在本页显示
  // 「「<x>」子页尚未接入」+ 回第一格的链接（PageState kind="unavailable"）。
  // 两种做法都满足「不静默回落」这条红线，各自的断言在各自的页面测试里
  // （FinancePage/ChangesPage/DesignPage/AuditPage/ActionsPage .test.tsx）。
});



describe("操作与审批（XM-ACTIONS0：操作目录 + 执行记录接真实数据，待审批仍受 F-B 门禁）", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("默认落在操作目录子页签，显示真实的 Action 声明", async () => {
    renderRoute("/actions");
    expect(await screen.findByRole("heading", { name: "操作与审批", level: 2 })).not.toBeNull();
    expect(await screen.findByRole("tab", { name: "操作目录", selected: true })).not.toBeNull();
    expect(await screen.findByText("registry.service.create")).not.toBeNull();
    // 不可执行的动作说明原因，不是单纯灰掉了事
    expect(screen.getByText(/需要 Action Advanced Controls/)).not.toBeNull();
  });

  it("页面级门禁始终可见：目录页不是执行入口（ADMIN-IA §七）", async () => {
    renderRoute("/actions");
    // 措辞改过两次。XM-0030-ENABLE 这次是因为旧措辞**两处都错了**：审批服务
    // 已无条件注入（「尚未在本环境启用」不再是编译期事实），而「本页不提供
    // 任何执行入口」也不成立——「待审批」里已批准的单上就有执行按钮。
    // 现在只断言唯一始终为真的那件事：目录页点不动 L2+，执行只长在单上。
    expect(await screen.findByText(/操作目录只用来看，不是执行入口/)).not.toBeNull();
    expect(screen.getByText(/执行按钮只出现在「待审批」里那张已批准的单上/)).not.toBeNull();
    // 反向钉死那两句收回去的话，别在后面某次改版里被顺手写回来。
    expect(screen.queryByText(/尚未在本环境启用/)).toBeNull();
    expect(screen.queryByText(/本页不提供任何执行入口/)).toBeNull();
  });

  it("待审批子页签接审批队列；未启用时说明原因而不是伪造空队列", async () => {
    renderRoute("/actions?sub=pending");
    expect(await screen.findByRole("tab", { name: "待审批", selected: true })).not.toBeNull();
    // 钉的是「这一格现在是真队列」，所以要找**只有真队列才有**的东西。
    // 用标题不行：旧的静态占位也叫「待审批」，那条断言两边都绿等于没测。
    // 状态筛选器是队列独有的。队列自身的各种状态由
    // components/ApprovalQueue.test.tsx 覆盖。
    expect(await screen.findByRole("combobox", { name: "按状态筛选审批单" })).not.toBeNull();
  });

  it("执行记录子页签接真实分页数据", async () => {
    renderRoute("/actions?sub=runs");
    expect(await screen.findByRole("tab", { name: "执行记录", selected: true })).not.toBeNull();
    expect(await screen.findByText("staff_alice")).not.toBeNull();
    expect(await screen.findByText("registry.service.create")).not.toBeNull();
  });

  it("风险与启用条件子页签展示 ADR-003 的风险等级表", async () => {
    renderRoute("/actions?sub=risk");
    expect(await screen.findByRole("tab", { name: "风险与启用条件", selected: true })).not.toBeNull();
    expect(await screen.findByText("退款、生产基础设施高影响动作、开票关键动作")).not.toBeNull();
  });

  it("未知子页签给 Not Found，不回落操作目录", async () => {
    renderRoute("/actions?sub=拼错了");
    expect(await screen.findByText(/「拼错了」子页尚未接入/)).not.toBeNull();
  });
});

describe("横切行为在迁移后仍然在场", () => {
  beforeEach(() => devLogin());
  afterEach(() => vi.unstubAllGlobals());

  it("演示数据横幅在平台详情页照样挂着（横幅挂在壳上，不跟着页面走）", async () => {
    const demo = { items: metricsBody.items.map((m) => ({ ...m, source: "sub2api-staging" })) };
    stubFetch((url) =>
      url.startsWith("/api/v1/metrics") && !url.startsWith("/api/v1/metrics/history")
        ? fakeResponse(200, demo)
        : okHandler(url),
    );
    renderRoute("/platforms/sub2api");
    expect(await screen.findByText(/当前展示的是演示数据（Fake 连接器）/)).not.toBeNull();
  });

  it("平台详情页缺权限时提示缺哪个 scope，而不是空白", async () => {
    stubFetch((url) =>
      url.startsWith("/api/v1/services")
        ? fakeResponse(403, {
            error: { code: "FORBIDDEN", message: "缺少 scope: registry.read" },
          })
        : okHandler(url),
    );
    renderRoute("/platforms/sub2api");
    expect(await screen.findByText(/registry\.read/)).not.toBeNull();
  });
});

describe("设置页", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("只读展示当前身份与 scope", async () => {
    renderRoute("/settings");
    expect(await screen.findByRole("heading", { name: "设置", level: 2 })).not.toBeNull();
    expect(screen.getByText("dev-operator")).not.toBeNull();
    expect(screen.getByText("registry.read")).not.toBeNull();
    expect(screen.getByText("audit.read")).not.toBeNull();
  });

  it("明说这是「请求时携带的身份」而非服务端授予的权限", async () => {
    renderRoute("/settings");
    expect(await screen.findByText(/服务端为最终裁决者/)).not.toBeNull();
  });

  it("告警规则与静默只保留规则页入口，不复制第二份编辑表单", async () => {
    renderRoute("/settings");
    expect(await screen.findByRole("link", { name: /打开告警与故障规则/ })).not.toBeNull();
    expect(screen.queryByLabelText("Critical 天数")).toBeNull();
  });

  it("设置里的凭据子页显示安全管理边界，并提供返回设置入口", async () => {
    renderRoute("/settings?sub=credentials");
    expect(await screen.findByRole("heading", { name: "凭据管理", level: 2 })).not.toBeNull();
    expect(screen.getByRole("link", { name: "返回设置" }).getAttribute("href")).toBe("/settings");
    expect(screen.getByText(/值不会回读到页面/)).not.toBeNull();
  });

  it("设置拼错子页时不回落到身份设置", async () => {
    renderRoute("/settings?sub=typo");
    expect(await screen.findByText("「typo」设置子页尚未接入")).not.toBeNull();
    expect(screen.getByRole("link", { name: "返回设置" }).getAttribute("href")).toBe("/settings");
    expect(screen.queryByText("身份与权限（只读）")).toBeNull();
  });
});

describe("告警规则子页", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("规则页显示阈值、影响预览入口与 Foundation-B 门禁", async () => {
    renderRoute("/alerts?sub=rules");
    expect(await screen.findByRole("heading", { name: "告警与故障", level: 2 })).not.toBeNull();
    expect(await screen.findByText("Foundation-B / C3c 尚未开放")).not.toBeNull();
    expect(screen.getByRole("button", { name: "预览影响" })).not.toBeNull();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("告警其它子页不回落到活跃告警表", async () => {
    renderRoute("/alerts?sub=incidents");
    expect(await screen.findByText("「故障事件」尚未接入")).not.toBeNull();
    expect(screen.queryByRole("table")).toBeNull();
  });
});

// --- XM-0033 告警中心 --------------------------------------------------------

describe("告警中心页", () => {
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("列表显示严重度、状态、首见/最近、次数与投递状态", async () => {
    renderRoute("/alerts");
    expect(await screen.findByText("指标 sub2api.revenue.daily 同步失败")).not.toBeNull();

    // 严重度与状态各是一个徽章。两者也都出现在筛选下拉里，所以限定在表格内找
    const alertsTable = within(screen.getByRole("table"));
    expect(alertsTable.getByText("严重")).not.toBeNull();
    expect(alertsTable.getByText("未处理")).not.toBeNull();
    // 首见与最近都要显示：只有一个就答不出「这个问题持续了多久」
    expect(screen.getByText(/首次 2026-08-26 10:00:00 UTC/)).not.toBeNull();
    expect(screen.getAllByText(/最近 2026-08-26 10:05:00 UTC/).length).toBeGreaterThan(0);
    // fire_count：抖了一下与持续了两小时的唯一区分依据
    expect(screen.getByText("6")).not.toBeNull();
    // 规则名翻成中文，原始键仍在 detail 之外可查
    expect(screen.getByText("指标同步失败")).not.toBeNull();
  });

  it("投递失败单独成列并显示原因——「已触发但没人被通知到」必须看得见", async () => {
    renderRoute("/alerts");
    await screen.findByText("指标 sub2api.revenue.daily 同步失败");
    expect(screen.getByText("投递失败")).not.toBeNull();
    expect(screen.getByText("telegram: HTTP 502")).not.toBeNull();
  });

  it("已静默的告警不显示成「已解决」，也不消失（静默不是解决）", async () => {
    renderRoute("/alerts");
    await screen.findByText("渠道乙 余额不足");
    // 它仍然在活跃列表里
    const silencedTable = within(screen.getByRole("table"));
    expect(silencedTable.getByText("已静默")).not.toBeNull();
    // 「已解决」在筛选下拉里是一个选项，但**表里没有这一行**——静默不是解决
    expect(silencedTable.queryByText("已解决")).toBeNull();
  });

  it("只有 OPEN / REOPENED 有「确认」按钮（与后端 WHERE 子句同一条规则）", async () => {
    renderRoute("/alerts");
    await screen.findByText("渠道乙 余额不足");
    // 两条告警：一条 OPEN、一条 SILENCED，所以只该有一个确认按钮
    expect(screen.getAllByRole("button", { name: "确认" })).toHaveLength(1);
  });

  it("零告警显示「无活动告警」，并提醒评估任务可能停了", async () => {
    stubFetch((url) =>
      url.startsWith("/api/v1/alerts") ? fakeResponse(200, { items: [] }) : okHandler(url),
    );
    renderRoute("/alerts");
    expect(await screen.findByText("无活动告警")).not.toBeNull();
    // 零告警既可能是好消息，也可能是评估器停了——不能只报喜
    expect(screen.getByText(/评估任务在跑/)).not.toBeNull();
  });

  it("确认走 Action 执行入口，回执带 run_id", async () => {
    const fetchMock = stubFetch((url, init) => {
      if (init?.method === "POST") return fakeResponse(200, { action_run_id: "run-ack-1" });
      return okHandler(url);
    });
    renderRoute("/alerts");
    fireEvent.click(await screen.findByRole("button", { name: "确认" }));

    await waitFor(() => expect(screen.getByText(/已确认，run_id=run-ack-1/)).not.toBeNull());
    const post = fetchMock.mock.calls.find(
      (c) => (c[1] as RequestInit | undefined)?.method === "POST",
    ) as unknown as [string, RequestInit];
    expect(post[0]).toBe("/api/v1/actions/alerts.alert.acknowledge/versions/1/execute");
    // 只带 params；request_id 在头里（后端 DisallowUnknownFields）
    expect(JSON.parse(String(post[1].body))).toEqual({
      params: { alert_id: "aaaa1111-2222-3333-4444-555555555555" },
    });
    expect((post[1].headers as Record<string, string>)["X-Request-ID"]).toBeTruthy();
  });

  it("确认被拒时错误就地显示，告警行不消失", async () => {
    stubFetch((url, init) => {
      if (init?.method === "POST")
        return fakeResponse(403, {
          error: {
            code: "PERMISSION_DENIED",
            message: "缺少权限 alerts.alert.manage",
            request_id: "req-9",
          },
        });
      return okHandler(url);
    });
    renderRoute("/alerts");
    fireEvent.click(await screen.findByRole("button", { name: "确认" }));

    expect(await screen.findByText(/缺少权限 alerts\.alert\.manage/)).not.toBeNull();
    // 一次 403 不该把这条告警从列表里抹掉，人还要看着它继续处理
    expect(screen.getByText("指标 sub2api.revenue.daily 同步失败")).not.toBeNull();
    expect(screen.getByText(/request_id: req-9/)).not.toBeNull();
  });
});

describe("创建静默窗口（写路径）", () => {
  beforeEach(() => devLogin());
  afterEach(() => vi.unstubAllGlobals());

  async function openSilenceDialog() {
    renderRoute("/alerts");
    fireEvent.click(await screen.findByRole("button", { name: "创建静默窗口" }));
    return within(await screen.findByRole("dialog"));
  }

  it("理由为空时不发请求，就地报错", async () => {
    const fetchMock = stubFetch(okHandler);
    const dialog = await openSilenceDialog();
    const before = fetchMock.mock.calls.filter(
      (c) => (c[1] as RequestInit | undefined)?.method === "POST",
    ).length;

    fireEvent.click(dialog.getByRole("button", { name: "创建" }));
    // 没有理由的静默在事后复盘时与「有人手滑」不可区分
    expect(await screen.findByText(/为什么静默/)).not.toBeNull();
    const after = fetchMock.mock.calls.filter(
      (c) => (c[1] as RequestInit | undefined)?.method === "POST",
    ).length;
    expect(after).toBe(before);
  });

  it("默认是全局静默，提交时 rule_key 传空串", async () => {
    const fetchMock = stubFetch((url, init) => {
      if (init?.method === "POST") return fakeResponse(200, { action_run_id: "run-s-1" });
      return okHandler(url);
    });
    const dialog = await openSilenceDialog();
    fireEvent.change(screen.getByLabelText(/理由/), {
      target: { value: "上游 Sub2API 维护窗口" },
    });
    fireEvent.click(dialog.getByRole("button", { name: "创建" }));

    await waitFor(() => expect(screen.getByText(/已创建静默窗口，run_id=run-s-1/)).not.toBeNull());
    const post = fetchMock.mock.calls.find(
      (c) => (c[1] as RequestInit | undefined)?.method === "POST",
    ) as unknown as [string, RequestInit];
    expect(post[0]).toBe("/api/v1/actions/alerts.silence.create/versions/1/execute");
    expect(JSON.parse(String(post[1].body))).toEqual({
      params: { rule_key: "", duration_minutes: 60, reason: "上游 Sub2API 维护窗口" },
    });
  });
});

describe("后台任务路由挂载（XM-OPS-TAILS0：回归修复）", () => {
  // navigation.ts 早把 jobs 标 built:true（navigation.test.ts 断言"后台任务
  // 已实装"），JobsPage.tsx/JobsPage.test.tsx 也一直都在——但这张路由表
  // 里独独少了 `{ path: "jobs", Component: JobsPage }` 这一行，侧栏「后台
  // 任务」点进去落到最后的 `*` 兜底 NotFoundPage。两边测试各自绿掉是因为
  // JobsPage.test.tsx 直接渲染组件、不经真实路由，这里之前没有一条用例
  // 真的导航到 /jobs 去验证。本用例锁定"路由表真的挂了 JobsPage"这件事,
  // 不重复 JobsPage.test.tsx 已经覆盖的字段级断言。
  beforeEach(() => {
    devLogin();
    stubFetch(okHandler);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("/jobs 渲染真实的后台任务页，不是兜底 404", async () => {
    renderRoute("/jobs");
    expect(await screen.findByRole("heading", { name: "后台任务", level: 2 })).not.toBeNull();
    // NotFoundPage 的兜底文案是"页面不存在"／"没有这个地址"；确认没有落到那条分支。
    expect(screen.queryByText("页面不存在")).toBeNull();
    expect(screen.queryByText(/没有这个地址/)).toBeNull();
  });

  it("/jobs?sub=scheduled 渲染「定时任务」页签内容", async () => {
    renderRoute("/jobs?sub=scheduled");
    expect(await screen.findByRole("tab", { name: "定时任务", selected: true })).not.toBeNull();
    expect(await screen.findByText("平台心跳")).not.toBeNull();
  });
});
