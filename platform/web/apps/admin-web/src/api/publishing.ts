import { apiClient, type ApiClient } from "./client";
import {
  executeAction,
  submitAction,
  type ActionOutcome,
  type ActionRun,
} from "./platform";

/** 内容发布（`/api/v1/publishing/*`，XM-EXT-PUBLISHING）。
 *
 *  后端契约见 internal/platform/httpapi/publishing.go。这一层只做取数与
 *  Action 提交，不做任何资格判断——能不能写由服务端裁决。
 *
 *  **这一页最要紧的一件事**：平台今天没有任何出站投递器。排期与审批是真的，
 *  内容不会被发送到任何外部平台。这个事实由服务端逐行给出
 *  （`can_deliver` / `platforms_with_deliverer` / 记录的 `delivered`），
 *  **前端不写死**——写死的那一刻，接上投递器的那天没人会想起来改它。 */

export const PUBLISHING_READ_PERMISSION = "publishing.read";
export const PUBLISHING_MANAGE_PERMISSION = "publishing.manage";
export const PUBLISHING_PUBLISH_PERMISSION = "publishing.publish";

/** 草稿状态。**没有 PUBLISHED**：平台发不出去，多一个终态就是多一处骗人的地方。 */
export type PublishingDraftStatus = "DRAFT" | "SCHEDULED" | "ARCHIVED";

export type PublishingChannelStatus = "ACTIVE" | "PAUSED" | "RETIRED";

/** 渠道平台。**不含站内公告**——Sub2API/NewAPI 的公告接口要求平台向它们发写
 *  请求，被 ADR-018 / ADR-021 排除（见交接文档）。 */
export type PublishingPlatform = "x" | "telegram" | "other";

export const PUBLISHING_PLATFORM_LABELS: Readonly<Record<PublishingPlatform, string>> = {
  x: "X",
  telegram: "Telegram",
  other: "其它社交平台",
};

export const PUBLISHING_DRAFT_STATUS_LABELS: Readonly<Record<PublishingDraftStatus, string>> = {
  DRAFT: "草稿",
  SCHEDULED: "已排期",
  ARCHIVED: "已归档",
};

/** 素材类型（publishing.AssetKind）。素材是**引用**不是上传——平台没有对象存储。
 *
 *  提到这一层而不是留在 PublishingPage 的行内选项里，是为了让
 *  lib/labels.reconcile.test.ts 能对着 model.go 逐条对账：后端加第四种素材
 *  类型时，界面上不该出现一个没有中文名的下拉项。 */
export const PUBLISHING_ASSET_KIND_LABELS: Readonly<Record<string, string>> = {
  image: "图片",
  video: "视频",
  link: "链接",
};

export const PUBLISHING_CHANNEL_STATUS_LABELS: Readonly<
  Record<PublishingChannelStatus, string>
> = {
  ACTIVE: "启用",
  PAUSED: "暂停",
  RETIRED: "已退役",
};

export interface PublishingDraft {
  id: string;
  title: string;
  body: string;
  status: PublishingDraftStatus;
  current_version: number;
  /** RFC3339，空串表示没有排期。 */
  scheduled_at: string;
  asset_ids: string[];
  created_by: string;
  updated_at: string;
}

export interface PublishingRevision {
  version: number;
  title: string;
  body: string;
  scheduled_at: string;
  note: string;
  created_by: string;
  created_at: string;
}

export interface PublishingAsset {
  id: string;
  name: string;
  kind: "image" | "video" | "link";
  uri: string;
  note: string;
  updated_at: string;
}

export interface PublishingChannel {
  id: string;
  platform: PublishingPlatform;
  handle: string;
  display_name: string;
  purpose: string;
  /** 凭据**引用**（secret://…），从来不是凭据本身。 */
  credential_ref: string;
  credential_ref_present: boolean;
  status: PublishingChannelStatus;
  note: string;
  /** 这个渠道**现在能不能真的发出去**。今天恒为 false，由服务端给。 */
  can_deliver: boolean;
  updated_at: string;
}

export interface PublishingRecord {
  id: string;
  draft_id: string;
  draft_version: number;
  channel_id: string;
  scheduled_at: string;
  requested_by: string;
  result: string;
  /** 内容是否真的发出去了。今天恒为 false。 */
  delivered: boolean;
  /** 平台返回编号。今天恒为空串——留列不留数。 */
  external_ref: string;
  /** 那句解释，服务端给的原文。**不在前端复写**：改软它就成了假话。 */
  detail: string;
  created_at: string;
}

interface ListEnvelope<T> {
  items?: T[] | null;
  limit?: number;
  truncated?: boolean;
}

export interface PublishingList<T> {
  items: T[];
  truncated: boolean;
}

function normalizeList<T>(body: ListEnvelope<T>): PublishingList<T> {
  return { items: body.items ?? [], truncated: body.truncated ?? false };
}

export interface ListDraftsOptions {
  status?: PublishingDraftStatus;
  /** 内容日历的排期区间（左闭右开，RFC3339）。 */
  scheduledFrom?: string;
  scheduledTo?: string;
  limit?: number;
  signal?: AbortSignal;
}

export async function listPublishingDrafts(
  options: ListDraftsOptions = {},
  client: ApiClient = apiClient,
): Promise<PublishingList<PublishingDraft>> {
  const body = await client.get<ListEnvelope<PublishingDraft>>("/api/v1/publishing/drafts", {
    searchParams: {
      status: options.status,
      scheduled_from: options.scheduledFrom,
      scheduled_to: options.scheduledTo,
      limit: options.limit === undefined ? undefined : String(options.limit),
    },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return normalizeList(body);
}

export interface PublishingDraftDetail {
  draft: PublishingDraft;
  revisions: PublishingRevision[];
  assets: PublishingAsset[];
}

export async function getPublishingDraft(
  id: string,
  options: { signal?: AbortSignal } = {},
  client: ApiClient = apiClient,
): Promise<PublishingDraftDetail> {
  const body = await client.get<{
    draft: PublishingDraft;
    revisions?: PublishingRevision[] | null;
    assets?: PublishingAsset[] | null;
  }>(`/api/v1/publishing/drafts/${encodeURIComponent(id)}`, {
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return {
    draft: body.draft,
    revisions: body.revisions ?? [],
    assets: body.assets ?? [],
  };
}

export async function listPublishingAssets(
  options: { limit?: number; signal?: AbortSignal } = {},
  client: ApiClient = apiClient,
): Promise<PublishingList<PublishingAsset>> {
  const body = await client.get<ListEnvelope<PublishingAsset>>("/api/v1/publishing/assets", {
    searchParams: { limit: options.limit === undefined ? undefined : String(options.limit) },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return normalizeList(body);
}

export interface PublishingChannelList extends PublishingList<PublishingChannel> {
  /** 此刻**真的**能发出去的平台。今天是空数组，页面据此渲染那条横幅。 */
  platformsWithDeliverer: PublishingPlatform[];
}

export async function listPublishingChannels(
  options: { limit?: number; signal?: AbortSignal } = {},
  client: ApiClient = apiClient,
): Promise<PublishingChannelList> {
  const body = await client.get<
    ListEnvelope<PublishingChannel> & { platforms_with_deliverer?: string[] | null }
  >("/api/v1/publishing/channels", {
    searchParams: { limit: options.limit === undefined ? undefined : String(options.limit) },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return {
    ...normalizeList(body),
    platformsWithDeliverer: (body.platforms_with_deliverer ?? []) as PublishingPlatform[],
  };
}

export async function listPublishingRecords(
  options: { limit?: number; signal?: AbortSignal } = {},
  client: ApiClient = apiClient,
): Promise<PublishingList<PublishingRecord>> {
  const body = await client.get<ListEnvelope<PublishingRecord>>("/api/v1/publishing/records", {
    searchParams: { limit: options.limit === undefined ? undefined : String(options.limit) },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return normalizeList(body);
}

// ---------------------------------------------------------------------------
// 写路径：一律走 Action
// ---------------------------------------------------------------------------

const ACTION_VERSION = "1";

export interface SaveDraftInput {
  /** 省略表示新建。 */
  draftId?: string;
  title: string;
  body: string;
  /** RFC3339；空串表示不排期。 */
  scheduledAt: string;
  note: string;
  assetIds: string[];
}

/** 保存草稿（publishing.draft.set@1，L1 直执行）。 */
export function savePublishingDraft(
  input: SaveDraftInput,
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    {
      actionId: "publishing.draft.set",
      version: ACTION_VERSION,
      params: {
        draft_id: input.draftId ?? "",
        title: input.title,
        body: input.body,
        scheduled_at: input.scheduledAt,
        note: input.note,
        asset_ids: input.assetIds,
      },
    },
    {},
    client,
  );
}

/** 归档草稿（publishing.draft.archive@1，L1）。 */
export function archivePublishingDraft(
  draftId: string,
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "publishing.draft.archive", version: ACTION_VERSION, params: { draft_id: draftId } },
    {},
    client,
  );
}

export interface SaveAssetInput {
  assetId?: string;
  name: string;
  kind: "image" | "video" | "link";
  uri: string;
  note: string;
}

/** 登记素材引用（publishing.asset.set@1，L1）。 */
export function savePublishingAsset(
  input: SaveAssetInput,
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    {
      actionId: "publishing.asset.set",
      version: ACTION_VERSION,
      params: {
        asset_id: input.assetId ?? "",
        name: input.name,
        kind: input.kind,
        uri: input.uri,
        note: input.note,
      },
    },
    {},
    client,
  );
}

/** 删除素材引用（publishing.asset.remove@1，L1）。仍被草稿引用时服务端回 CONFLICT。 */
export function removePublishingAsset(
  assetId: string,
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "publishing.asset.remove", version: ACTION_VERSION, params: { asset_id: assetId } },
    {},
    client,
  );
}

export interface SaveChannelInput {
  platform: PublishingPlatform;
  handle: string;
  displayName: string;
  purpose: string;
  /** **只收引用**（secret://<scope>/<name>），空串表示还没登记。
   *  粘凭据本身会被服务端当场拒（领域层 + 库层两道），且错误里不回显原串。 */
  credentialRef: string;
  note: string;
}

/** 登记渠道账号（publishing.channel.set@1，L1）。 */
export function savePublishingChannel(
  input: SaveChannelInput,
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    {
      actionId: "publishing.channel.set",
      version: ACTION_VERSION,
      params: {
        platform: input.platform,
        handle: input.handle,
        display_name: input.displayName,
        purpose: input.purpose,
        credential_ref: input.credentialRef,
        note: input.note,
      },
    },
    {},
    client,
  );
}

/** 启停/退役渠道（publishing.channel.set_status@1，L1）。 */
export function setPublishingChannelStatus(
  channelId: string,
  status: PublishingChannelStatus,
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    {
      actionId: "publishing.channel.set_status",
      version: ACTION_VERSION,
      params: { channel_id: channelId, status },
    },
    {},
    client,
  );
}

/** 提起一次对外发布（publishing.publish.submit@1，**L3**）。
 *
 *  用 submitAction 而不是 executeAction：L3 一定被内核受理成审批单（202 +
 *  单号），调用点必须把「已受理，尚未发生」这种结局画出来。用 executeAction
 *  会抛 ApprovalRequiredError——那是给没有承接界面的调用点用的。
 *
 *  reason 由人自己写，**不在任何一层兜底生成套话**：它会原样写进审批单给
 *  审批人看，一句机器话等于让审批人对着空气做判断。 */
export function submitPublish(
  draftId: string,
  channelId: string,
  reason: string,
  client: ApiClient = apiClient,
): Promise<ActionOutcome> {
  return submitAction(
    {
      actionId: "publishing.publish.submit",
      version: ACTION_VERSION,
      params: { draft_id: draftId, channel_id: channelId },
      reason,
    },
    {},
    client,
  );
}

/** 本页发布动作的 Action ID。审批队列那一格用它把审批中心的单筛出来。 */
export const PUBLISHING_PUBLISH_ACTION_ID = "publishing.publish.submit";
