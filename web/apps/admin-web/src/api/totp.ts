/** TOTP 二因素自助启用/确认（XM-AUTH-TOTP0）。
 *
 *  enroll/confirm 两个端点是**专用**端点（不经通用 Action 执行入口）：
 *  CR-0006 技术规格 §5.1 只为这两个自助操作开了专用路径，响应体是内核
 *  Action 结果的原样 JSON（没有 `{action_run_id,result}` 包装），因此这里
 *  直接 `client.post` 而不复用 executeAction/ActionRun 形状——那是给通用
 *  执行入口用的。
 *
 *  管理员重置他人 TOTP（staff.account.reset_totp@1）没有专用端点，走既有的
 *  通用执行入口，与 resetStaffAccountPassword 同一形状，见下方
 *  resetStaffAccountTotp。 */
import { apiClient, type ApiClient } from "./client";
import { executeAction, type ActionRun, type ListOptions } from "./platform";

export interface EnrollTotpResult {
  otpauthUri: string;
  secretBase32: string;
  issuer: string;
  username: string;
}

interface EnrollTotpRaw {
  otpauth_uri?: unknown;
  secret_base32?: unknown;
  issuer?: unknown;
  username?: unknown;
}

function str(value: unknown): string {
  return typeof value === "string" ? value : "";
}

/** 启用 TOTP：生成一把新密钥（未激活），返回 otpauth URI 与手动录入串。
 *  密钥明文只在这一次响应里出现，本模块不缓存它。 */
export async function enrollTotp(
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<EnrollTotpResult> {
  const body = await client.post<EnrollTotpRaw>(
    "/api/v1/auth/totp/enroll",
    {},
    { ...(options.signal ? { signal: options.signal } : {}) },
  );
  return {
    otpauthUri: str(body.otpauth_uri),
    secretBase32: str(body.secret_base32),
    issuer: str(body.issuer),
    username: str(body.username),
  };
}

export interface ConfirmTotpResult {
  totpEnrolledAt: string;
  /** 一次性恢复码明文，仅在这次响应里出现；调用方负责只展示一次。 */
  recoveryCodes: string[];
}

interface ConfirmTotpRaw {
  totp_enrolled_at?: unknown;
  recovery_codes?: unknown;
}

/** 确认启用：校验一次动态码，通过则激活并生成 10 个一次性恢复码。 */
export async function confirmTotp(
  code: string,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ConfirmTotpResult> {
  const body = await client.post<ConfirmTotpRaw>(
    "/api/v1/auth/totp/confirm",
    { code: code.trim() },
    { ...(options.signal ? { signal: options.signal } : {}) },
  );
  const codes = Array.isArray(body.recovery_codes)
    ? body.recovery_codes.filter((c): c is string => typeof c === "string")
    : [];
  return { totpEnrolledAt: str(body.totp_enrolled_at), recoveryCodes: codes };
}

/** 管理员重置他人的 TOTP 启用状态（staff.account.reset_totp@1）。 */
export function resetStaffAccountTotp(
  username: string,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "staff.account.reset_totp", version: "1", params: { username: username.trim() } },
    options,
    client,
  );
}
