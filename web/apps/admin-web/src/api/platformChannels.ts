import { apiClient, type ApiClient } from "./client";
import { executeAction, type ActionRun, type ListOptions } from "./platform";

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

/** XM-CHAN-FIELDS0（代理 chanfields）扩展渠道目录契约要新增的字段
 *  （2026-09-02 07:25 产品负责人补充裁定，JSON 名逐字照裁定原文）。
 *
 *  这一片（XM-CHAN-MERGE0）开工时确认过 chanfields 分支尚未交付任何东西——
 *  这些字段今天在 `GET /api/v1/platforms/{p}/channels` 里恒为 null，不是
 *  "查出来是空"。提前把类型定出来、按这些 JSON 名解析（下面的 `RawPage`
 *  与映射函数），是为了让 chanfields 交付之后**前端不用再改一次**：只要
 *  后端开始下发非 null 值，`ManagedChannelTable.tsx`/`ChannelDetailPage.tsx`
 *  已经在读这些字段的渲染逻辑会自动"亮起来"。 */
export interface PlatformChannelFieldsExtension {
  /** 粗二分"订阅账号/上游渠道"；今天前端自己按绑定账号的 access_method 派生
   *  （`accountRowType`），这里是等 chanfields 提供更权威的来源之后的覆盖项。 */
  kind: "subscription" | "upstream" | null;
  /** 真实供应商/上游名称——不是原型的假 AI 供应商分类。今天前端已经能通过
   *  绑定账号 join 到 `upstream_name`，这里同样是等更权威来源的覆盖项。 */
  vendor: string | null;
  status: string | null;
  capacity: { used: number; limit: number } | null;
  scheduling: { enabled: boolean; priority: number } | null;
  today: { requests: number; successRate: number; costMinor: string; currency: string; scale: number } | null;
  usageWindow: { usedRatio: number; resetsAt: string | null } | null;
  proxy: string | null;
  rateMultiplier: string | null;
  upstreamMultiplier: string | null;
  lastUsedAt: string | null;
  createdAt: string | null;
  expiresAt: string | null;
}

export interface PlatformChannelRow extends PlatformChannelFieldsExtension {
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
    // XM-CHAN-FIELDS0 扩展字段。今天服务端不下发这些键，全部走 `?? null`
    kind?: "subscription" | "upstream" | null;
    vendor?: string | null;
    status?: string | null;
    capacity?: { used?: number; limit?: number } | null;
    scheduling?: { enabled?: boolean; priority?: number } | null;
    today?: { requests?: number; success_rate?: number; cost_minor?: string; currency?: string; scale?: number } | null;
    usage_window?: { used_ratio?: number; resets_at?: string | null } | null;
    proxy?: string | null;
    rate_multiplier?: string | null;
    upstream_multiplier?: string | null;
    last_used_at?: string | null;
    created_at?: string | null;
    expires_at?: string | null;
  }> | null;
  runway_coverage?: { total?: number; known?: number; reasons?: Record<string, number> };
  next_cursor?: string | null;
}

/** 解析 XM-CHAN-FIELDS0 扩展字段。定义字段的关键子键缺失时整体按 null 处理,
 *  不拼一个"半真半假"的对象——比如 `capacity.used` 有、`capacity.limit` 没有,
 *  这时不该显示"3 / undefined"，应该跟完全没有这个字段一样显示未接入。 */
function parseFieldsExtension(item: {
  kind?: "subscription" | "upstream" | null;
  vendor?: string | null;
  status?: string | null;
  capacity?: { used?: number; limit?: number } | null;
  scheduling?: { enabled?: boolean; priority?: number } | null;
  today?: { requests?: number; success_rate?: number; cost_minor?: string; currency?: string; scale?: number } | null;
  usage_window?: { used_ratio?: number; resets_at?: string | null } | null;
  proxy?: string | null;
  rate_multiplier?: string | null;
  upstream_multiplier?: string | null;
  last_used_at?: string | null;
  created_at?: string | null;
  expires_at?: string | null;
}): PlatformChannelFieldsExtension {
  return {
    kind: item.kind ?? null,
    vendor: item.vendor ?? null,
    status: item.status ?? null,
    capacity:
      typeof item.capacity?.used === "number" && typeof item.capacity?.limit === "number"
        ? { used: item.capacity.used, limit: item.capacity.limit }
        : null,
    scheduling:
      item.scheduling && typeof item.scheduling.enabled === "boolean"
        ? { enabled: item.scheduling.enabled, priority: item.scheduling.priority ?? 0 }
        : null,
    today:
      item.today && typeof item.today.requests === "number"
        ? {
            requests: item.today.requests,
            successRate: item.today.success_rate ?? 0,
            costMinor: item.today.cost_minor ?? "0",
            currency: item.today.currency ?? "",
            scale: item.today.scale ?? 0,
          }
        : null,
    usageWindow:
      item.usage_window && typeof item.usage_window.used_ratio === "number"
        ? { usedRatio: item.usage_window.used_ratio, resetsAt: item.usage_window.resets_at ?? null }
        : null,
    proxy: item.proxy ?? null,
    rateMultiplier: item.rate_multiplier ?? null,
    upstreamMultiplier: item.upstream_multiplier ?? null,
    lastUsedAt: item.last_used_at ?? null,
    createdAt: item.created_at ?? null,
    expiresAt: item.expires_at ?? null,
  };
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
      ...parseFieldsExtension(item),
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

// ============================================================================
// 渠道绑定的两个 L1 Action（XM-C-MAP0 后端已注册，之前没有前端 UI）。
//
// internal/platform/finance/channel_binding_actions.go：
//   finance.platform_channel_binding.set@1 / .remove@1，
//   Permission = finance.platform_channel_binding.manage，PrincipalTypes 只认 HUMAN。
// 候选/冲突/已绑定这些状态本身仍来自上面的 listPlatformChannels 只读 Query；
// 这里只是给已经存在的候选态一个可以真的点下去的确认/解绑入口。
// ============================================================================

/** 确认/改绑（`finance.platform_channel_binding.set@1`）需要的权限。 */
export const PLATFORM_CHANNEL_BINDING_MANAGE_PERMISSION = "finance.platform_channel_binding.manage";

export interface ConfirmPlatformChannelBindingParams {
  serviceId: string;
  externalChannelId: string;
  upstreamAccountId: string;
  /** 改绑时用于乐观并发校验；首次绑定留空。 */
  expectedBindingId?: string;
  reason: string;
}

/** 人工确认（或改绑）一条渠道 → 上游账号的映射。
 *
 *  `expectedBindingId` 留空 = 首次绑定；带上现有 `binding.id` = 改绑，
 *  后端按它做乐观并发校验，绑定在这期间被别人改过会拒绝（CONFLICT）。 */
export function confirmPlatformChannelBinding(
  params: ConfirmPlatformChannelBindingParams,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  const allowed: Record<string, string> = {
    service_id: params.serviceId,
    external_channel_id: params.externalChannelId,
    upstream_account_id: params.upstreamAccountId,
    reason: params.reason,
    ...(params.expectedBindingId ? { expected_binding_id: params.expectedBindingId } : {}),
  };
  return executeAction(
    { actionId: "finance.platform_channel_binding.set", version: "1", params: allowed },
    options,
    client,
  );
}

export interface RemovePlatformChannelBindingParams {
  serviceId: string;
  externalChannelId: string;
  /** 必填：解绑同样按乐观并发校验，必须带上当前 `binding.id`。 */
  expectedBindingId: string;
  reason: string;
}

/** 解除一条已确认的绑定。`reason` 必填——理由见 recharge_ratio 那批 Action 的同一条纪律。 */
export function removePlatformChannelBinding(
  params: RemovePlatformChannelBindingParams,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  const allowed: Record<string, string> = {
    service_id: params.serviceId,
    external_channel_id: params.externalChannelId,
    expected_binding_id: params.expectedBindingId,
    reason: params.reason,
  };
  return executeAction(
    { actionId: "finance.platform_channel_binding.remove", version: "1", params: allowed },
    options,
    client,
  );
}
