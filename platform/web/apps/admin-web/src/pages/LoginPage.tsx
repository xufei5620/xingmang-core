import { Button, FormField, Input } from "@xingmang/ui-primitives";
import { useId, useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { ApiError } from "../api/client";
import { devLogin } from "../auth/devSession";
import {
  completeTotpLogin,
  login as localLogin,
  type LocalUser,
  type TotpChallenge,
} from "../auth/localSession";
import { safeNextPath } from "../auth/paths";
import { getRuntimeConfig } from "../auth/runtimeConfig";
import { loginReasonMessage } from "../auth/session";
import { TotpVerifyForm } from "../components/TotpVerifyForm";
import { errorCodeNote } from "../lib/labels";

/** 配置错误不能回落成开发登录。 */
export function LoginPage() {
  const config = getRuntimeConfig();
  if (config.problems.length) return <div role="alert" className="p-8 text-danger">{config.problems.join("；")}</div>;
  return config.authMode === "local" ? <LocalLoginPage /> : <DevLoginPage />;
}

/** local 模式的错误文案：按后端契约的错误码给「如实且具体」的说明；
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
      case "ADMIN_NETWORK_DENIED":
        return "当前网络不在管理员访问名单内，请更换网络或联系管理员。";
      case "RATE_LIMITED":
        return "尝试过于频繁，请稍后重试。";
      default:
        return `${cause.message}（${errorCodeNote(cause.code)}）`;
    }
  }
  return cause instanceof Error ? cause.message : "登录失败，请重试。";
}

/** 登录第二步（TOTP/恢复码）的错误文案。CHALLENGE_INVALID 单独区分：
 *  这不是"码不对"，是整个登录请求已经过期，重试码没有意义，只能回到第一步
 *  重新输入密码——两种情形对用户是不同的下一步动作，不该用同一句话糊弄过去。 */
function totpLoginErrorMessage(cause: unknown): { message: string; expired: boolean } {
  if (cause instanceof ApiError) {
    switch (cause.code) {
      case "CHALLENGE_INVALID":
        return { message: "登录请求已过期，请重新输入密码。", expired: true };
      case "INVALID_CREDENTIALS":
        return { message: "验证码或恢复码不正确。", expired: false };
      case "ACCOUNT_LOCKED":
        return { message: "验证失败次数过多，账号已被锁定，请稍后重试或联系管理员。", expired: false };
      case "ADMIN_NETWORK_DENIED":
        return { message: "当前网络不在管理员访问名单内，请更换网络或联系管理员。", expired: false };
      case "RATE_LIMITED":
        return { message: "尝试过于频繁，请稍后重试。", expired: false };
      default:
        return { message: `${cause.message}（${errorCodeNote(cause.code)}）`, expired: false };
    }
  }
  return { message: cause instanceof Error ? cause.message : "验证失败，请重试。", expired: false };
}

function LocalLoginPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const next = safeNextPath(params.get("next"));
  const notice = loginReasonMessage(params.get("reason"));

  const [challenge, setChallenge] = useState<TotpChallenge | null>(null);
  const [expiredNotice, setExpiredNotice] = useState(false);

  const afterAuthenticated = (user: LocalUser) => {
    // must_change_password 的强制改密由 RequireAuth 统一处理；这里也直接
    // 判一次是为了不必绕经门禁再跳一次——提交成功后一步到位。TOTP 待启用
    // （must_enroll_totp）不在这里判断：走到这个函数说明登录已经完全成功，
    // 而只有"尚未激活 TOTP"的账号才可能 must_enroll_totp=true，那种账号根本
    // 不会经过二步验证——RequireAuth 会在下一次渲染时统一处理引导。
    void navigate(user.must_change_password ? "/account/password" : next);
  };

  if (challenge) {
    return (
      <TotpStepPage
        challenge={challenge}
        expiredNotice={expiredNotice}
        onSuccess={afterAuthenticated}
        onExpired={() => {
          setChallenge(null);
          setExpiredNotice(true);
        }}
      />
    );
  }

  return (
    <PasswordStepPage
      notice={notice}
      onRequiresTotp={(c) => {
        setExpiredNotice(false);
        setChallenge(c);
      }}
      onAuthenticated={afterAuthenticated}
    />
  );
}

function PasswordStepPage({
  notice,
  onRequiresTotp,
  onAuthenticated,
}: {
  notice: string | null;
  onRequiresTotp: (challenge: TotpChallenge) => void;
  onAuthenticated: (user: LocalUser) => void;
}) {
  const fieldPrefix = useId();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async () => {
    setBusy(true);
    setError(null);
    try {
      const outcome = await localLogin(username, password);
      if (outcome.kind === "totp_required") {
        onRequiresTotp(outcome.challenge);
        return;
      }
      onAuthenticated(outcome.user);
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

/** 登录第二步：动态码 / 恢复码二选一。委托给共享的 `TotpVerifyForm`
 *  （XM-AUTH-TOTP0 起；CR-0006 XM-INVCON1 把它抽成独立组件后，这里是它的
 *  第一个调用方，第二个是 InvoiceConsolePanel 的步进提示）——外层只保留
 *  这个页面独有的部分：欢迎语里的用户名前缀、页面级卡片外壳、以及
 *  过期提示条。 */
function TotpStepPage({
  challenge,
  expiredNotice,
  onSuccess,
  onExpired,
}: {
  challenge: TotpChallenge;
  expiredNotice: boolean;
  onSuccess: (user: LocalUser) => void;
  onExpired: () => void;
}) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-canvas font-sans">
      <div className="w-96 max-w-full">
        <div className="mb-4 flex items-center gap-2 px-1">
          <span
            aria-hidden="true"
            className="h-5 w-0.5 shrink-0 rounded-full bg-linear-to-b from-accent to-transparent"
          />
          <h1 className="text-lg font-semibold text-fg">星芒统一控制平台</h1>
        </div>

        {expiredNotice ? (
          <p
            role="status"
            className="mb-4 rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg"
          >
            登录请求已过期，请重新输入密码。
          </p>
        ) : null}

        <TotpVerifyForm
          title="两步验证"
          description={`${challenge.username ? `${challenge.username}，` : ""}请输入认证器 App 中显示的 6 位动态码。`}
          submitLabel="验证并登录"
          mapError={totpLoginErrorMessage}
          onExpired={onExpired}
          onVerify={async (input) => {
            const user = await completeTotpLogin(challenge.tempToken, input);
            onSuccess(user);
          }}
        />
      </div>
    </div>
  );
}

/** 非生产开发入口。 */
function DevLoginPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const next = safeNextPath(params.get("next"));
  const notice = loginReasonMessage(params.get("reason"));
  return <div className="flex min-h-screen items-center justify-center bg-canvas font-sans">
    <div className="w-96 max-w-full rounded-lg border border-edge bg-surface p-8 shadow-md">
      <h1 className="text-lg font-semibold text-fg">星芒统一控制平台</h1>
      <p className="my-4 text-xs text-fg-muted">开发模式：无需登录。身份由 X-Dev-* 请求头自称，只有 development / staging 的后端接受；生产后端会拒绝这套请求头。</p>
      {notice ? <p role="status">{notice}</p> : null}
      <Button className="w-full" onClick={() => { devLogin(); void navigate(next); }}>开发模式进入</Button>
    </div>
  </div>;
}
