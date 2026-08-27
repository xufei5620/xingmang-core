import type { FreshnessContract } from "@xingmang/ui-admin";
import { apiClient, type ApiClient } from "./client";
import type { ListOptions } from "./platform";

/** 请求详情（XM-0039）。数据源是生产上已在运行的外挂「请求审计系统」(reqlog)，
 *  平台只是带权限与审计的只读网关——正文永不落平台库、不缓存、默认不可导出。
 *
 *  两个端点、两个权限，分级不是「谨慎一点」而是本能力成立的前提：
 *
 *    request.read          元数据列表，回答「这个人用得多不多」
 *    request.content.read  完整正文，回答「这个人问了什么」
 *
 *  后者每读一次就在审计链上留一条 request.content.viewed（交接文档 §9.4）。 */

/** 一条请求元数据（后端 httpapi/requests.go requestSummaryItem）。
 *
 *  **没有正文字段**，这不是「前端暂时没用到」——摘要与正文的权限分级
 *  就落在这个类型边界上。 */
export interface RequestSummary {
  id: string;
  /** 来源平台：sub2api / newapi。 */
  source: string;
  occurred_at: string;
  /** 空串表示令牌没映射到用户——必须显示成「未映射」而不是空白。 */
  username: string;
  token_prefix: string;
  model: string;
  /** 0 表示 reqlog 没记到状态码（连接中断）。 */
  status: number;
  duration_ms: number;
  /** null = 未记录；0 是合法观测值（缓存命中）。两者不可混同。 */
  ttfb_ms: number | null;
  tokens_in: number;
  tokens_out: number;
  tokens_cache: number;
  stream: boolean;
  upstream_request_id: string;
  /** 已在后端脱敏（形如 203.0.113.x）。前端不做二次处理。 */
  client_ip: string;
}

export interface RequestListPage {
  items: RequestSummary[];
  /** 空串表示已经翻到底。不透明字符串，前端不解析、不构造。 */
  nextCursor: string;
  /** reqlog 的保留窗口。界面靠它说清「只覆盖最近 N 天」。 */
  retentionDays: number;
  /** 回答这次读取的 reqlog 实例；命中已知演示实例时挂演示横幅。 */
  dataSource: string;
  freshness: FreshnessContract;
}

export interface RequestMessage {
  role: string;
  content: string;
  truncated: boolean;
  /** 截断前的字节数。界面靠它说「只显示了 X / 共 Y」。 */
  original_bytes: number;
}

export interface RequestPayload {
  body: string;
  truncated: boolean;
  original_bytes: number;
  content_type: string;
}

export interface RequestContent {
  summary: RequestSummary;
  messages: RequestMessage[];
  /** 区分「请求体里就没有消息」与「我们没解析出来」——后者要让人去看原文。 */
  messagesParsed: boolean;
  finalReply: string;
  finalReplyTruncated: boolean;
  finalReplyBytes: number;
  rawRequest: RequestPayload;
  rawResponse: RequestPayload;
  dataSource: string;
  freshness: FreshnessContract;
}

/** 状态过滤口径。与后端逐字对应（reqlog.StatusFilter）。 */
export type RequestStatusFilter = "" | "success" | "error";

export interface RequestListFilters {
  username?: string;
  model?: string;
  status?: RequestStatusFilter;
  /** RFC3339。 */
  since?: string;
  until?: string;
}

export interface RequestListOptions extends ListOptions, RequestListFilters {
  limit?: number;
  cursor?: string;
}

/** 列表每页拉多少条。 */
export const REQUEST_PAGE_SIZE = 50;

interface RawRequestPage {
  items: RequestSummary[] | null;
  next_cursor?: string;
  retention_days?: number;
  data_source?: string;
  freshness: FreshnessContract;
}

interface RawRequestContent {
  summary: RequestSummary;
  messages: RequestMessage[] | null;
  messages_parsed: boolean;
  final_reply: string;
  final_reply_truncated: boolean;
  final_reply_bytes: number;
  raw_request: RequestPayload;
  raw_response: RequestPayload;
  data_source?: string;
  freshness: FreshnessContract;
}

/** 空串不进 URL：「不传」和「传空串」对后端不是一回事（见 client.ts）。 */
function omitEmpty(value: string | undefined): string | undefined {
  const trimmed = (value ?? "").trim();
  return trimmed === "" ? undefined : trimmed;
}

/** 列出某平台的请求元数据（需 request.read）。 */
export async function listPlatformRequests(
  platform: string,
  options: RequestListOptions = {},
  client: ApiClient = apiClient,
): Promise<RequestListPage> {
  const body = await client.get<RawRequestPage>(
    `/api/v1/platforms/${encodeURIComponent(platform)}/requests`,
    {
      searchParams: {
        limit: String(options.limit ?? REQUEST_PAGE_SIZE),
        cursor: omitEmpty(options.cursor),
        username: omitEmpty(options.username),
        model: omitEmpty(options.model),
        status: omitEmpty(options.status),
        since: omitEmpty(options.since),
        until: omitEmpty(options.until),
      },
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
  return {
    items: body.items ?? [],
    nextCursor: body.next_cursor ?? "",
    retentionDays: body.retention_days ?? 0,
    dataSource: body.data_source ?? "",
    freshness: body.freshness,
  };
}

/** 读取单条请求的完整正文（需 request.content.read）。
 *
 *  **每次调用都会在审计链上留一条 request.content.viewed。** 所以它不该被
 *  预取、不该在鼠标悬停时触发、也不该在列表里批量调用——那会让「谁看过什么」
 *  这份清单里混进大量没人真的看过的条目。调用点只有请求详情页一个。
 *
 *  `reason` 目前可选（是否必填是产品决定，见后端 ContentInput.Reason）。 */
export async function getPlatformRequestContent(
  platform: string,
  requestId: string,
  options: ListOptions & { reason?: string } = {},
  client: ApiClient = apiClient,
): Promise<RequestContent> {
  const body = await client.get<RawRequestContent>(
    `/api/v1/platforms/${encodeURIComponent(platform)}/requests/${encodeURIComponent(requestId)}`,
    {
      searchParams: { reason: omitEmpty(options.reason) },
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
  return {
    summary: body.summary,
    messages: body.messages ?? [],
    messagesParsed: body.messages_parsed,
    finalReply: body.final_reply,
    finalReplyTruncated: body.final_reply_truncated,
    finalReplyBytes: body.final_reply_bytes,
    rawRequest: body.raw_request,
    rawResponse: body.raw_response,
    dataSource: body.data_source ?? "",
    freshness: body.freshness,
  };
}
