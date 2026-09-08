/** 「接口与自动化」两张登记簿的 API 客户端（XM-EXT-INTEGRATION）。
 *
 *  两条必须一直跟着数据一起传下去的限定，它们不是可选的说明文字：
 *
 *  1. **调用方登记簿不是授权面**——登记不发凭据、不授权、不限流，停用也不会
 *     让任何请求被拒绝。这句话由后端随响应下发（`registry_note`），前端照它
 *     渲染而不是自己写一份：一份读数与它的限定必须同源。
 *  2. **规则登记簿没有执行器**——登记一条规则不会让任何 Action 跑起来
 *     （`automatic_execution` 恒为 false，`execution_note` 说明为什么）。
 *
 *  条目形状保持后端 snake_case 原样，不另建驼峰映射层：这两个端点各只有一个
 *  消费者（ExtIntegrationPage 的两格），映射层的价值全在多处消费时形状一致，
 *  为一个消费者建一层只是多一个会漂的地方（同 api/server.ts 的选择）。
 */

import { apiClient, type ApiClient } from "./client";
import { executeAction, type ActionRun, type ListOptions } from "./platform";

/** 读两张登记簿需要的权限。
 *
 *  **不复用 registry.read**：那个默认发给 staff，而调用方登记簿是一张授权面
 *  的地图（谁该来调我们、期望持有哪些 scope）。见后端
 *  internal/platform/integration.ScopeRead 的注释。 */
export const INTEGRATION_READ_PERMISSION = "integration.read";

/** 写两张登记簿需要的权限（四个 L1 Action）。 */
export const INTEGRATION_MANAGE_PERMISSION = "integration.manage";

export const API_CLIENTS_QUERY = "integration-api-clients";
export const AUTOMATION_RULES_QUERY = "integration-automation-rules";

/** 一个身份在观测窗口内的活动。
 *
 *  **只覆盖写操作**：它来自 `action.action_run`，也就是经 Action 内核的调用。
 *  读操作（GET）今天只进进程访问日志，不落库也没有查询端点。 */
export interface CallerActivity {
  principal_id: string;
  principal_type: string;
  run_count: number;
  failed_count: number;
  first_seen_at: string;
  last_seen_at: string;
  last_action_id: string;
  last_status: string;
}

/** 调用方登记簿一行 + 它在窗口内的观测活动。 */
export interface ApiClientItem {
  id: string;
  principal_id: string;
  principal_type: string;
  display_name: string;
  purpose: string;
  owner: string;
  /** **期望**的权限范围，不是生效的权限范围。 */
  expected_scopes: string[];
  /** 凭据**引用**（secret://…），不是值；空串 = 未登记引用。 */
  credential_ref: string;
  status: string;
  notes: string;
  environment: string;
  created_at: string;
  created_by: string;
  updated_at: string;
  updated_by: string;
  /** null = 窗口内没有做过写操作。**不等于「没来过」**，见 observed_note。 */
  observed: CallerActivity | null;
}

/** 调用方对账的完整响应。 */
export interface ApiClientsResponse {
  items: ApiClientItem[];
  /** 窗口内调用过但没有登记的身份。 */
  unregistered: CallerActivity[];
  window_days: number;
  observed_since: string;
  /** 为真表示窗口内出现的身份数超过上限，观测侧不完整。 */
  observed_truncated: boolean;
  observed_source: string;
  observed_note: string;
  registry_note: string;
}

/** 自动化规则登记簿一行。 */
export interface AutomationRuleItem {
  id: string;
  name: string;
  description: string;
  trigger_kind: string;
  trigger_detail: string;
  target_action_id: string;
  target_action_version: string;
  /** 这条登记指向的 Action 此刻在不在注册表里。 */
  target_action_registered: boolean;
  /** 目标此刻声明的风险等级；未注册时为空串。 */
  target_action_risk_level: string;
  status: string;
  notes: string;
  environment: string;
  created_at: string;
  created_by: string;
  updated_at: string;
  updated_by: string;
}

export interface AutomationRulesResponse {
  items: AutomationRuleItem[];
  /** 恒为 false，且不是一个配置项。 */
  automatic_execution: boolean;
  execution_note: string;
}

/** 触发类别的中文标签。不认识的取值原样显示内部名——吞掉它只会让一个
 *  将来新增的类别看起来像空白。 */
export const TRIGGER_KIND_LABELS: Readonly<Record<string, string>> = {
  // manual 就是 manual。原来这一条写的是「手动或定时触发」，把两个不同的
  // 取值揉进了一个标签——下拉里于是同时出现「手动或定时触发」和「定时」两项，
  // 选的人无从分辨。后端 TriggerManual/TriggerSchedule 是两个独立取值。
  manual: "手动",
  schedule: "定时",
  event: "事件",
  webhook: "Webhook",
};

/** 规则登记状态的中文标签。
 *
 *  三个词都刻意避开「启用 / 生效 / 运行中」：这张表没有执行器，任何暗示
 *  运行态的词都会让人以为配了就生效。 */
export const RULE_STATUS_LABELS: Readonly<Record<string, string>> = {
  draft: "草稿",
  registered: "已登记",
  disabled: "已作废",
};

/** 调用方**登记**状态的中文标签。
 *
 *  「已停用」三个字单独摆着会被读成「这个调用方被拦下了」，而 doc.go 第 1 条
 *  写得很清楚：**停用一行不会让任何请求被拒绝**，登记簿不是授权面。所以中文
 *  里必须带上「登记」二字——否则运营会以为停用等于断供，出事时找错地方。 */
export const CLIENT_STATUS_LABELS: Readonly<Record<string, string>> = {
  active: "登记在册",
  disabled: "登记已停用",
};

/** 状态徽章的悬停解释，与上面那张表配套。 */
export const CLIENT_STATUS_HINTS: Readonly<Record<string, string>> = {
  active: "登记簿里是有效的一行。登记不发凭据、不授予权限，真实授权仍由角色决定。",
  disabled:
    "只是登记簿上停用了，**该身份的请求照样会被放行**——授权由 Keycloak 角色决定，不看这张表。要真正断供得去改角色。",
};

export async function listApiClients(
  options: ListOptions & { windowDays?: number } = {},
  client: ApiClient = apiClient,
): Promise<ApiClientsResponse> {
  const query = options.windowDays ? `?window_days=${options.windowDays}` : "";
  return client.get<ApiClientsResponse>(`/api/v1/integration/api-clients${query}`, {
    ...(options.signal ? { signal: options.signal } : {}),
  });
}

export async function listAutomationRules(
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<AutomationRulesResponse> {
  return client.get<AutomationRulesResponse>("/api/v1/integration/automation-rules", {
    ...(options.signal ? { signal: options.signal } : {}),
  });
}

/** 登记 / 修改一个 API 调用方（`integration.api_client.set@1`）。
 *  `client_id` 留空 = 新登记。`credential_ref` 只收 secret:// 引用。 */
export function setApiClient(
  params: Record<string, unknown>,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "integration.api_client.set", version: "1", params },
    options,
    client,
  );
}

/** 启用 / 停用一条调用方登记（`integration.api_client.set_status@1`）。
 *  `reason` 必填。**停用不会挡住任何请求**，只是改登记状态。 */
export function setApiClientStatus(
  params: { client_id: string; status: string; reason: string },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "integration.api_client.set_status", version: "1", params },
    options,
    client,
  );
}

/** 登记 / 修改一条自动化规则（`integration.automation_rule.set@1`）。
 *  `rule_id` 留空 = 新登记。**登记不会让规则跑起来**。 */
export function setAutomationRule(
  params: Record<string, unknown>,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "integration.automation_rule.set", version: "1", params },
    options,
    client,
  );
}

/** 改一条规则登记的状态（`integration.automation_rule.set_status@1`）。 */
export function setAutomationRuleStatus(
  params: { rule_id: string; status: string },
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "integration.automation_rule.set_status", version: "1", params },
    options,
    client,
  );
}
