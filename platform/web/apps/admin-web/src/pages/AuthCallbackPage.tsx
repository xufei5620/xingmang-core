import { Button, LoadingState } from "@xingmang/ui-primitives";
import { useEffect, useRef, useState } from "react";
import { useLocation, useNavigate } from "react-router";
import { oidc } from "../auth/session";

/** `/auth/callback`：Keycloak 授权码回调（XM-AUTH1）。
 *
 *  只做一件事：把查询参数交给 oidc.handleCallback（校验 state、换令牌、存会话），
 *  成功就回到发起登录时记下的路径，失败就把原因写在屏幕上并给一个回登录页的
 *  入口。这里没有任何「静默回退到工作台」——授权码换令牌失败时假装登录成功
 *  只会让人在下一屏看到一串 403。 */
export function AuthCallbackPage({
  handle = (params) => oidc.handleCallback(params),
}: {
  /** 注入点：测试不必伪造 Keycloak。 */
  handle?: (params: URLSearchParams) => Promise<{ next: string }>;
}) {
  const navigate = useNavigate();
  const location = useLocation();
  const [error, setError] = useState<string | null>(null);
  // 授权码只能用一次。StrictMode 会把 effect 跑两遍，第二遍必须是空操作
  const started = useRef(false);

  useEffect(() => {
    if (started.current) return;
    started.current = true;
    handle(new URLSearchParams(location.search)).then(
      ({ next }) => void navigate(next, { replace: true }),
      (cause: unknown) => setError(cause instanceof Error ? cause.message : "登录回调处理失败"),
    );
  }, [handle, location.search, navigate]);

  if (error) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-canvas font-sans">
        <div className="w-96 max-w-full rounded-lg border border-edge bg-surface p-8 shadow-md">
          <h1 className="mb-2 text-lg font-semibold text-fg">登录未完成</h1>
          <p role="alert" className="mb-6 text-xs text-danger">
            {error}
          </p>
          <Button className="w-full" onClick={() => void navigate("/login", { replace: true })}>
            回到登录页
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-canvas font-sans">
      <LoadingState label="正在完成登录…" />
    </div>
  );
}
