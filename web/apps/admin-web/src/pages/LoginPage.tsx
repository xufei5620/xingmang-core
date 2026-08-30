import { Button, FormField, Input } from "@xingmang/ui-primitives";
import { useId, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { ApiError } from "../api/client";
import { devLogin } from "../auth/devSession";
import { login as localLogin } from "../auth/localSession";
import { safeNextPath } from "../auth/oidc";
import { getRuntimeConfig } from "../auth/runtimeConfig";
import { loginReasonMessage, oidc } from "../auth/session";

/** 登录页（XM-AUTH1 起 oidc/dev-header 两态；XM-LOGIN 加入 local）。
 *
 *  按 authMode 分派到三个互不相干的实现：
 *    - local：本页面新增，账号密码表单，见 LocalLoginPage；
 *    - oidc/dev-header：XM-AUTH1 原有实现，原样保留在 OidcOrDevLoginPage。
 *  拆成独立组件而不是在同一个函数里三路分支，是为了不让 hooks 的调用顺序
 *  依赖 authMode()（它在应用生命周期内不会变，但没必要让这一点成为正确性
 *  的前提——分派本身不调用任何 hook，天然满足 Rules of Hooks）。 */
export function LoginPage() {
  const config = getRuntimeConfig();
  if (config.authMode === "local") return <LocalLoginPage />;
  return <OidcOrDevLoginPage />;
}

/** local 模式的错误文案：按后端契约的三个错误码给「如实且具体」的说明；
 *  不认识的错误码回落到通用文案 + 错误码本身，方便报障。 */
function localLoginErrorMessage(cause: unknown): string {
  if (cause instanceof ApiError) {
    switch (cause.code) {
      case "INVALID_CREDENTIALS":
        return "用户名或密码不正确。";
      case "ACCOUNT_LOCKED":
        return "账号已被锁定，请联系管理员解锁后重试。";
      case "ACCOUNT_DISABLED":
        return "账号已被停用，请联系管理员。";
      default:
        return `${cause.message}（错误码 ${cause.code}）`;
    }
  }
  return cause instanceof Error ? cause.message : "登录失败，请重试。";
}

function LocalLoginPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const next = safeNextPath(params.get("next"));
  const notice = loginReasonMessage(params.get("reason"));
  const fieldPrefix = useId();

  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const user = await localLogin(username, password);
      // must_change_password 的强制改密由 RequireAuth 统一处理；这里也直接
      // 判一次是为了不必绕经门禁再跳一次——提交成功后一步到位
      void navigate(user.must_change_password ? "/account/password" : next);
    } catch (cause) {
      setError(localLoginErrorMessage(cause));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex min-h-screen items-center justify-center bg-canvas font-sans">
      <div className="w-96 max-w-full rounded-lg border border-edge bg-surface p-8 shadow-md">
        <div className="mb-1 flex items-center gap-2">
          <span
            aria-hidden="true"
            className="h-5 w-0.5 shrink-0 rounded-full bg-linear-to-b from-accent to-transparent"
          />
          <h1 className="text-lg font-semibold text-fg">星芒统一控制平台</h1>
        </div>
        <p className="mb-6 text-xs text-fg-muted">员工入口。使用管理员分配的账号与密码登录。</p>

        {notice ? (
          <p
            role="status"
            className="mb-4 rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg"
          >
            {notice}
          </p>
        ) : null}

        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          <FormField label="用户名" htmlFor={`${fieldPrefix}-username`} required>
            <Input
              id={`${fieldPrefix}-username`}
              value={username}
              disabled={busy}
              onChange={(event) => setUsername(event.target.value)}
              autoComplete="username"
              autoFocus
              spellCheck={false}
            />
          </FormField>
          <FormField label="密码" htmlFor={`${fieldPrefix}-password`} required>
            <Input
              id={`${fieldPrefix}-password`}
              type="password"
              value={password}
              disabled={busy}
              onChange={(event) => setPassword(event.target.value)}
              autoComplete="current-password"
            />
          </FormField>
          <Button
            type="submit"
            className="w-full"
            loading={busy}
            disabled={!username.trim() || !password}
          >
            登录
          </Button>
        </form>

        {error ? (
          <p role="alert" className="mt-4 text-xs text-danger">
            {error}
          </p>
        ) : null}
      </div>
    </div>
  );
}

/** oidc 模式：一个按钮，跳 Keycloak solov-staff Realm 走授权码 + PKCE；
 *  dev-header 模式：如实说明「无需登录」——身份由 X-Dev-* 请求头自称，
 *  这个按钮只是把前后壳切换一下（./auth/devSession.ts）。
 *
 *  `?next=` 是登录后要回到的站内路径，`?reason=` 说明为什么回到了这里
 *  （会话过期 / 令牌被拒 / 已登出），两者都由 auth/session.ts 生成。
 *
 *  XM-LOGIN 之后这条路径不再是生产默认（生产改用 local），但 staging /
 *  个别环境仍可能配成 oidc 或 dev-header，实现原样保留。 */
function OidcOrDevLoginPage() {
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
