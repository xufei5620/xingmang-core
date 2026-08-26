/** DEV-ONLY 登录壳。仅为 UI 骨架提供前后壳切换；不是安全控制
 *  （宪法：前端隐藏不构成安全控制；服务端为最终裁决）。
 *  XM-0008 将本模块替换为 Keycloak OIDC（solov-staff Realm），签名保持不变。 */
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
