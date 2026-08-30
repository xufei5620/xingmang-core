import { LoadingState } from "@xingmang/ui-primitives";
import { useEffect, useState, type ReactNode } from "react";
import { Navigate, Outlet, useLocation } from "react-router";
import { cachedLocalUser, me, setCachedLocalUser, type LocalUser } from "./localSession";
import { authMode, isSignedIn, loginPath } from "./session";

/** 强制改密页的路径。写死在这里（不是 NAV_GROUPS 的一部分——它不是一个可
 *  从侧栏进入的产品页，是登录流程的一环，见 pages/ChangePasswordPage.tsx）。 */
const CHANGE_PASSWORD_PATH = "/account/password";

type LocalAuthState =
  | { status: "loading" }
  | { status: "authenticated"; user: LocalUser }
  | { status: "unauthenticated" };

/** local 模式的会话探测。只在**还没有缓存用户**时才真的发请求——登录页
 *  login() 成功后已经把用户存进缓存，跳过来的这一次挂载不必再问服务端一遍。
 *
 *  `enabled` 由调用方按 authMode() 传入：dev-header/oidc 模式下必须是
 *  false，否则这个 Hook 会在每一次挂载都悄悄打一个 GET /api/v1/auth/me——
 *  那两种模式根本没有这个后端接口，会平白多一个失败请求。Hook 本身必须
 *  无条件调用（Rules of Hooks），能不能真的发请求是内部按 enabled 判断。 */
function useLocalAuthState(
  enabled: boolean,
  fetchLocalUser: () => Promise<LocalUser>,
): LocalAuthState {
  const cached = cachedLocalUser();
  const [state, setState] = useState<LocalAuthState>(
    cached ? { status: "authenticated", user: cached } : { status: "loading" },
  );

  useEffect(() => {
    if (!enabled) return;
    if (cachedLocalUser()) return;
    let cancelled = false;
    fetchLocalUser().then(
      (user) => {
        setCachedLocalUser(user);
        if (!cancelled) setState({ status: "authenticated", user });
      },
      () => {
        if (!cancelled) setState({ status: "unauthenticated" });
      },
    );
    return () => {
      cancelled = true;
    };
  }, [enabled, fetchLocalUser]);

  return state;
}

/** 路由门禁：没登录就去登录页，并把当前地址带在 `next` 里，登录完回来。
 *
 *  这是**体验**上的门禁，不是安全控制：真正决定能不能读的是服务端对
 *  Bearer 令牌 / 开发头 / Cookie 会话的裁决（宪法：前端隐藏不构成安全控制）。
 *  它存在的理由是不让人对着一屏「缺少身份」的 403 发呆。
 *
 *  作为布局路由使用时不传 children，渲染 <Outlet />；也可以直接包一段子树。
 *
 *  local 模式（XM-LOGIN）走一条独立的异步分支：dev-header/oidc 的登录态能
 *  同步判断（本地开关 / sessionStorage），local 模式的会话是 HttpOnly
 *  Cookie，前端读不到，只能真的问一次 GET /api/v1/auth/me；探测结果里的
 *  must_change_password 为真时，无论原本要去哪都先拦到强制改密页。 */
export function RequireAuth({
  children,
  signedIn = isSignedIn,
  fetchLocalUser = me,
}: {
  children?: ReactNode;
  /** 注入点：测试不必伪造 sessionStorage/localStorage。仅用于 dev-header/oidc。 */
  signedIn?: () => boolean;
  /** 注入点：测试不必伪造真实的 /api/v1/auth/me。仅用于 local。 */
  fetchLocalUser?: () => Promise<LocalUser>;
}) {
  const location = useLocation();
  const mode = authMode();
  const localState = useLocalAuthState(mode === "local", fetchLocalUser);

  if (mode === "local") {
    if (localState.status === "loading") {
      return (
        <div className="flex min-h-screen items-center justify-center bg-canvas font-sans">
          <LoadingState label="正在验证登录状态…" />
        </div>
      );
    }
    if (localState.status === "unauthenticated") {
      return <Navigate to={loginPath(`${location.pathname}${location.search}`)} replace />;
    }
    if (localState.user.must_change_password && location.pathname !== CHANGE_PASSWORD_PATH) {
      return <Navigate to={CHANGE_PASSWORD_PATH} replace />;
    }
    return <>{children ?? <Outlet />}</>;
  }

  if (!signedIn()) {
    return <Navigate to={loginPath(`${location.pathname}${location.search}`)} replace />;
  }
  return <>{children ?? <Outlet />}</>;
}
