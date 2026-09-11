/** 前端应用登记簿与发布记录簿的 API 客户端（XM-EXT-APP）。
 *
 *  「应用」= **平台自己纳管的前端站点**（ADMIN-IA §5.4.1）：admin-web、
 *  console 这类我们自己部署的前端。不是被管平台（Sub2API / NewAPI）的前端，
 *  也不是页面搭建器的应用。
 *
 *  ⚠️ 这里**没有发布端点，也不该有**：发布与回滚是 Platform Lifecycle
 *  Operation（宪法 2、3 条），走版本化脚本 + 人工批准，不经 Action 通道。
 *  `recordExtAppRelease` 记录的是**已经发生**的发布，它不发布任何东西。
 *
 *  条目形状保持后端 snake_case 原样，不另建一层驼峰映射：这批端点只有一个
 *  消费者（ExtAppPage），映射层的价值全在「多处消费时形状一致」，为一个
 *  消费者建一层只是多一个会漂的地方（同 api/server.ts 的选择）。 */

import { apiClient, type ApiClient } from "./client";
import { executeAction, type ActionRun, type ListOptions } from "./platform";

interface ListResponse<T> {
  items: T[] | null;
}

/** 一次已经发生的发布。 */
export interface ExtAppReleaseItem {
  id: string;
  app_id: string;
  /** 这次上线的版本 / 构建标签；回滚时是**回滚到的那个版本**。 */
  version: string;
  /** 空串 = 这次发布没取到提交号。 */
  commit_sha: string;
  /** deploy | rollback */
  kind: string;
  /** 发布**真正发生**的时刻。 */
  released_at: string;
  /** 执行那次发布的人；可能不是来登记的人（登记者在审计链里）。 */
  released_by: string;
  notes: string;
  /** 登记时刻。与 released_at 刻意分开：补记历史发布时两者相差很远。 */
  created_at: string;
}

/** 前端应用登记簿一行。 */
export interface ExtAppItem {
  id: string;
  app_key: string;
  display_name: string;
  /** 空串 = 还没有域名（规划中的站点）。是主机名，不是 URL。 */
  primary_domain: string;
  /** 空串 = 未登记登录方式。dev-header | oidc | local */
  auth_mode: string;
  owner: string;
  /** planned | active | retired。登记簿口径，不是探活结果。 */
  status: string;
  notes: string;
  environment: string;
  created_at: string;
  updated_at: string;
  /** null = **没有登记过任何一次发布**，不是「版本未知」的占位。 */
  current_release: ExtAppReleaseItem | null;
}

/** 读两张表需要的权限（复用 registry.read，见后端
 *  internal/platform/extapp.ScopeManage 的注释：能看服务清单与服务器登记簿
 *  的人本就该能看我们自己有哪些前端站点）。 */
export const EXT_APP_READ_PERMISSION = "registry.read";

/** 写三个 Action 需要的权限（新建 scope，同一处注释：改前端站点登记簿是一道
 *  可以单独审定、单独撤销的授权面）。 */
export const EXT_APP_MANAGE_PERMISSION = "extapp.manage";

export const EXT_APPS_QUERY = "ext-apps";
export const EXT_APP_RELEASES_QUERY = "ext-app-releases";

/** 登录方式的显示名。取值逐字对齐 deploy/docker/web-app-config.sh 的
 *  XM_WEB_AUTH_MODE——那是前端运行时真正认得的三个值。 */
export const AUTH_MODE_OPTIONS: { value: string; label: string }[] = [
  { value: "", label: "未登记" },
  { value: "oidc", label: "OIDC" },
  { value: "local", label: "本地账号密码" },
  { value: "dev-header", label: "开发态身份头" },
];

/** 登记状态的显示名。**登记簿口径，不是探活结果**——平台不请求这些站点。 */
export const APP_STATUS_OPTIONS: { value: string; label: string }[] = [
  { value: "active", label: "在线" },
  { value: "planned", label: "规划中" },
  { value: "retired", label: "已下线" },
];

export const RELEASE_KIND_OPTIONS: { value: string; label: string }[] = [
  { value: "deploy", label: "发布" },
  { value: "rollback", label: "回滚" },
];

function labelOf(
  options: readonly { value: string; label: string }[],
  value: string,
  fallback: string,
): string {
  return options.find((o) => o.value === value)?.label ?? (value || fallback);
}

export function authModeLabel(value: string): string {
  return labelOf(AUTH_MODE_OPTIONS, value, "未登记");
}

export function appStatusLabel(value: string): string {
  return labelOf(APP_STATUS_OPTIONS, value, "未知状态");
}

export function releaseKindLabel(value: string): string {
  return labelOf(RELEASE_KIND_OPTIONS, value, "未知类型");
}

export async function listExtApps(
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ExtAppItem[]> {
  const body = await client.get<ListResponse<ExtAppItem>>("/api/v1/ext/apps", {
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return body.items ?? [];
}

export async function listExtAppReleases(
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ExtAppReleaseItem[]> {
  const body = await client.get<ListResponse<ExtAppReleaseItem>>("/api/v1/ext/apps/releases", {
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return body.items ?? [];
}

/** 登记 / 修改一个前端站点（`extapp.app.set@1`）。`app_id` 留空 = 新登记。 */
export function setExtApp(
  params: Record<string, unknown>,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction({ actionId: "extapp.app.set", version: "1", params }, options, client);
}

/** 把一个站点标记为已下线（`extapp.app.retire@1`）。`reason` 必填。
 *
 *  **这不会真的把站点关掉**：平台没有关停站点的通道，那是 Platform Lifecycle
 *  Operation。它改的只是登记簿里的状态。 */
export function retireExtApp(
  params: { app_id: string; reason: string },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction({ actionId: "extapp.app.retire", version: "1", params }, options, client);
}

/** 记录一次**已经发生**的发布（`extapp.release.record@1`）。
 *
 *  ⚠️ 它不发布任何东西。见文件顶部。 */
export function recordExtAppRelease(
  params: Record<string, unknown>,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "extapp.release.record", version: "1", params },
    options,
    client,
  );
}
