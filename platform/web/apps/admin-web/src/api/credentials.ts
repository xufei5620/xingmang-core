import { appApiConfig, type PlatformApiConfig } from "./config";
import { apiClient, type ApiClient } from "./client";
import {
  executeAction,
  type ActionRun,
  type ListOptions,
} from "./platform";
import { parseCredentialRef } from "../lib/credentialForm";

/** 仅给页面的凭据元数据；这里没有 value/secret_value 字段。 */
export interface CredentialMetadata {
  credential_ref: string;
  scope: string;
  updated_at: string;
  fingerprint: string;
  /** 每次 upsert/rotate 递增。0 表示服务端没给版本号，页面显示「—」。 */
  version: number;
  /** SecretProvider 当前能否读到这条引用的值。 */
  available: boolean;
  revoked: boolean;
}

/** 平台自己声明「我需要这几条凭据」的一行（GET /api/v1/credentials/expected）。
 *
 *  这份清单由后端给，前端不硬编码那五个引用名：硬编码的清单会在后端多一条
 *  或改名时继续显示「全部已配置」，而那正是上线前最不能说谎的一屏。 */
export interface ExpectedCredential {
  credential_ref: string;
  platform: string;
  purpose: string;
  /** 后端判定：该引用已保存且未撤销。 */
  configured: boolean;
}

interface ItemsResponse {
  items?: unknown;
}

export const CREDENTIAL_MANAGE_PERMISSION = "credential.manage";

/** react-query 键。共享前缀 ["credentials"]：任一写操作后一次作废元数据表与预期清单——
 *  保存一条缺失凭据后，「缺失」徽章与元数据表要一起变。 */
export const CREDENTIAL_QUERY_KEYS = {
  all: ["credentials"] as const,
  metadata: (environment: string | undefined) =>
    ["credentials", "metadata", environment ?? ""] as const,
  expected: ["credentials", "expected"] as const,
};

/**
 * Action 内核要求三段 ID；产品卡片里的短名仍写作
 * credential.upsert / credential.rotate / credential.revoke。
 */
export const CREDENTIAL_ACTION_IDS = {
  upsert: "credential.secret.upsert",
  rotate: "credential.secret.rotate",
  revoke: "credential.secret.revoke",
} as const;

export interface CredentialValueInput {
  credentialRef: string;
  /** 只在 API 请求期间存在；调用方成功后应立即清空。 */
  secretValue: string;
}

export interface CredentialRevokeInput {
  credentialRef: string;
  reason: string;
}

function stringOrEmpty(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function nonNegativeIntegerOrZero(value: unknown): number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0 ? value : 0;
}

function asRecord(raw: unknown, what: string, index: number): Record<string, unknown> {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    throw new Error(`${what}第 ${index + 1} 行不是对象`);
  }
  return raw as Record<string, unknown>;
}

/** 将服务端一行投影成允许进入 UI 的元数据字段，丢弃所有其它字段。 */
function projectCredential(raw: unknown, index: number): CredentialMetadata {
  const row = asRecord(raw, "凭据列表", index);
  const ref = stringOrEmpty(row.credential_ref).trim();
  const parsed = parseCredentialRef(ref);
  if (!parsed) throw new Error(`凭据列表第 ${index + 1} 行的 CredentialRef 格式不合法`);

  const declaredScope = stringOrEmpty(row.scope).trim();
  if (declaredScope && declaredScope !== parsed.scope) {
    throw new Error(`凭据列表第 ${index + 1} 行的 scope 与 CredentialRef 不一致`);
  }

  return {
    credential_ref: parsed.ref,
    scope: declaredScope || parsed.scope,
    // 元数据缺失时交给页面显示「—」，不拿当前时间补一个假的更新时间。
    updated_at: stringOrEmpty(row.updated_at),
    fingerprint: stringOrEmpty(row.fingerprint),
    version: nonNegativeIntegerOrZero(row.version),
    // 缺字段按「不可用/未撤销」处理：宁可少说一句「可用」，也不凭空说值能读到。
    available: row.available === true,
    revoked: row.revoked === true,
  };
}

function projectExpected(raw: unknown, index: number): ExpectedCredential {
  const row = asRecord(raw, "预期凭据清单", index);
  const ref = stringOrEmpty(row.credential_ref).trim();
  if (!parseCredentialRef(ref)) {
    throw new Error(`预期凭据清单第 ${index + 1} 行的 CredentialRef 格式不合法`);
  }
  return {
    credential_ref: ref,
    platform: stringOrEmpty(row.platform).trim(),
    purpose: stringOrEmpty(row.purpose).trim(),
    configured: row.configured === true,
  };
}

/** 读取当前身份/环境下的凭据元数据；不会请求或返回凭据正文。 */
export async function listCredentials(
  options: ListOptions = {},
  client: ApiClient = apiClient,
  config: PlatformApiConfig = appApiConfig,
): Promise<CredentialMetadata[]> {
  const body = await client.get<ItemsResponse>("/api/v1/credentials", {
    searchParams: { environment: config.environment },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  if (!Array.isArray(body?.items)) return [];
  return body.items.map(projectCredential);
}

/** 读取平台声明需要的凭据清单及各自是否已配置。 */
export async function listExpectedCredentials(
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ExpectedCredential[]> {
  const body = await client.get<ItemsResponse>("/api/v1/credentials/expected", {
    ...(options.signal ? { signal: options.signal } : {}),
  });
  if (!Array.isArray(body?.items)) return [];
  return body.items.map(projectExpected);
}

/** 只生成 Action 参数，不把凭据值复制进其它对象。 */
function valueParams(input: CredentialValueInput): Record<string, unknown> {
  return {
    credential_ref: input.credentialRef.trim(),
    secret_value: input.secretValue,
  };
}

export function upsertCredential(
  input: CredentialValueInput,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: CREDENTIAL_ACTION_IDS.upsert, version: "1", params: valueParams(input) },
    options,
    client,
  );
}

export function rotateCredential(
  input: CredentialValueInput,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: CREDENTIAL_ACTION_IDS.rotate, version: "1", params: valueParams(input) },
    options,
    client,
  );
}

export function revokeCredential(
  input: CredentialRevokeInput,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    {
      actionId: CREDENTIAL_ACTION_IDS.revoke,
      version: "1",
      params: {
        credential_ref: input.credentialRef.trim(),
        reason: input.reason.trim(),
      },
    },
    options,
    client,
  );
}

/** 指纹是可核对的安全证据，页面只需显示前八位。 */
export function fingerprintPrefix(value: string): string {
  const fingerprint = value.trim();
  if (!fingerprint) return "—";
  if (fingerprint.toLowerCase().startsWith("sha256:")) {
    const hash = fingerprint.slice("sha256:".length);
    return hash.length <= 8 ? `sha256:${hash}` : `sha256:${hash.slice(0, 8)}…`;
  }
  return fingerprint.length <= 8 ? fingerprint : `${fingerprint.slice(0, 8)}…`;
}
