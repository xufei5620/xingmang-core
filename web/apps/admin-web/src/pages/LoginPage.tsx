import { Button } from "@xingmang/ui-primitives";
import { useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { devLogin } from "../auth/devSession";
import { safeNextPath } from "../auth/oidc";
import { getRuntimeConfig } from "../auth/runtimeConfig";
import { loginReasonMessage, oidc } from "../auth/session";

/** 登录页（XM-AUTH1）。
 *
 *  oidc 模式：一个按钮，跳 Keycloak solov-staff Realm 走授权码 + PKCE；
 *  dev-header 模式：如实说明「无需登录」——身份由 X-Dev-* 请求头自称，
 *  这个按钮只是把前后壳切换一下（./auth/devSession.ts）。
 *
 *  `?next=` 是登录后要回到的站内路径，`?reason=` 说明为什么回到了这里
 *  （会话过期 / 令牌被拒 / 已登出），两者都由 auth/session.ts 生成。 */
export function LoginPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const config = getRuntimeConfig();
  const next = safeNextPath(params.get("next"));
  const notice = loginReasonMessage(params.get("reason"));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const oidcMode = config.authMode === "oidc";

  async function startOidcLogin() {
    setBusy(true);
    setError(null);
    try {
      // 成功时整页跳走，这个 Promise 之后的代码不会再跑；只有失败会回来
      await oidc.login(next);
    } catch (cause) {
      setBusy(false);
      setError(cause instanceof Error ? cause.message : "无法发起登录");
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-canvas font-sans">
      <div className="w-96 max-w-full rounded-lg border border-edge bg-surface p-8 shadow-md">
        <div className="mb-1 flex items-center gap-2">
          {/* 靛蓝亮边与壳的标题栏同源：整套设计只在导航、页面标题、指标卡三处用它 */}
          <span
            aria-hidden="true"
            className="h-5 w-0.5 shrink-0 rounded-full bg-linear-to-b from-accent to-transparent"
          />
          <h1 className="text-lg font-semibold text-fg">星芒统一控制平台</h1>
        </div>
        <p className="mb-6 text-xs text-fg-muted">
          {oidcMode
            ? "员工入口（solov-staff Realm）。登录在身份服务完成，本站不接触口令。"
            : "员工入口（solov-staff）。当前是开发模式。"}
        </p>

        {notice ? (
          <p
            role="status"
            className="mb-4 rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg"
          >
            {notice}
          </p>
        ) : null}

        {oidcMode ? (
          <Button
            className="w-full"
            loading={busy}
            disabled={config.problems.length > 0}
            onClick={() => void startOidcLogin()}
          >
            使用 solov 账号登录
          </Button>
        ) : (
          <>
            <p className="mb-4 text-xs text-fg-muted">
              开发模式：无需登录。身份由 X-Dev-* 请求头自称，只有 development / staging
              的后端接受；生产后端会拒绝这套请求头。
            </p>
            <Button
              className="w-full"
              onClick={() => {
                devLogin();
                void navigate(next);
              }}
            >
              开发模式进入
            </Button>
          </>
        )}

        {error ? (
          <p role="alert" className="mt-4 text-xs text-danger">
            {error}
          </p>
        ) : null}

        {config.problems.length > 0 ? (
          <ul role="alert" className="mt-4 space-y-1 text-xs text-danger">
            {config.problems.map((problem) => (
              <li key={problem}>{problem}</li>
            ))}
          </ul>
        ) : null}
      </div>
    </div>
  );
}
