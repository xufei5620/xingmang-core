import { apiClient, type ApiClient } from "./client";
import { appApiConfig, type PlatformApiConfig } from "./config";

export interface RunwayThresholdSnapshot {
  environment: string;
  criticalDays: number;
  warningDays: number;
  seriousDays: number;
  revision: number;
  source: "database";
  updatedAt: string;
  updatedBy: string;
  reason: string;
}

export interface RunwayThresholdHistoryItem {
  environment: string;
  revision: number;
  criticalDays: number;
  warningDays: number;
  seriousDays: number;
  changedAt: string;
  changedBy: string;
  reason: string;
  requestId: string;
  changeSource: "bootstrap" | "action" | string;
}

export interface RunwayThresholdDraft {
  criticalDays: number;
  warningDays: number;
  seriousDays: number;
}

export type RunwayImpactTransition =
  | "would_open"
  | "would_escalate"
  | "would_deescalate"
  | "would_resolve"
  | "unchanged"
  | "current_inconsistent";

export interface RunwayImpactItem {
  accountId: string;
  name: string;
  days: number | null;
  oldLevel: string;
  newLevel: string;
  currentAlertSeverity: string;
  currentAlertStatus: string;
  alertCount: number;
  transition: RunwayImpactTransition;
  consistencyReason: string | null;
  observedAt: string | null;
}

export interface RunwayImpactPreview {
  current: RunwayThresholdDraft;
  proposed: RunwayThresholdDraft;
  currentRevision: number;
  evaluationAt: string;
  coverage: { total: number; known: number; unknownReasons: Record<string, number> };
  counts: {
    wouldOpen: number;
    wouldEscalate: number;
    wouldDeescalate: number;
    wouldResolve: number;
    unchanged: number;
    currentInconsistent: number;
  };
  items: RunwayImpactItem[];
  hasMore: boolean;
  /** 活跃 R5 是否完整；false 时 transition/counts 只能作不完整提示。 */
  alertCoverageComplete: boolean;
}

export const RUNWAY_THRESHOLD_QUERY = "finance-runway-threshold";
export const RUNWAY_THRESHOLD_HISTORY_QUERY = "finance-runway-threshold-history";
export const RUNWAY_THRESHOLD_PREVIEW_QUERY = "finance-runway-threshold-preview";

interface RawSnapshot {
  environment?: string;
  critical_days?: number;
  warning_days?: number;
  serious_days?: number;
  revision?: number;
  source?: string;
  updated_at?: string;
  updated_by?: string;
  reason?: string;
}

interface RawHistoryItem {
  environment?: string;
  revision?: number;
  critical_days?: number;
  warning_days?: number;
  serious_days?: number;
  changed_at?: string;
  changed_by?: string;
  reason?: string;
  request_id?: string;
  change_source?: string;
}

interface RawPreview {
  current?: RawSnapshot;
  proposed?: RawSnapshot;
  current_revision?: number;
  evaluation_at?: string;
  coverage?: { total?: number; known?: number; unknown_reasons?: Record<string, number> };
  counts?: {
    would_open?: number;
    would_escalate?: number;
    would_deescalate?: number;
    would_resolve?: number;
    unchanged?: number;
    current_inconsistent?: number;
  };
  items?: Array<{
    account_id?: string;
    name?: string;
    days?: number | null;
    old_level?: string;
    new_level?: string;
    current_alert_severity?: string;
    current_alert_status?: string;
    alert_count?: number;
    alert_transition?: RunwayImpactTransition;
    consistency_reason?: string | null;
    observed_at?: string | null;
  }>;
  has_more?: boolean;
  alert_coverage_complete?: boolean;
}

function requiredInteger(value: number | undefined, field: string): number {
  if (value === undefined || !Number.isSafeInteger(value) || value <= 0) {
    throw new Error(`阈值响应缺少有效的 ${field}`);
  }
  return value;
}

function requiredNonNegativeInteger(value: number | undefined, field: string): number {
  if (value === undefined || !Number.isSafeInteger(value) || value < 0) {
    throw new Error(`阈值响应缺少有效的 ${field}`);
  }
  return value;
}

function requiredString(value: string | undefined, field: string): string {
  if (typeof value !== "string" || value.trim() === "") throw new Error(`阈值响应缺少有效的 ${field}`);
  return value;
}

function requiredBoolean(value: boolean | undefined, field: string): boolean {
  if (typeof value !== "boolean") throw new Error(`阈值响应缺少有效的 ${field}`);
  return value;
}

function requiredRecord(value: Record<string, number> | undefined, field: string): Record<string, number> {
  if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(`阈值响应缺少有效的 ${field}`);
  for (const [key, count] of Object.entries(value)) {
    if (!Number.isSafeInteger(count) || count < 0) throw new Error(`阈值响应的 ${field}.${key} 无效`);
  }
  return value;
}

const TRANSITIONS: RunwayImpactTransition[] = [
  "would_open", "would_escalate", "would_deescalate", "would_resolve", "unchanged", "current_inconsistent",
];

function requiredTransition(value: RunwayImpactTransition | undefined): RunwayImpactTransition {
  if (!value || !TRANSITIONS.includes(value)) throw new Error("阈值预览响应包含未知影响类型");
  return value;
}

function draft(raw: RawSnapshot | undefined): RunwayThresholdDraft {
  if (!raw) throw new Error("阈值响应缺少配置快照");
  const values = {
    criticalDays: requiredInteger(raw.critical_days, "critical_days"),
    warningDays: requiredInteger(raw.warning_days, "warning_days"),
    seriousDays: requiredInteger(raw.serious_days, "serious_days"),
  };
  if (!(values.criticalDays < values.warningDays && values.warningDays < values.seriousDays)) {
    throw new Error("阈值响应违反严格递增约束");
  }
  return values;
}

function snapshot(raw: RawSnapshot): RunwayThresholdSnapshot {
  const values = draft(raw);
  if (!raw.environment || raw.source !== "database" || !raw.updated_at || !raw.updated_by || !raw.reason || raw.revision === undefined || !Number.isSafeInteger(raw.revision) || raw.revision <= 0) {
    throw new Error("阈值响应缺少完整的 revision/source/更新时间/变更证据");
  }
  return {
    environment: raw.environment ?? "",
    ...values,
    revision: raw.revision,
    source: raw.source,
    updatedAt: raw.updated_at,
    updatedBy: raw.updated_by,
    reason: raw.reason,
  };
}

export async function getRunwayThresholds(
  client: ApiClient = apiClient,
  config: PlatformApiConfig = appApiConfig,
  options: { signal?: AbortSignal } = {},
): Promise<RunwayThresholdSnapshot> {
  const body = await client.get<RawSnapshot>("/api/v1/finance/runway-thresholds", {
    searchParams: { environment: config.environment },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return snapshot(body);
}

export async function listRunwayThresholdHistory(
  options: { limit?: number; beforeRevision?: number; signal?: AbortSignal } = {},
  client: ApiClient = apiClient,
  config: PlatformApiConfig = appApiConfig,
): Promise<{ items: RunwayThresholdHistoryItem[]; hasMore: boolean }> {
  const body = await client.get<{ items?: RawHistoryItem[]; has_more?: boolean }>(
    "/api/v1/finance/runway-thresholds/history",
    {
      searchParams: {
        environment: config.environment,
        ...(options.limit === undefined ? {} : { limit: String(options.limit) }),
        ...(options.beforeRevision === undefined ? {} : { before_revision: String(options.beforeRevision) }),
      },
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
  if (!body || typeof body !== "object") throw new Error("阈值历史响应不是对象");
  const rawItems = body.items;
  if (!Array.isArray(rawItems)) throw new Error("阈值历史响应缺少 items 数组");
  return {
    items: rawItems.map((item) => ({
      environment: requiredString(item.environment, "history.environment"),
      revision: requiredInteger(item.revision, "history.revision"),
      criticalDays: requiredInteger(item.critical_days, "history.critical_days"),
      warningDays: requiredInteger(item.warning_days, "history.warning_days"),
      seriousDays: requiredInteger(item.serious_days, "history.serious_days"),
      changedAt: requiredString(item.changed_at, "history.changed_at"),
      changedBy: requiredString(item.changed_by, "history.changed_by"),
      reason: requiredString(item.reason, "history.reason"),
      requestId: requiredString(item.request_id, "history.request_id"),
      changeSource: requiredString(item.change_source, "history.change_source"),
    })),
    hasMore: requiredBoolean(body.has_more, "history.has_more"),
  };
}

export async function previewRunwayThresholds(
  draftValue: RunwayThresholdDraft,
  options: { limit?: number; signal?: AbortSignal } = {},
  client: ApiClient = apiClient,
  config: PlatformApiConfig = appApiConfig,
): Promise<RunwayImpactPreview> {
  const body = await client.get<RawPreview>("/api/v1/finance/runway-thresholds/preview", {
    searchParams: {
      environment: config.environment,
      critical_days: String(draftValue.criticalDays),
      warning_days: String(draftValue.warningDays),
      serious_days: String(draftValue.seriousDays),
      ...(options.limit === undefined ? {} : { limit: String(options.limit) }),
    },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  if (!body || typeof body !== "object" || !body.evaluation_at || body.current_revision === undefined || !Number.isSafeInteger(body.current_revision) || body.current_revision <= 0 || !body.coverage || !body.counts || !Array.isArray(body.items) || typeof body.alert_coverage_complete !== "boolean") {
    throw new Error("阈值预览响应缺少 evaluation_at、revision、coverage 或 counts");
  }
  return {
    current: draft(body.current),
    proposed: draft(body.proposed),
    currentRevision: body.current_revision,
    evaluationAt: body.evaluation_at,
    coverage: (() => {
      const total = requiredNonNegativeInteger(body.coverage.total, "coverage.total");
      const known = requiredNonNegativeInteger(body.coverage.known, "coverage.known");
      if (known > total) throw new Error("阈值预览响应的 coverage.known 超过 total");
      return { total, known, unknownReasons: requiredRecord(body.coverage.unknown_reasons, "coverage.unknown_reasons") };
    })(),
    counts: {
      wouldOpen: requiredNonNegativeInteger(body.counts.would_open, "counts.would_open"),
      wouldEscalate: requiredNonNegativeInteger(body.counts.would_escalate, "counts.would_escalate"),
      wouldDeescalate: requiredNonNegativeInteger(body.counts.would_deescalate, "counts.would_deescalate"),
      wouldResolve: requiredNonNegativeInteger(body.counts.would_resolve, "counts.would_resolve"),
      unchanged: requiredNonNegativeInteger(body.counts.unchanged, "counts.unchanged"),
      currentInconsistent: requiredNonNegativeInteger(body.counts.current_inconsistent, "counts.current_inconsistent"),
    },
    items: body.items.map((item) => ({
      accountId: requiredString(item.account_id, "items.account_id"),
      name: requiredString(item.name, "items.name"),
      days: item.days === null || item.days === undefined ? null : requiredNonNegativeInteger(item.days, "items.days"),
      oldLevel: item.old_level ?? "",
      newLevel: item.new_level ?? "",
      currentAlertSeverity: item.current_alert_severity ?? "",
      currentAlertStatus: item.current_alert_status ?? "",
      alertCount: item.alert_count === undefined ? 0 : requiredNonNegativeInteger(item.alert_count, "items.alert_count"),
      transition: requiredTransition(item.alert_transition),
      consistencyReason: item.consistency_reason ?? null,
      observedAt: item.observed_at ?? null,
    })),
    hasMore: requiredBoolean(body.has_more, "preview.has_more"),
    alertCoverageComplete: body.alert_coverage_complete,
  };
}
