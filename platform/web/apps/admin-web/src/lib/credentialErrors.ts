import { ApiError } from "../api/client";

const REDACTED_MESSAGE = "凭据 Action 失败；请检查 CredentialRef、权限与 SecretProvider 状态";

/** 后端契约要求错误不回显秘密；这里再做一层本地防护，避免错误代理误把
 *  本次粘贴值带回页面。只在消息里真的含有该值时才替换，其余错误原样保留
 *  ——错误码与 request_id 是排障线索，不能一并抹掉。 */
export function redactActionError(error: unknown, secret: string): unknown {
  if (!secret) return error;
  if (error instanceof ApiError && error.message.includes(secret)) {
    return new ApiError(error.status, error.code, REDACTED_MESSAGE, error.requestId);
  }
  if (error instanceof Error && error.message.includes(secret)) {
    return new Error(REDACTED_MESSAGE);
  }
  return error;
}
