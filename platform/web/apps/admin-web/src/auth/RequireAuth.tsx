import { getRuntimeConfig } from "./runtimeConfig";
import { LoadingState } from "@xingmang/ui-primitives";
import { useEffect, useState, type ReactNode } from "react";
import { Navigate, Outlet, useLocation } from "react-router";
import { cachedLocalUser, me, setCachedLocalUser, type LocalUser } from "./localSession";
import { isSignedIn, loginPath } from "./session";

/** 强制改密页的路径。写死在这里（不是 NAV_GROUPS 的一部分——它不是一个可
 *  从侧栏进入的产品页，是登录流程的一环，见 pages/ChangePasswordPage.tsx）。 */
const CHANGE_PASSWORD_PATH = "/account/password";

/** 强制启用 TOTP 页的路径（XM-AUTH-TOTP0），同一条纪律：登录流程的一环，
 *  不是侧栏可达的产品页，见 pages/TotpEnrollPage.tsx。 */
const TOTP_ENROLL_PATH = "/account/totp";

type LocalAuthState =
  | { status: "loading" }
  | { status: "authenticated"; user: LocalUser }
  | { status: "unauthenticated" };

/** local 模式的会话探测。只在**还没有缓存用户**时才真的发请求——登录页
 *  login() 成功后已经把用户存进缓存，跳过来的这一次挂载不必再问服务端一遍。
 *
 *  `enabled` 由调用方按 authMode() 传入：dev-header 模式下必须是
 *  false，否则这个 Hook 会在每一次挂载都悄悄打一个 GET /api/v1/auth/me——
 *  开发模式没有这个后端接口，会平白多一个失败请求。Hook 本身必须
 *  无条件调用（Rules of Hooks），能不能真的发请求是内部按 enabled 判断。
 *
 *  **不把"已认证"状态冻结进 useState**：RequireAuth 是父级布局路由，
 *  子路由切换（/account/password → /dashboard、/account/totp → /dashboard
 *  这类强制引导页完成后的跳转）不会让它重新挂载，只会重新渲染。
 *  changePassword()/confirmTotp() 这类自助操作成功后会**原地更新**
 *  auth/localSession.ts 里的内存缓存对象，但如果这里把"已认证"这份数据
 *  存进只在挂载时求值一次的 useState，那次原地更新永远不会触发这个 Hook
 *  重新渲染——RequireAuth 会拿着"改密/启用 TOTP 之前"的那份旧缓存去判
 *  must_change_password/must_enroll_totp，把刚刚完成强制引导的人重新弹回
 *  同一个页面（曾用真实浏览器走一遍"改密→启用 TOTP→进工作台"全流程时
 *  实测复现：提交改密表单后被弹回改密页本身，而不是继续往前走）。
 *  正确做法是每次渲染都**现读**当前缓存值：有缓存就直接权威；没有缓存
 *  才落到"上一次异步请求"的结果，而那次异步请求成功时本身也会先调用
 *  setCachedLocalUser，下一次渲染读缓存同样能拿到最新值——两条路径最终
 *  都收敛到"缓存是唯一真相来源"，不再需要一份独立、可能过期的 React state
 *  副本。 */
function useLocalAuthState(
  enabled: boolean,
  fetchLocalUser: () => Promise<LocalUser>,
): LocalAuthState {
  const cached = cachedLocalUser();
  // fetchedState 只承担"挂载时缓存为空、后来异步请求出了结果"这一种场景；
  // 一旦 cached 非空，它的值就不再被读取（见下方 return 语句），因此不用
  // 担心它在缓存被别处更新后继续携带一份过期的 "authenticated" 副本。
  const [fetchedState, setFetchedState] = useState<LocalAuthState | null>(null);

  useEffect(() => {
    if (!enabled) return;
    if (cachedLocalUser()) return;
    let cancelled = false;
    fetchLocalUser().then(
      (user) => {
        setCachedLocalUser(user);
        if (!cancelled) setFetchedState({ status: "authenticated", user });
      },
      () => {
        if (!cancelled) setFetchedState({ status: "unauthenticated" });
      },
    );
    return () => {
      cancelled = true;
    };
  }, [enabled, fetchLocalUser]);

  if (cached) {
    return { status: "authenticated", user: cached };
  }
  return fetchedState ?? { status: "loading" };
}

/** 路由门禁：没登录就去登录页，并把当前地址带在 `next` 里，登录完回来。
 *
 *  这是**体验**上的门禁，不是安全控制：真正决定能不能读的是服务端对
 *  开发头 / Cookie 会话的裁决（宪法：前端隐藏不构成安全控制）。
 *  它存在的理由是不让人对着一屏「缺少身份」的 403 发呆。
 *
 *  作为布局路由使用时不传 children，渲染 <Outlet />；也可以直接包一段子树。
 *
 *  local 模式（XM-LOGIN）走一条独立的异步分支：dev-header 的登录态能
 *  同步判断（本地开关），local 模式的会话是 HttpOnly
 *  Cookie，前端读不到，只能真的问一次 GET /api/v1/auth/me；探测结果里的
 *  must_change_password 为真时，无论原本要去哪都先拦到强制改密页；改密
 *  页放行后，must_enroll_totp 为真（且尚未激活）时同理拦到强制启用 TOTP
 *  页（XM-AUTH-TOTP0，语义与 must_change_password 完全对称）——改密优先于
 *  启用 TOTP：先建立一个只有本人知道的私有凭据，再在它之上加二因素。 */
export function RequireAuth({
  children,
  signedIn = isSignedIn,
  fetchLocalUser = me,
}: {
  children?: ReactNode;
  /** 注入点：测试不必伪造 sessionStorage/localStorage。仅用于 dev-header。 */
  signedIn?: () => boolean;
  /** 注入点：测试不必伪造真实的 /api/v1/auth/me。仅用于 local。 */
  fetchLocalUser?: () => Promise<LocalUser>;
}) {
  const location = useLocation();
  const config = getRuntimeConfig();
  const mode = config.authMode;
  const localState = useLocalAuthState(mode === "local" && config.problems.length === 0, fetchLocalUser);

  if (config.problems.length) return <Navigate to="/login" replace />;

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
    if (localState.user.must_change_password) {
      // 强制改密优先级最高：密码没换之前不检查 TOTP 门禁——TOTP 门禁自己的
      // 判断只在"改密已经满足"之后才生效（下面的 else if），否则两个门禁会在
      // 各自的目标页之间来回把对方判定成"还没到目标页"，无限重定向。
      return location.pathname === CHANGE_PASSWORD_PATH ? (
        <>{children ?? <Outlet />}</>
      ) : (
        <Navigate to={CHANGE_PASSWORD_PATH} replace />
      );
    }
    if (localState.user.must_enroll_totp && !localState.user.totp_enrolled) {
      return location.pathname === TOTP_ENROLL_PATH ? (
        <>{children ?? <Outlet />}</>
      ) : (
        <Navigate to={TOTP_ENROLL_PATH} replace />
      );
    }
    return <>{children ?? <Outlet />}</>;
  }

  if (!signedIn()) {
    return <Navigate to={loginPath(`${location.pathname}${location.search}`)} replace />;
  }
  return <>{children ?? <Outlet />}</>;
}
