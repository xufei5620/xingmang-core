/** 管理台使用本地 Cookie 会话。开发头须显式选择，生产始终禁止。 */
export type AuthMode = "dev-header" | "local";
export interface RuntimeConfigInput { authMode?: AuthMode; environment?: string; }
export interface RuntimeConfig {
  authMode: AuthMode;
  environment: string | undefined;
  source: "app-config" | "vite-env" | "default";
  /** 非空时登录页和受保护路由必须阻止继续，不能降级为开发身份。 */
  problems: string[];
}
export type RuntimeEnv = Pick<ImportMetaEnv, "VITE_XM_AUTH_MODE" | "VITE_XM_ENVIRONMENT">;
declare global { interface Window { __XM_CONFIG__?: unknown; } }
function nonEmptyString(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value.trim() : undefined;
}
export function resolveRuntimeConfig(raw: unknown, env: RuntimeEnv): RuntimeConfig {
  const problems: string[] = [];
  let input: Record<string, unknown> = {};
  if (raw !== undefined && raw !== null) {
    if (typeof raw === "object" && !Array.isArray(raw)) input = raw as Record<string, unknown>;
    else problems.push("app-config.js 的配置必须是对象，请联系管理员修正");
  }
  const environment = nonEmptyString(input.environment) ?? nonEmptyString(env.VITE_XM_ENVIRONMENT);
  let authMode: AuthMode = "local";
  let source: RuntimeConfig["source"] = "default";
  const runtimeValue = nonEmptyString(input.authMode);
  const runtimeSpecified = runtimeValue !== undefined || (input.authMode !== undefined && typeof input.authMode !== "string");
  const mode = runtimeSpecified ? runtimeValue ?? input.authMode : nonEmptyString(env.VITE_XM_AUTH_MODE);
  if (mode !== undefined) {
    source = runtimeSpecified ? "app-config" : "vite-env";
    if (mode === "local" || mode === "dev-header") authMode = mode;
    else problems.push("登录方式配置已失效：仅支持 local 或非生产 dev-header，请联系管理员修正");
  }
  if (authMode === "dev-header" && environment === "production") {
    problems.push("生产环境禁止开发身份，请使用 local 登录");
    authMode = "local";
  }
  for (const key of ["oidcIssuer", "oidcClientId", "oidcScopes", "invoiceConsoleOrigin"]) {
    if (nonEmptyString(input[key])) problems.push(`旧配置 ${key} 已退役，请联系管理员移除`);
  }
  const rawEnv = env as Record<string, unknown>;
  for (const key of ["VITE_XM_OIDC_ISSUER", "VITE_XM_OIDC_CLIENT_ID", "VITE_XM_OIDC_SCOPES"]) {
    if (nonEmptyString(rawEnv[key])) problems.push(`旧配置 ${key} 已退役，请联系管理员移除`);
  }
  if (problems.length) authMode = "local";
  return { authMode, environment, source, problems };
}
export function getRuntimeConfig(): RuntimeConfig {
  return resolveRuntimeConfig(typeof window === "undefined" ? undefined : window.__XM_CONFIG__, import.meta.env);
}
