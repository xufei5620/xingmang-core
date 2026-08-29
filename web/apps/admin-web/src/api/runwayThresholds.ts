import { apiClient, type ApiClient } from "./client";
import { appApiConfig, type PlatformApiConfig } from "./config";

export interface RunwayThresholdSnapshot {
  environment: string;
  criticalDays: number;
  warningDays: number;
  seriousDays: number;
  revision: number;
  source: "database" | string;
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
}

function draft(raw: RawSnapshot | undefined): RunwayThresholdDraft {
  return {
    criticalDays: raw?.critical_days ?? 0,
    warningDays: raw?.warning_days ?? 0,
    seriousDays: raw?.serious_days ?? 0,
  };
}

function snapshot(raw: RawSnapshot): RunwayThresholdSnapshot {
  return {
    environment: raw.environment ?? "",
    ...draft(raw),
    revision: raw.revision ?? 0,
    source: raw.source ?? "",
    updatedAt: raw.updated_at ?? "",
    updatedBy: raw.updated_by ?? "",
    reason: raw.reason ?? "",
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
  return {
    hasMore: body.has_more ?? false,
    items: (body.items ?? []).map((item) => ({
      environment: item.environment ?? "",
      revision: item.revision ?? 0,
      criticalDays: item.critical_days ?? 0,
      warningDays: item.warning_days ?? 0,
      seriousDays: item.serious_days ?? 0,
      changedAt: item.changed_at ?? "",
      changedBy: item.changed_by ?? "",
      reason: item.reason ?? "",
      requestId: item.request_id ?? "",
      changeSource: item.change_source ?? "",
    })),
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
  return {
    current: draft(body.current),
    proposed: draft(body.proposed),
    currentRevision: body.current_revision ?? 0,
    evaluationAt: body.evaluation_at ?? "",
    coverage: {
      total: body.coverage?.total ?? 0,
      known: body.coverage?.known ?? 0,
      unknownReasons: body.coverage?.unknown_reasons ?? {},
    },
    counts: {
      wouldOpen: body.counts?.would_open ?? 0,
      wouldEscalate: body.counts?.would_escalate ?? 0,
      wouldDeescalate: body.counts?.would_deescalate ?? 0,
      wouldResolve: body.counts?.would_resolve ?? 0,
      unchanged: body.counts?.unchanged ?? 0,
      currentInconsistent: body.counts?.current_inconsistent ?? 0,
    },
    items: (body.items ?? []).map((item) => ({
      accountId: item.account_id ?? "",
      name: item.name ?? "",
      days: item.days ?? null,
      oldLevel: item.old_level ?? "",
      newLevel: item.new_level ?? "",
      currentAlertSeverity: item.current_alert_severity ?? "",
      currentAlertStatus: item.current_alert_status ?? "",
      alertCount: item.alert_count ?? 0,
      transition: item.alert_transition ?? "unchanged",
      consistencyReason: item.consistency_reason ?? null,
      observedAt: item.observed_at ?? null,
    })),
    hasMore: body.has_more ?? false,
  };
}
