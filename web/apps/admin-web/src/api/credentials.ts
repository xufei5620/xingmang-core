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
}

interface CredentialListResponse {
  items?: unknown;
}

export const CREDENTIAL_MANAGE_PERMISSION = "credential.manage";

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

/** 将服务端一行投影成允许进入 UI 的四个字段，丢弃所有其它字段。 */
function projectCredential(raw: unknown, index: number): CredentialMetadata {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    throw new Error(`凭据列表第 ${index + 1} 行不是对象`);
  }
  const row = raw as Record<string, unknown>;
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
  };
}

/** 读取当前身份/环境下的凭据元数据；不会请求或返回凭据正文。 */
export async function listCredentials(
  options: ListOptions = {},
  client: ApiClient = apiClient,
  config: PlatformApiConfig = appApiConfig,
): Promise<CredentialMetadata[]> {
  const body = await client.get<CredentialListResponse>("/api/v1/credentials", {
    searchParams: { environment: config.environment },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  if (!Array.isArray(body?.items)) return [];
  return body.items.map(projectCredential);
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
