import { apiClient, type ApiClient } from "./client";
import { executeAction, type ActionRun, type ListOptions } from "./platform";
import { splitAllowlist } from "../lib/connectorForm";

/** connector.config.set@1 声明的 Permission；读 GET /api/v1/connectors/config 同用它。 */
export const CONNECTOR_MANAGE_PERMISSION = "connector.manage";
export const CONNECTOR_CONFIG_ACTION_ID = "connector.config.set";
export const CONNECTOR_CONFIG_QUERY_KEY = ["connectors", "config"] as const;

/** 目前有接入配置的两个平台。CPA/服务器还没有连接器，这里不列它们——
 *  列了就等于画出一张「看起来能配」的表单。 */
export const CONNECTOR_PLATFORMS = ["sub2api", "newapi"] as const;
export type ConnectorPlatform = (typeof CONNECTOR_PLATFORMS)[number];

/** fake：worker 用演示数据，不连上游；real：只读连接真实上游。 */
export const CONNECTOR_MODES = ["fake", "real"] as const;
export type ConnectorMode = (typeof CONNECTOR_MODES)[number];

export function isConnectorPlatform(value: string): value is ConnectorPlatform {
  return (CONNECTOR_PLATFORMS as readonly string[]).includes(value);
}

export function isConnectorMode(value: string): value is ConnectorMode {
  return (CONNECTOR_MODES as readonly string[]).includes(value);
}

/** GET /api/v1/connectors/config 的一行：数据库里当前生效的接入配置。
 *
 *  platform/mode 保持为 string 而不是收窄成联合类型：读到一个前端不认识的
 *  平台或模式时，应当原样显示出来，而不是在投影阶段悄悄丢掉或改成 fake。
 *
 *  probe_enabled/probe_credential_registered 是 XM-ASSURE1-glue 补的两个
 *  字段（后端 internal/platform/httpapi/credentials.go 的
 *  connectorConfigItem 同批新增，见 docs/handoffs/slices/XM-ASSURE1-glue.md）
 *  ——检测任务 Kill Switch 控件用它们在该平台零声明时也能显示当前状态，
 *  见 components/AssuranceProbeKillSwitch.tsx。probe_credential_registered
 *  是布尔值，不是探测凭据引用的字面串——后端刻意不透出引用文本本身。 */
export interface ConnectorConfig {
  platform: string;
  mode: string;
  endpoint: string;
  target_allowlist: string[];
  credential_ref: string;
  version: number;
  updated_at: string;
  updated_by: string;
  probe_enabled: boolean;
  probe_credential_registered: boolean;
}

export interface ConnectorConfigInput {
  platform: ConnectorPlatform;
  mode: ConnectorMode;
  endpoint: string;
  /** 逗号分隔的主机清单；发送前去空白、去空项、去重。 */
  targetAllowlist: string;
  credentialRef: string;
}

interface ItemsResponse {
  items?: unknown;
}

function stringOrEmpty(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function projectConfig(raw: unknown, index: number): ConnectorConfig {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    throw new Error(`接入配置列表第 ${index + 1} 行不是对象`);
  }
  const row = raw as Record<string, unknown>;
  const platform = stringOrEmpty(row.platform).trim();
  if (!platform) throw new Error(`接入配置列表第 ${index + 1} 行缺少 platform`);
  const allowlist = Array.isArray(row.target_allowlist)
    ? row.target_allowlist.filter((host): host is string => typeof host === "string")
    : [];
  const version = row.version;
  return {
    platform,
    mode: stringOrEmpty(row.mode).trim(),
    endpoint: stringOrEmpty(row.endpoint),
    target_allowlist: allowlist,
    credential_ref: stringOrEmpty(row.credential_ref).trim(),
    version: typeof version === "number" && Number.isSafeInteger(version) && version >= 0 ? version : 0,
    updated_at: stringOrEmpty(row.updated_at),
    updated_by: stringOrEmpty(row.updated_by),
    probe_enabled: row.probe_enabled === true,
    probe_credential_registered: row.probe_credential_registered === true,
  };
}

/** 读取各平台当前生效的接入配置（需 connector.manage）。 */
export async function listConnectorConfigs(
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ConnectorConfig[]> {
  const body = await client.get<ItemsResponse>("/api/v1/connectors/config", {
    ...(options.signal ? { signal: options.signal } : {}),
  });
  if (!Array.isArray(body?.items)) return [];
  return body.items.map(projectConfig);
}

/** 写入一个平台的接入配置（connector.config.set@1）。
 *
 *  target_allowlist 按契约以**逗号分隔字符串**发送，不是数组；endpoint 与
 *  credential_ref 去首尾空白。这里只传引用名，永远不会出现凭据值。 */
export function setConnectorConfig(
  input: ConnectorConfigInput,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    {
      actionId: CONNECTOR_CONFIG_ACTION_ID,
      version: "1",
      params: {
        platform: input.platform,
        mode: input.mode,
        endpoint: input.endpoint.trim(),
        target_allowlist: splitAllowlist(input.targetAllowlist).join(","),
        credential_ref: input.credentialRef.trim(),
      },
    },
    options,
    client,
  );
}
