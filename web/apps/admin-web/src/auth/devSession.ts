/** DEV-ONLY 登录壳（dev-header 模式）。仅为 UI 骨架提供前后壳切换；不是安全控制
 *  （宪法：前端隐藏不构成安全控制；服务端为最终裁决）。
 *
 *  XM-AUTH1 之后它只在 authMode=dev-header 时生效；oidc 模式走 ./oidc.ts。
 *  没有删掉，是因为 development / staging 的后端仍按 X-Dev-* 头认身份，
 *  本地开发不该被迫连一个真 Keycloak。 */
const KEY = "xm_dev_auth";

export function isAuthenticated(): boolean {
  try {
    return localStorage.getItem(KEY) === "1";
  } catch {
    return false;
  }
}

export function devLogin(): void {
  localStorage.setItem(KEY, "1");
}

export function devLogout(): void {
  localStorage.removeItem(KEY);
}
