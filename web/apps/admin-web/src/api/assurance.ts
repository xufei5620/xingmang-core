import type { FreshnessContract } from "@xingmang/ui-admin";
import { apiClient, FeatureNotMountedError, looksLikeUnmountedRoute, type ApiClient } from "./client";
import type { ListOptions } from "./platform";

/** 渠道保障 · 保障概览 / 历史记录（XM-ASSURE0 第一片，被动指标）。
 *
 *  数据源与「请求详情」（api/requests.ts）完全相同——都是记录代理落盘的
 *  请求审计索引，只是这里读的是服务端已经聚合好的状态分类/延迟百分位/
 *  按模型分组，而不是逐条列表。
 *
 *  **不提供按渠道拆分**：请求审计的磁盘格式本身不采集渠道/上游字段，
 *  这不是「这一片没做」，后端在每次响应里显式给出
 *  `channel_breakdown_supported: false` 与具体原因（见
 *  `channelBreakdownReason`），页面必须原样转述这条限制，不能自己拿
 *  model 或别的字段冒充渠道维度。 */

/** 保障概览允许选择的窗口，与后端 reqlog.AssuranceWindowPreset 逐字对应。 */
export type AssuranceWindow = "15m" | "1h" | "24h";

export const ASSURANCE_WINDOWS: readonly AssuranceWindow[] = ["15m", "1h", "24h"];

export const ASSURANCE_WINDOW_LABELS: Record<AssuranceWindow, string> = {
  "15m": "15 分钟",
  "1h": "1 小时",
  "24h": "24 小时",
};

export interface AssuranceStatusClasses {
  /** 2xx。 */
  success: number;
  /** 4xx。 */
  clientError: number;
  /** 5xx。 */
  serverError: number;
  /** status==0：响应头已收到、读响应体中途连接中断——不是「完全没连上」
   *  （那种情况响应头之前就失败，reqlog 整条都不落盘，永远不出现在这里）。 */
  disconnected: number;
  /** 1xx/3xx 或其余取值，占比理应恒为 0。 */
  other: number;
}

/** 延迟百分位。sampleCount 为 0 时三个百分位都是 null——没有样本时不存在
 *  百分位这个概念，不能显示成 0ms（宪法 12 条同一条纪律用在延迟上）。 */
export interface AssuranceLatencyPercentiles {
  sampleCount: number;
  p50Ms: number | null;
  p95Ms: number | null;
  p99Ms: number | null;
}

export interface AssuranceModelRow {
  /** 空串表示 reqlog 没记到模型名，必须显示成「未知模型」而不是空白。 */
  model: string;
  requestCount: number;
  statusClasses: AssuranceStatusClasses;
  durationMs: AssuranceLatencyPercentiles;
  /** 只统计测量过首字节的那部分请求，sampleCount 可能小于 requestCount。 */
  ttfbMs: AssuranceLatencyPercentiles;
}

/** 这次聚合覆盖了多少天的目录、其中几天缺失、跳过了多少坏行。
 *  missingDays > 0 表示覆盖不全，不能当成「这段时间确实没有请求」。 */
export interface AssuranceCoverage {
  spannedDays: number;
  missingDays: number;
  badLines: number;
}

/** 一次聚合结果的公共形状：保障概览的一个窗口，与历史记录的每一天，
 *  都是这个形状。 */
export interface AssuranceAggregate {
  /** 只在历史记录里非空（YYYY-MM-DD 业务日）；保障概览里恒为空串。 */
  day: string;
  since: string;
  until: string;
  requestCount: number;
  statusClasses: AssuranceStatusClasses;
  durationMs: AssuranceLatencyPercentiles;
  ttfbMs: AssuranceLatencyPercentiles;
  /** 按 requestCount 降序；超过上限时 modelsTruncated 为 true。 */
  models: AssuranceModelRow[];
  modelsTruncated: boolean;
  coverage: AssuranceCoverage;
}

export interface AssuranceOverview extends AssuranceAggregate {
  source: string;
  window: AssuranceWindow;
  /** 恒为 false：见本文件顶部说明。 */
  channelBreakdownSupported: boolean;
  channelBreakdownReason: string;
  retentionDays: number;
  freshness: FreshnessContract;
}

export interface AssuranceHistoryDay extends AssuranceAggregate {
  /** coverage.missingDays > 0 的布尔简写。 */
  missing: boolean;
}

export interface AssuranceHistory {
  source: string;
  days: AssuranceHistoryDay[];
  channelBreakdownSupported: boolean;
  channelBreakdownReason: string;
  retentionDays: number;
  freshness: FreshnessContract;
}

// --- 原始响应形状与解析 ---

interface RawStatusClasses {
  success: number;
  client_error: number;
  server_error: number;
  disconnected: number;
  other: number;
}

interface RawLatencyPercentiles {
  sample_count: number;
  p50_ms: number | null;
  p95_ms: number | null;
  p99_ms: number | null;
}

interface RawModelRow {
  model: string;
  request_count: number;
  status_classes: RawStatusClasses;
  duration_ms: RawLatencyPercentiles;
  ttfb_ms: RawLatencyPercentiles;
}

interface RawCoverage {
  spanned_days: number;
  missing_days: number;
  bad_lines: number;
}

interface RawAggregate {
  day?: string;
  since: string;
  until: string;
  request_count: number;
  status_classes: RawStatusClasses;
  duration_ms: RawLatencyPercentiles;
  ttfb_ms: RawLatencyPercentiles;
  models: RawModelRow[] | null;
  models_truncated: boolean;
  coverage: RawCoverage;
}

interface RawOverviewBody extends RawAggregate {
  source: string;
  window: string;
  channel_breakdown_supported: boolean;
  channel_breakdown_reason: string;
  retention_days: number;
  freshness: FreshnessContract;
}

interface RawHistoryDayBody extends RawAggregate {
  missing: boolean;
}

interface RawHistoryBody {
  source: string;
  days: RawHistoryDayBody[] | null;
  channel_breakdown_supported: boolean;
  channel_breakdown_reason: string;
  retention_days: number;
  freshness: FreshnessContract;
}

function toFiniteNonNegativeInt(value: unknown, label: string): number {
  if (typeof value !== "number" || !Number.isFinite(value) || !Number.isInteger(value) || value < 0) {
    throw new Error(`渠道保障 ${label} 格式异常`);
  }
  return value;
}

function toNullableInt(value: unknown, label: string): number | null {
  if (value === null || value === undefined) return null;
  return toFiniteNonNegativeInt(value, label);
}

function parseStatusClasses(raw: RawStatusClasses, label: string): AssuranceStatusClasses {
  return {
    success: toFiniteNonNegativeInt(raw?.success, `${label}.success`),
    clientError: toFiniteNonNegativeInt(raw?.client_error, `${label}.client_error`),
    serverError: toFiniteNonNegativeInt(raw?.server_error, `${label}.server_error`),
    disconnected: toFiniteNonNegativeInt(raw?.disconnected, `${label}.disconnected`),
    other: toFiniteNonNegativeInt(raw?.other, `${label}.other`),
  };
}

function parseLatencyPercentiles(raw: RawLatencyPercentiles, label: string): AssuranceLatencyPercentiles {
  const sampleCount = toFiniteNonNegativeInt(raw?.sample_count, `${label}.sample_count`);
  const p50 = toNullableInt(raw?.p50_ms, `${label}.p50_ms`);
  const p95 = toNullableInt(raw?.p95_ms, `${label}.p95_ms`);
  const p99 = toNullableInt(raw?.p99_ms, `${label}.p99_ms`);
  // 没有样本时三个百分位必须都是 null；有样本时都不该是 null——
  // 这条一致性只在前端校验一次，后端已经保证，这里是双重保险，不是权威判据。
  if (sampleCount === 0 && (p50 !== null || p95 !== null || p99 !== null)) {
    throw new Error(`渠道保障 ${label} 格式异常：无样本却给出了百分位`);
  }
  if (sampleCount > 0 && (p50 === null || p95 === null || p99 === null)) {
    throw new Error(`渠道保障 ${label} 格式异常：有样本却缺百分位`);
  }
  return { sampleCount, p50Ms: p50, p95Ms: p95, p99Ms: p99 };
}

function parseModelRow(raw: RawModelRow): AssuranceModelRow {
  return {
    model: raw.model,
    requestCount: toFiniteNonNegativeInt(raw.request_count, "models[].request_count"),
    statusClasses: parseStatusClasses(raw.status_classes, "models[].status_classes"),
    durationMs: parseLatencyPercentiles(raw.duration_ms, "models[].duration_ms"),
    ttfbMs: parseLatencyPercentiles(raw.ttfb_ms, "models[].ttfb_ms"),
  };
}

function parseCoverage(raw: RawCoverage): AssuranceCoverage {
  return {
    spannedDays: toFiniteNonNegativeInt(raw?.spanned_days, "coverage.spanned_days"),
    missingDays: toFiniteNonNegativeInt(raw?.missing_days, "coverage.missing_days"),
    badLines: toFiniteNonNegativeInt(raw?.bad_lines, "coverage.bad_lines"),
  };
}

function parseAggregate(raw: RawAggregate): AssuranceAggregate {
  return {
    day: raw.day ?? "",
    since: raw.since,
    until: raw.until,
    requestCount: toFiniteNonNegativeInt(raw.request_count, "request_count"),
    statusClasses: parseStatusClasses(raw.status_classes, "status_classes"),
    durationMs: parseLatencyPercentiles(raw.duration_ms, "duration_ms"),
    ttfbMs: parseLatencyPercentiles(raw.ttfb_ms, "ttfb_ms"),
    models: (raw.models ?? []).map(parseModelRow),
    modelsTruncated: raw.models_truncated,
    coverage: parseCoverage(raw.coverage),
  };
}

function parseAssuranceWindow(raw: string): AssuranceWindow {
  if (raw === "15m" || raw === "1h" || raw === "24h") return raw;
  throw new Error(`渠道保障响应 window 格式异常：${raw}`);
}

/** 「保障概览 / 历史记录」在当前环境未启用的说明文案。
 *
 *  与 REQUESTS_NOT_MOUNTED_DESCRIPTION（api/requests.ts）同一条纪律：
 *  这条链路只在 `XM_REQLOG_MODE=file` 时挂载（见后端 newChannelAssuranceService
 *  的说明——聚合直接扫描记录代理落盘的数据，fake/real 两种模式没有真实
 *  磁盘数据可读），未挂载时端点整组不存在（404），前端据此分辨「没接」与
 *  「坏了」，不能显示成加载失败。 */
const ASSURANCE_NOT_MOUNTED_DESCRIPTION =
  "渠道保障的被动指标在当前环境未启用（需要 XM_REQLOG_MODE=file：这批聚合直接读取记录代理落盘的请求审计数据）。接入后会自动出现，无需手动开启。";

/** 读取某平台在指定窗口内的被动保障聚合（需 request.read，与「请求详情」
 *  列表同一个权限）。 */
export async function getPlatformAssuranceOverview(
  platform: string,
  window: AssuranceWindow,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<AssuranceOverview> {
  let body: RawOverviewBody;
  try {
    body = await client.get<RawOverviewBody>(
      `/api/v1/platforms/${encodeURIComponent(platform)}/assurance/overview`,
      {
        searchParams: { window },
        ...(options.signal ? { signal: options.signal } : {}),
      },
    );
  } catch (error) {
    if (looksLikeUnmountedRoute(error)) {
      throw new FeatureNotMountedError(error, ASSURANCE_NOT_MOUNTED_DESCRIPTION);
    }
    throw error;
  }
  return {
    ...parseAggregate(body),
    source: body.source,
    window: parseAssuranceWindow(body.window),
    channelBreakdownSupported: body.channel_breakdown_supported,
    channelBreakdownReason: body.channel_breakdown_reason,
    retentionDays: body.retention_days,
    freshness: body.freshness,
  };
}

/** 读取某平台最近 7 天的逐日被动保障聚合（需 request.read）。固定 7 天,
 *  不接受任意区间——服务端没有可用的降采样 rollup 前，这是唯一的历史来源,
 *  显式钉死上限，不让这条链路被当成数据导出口。 */
export async function getPlatformAssuranceHistory(
  platform: string,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<AssuranceHistory> {
  let body: RawHistoryBody;
  try {
    body = await client.get<RawHistoryBody>(
      `/api/v1/platforms/${encodeURIComponent(platform)}/assurance/history`,
      options.signal ? { signal: options.signal } : {},
    );
  } catch (error) {
    if (looksLikeUnmountedRoute(error)) {
      throw new FeatureNotMountedError(error, ASSURANCE_NOT_MOUNTED_DESCRIPTION);
    }
    throw error;
  }
  return {
    source: body.source,
    days: (body.days ?? []).map((d) => ({ ...parseAggregate(d), missing: d.missing })),
    channelBreakdownSupported: body.channel_breakdown_supported,
    channelBreakdownReason: body.channel_breakdown_reason,
    retentionDays: body.retention_days,
    freshness: body.freshness,
  };
}
