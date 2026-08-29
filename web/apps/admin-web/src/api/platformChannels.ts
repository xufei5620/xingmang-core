import { apiClient, type ApiClient } from "./client";
import type { ListOptions } from "./platform";

export type CandidateState = "unmapped" | "candidate" | "conflict" | "orphan";
export type CandidateEvidenceStatus = "sufficient" | "insufficient" | "conflicting";

export interface PlatformChannelRef {
  serviceId: string;
  externalChannelId: string;
}

export interface PlatformChannelInventory {
  state: string;
  source: string;
  observedAt: string | null;
  complete: boolean;
  truncated: boolean;
  reportedCount: number | null;
  fetchedCount: number;
  coveragePartial: boolean;
  evidence: string;
}

export interface PlatformChannelRow {
  channelRef: PlatformChannelRef;
  name: string;
  binding: { id: string; upstreamAccountId: string; validFrom: string; reason: string } | null;
  candidate: {
    state: CandidateState;
    evidenceStatus: CandidateEvidenceStatus;
    upstreamAccountIds: string[];
    reasonCodes: string[];
    platformAssignmentMissing: boolean;
    inventoryUnknown: boolean;
  };
  economics: Record<string, unknown> | null;
  economicsState: string;
  conflicts: string[];
  health: Record<string, unknown> | null;
  models: Record<string, unknown> | null;
  assurance: Record<string, unknown> | null;
  runway: Record<string, unknown> | null;
  observed: { source: string; observedAt: string | null; isStale: boolean };
}

export interface PlatformChannelPage {
  service: { id: string; serviceType: string; instanceId: string; environment: string };
  inventory: PlatformChannelInventory;
  from: string;
  to: string;
  items: PlatformChannelRow[];
  runwayCoverage: { total: number; known: number; reasons: Record<string, number> };
  nextCursor: string | null;
}

interface RawPage {
  service?: { id?: string; service_type?: string; instance_id?: string; environment?: string };
  inventory?: { state?: string; source?: string; observed_at?: string | null; complete?: boolean; truncated?: boolean; reported_count?: number | null; fetched_count?: number; coverage_partial?: boolean; evidence?: string };
  from?: string;
  to?: string;
  items?: Array<{
    channel_ref?: { service_id?: string; external_channel_id?: string };
    name?: string;
    binding?: { id?: string; upstream_account_id?: string; valid_from?: string; reason?: string } | null;
    candidate?: { state?: CandidateState; evidence_status?: CandidateEvidenceStatus; upstream_account_ids?: string[]; reason_codes?: string[]; platform_assignment_missing?: boolean; inventory_unknown?: boolean };
    economics?: Record<string, unknown> | null;
    economics_state?: string;
    conflicts?: string[];
    health?: Record<string, unknown> | null;
    models?: Record<string, unknown> | null;
    assurance?: Record<string, unknown> | null;
    runway?: Record<string, unknown> | null;
    observed?: { source?: string; observed_at?: string | null; is_stale?: boolean };
  }> | null;
  runway_coverage?: { total?: number; known?: number; reasons?: Record<string, number> };
  next_cursor?: string | null;
}

export interface PlatformChannelListOptions extends ListOptions {
  from?: string;
  to?: string;
  limit?: number;
  cursor?: string;
}

export async function listPlatformChannels(
  platform: "sub2api" | "newapi",
  serviceId: string,
  options: PlatformChannelListOptions = {},
  client: ApiClient = apiClient,
): Promise<PlatformChannelPage> {
  const body = await client.get<RawPage>(`/api/v1/platforms/${encodeURIComponent(platform)}/channels`, {
    searchParams: {
      service_id: serviceId,
      ...(options.from ? { from: options.from } : {}),
      ...(options.to ? { to: options.to } : {}),
      ...(options.limit === undefined ? {} : { limit: String(options.limit) }),
      ...(options.cursor ? { cursor: options.cursor } : {}),
    },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return {
    service: {
      id: body.service?.id ?? serviceId,
      serviceType: body.service?.service_type ?? platform,
      instanceId: body.service?.instance_id ?? "",
      environment: body.service?.environment ?? "",
    },
    inventory: {
      state: body.inventory?.state ?? "unknown",
      source: body.inventory?.source ?? "",
      observedAt: body.inventory?.observed_at ?? null,
      complete: body.inventory?.complete ?? false,
      truncated: body.inventory?.truncated ?? false,
      reportedCount: body.inventory?.reported_count ?? null,
      fetchedCount: body.inventory?.fetched_count ?? 0,
      coveragePartial: body.inventory?.coverage_partial ?? false,
      evidence: body.inventory?.evidence ?? "",
    },
    from: body.from ?? "",
    to: body.to ?? "",
    items: (body.items ?? []).map((item) => ({
      channelRef: {
        serviceId: item.channel_ref?.service_id ?? serviceId,
        externalChannelId: item.channel_ref?.external_channel_id ?? "",
      },
      name: item.name ?? "",
      binding: item.binding?.id
        ? { id: item.binding.id, upstreamAccountId: item.binding.upstream_account_id ?? "", validFrom: item.binding.valid_from ?? "", reason: item.binding.reason ?? "" }
        : null,
      candidate: {
        state: item.candidate?.state ?? "unmapped",
        evidenceStatus: item.candidate?.evidence_status ?? "insufficient",
        upstreamAccountIds: item.candidate?.upstream_account_ids ?? [],
        reasonCodes: item.candidate?.reason_codes ?? [],
        platformAssignmentMissing: item.candidate?.platform_assignment_missing ?? false,
        inventoryUnknown: item.candidate?.inventory_unknown ?? false,
      },
      economics: item.economics ?? null,
      economicsState: item.economics_state ?? "unknown",
      conflicts: item.conflicts ?? [],
      health: item.health ?? null,
      models: item.models ?? null,
      assurance: item.assurance ?? null,
      runway: item.runway ?? null,
      observed: {
        source: item.observed?.source ?? "",
        observedAt: item.observed?.observed_at ?? null,
        isStale: item.observed?.is_stale ?? false,
      },
    })),
    runwayCoverage: {
      total: body.runway_coverage?.total ?? 0,
      known: body.runway_coverage?.known ?? 0,
      reasons: body.runway_coverage?.reasons ?? {},
    },
    nextCursor: body.next_cursor ?? null,
  };
}

export function platformChannelRowKey(row: PlatformChannelRow): string {
  return `${row.channelRef.serviceId}:${row.channelRef.externalChannelId}`;
}
