/** 服务器登记簿的 API 客户端（XM-SERVER0）。
 *
 *  拍板原话「服务器只做记录好了」：本文件对应的四个查询与六个 Action **不**
 *  依赖 Server Agent、不做任何 SSH/docker/端口探测——它们只是登记表的
 *  Query/Action 客户端，同 api/finance.ts 上半部分（登记簿而不是看板供数）
 *  的定位。
 *
 *  条目形状**保持后端 snake_case 原样**，不另建一层驼峰映射：这批端点只有
 *  一个消费者（对应的 Panel 组件），映射层的价值全在「多处消费时形状一致」，
 *  为一个消费者建一层只是多一个会漂的地方（同 UpstreamAccountItem 的选择）。
 */

import { apiClient, type ApiClient } from "./client";
import { executeAction, type ActionRun, type ListOptions } from "./platform";

interface ListResponse<T> {
  items: T[] | null;
}

/** 服务器资产登记簿一行。 */
export interface ServerAssetItem {
  id: string;
  hostname: string;
  ip_addresses: string[];
  datacenter: string;
  /** 空串 = 未登记供应商。 */
  supplier_id: string;
  vcpu: number | null;
  memory_gb: number | null;
  disk_gb: number | null;
  purpose: string;
  status: string;
  /** 整数最小单位字符串；null = 未登记（**不是** 0——0 是「免费」的合法取值）。 */
  monthly_cost_minor_units: string | null;
  currency: string;
  billing_cycle: string;
  /** 空串 = 未登记到期日；否则形如 2026-09-30。 */
  expires_at: string;
  notes: string;
  environment: string;
  created_at: string;
  updated_at: string;
}

/** 服务器供应商登记簿一行。 */
export interface ServerSupplierItem {
  id: string;
  name: string;
  website: string;
  console_url: string;
  contact_name: string;
  contact_info: string;
  notes: string;
  environment: string;
  created_at: string;
  updated_at: string;
}

/** 域名与证书登记簿一行。 */
export interface ServerDomainItem {
  id: string;
  domain_name: string;
  registrar: string;
  dns_provider: string;
  expires_at: string;
  cert_source: string;
  cert_expires_at: string;
  bound_service_note: string;
  environment: string;
  created_at: string;
  updated_at: string;
}

/** 服务与容器手工登记一行。 */
export interface ServerServiceNoteItem {
  id: string;
  server_id: string;
  service_name: string;
  service_kind: string;
  port: number | null;
  notes: string;
  created_at: string;
  updated_at: string;
}

/** 读四张登记表需要的权限（复用 registry.read，见后端
 *  internal/platform/server.ScopeManage 的注释：能看服务清单的人本就该能看
 *  服务器登记簿，两者是同一类知识面）。 */
export const SERVER_REGISTRY_READ_PERMISSION = "registry.read";

/** 写六个 Action 需要的权限（新建 scope，见后端同一处注释：改服务器登记簿
 *  是一道独立可审定的授权面，不搭在别的能力上顺带获得）。 */
export const SERVER_MANAGE_PERMISSION = "server.manage";

export const SERVER_ASSETS_QUERY = "server-assets";
export const SERVER_SUPPLIERS_QUERY = "server-suppliers";
export const SERVER_DOMAINS_QUERY = "server-domains";
export const SERVER_SERVICE_NOTES_QUERY = "server-service-notes";

export async function listServerAssets(
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ServerAssetItem[]> {
  const body = await client.get<ListResponse<ServerAssetItem>>("/api/v1/servers/assets", {
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return body.items ?? [];
}

export async function listServerSuppliers(
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ServerSupplierItem[]> {
  const body = await client.get<ListResponse<ServerSupplierItem>>("/api/v1/servers/suppliers", {
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return body.items ?? [];
}

export async function listServerDomains(
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ServerDomainItem[]> {
  const body = await client.get<ListResponse<ServerDomainItem>>("/api/v1/servers/domains", {
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return body.items ?? [];
}

export async function listServerServiceNotes(
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ServerServiceNoteItem[]> {
  const body = await client.get<ListResponse<ServerServiceNoteItem>>("/api/v1/servers/service-notes", {
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return body.items ?? [];
}

/** 登记 / 修改服务器资产（`server.asset.set@1`）。`asset_id` 留空 = 新登记。 */
export function setServerAsset(
  params: Record<string, unknown>,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction({ actionId: "server.asset.set", version: "1", params }, options, client);
}

/** 把一台资产标记为已退役（`server.asset.retire@1`）。`reason` 必填。 */
export function retireServerAsset(
  params: { asset_id: string; reason: string },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction({ actionId: "server.asset.retire", version: "1", params }, options, client);
}

/** 登记 / 修改服务器供应商（`server.supplier.set@1`）。`supplier_id` 留空 = 新登记。 */
export function setServerSupplier(
  params: Record<string, unknown>,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction({ actionId: "server.supplier.set", version: "1", params }, options, client);
}

/** 登记 / 修改域名（`server.domain.set@1`）。`domain_id` 留空 = 新登记。 */
export function setServerDomain(
  params: Record<string, unknown>,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction({ actionId: "server.domain.set", version: "1", params }, options, client);
}

/** 登记 / 修改服务与容器备注（`server.service_note.set@1`）。
 *  `service_note_id` 留空 = 新登记，此时必须带 `server_id`。 */
export function setServerServiceNote(
  params: Record<string, unknown>,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction({ actionId: "server.service_note.set", version: "1", params }, options, client);
}

/** 删除一条服务/容器备注（`server.service_note.remove@1`）。`reason` 必填。 */
export function removeServerServiceNote(
  params: { service_note_id: string; reason: string },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction({ actionId: "server.service_note.remove", version: "1", params }, options, client);
}
