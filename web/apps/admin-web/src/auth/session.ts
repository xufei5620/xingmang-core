/** 应用层的鉴权门面：把「当前是哪种模式」这个问题收口在一处。
 *
 *  两种模式：
 *    - dev-header：./devSession.ts 的 localStorage 开关 + X-Dev-* 请求头（现状不变）
 *    - oidc：./oidc.ts 的 PKCE 会话 + Authorization: Bearer
 *
 *  路由门禁、登录页、顶栏、API 客户端都只问这里，不各自判断模式。 */
import type { BearerTokenProvider, UnauthenticatedReason } from "../api/client";
import { devLogout, isAuthenticated as devIsAuthenticated } from "./devSession";
import { createOidcClient, safeNextPath, type OidcClient, type OidcConfig } from "./oidc";
import { getRuntimeConfig, type AuthMode } from "./runtimeConfig";

/** 登录页据此说明「为什么回到了这里」。 */
export type LoginReason = "session_expired" | "token_rejected" | "logged_out";

export const LOGIN_REASON_MESSAGES: Record<LoginReason, string> = {
  session_expired: "登录已过期，请重新登录。",
  token_rejected:
    "服务端拒绝了当前登录令牌，请重新登录。若反复出现，请核对 Keycloak Client 与后端 XM_OIDC_ISSUER / XM_OIDC_AUDIENCE 的配置。",
  logged_out: "已退出登录。",
};

export function loginReasonMessage(raw: string | null | undefined): string | null {
  if (!raw) return null;
  return raw in LOGIN_REASON_MESSAGES ? LOGIN_REASON_MESSAGES[raw as LoginReason] : null;
}

/** 回调与登出地址从当前 origin 推导：同一个构建产物服务 staging 与生产。
 *  两者都必须出现在 Keycloak Client 的白名单里（AUTH-SWITCH.md「前端」一节）。 */
export function oidcConfigFromRuntime(): OidcConfig {
  const rc = getRuntimeConfig();
  const origin = window.location.origin;
  return {
    issuer: rc.oidcIssuer,
    clientId: rc.oidcClientId,
    scopes: rc.oidcScopes,
    redirectUri: `${origin}/auth/callback`,
    postLogoutRedirectUri: `${origin}/login`,
  };
}

/** 应用唯一的 OIDC 客户端。配置惰性读取：dev-header 模式下它永远不会被调用，
 *  但模块照常加载，不能因为 issuer 为空就在这里抛。 */
export const oidc: OidcClient = createOidcClient(oidcConfigFromRuntime);

export function authMode(): AuthMode {
  return getRuntimeConfig().authMode;
}

export function isSignedIn(): boolean {
  return authMode() === "oidc" ? oidc.hasSession() : devIsAuthenticated();
}

/** 顶栏右上角的名字。oidc 模式取 id_token 的 preferred_username / name；
 *  dev-header 模式保留「开发模式」这四个字——那不是名字，是一个提醒。 */
export function currentUserLabel(): string {
  if (authMode() !== "oidc") return "开发模式";
  return oidc.getIdentity()?.displayName ?? "已登录";
}

export function loginPath(next?: string, reason?: LoginReason): string {
  const params = new URLSearchParams();
  const safeNext = safeNextPath(next, "");
  if (safeNext) params.set("next", safeNext);
  if (reason) params.set("reason", reason);
  const qs = params.toString();
  return `/login${qs ? `?${qs}` : ""}`;
}

/** 退出登录。oidc 走 Keycloak 的 end_session（整页跳转）；dev-header 只清本地开关。 */
export function signOut(navigate: (to: string) => void): void {
  if (authMode() === "oidc") {
    void oidc.logout();
    return;
  }
  devLogout();
  navigate("/login");
}

let redirecting = false;

/** 会话失效时回登录页（整页跳转，顺带清掉内存里的查询缓存）。
 *  同一页面只跳一次：并发的十几个请求一起 401 不该发起十几次跳转。 */
export function redirectToLogin(reason: LoginReason): void {
  if (redirecting) return;
  redirecting = true;
  oidc.clearSession();
  const next = `${window.location.pathname}${window.location.search}`;
  window.location.assign(loginPath(next, reason));
}

/** 供 api/client.ts 在 oidc 模式下注入 Bearer 令牌。 */
export const oidcBearerProvider: BearerTokenProvider = {
  getAccessToken: () => oidc.getAccessToken(),
  onUnauthenticated: (reason: UnauthenticatedReason) =>
    redirectToLogin(reason === "no_token" ? "session_expired" : "token_rejected"),
};
