import { Button, FormField, Input } from "@xingmang/ui-primitives";
import { useId, useState } from "react";
import { validateRecoveryCode, validateTotpCode } from "../lib/totpForm";

/** 动态码 / 恢复码二选一的校验表单（XM-AUTH-TOTP0 起，从 LoginPage.tsx 的
 *  `TotpStepPage` 抽出，供 CR-0006 XM-INVCON1 的断言步进提示复用同一份 UI
 *  与状态机——"重用 XM-AUTH-TOTP0 的 TOTP 登录组件"是任务书原话，这里是
 *  具体做法：把两处唯一的差异（提示文案、成功/过期后做什么、错误文案怎么
 *  从 ApiError 映射）参数化，其余（默认展示动态码、"改用恢复码"切换、
 *  互斥校验、常量时间输入限制）完全共享，不留两份平行实现分叉的风险。 */
export interface TotpVerifyFormProps {
  /** 顶部标题，默认「两步验证」。 */
  title?: string;
  /** 动态码模式下的说明；恢复码模式下固定改用统一文案（与既有 LoginPage
   *  行为一致，不必让每个调用方都重写一遍）。 */
  description: string;
  /** 提交按钮文案，默认「验证」。 */
  submitLabel?: string;
  /** 校验成功后的回调；抛出的错误交给 `mapError` 转成文案。 */
  onVerify: (input: { code: string } | { recoveryCode: string }) => Promise<void>;
  /** 把捕获到的错误映射成文案；`expired` 为真时调用 `onExpired`（如果给了）
   *  而不是就地显示错误——两种情形对用户是不同的下一步动作。 */
  mapError: (cause: unknown) => { message: string; expired: boolean };
  /** 请求整个挑战已过期，无法继续用同一份状态重试（如临时令牌过期）；
   *  不传时"expired"错误按普通错误显示。 */
  onExpired?: () => void;
}

/** 断言步进/登录第二步共用的动态码+恢复码表单。 */
export function TotpVerifyForm({
  title = "两步验证",
  description,
  submitLabel = "验证",
  onVerify,
  mapError,
  onExpired,
}: TotpVerifyFormProps) {
  const fieldPrefix = useId();
  const [useRecoveryCode, setUseRecoveryCode] = useState(false);
  const [code, setCode] = useState("");
  const [recoveryCode, setRecoveryCode] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fieldError = useRecoveryCode ? validateRecoveryCode(recoveryCode) : validateTotpCode(code);

  const submit = async () => {
    if (fieldError) return;
    setBusy(true);
    setError(null);
    try {
      await onVerify(useRecoveryCode ? { recoveryCode } : { code });
    } catch (cause) {
      const mapped = mapError(cause);
      if (mapped.expired && onExpired) {
        onExpired();
        return;
      }
      setError(mapped.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="rounded-lg border border-edge bg-surface p-6">
      <h2 className="mb-1 text-base font-semibold text-fg">{title}</h2>
      <p className="mb-4 text-xs text-fg-muted">
        {useRecoveryCode ? "请输入一张尚未使用过的恢复码。" : description}
      </p>

      <form
        className="flex flex-col gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        {useRecoveryCode ? (
          <FormField
            label="恢复码"
            htmlFor={`${fieldPrefix}-recovery-code`}
            required
            hint="启用 TOTP 时生成的一次性恢复码，每张只能用一次"
          >
            <Input
              id={`${fieldPrefix}-recovery-code`}
              value={recoveryCode}
              disabled={busy}
              onChange={(event) => setRecoveryCode(event.target.value)}
              autoComplete="off"
              autoFocus
              spellCheck={false}
            />
          </FormField>
        ) : (
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
        )}

        <Button type="submit" className="w-full" loading={busy} disabled={Boolean(fieldError)}>
          {submitLabel}
        </Button>
      </form>

      {error ? (
        <p role="alert" className="mt-4 text-xs text-danger">
          {error}
        </p>
      ) : null}

      <button
        type="button"
        className="mt-4 text-xs font-medium text-accent hover:underline"
        onClick={() => {
          setUseRecoveryCode((prev) => !prev);
          setError(null);
        }}
      >
        {useRecoveryCode ? "改用动态码" : "改用恢复码"}
      </button>
    </div>
  );
}
