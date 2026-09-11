/** 管理员本地会话与非生产开发身份的统一入口。 */
import { appApiConfig } from "../api/config";
import { devLogout, isAuthenticated as devIsAuthenticated } from "./devSession";
import { cachedLocalUser, setCachedLocalUser, logout as localLogout } from "./localSession";
import { safeNextPath } from "./paths";
import { getRuntimeConfig, type AuthMode } from "./runtimeConfig";

export type LoginReason = "session_expired" | "logged_out";
export const LOGIN_REASON_MESSAGES: Record<LoginReason, string> = {
  session_expired: "登录已过期，请重新登录。",
  logged_out: "已退出登录。",
};
export function loginReasonMessage(raw: string | null | undefined): string | null {
  if (!raw || !Object.hasOwn(LOGIN_REASON_MESSAGES, raw)) return null;
  return LOGIN_REASON_MESSAGES[raw as LoginReason];
}
export function authMode(): AuthMode { return getRuntimeConfig().authMode; }
export function isSignedIn(): boolean {
  const config = getRuntimeConfig();
  if (config.problems.length) return false;
  return config.authMode === "local" ? cachedLocalUser() !== null : devIsAuthenticated();
}
export function currentUserLabel(): string {
  if (authMode() !== "local") return "开发模式";
  const user = cachedLocalUser();
  return user ? user.display_name || user.username : "已登录";
}
/** 仅用于记录归属显示；权限始终由服务端裁决。 */
export function currentPrincipalId(): string {
  return authMode() === "local" ? cachedLocalUser()?.username ?? "" : appApiConfig.principalId;
}
export function currentUserRoles(): string[] | null {
  return authMode() === "local" ? cachedLocalUser()?.roles ?? null : null;
}
export function currentUserHasRole(role: string): boolean {
  return (currentUserRoles() ?? []).includes(role);
}
export function loginPath(next?: string, reason?: LoginReason): string {
  const params = new URLSearchParams();
  const safeNext = safeNextPath(next, "");
  if (safeNext) params.set("next", safeNext);
  if (reason) params.set("reason", reason);
  const qs = params.toString();
  return `/login${qs ? `?${qs}` : ""}`;
}
export function signOut(navigate: (to: string) => void): void {
  if (authMode() === "local") {
    void localLogout().finally(() => navigate(loginPath(undefined, "logged_out"))).catch(() => {});
    return;
  }
  devLogout(); navigate("/login");
}
let redirecting = false;
export function redirectToLogin(reason: LoginReason): void {
  if (redirecting) return;
  redirecting = true;
  setCachedLocalUser(null);
  const next = `${window.location.pathname}${window.location.search}`;
  window.location.assign(loginPath(next, reason));
}
export function onLocalSessionLoss(): void { redirectToLogin("session_expired"); }
