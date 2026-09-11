import { formatUtcTimestamp } from "@xingmang/ui-admin";
import { Button, FormField, Input } from "@xingmang/ui-primitives";
import { useEffect, useId, useState, type ReactNode } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { ApiError } from "../api/client";
import { confirmTotp, enrollTotp, type EnrollTotpResult } from "../api/totp";
import { cachedLocalUser, setCachedLocalUser, type LocalUser } from "../auth/localSession";
import { safeNextPath } from "../auth/paths";
import { validateTotpCode } from "../lib/totpForm";
import { errorCodeNote } from "../lib/labels";

/** 密码只在浏览器剪贴板 API 存在时尝试复制；不存在（旧浏览器、非安全上下文、
 *  测试环境）就什么也不做——内容已经显示在屏幕上，人仍能手动选中复制。
 *  与 components/StaffAccountsPanel.tsx 里同名函数逻辑一致，各自独立一份：
 *  两处都是仅有的调用方，一个共享工具模块换来的复用价值不大。 */
function copyToClipboard(value: string): void {
  try {
    void navigator.clipboard?.writeText(value);
  } catch {
    // 见上：复制失败不是错误，只是少一个便利
  }
}

function downloadRecoveryCodes(username: string, codes: readonly string[]): void {
  const text =
    `星芒统一控制平台 - ${username} 的 TOTP 恢复码\n` +
    `生成时间：${new Date().toISOString()}\n` +
    `每张只能使用一次，请妥善保管，不要以明文形式长期存放在无关人员可访问的地方。\n\n` +
    codes.map((c, i) => `${i + 1}. ${c}`).join("\n") +
    "\n";
  const blob = new Blob([text], { type: "text/plain;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  try {
    const a = document.createElement("a");
    a.href = url;
    a.download = `xingmang-totp-recovery-codes-${username}.txt`;
    document.body.appendChild(a);
    a.click();
    a.remove();
  } finally {
    URL.revokeObjectURL(url);
  }
}

/** `/account/totp`：TOTP 自助启用页 + 账号安全状态展示（XM-AUTH-TOTP0）。
 *
 *  两个入口都会落到这里：must_enroll_totp 为真且尚未激活时，RequireAuth
 *  无论原本要去哪都先拦到这一页（见 auth/RequireAuth.tsx，与
 *  must_change_password 强制改密同一模式）；也允许已登录的人主动访问查看
 *  自己的启用状态。已激活时只展示状态，不提供"重新生成"（那会让当前恢复码
 *  与认证器 App 的绑定同时失效，属于需要管理员走 reset_totp 的操作，见
 *  组件 StaffAccountsPanel 的"重置 TOTP"入口）。 */
export function TotpEnrollPage() {
  const [user, setUser] = useState<LocalUser | null>(() => cachedLocalUser());

  useEffect(() => {
    // 不主动发请求探测：这一页只应该在已经拿到过一次 /me（登录、或
    // RequireAuth 的会话探测）之后才会被渲染到；没有缓存说明还没走完那一步，
    // 直接显示占位而不是再打一次请求造成竞态。
    setUser(cachedLocalUser());
  }, []);

  if (!user) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-canvas font-sans">
        <p className="text-sm text-fg-muted">正在加载账号信息…</p>
      </div>
    );
  }

  if (user.totp_enrolled) {
    return <TotpStatusCard user={user} />;
  }

  return (
    <EnrollFlow
      user={user}
      onEnrolled={(updated) => {
        setCachedLocalUser(updated);
        setUser(updated);
      }}
    />
  );
}

function Card({ title, description, children }: { title: string; description: string; children: ReactNode }) {
  return (
    <div className="flex min-h-screen items-center justify-center bg-canvas font-sans">
      <div className="w-96 max-w-full rounded-lg border border-edge bg-surface p-8 shadow-md">
        <div className="mb-1 flex items-center gap-2">
          <span
            aria-hidden="true"
            className="h-5 w-0.5 shrink-0 rounded-full bg-linear-to-b from-accent to-transparent"
          />
          <h1 className="text-lg font-semibold text-fg">{title}</h1>
        </div>
        <p className="mb-6 text-xs text-fg-muted">{description}</p>
        {children}
      </div>
    </div>
  );
}

function TotpStatusCard({ user }: { user: LocalUser }) {
  const navigate = useNavigate();
  return (
    <Card title="两步验证已启用" description={`账号 ${user.username} 已启用 TOTP 二次验证。`}>
      <dl className="flex flex-col gap-2 text-sm">
        <div className="flex items-center justify-between">
          <dt className="text-fg-muted">启用时间</dt>
          <dd className="font-mono text-xs text-fg">
            {user.totp_enrolled_at ? formatUtcTimestamp(user.totp_enrolled_at) : "—"}
          </dd>
        </div>
        <div className="flex items-center justify-between">
          <dt className="text-fg-muted">剩余恢复码</dt>
          <dd className="text-fg">
            {user.recovery_codes_remaining === null ? "—" : `${user.recovery_codes_remaining} 张`}
          </dd>
        </div>
      </dl>
      <p className="mt-4 text-xs text-fg-muted">
        若需要更换认证器或恢复码已用尽，请联系持有「人员与权限」管理权限的同事，通过重置流程重新启用。
      </p>
      <Button type="button" className="mt-6 w-full" onClick={() => void navigate("/dashboard")}>
        返回工作台
      </Button>
    </Card>
  );
}

type EnrollStep =
  | { kind: "pending"; secret: EnrollTotpResult }
  | { kind: "recovery"; codes: string[]; totpEnrolledAt: string };

function EnrollFlow({ user, onEnrolled }: { user: LocalUser; onEnrolled: (user: LocalUser) => void }) {
  const [step, setStep] = useState<EnrollStep | null>(null);
  const [loadError, setLoadError] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);

  const startEnroll = async () => {
    setLoading(true);
    setLoadError(null);
    try {
      const secret = await enrollTotp();
      setStep({ kind: "pending", secret });
    } catch (cause) {
      setLoadError(cause);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void startEnroll();
    // 只在挂载时自动发起一次；用户点"重新生成"会显式再调一次，不依赖依赖数组
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  if (step?.kind === "recovery") {
    return (
      <RecoveryCodesReveal
        username={user.username}
        codes={step.codes}
        totpEnrolledAt={step.totpEnrolledAt}
        onDone={() =>
          onEnrolled({
            ...user,
            totp_enrolled: true,
            must_enroll_totp: false,
            totp_enrolled_at: step.totpEnrolledAt,
            recovery_codes_remaining: step.codes.length,
          })
        }
      />
    );
  }

  return (
    <Card
      title="启用两步验证"
      description={
        user.must_enroll_totp
          ? "为保护管理员账号安全，首次登录需要启用两步验证（TOTP）后才能继续使用控制台。"
          : "为账号额外启用一层两步验证（TOTP）。"
      }
    >
      {loading && !step ? <p className="text-sm text-fg-muted">正在生成密钥…</p> : null}
      {loadError ? (
        <div className="flex flex-col gap-3">
          <p role="alert" className="text-xs text-danger">
            {loadError instanceof ApiError
              ? `${loadError.message}（${errorCodeNote(loadError.code)}）`
              : "生成密钥失败，请重试。"}
          </p>
          <Button type="button" size="sm" onClick={() => void startEnroll()}>
            重试
          </Button>
        </div>
      ) : null}
      {step?.kind === "pending" ? (
        <ConfirmStep
          secret={step.secret}
          onRegenerate={() => void startEnroll()}
          onConfirmed={(codes, totpEnrolledAt) => setStep({ kind: "recovery", codes, totpEnrolledAt })}
        />
      ) : null}
    </Card>
  );
}

function ConfirmStep({
  secret,
  onRegenerate,
  onConfirmed,
}: {
  secret: EnrollTotpResult;
  onRegenerate: () => void;
  onConfirmed: (codes: string[], totpEnrolledAt: string) => void;
}) {
  const fieldPrefix = useId();
  const [code, setCode] = useState("");
  const [copiedKey, setCopiedKey] = useState(false);
  const [copiedUri, setCopiedUri] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);

  const submit = async () => {
    const fieldErr = validateTotpCode(code);
    if (fieldErr) {
      setError(new Error(fieldErr));
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const result = await confirmTotp(code);
      onConfirmed(result.recoveryCodes, result.totpEnrolledAt);
    } catch (cause) {
      setError(cause);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-col gap-4">
      <div>
        <p className="mb-1 text-xs font-medium text-fg">1. 在认证器 App 中添加账号</p>
        <p className="mb-2 text-xs text-fg-muted">
          在 Google Authenticator、Microsoft Authenticator 等 App 中选择"手动输入密钥"，填入下面的密钥；
          Issuer 填 <code className="font-mono">{secret.issuer || "xingmang"}</code>，账号名填{" "}
          <code className="font-mono">{secret.username}</code>。
        </p>
        <div className="flex items-center gap-2 rounded-md border border-edge bg-surface-muted px-3 py-2">
          <code className="flex-1 min-w-0 break-all font-mono text-sm text-fg">{secret.secretBase32}</code>
          <Button
            type="button"
            size="sm"
            variant="secondary"
            onClick={() => {
              copyToClipboard(secret.secretBase32);
              setCopiedKey(true);
            }}
          >
            {copiedKey ? "已复制" : "复制"}
          </Button>
        </div>
        <details className="mt-2">
          <summary className="cursor-pointer text-xs text-fg-muted">支持扫码的 App？改用完整链接</summary>
          <div className="mt-2 flex items-center gap-2 rounded-md border border-edge bg-surface-muted px-3 py-2">
            <code className="flex-1 min-w-0 break-all font-mono text-xs text-fg">{secret.otpauthUri}</code>
            <Button
              type="button"
              size="sm"
              variant="secondary"
              onClick={() => {
                copyToClipboard(secret.otpauthUri);
                setCopiedUri(true);
              }}
            >
              {copiedUri ? "已复制" : "复制"}
            </Button>
          </div>
        </details>
      </div>

      <form
        className="flex flex-col gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        <p className="text-xs font-medium text-fg">2. 输入 App 中显示的 6 位动态码确认</p>
        <FormField label="动态码" htmlFor={`${fieldPrefix}-code`} required>
          <Input
            id={`${fieldPrefix}-code`}
            value={code}
            disabled={busy}
            onChange={(event) => setCode(event.target.value.replace(/\D/g, "").slice(0, 6))}
            inputMode="numeric"
            autoComplete="one-time-code"
            autoFocus
            spellCheck={false}
          />
        </FormField>
        {error ? (
          <p role="alert" className="text-xs text-danger">
            {error instanceof ApiError
              ? `${error.message}（${errorCodeNote(error.code)}）`
              : error instanceof Error
                ? error.message
                : "验证失败，请重试。"}
          </p>
        ) : null}
        <Button type="submit" className="w-full" loading={busy} disabled={Boolean(validateTotpCode(code))}>
          确认启用
        </Button>
        <button
          type="button"
          className="text-xs font-medium text-accent hover:underline"
          onClick={onRegenerate}
        >
          换一把密钥重新生成
        </button>
      </form>
    </div>
  );
}

function RecoveryCodesReveal({
  username,
  codes,
  totpEnrolledAt,
  onDone,
}: {
  username: string;
  codes: string[];
  totpEnrolledAt: string;
  onDone: () => void;
}) {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const next = safeNextPath(params.get("next"), "/dashboard");
  const [copiedAll, setCopiedAll] = useState(false);
  const [acknowledged, setAcknowledged] = useState(false);

  return (
    <Card title="保存恢复码" description="这 10 个恢复码只会显示这一次，请立即保存到安全的地方。">
      <div className="grid grid-cols-2 gap-2 rounded-md border border-edge bg-surface-muted p-3 font-mono text-sm text-fg">
        {codes.map((code, i) => (
          <span key={code}>
            {i + 1}. {code}
          </span>
        ))}
      </div>
      <p className="mt-3 text-xs text-fg-muted">
        当认证器 App 无法使用时，可以用一张尚未使用过的恢复码代替动态码登录，每张只能使用一次。
      </p>
      <div className="mt-4 flex gap-2">
        <Button
          type="button"
          size="sm"
          variant="secondary"
          className="flex-1"
          onClick={() => {
            copyToClipboard(codes.join("\n"));
            setCopiedAll(true);
          }}
        >
          {copiedAll ? "已复制" : "复制全部"}
        </Button>
        <Button
          type="button"
          size="sm"
          variant="secondary"
          className="flex-1"
          onClick={() => downloadRecoveryCodes(username, codes)}
        >
          下载为文本文件
        </Button>
      </div>

      <label className="mt-4 flex items-start gap-2 text-xs text-fg">
        <input
          type="checkbox"
          checked={acknowledged}
          onChange={(event) => setAcknowledged(event.target.checked)}
          className="mt-0.5 size-4 rounded-sm border-edge accent-accent"
        />
        我已经保存好这些恢复码，关闭后将无法再次查看
      </label>

      <Button
        type="button"
        className="mt-4 w-full"
        disabled={!acknowledged}
        onClick={() => {
          onDone();
          void navigate(next);
        }}
      >
        完成
      </Button>
      {/* totpEnrolledAt 只用于把状态回写进本地缓存（onDone 的调用方），
          这里不需要单独展示——启用时间在下次进入这一页的状态卡片里可见。 */}
      <span className="sr-only">{totpEnrolledAt}</span>
    </Card>
  );
}
