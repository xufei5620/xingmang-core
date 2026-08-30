import type { ReactNode } from "react";
import { Navigate, Outlet, useLocation } from "react-router";
import { isSignedIn, loginPath } from "./session";

/** 路由门禁：没登录就去登录页，并把当前地址带在 `next` 里，登录完回来。
 *
 *  这是**体验**上的门禁，不是安全控制：真正决定能不能读的是服务端对
 *  Bearer 令牌 / 开发头的裁决（宪法：前端隐藏不构成安全控制）。它存在的
 *  理由是不让人对着一屏「缺少身份」的 403 发呆。
 *
 *  作为布局路由使用时不传 children，渲染 <Outlet />；也可以直接包一段子树。 */
export function RequireAuth({
  children,
  signedIn = isSignedIn,
}: {
  children?: ReactNode;
  /** 注入点：测试不必伪造 sessionStorage/localStorage。 */
  signedIn?: () => boolean;
}) {
  const location = useLocation();
  if (!signedIn()) {
    return <Navigate to={loginPath(`${location.pathname}${location.search}`)} replace />;
  }
  return <>{children ?? <Outlet />}</>;
}
