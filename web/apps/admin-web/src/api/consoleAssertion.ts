/** 断言签发（CR-0006 XM-INVCON1）：POST /api/v1/auth/console-assertion。
 *
 *  只在 local 模式（XM_AUTH_MODE=local）下有意义——断言依赖 TOTP 新鲜度
 *  （mfa_at），这个概念只有本地登录会话才有，见后端
 *  internal/platform/consoleassertion 的注释。调用方（InvoiceConsolePanel）
 *  负责只在 local 模式下调用本模块，这里不重复判断。 */
import { apiClient, type ApiClient } from "./client";

/** 三个已批准的嵌入入口，与 InvoiceConsoleMode 同值——这里独立声明一份
 *  而不是从 InvoiceConsolePanel 反向 import：api/ 目录不依赖 components/，
 *  同仓库既有的分层约定（组件依赖 api，反过来不成立）。 */
export type ConsoleAssertionScope = "sub2api" | "newapi" | "global";

export interface ConsoleAssertionResult {
  assertion: string;
  /** ISO 8601（服务端已格式化为 RFC3339 UTC），供调用方计算何时该重新签发。 */
  expiresAt: string;
}

interface ConsoleAssertionResponseRaw {
  assertion?: unknown;
  expires_at?: unknown;
}

/** 签发一枚断言。失败时抛 ApiError——调用方按 code 区分
 *  FINANCE_SCOPE_REQUIRED / ADMIN_NETWORK_DENIED / ADMIN_STEP_UP_REQUIRED /
 *  其它，见 InvoiceConsolePanel 的错误状态映射。 */
export async function issueConsoleAssertion(
  scope: ConsoleAssertionScope,
  client: ApiClient = apiClient,
): Promise<ConsoleAssertionResult> {
  const body = await client.post<ConsoleAssertionResponseRaw>("/api/v1/auth/console-assertion", { scope });
  return {
    assertion: typeof body.assertion === "string" ? body.assertion : "",
    expiresAt: typeof body.expires_at === "string" ? body.expires_at : "",
  };
}
