/** 仅供显式非生产 dev-header 模式使用的开发登录开关。
 *  服务端仍是身份与权限的最终裁决者；生产拒绝开发头。 */
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
