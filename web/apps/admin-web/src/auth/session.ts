/** 应用层的鉴权门面：把「当前是哪种模式」这个问题收口在一处。
 *
 *  三种模式：
 *    - dev-header：./devSession.ts 的 localStorage 开关 + X-Dev-* 请求头（现状不变）
 *    - oidc：./oidc.ts 的 PKCE 会话 + Authorization: Bearer
 *    - local（XM-LOGIN）：./localSession.ts 的账号密码登录 + HttpOnly Cookie 会话
 *
 *  路由门禁、登录页、顶栏、API 客户端都只问这里，不各自判断模式。 */
import type { BearerTokenProvider, UnauthenticatedReason } from "../api/client";
import { devLogout, isAuthenticated as devIsAuthenticated } from "./devSession";
import { cachedLocalUser, logout as localLogout } from "./localSession";
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
  const mode = authMode();
  if (mode === "oidc") return oidc.hasSession();
  if (mode === "local") return cachedLocalUser() !== null;
  return devIsAuthenticated();
}

/** 顶栏右上角的名字。oidc 模式取 id_token 的 preferred_username / name；
 *  local 模式取 RequireAuth 探测到并缓存的 display_name（没有就退到
 *  username）；dev-header 模式保留「开发模式」这四个字——那不是名字，
 *  是一个提醒。 */
export function currentUserLabel(): string {
  const mode = authMode();
  if (mode === "oidc") return oidc.getIdentity()?.displayName ?? "已登录";
  if (mode === "local") {
    const user = cachedLocalUser();
    return user ? user.display_name || user.username : "已登录";
  }
  return "开发模式";
}

export function loginPath(next?: string, reason?: LoginReason): string {
  const params = new URLSearchParams();
  const safeNext = safeNextPath(next, "");
  if (safeNext) params.set("next", safeNext);
  if (reason) params.set("reason", reason);
  const qs = params.toString();
  return `/login${qs ? `?${qs}` : ""}`;
}

/** 退出登录。oidc 走 Keycloak 的 end_session（整页跳转）；local 调用
 *  POST /api/v1/auth/logout 清 Cookie 后站内跳转；dev-header 只清本地开关。 */
export function signOut(navigate: (to: string) => void): void {
  const mode = authMode();
  if (mode === "oidc") {
    void oidc.logout();
    return;
  }
  if (mode === "local") {
    // 退出请求失败（比如离线）也不该把人卡在原地：无论成败都清本地状态并
    // 跳登录页——服务端那份 Cookie 若仍然有效，下一次受保护请求的 401
    // 会再触发一次真正的会话清理
    void localLogout()
      .finally(() => navigate(loginPath(undefined, "logged_out")))
      .catch(() => {});
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

/** 供 api/client.ts 在 local 模式下的会话失效回调（应用单例 apiClient 专用，
 *  见该文件的 createApiClient 调用）。与 oidc 共用同一个 redirectToLogin——
 *  两者都是整页跳转，跳转本身会清空所有内存态，包括 localSession.ts 的内存
 *  用户缓存，不需要在这里另外清理。reason 固定给 "session_expired"：
 *  "token_rejected" 那句文案提到 Keycloak Client/OIDC 配置，对 local 模式
 *  没有意义。 */
export function onLocalSessionLoss(): void {
  redirectToLogin("session_expired");
}
