/** 兼容入口：DEV-ONLY 登录壳已搬到 ./auth/devSession.ts（XM-AUTH1 把鉴权
 *  相关代码归到 src/auth/ 目录）。旧的 import 路径保留，签名不变。 */
export { devLogin, devLogout, isAuthenticated } from "./auth/devSession";
