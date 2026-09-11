import { Button, FormField, Input } from "@xingmang/ui-primitives";
import { useId, useState } from "react";
import { Navigate, useNavigate, useSearchParams } from "react-router";
import { changePassword } from "../auth/localSession";
import { safeNextPath } from "../auth/paths";
import { authMode } from "../auth/session";
import { ActionErrorNote } from "../components/ActionErrorNote";
import {
  EMPTY_CHANGE_PASSWORD_FORM,
  changePasswordFieldLabel,
  hasChangePasswordFormErrors,
  validateChangePasswordForm,
  type ChangePasswordFormErrors,
  type ChangePasswordFormField,
  type ChangePasswordFormValues,
} from "../lib/passwordForm";

/** `/account/password`：local 模式的改密页（XM-LOGIN）。
 *
 *  两个入口都会落到这里：首次登录/管理员重置密码后 must_change_password
 *  为真，RequireAuth 无论原本要去哪都先拦到这一页（见 auth/RequireAuth.tsx）；
 *  也允许已登录的人主动访问改密。改完之后本地立即把 must_change_password
 *  标记清掉（auth/localSession.ts 的 changePassword），门禁不会再把人拽回来。 */
export function ChangePasswordPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const next = safeNextPath(params.get("next"), "/dashboard");
  const fieldPrefix = useId();

  const [values, setValues] = useState<ChangePasswordFormValues>(EMPTY_CHANGE_PASSWORD_FORM);
  const [errors, setErrors] = useState<ChangePasswordFormErrors>({});
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState<unknown>(null);
  const [attempts, setAttempts] = useState(0);

  // 这一页只对 local 模式有意义（改的是自带账号密码）；oidc/dev-header
  // 没有「本站密码」这个概念，直接回工作台，不显示一个改不了任何东西的表单
  if (authMode() !== "local") {
    return <Navigate to="/dashboard" replace />;
  }

  const setField = (field: ChangePasswordFormField) => (value: string) => {
    setValues((prev) => ({ ...prev, [field]: value }));
    setErrors((prev) => {
      if (!(field in prev)) return prev;
      const next = { ...prev };
      delete next[field];
      return next;
    });
    setActionError(null);
  };

  const submit = async () => {
    setAttempts((n) => n + 1);
    const found = validateChangePasswordForm(values);
    setErrors(found);
    if (hasChangePasswordFormErrors(found)) return;
    setBusy(true);
    setActionError(null);
    try {
      await changePassword(values.currentPassword, values.newPassword);
      void navigate(next);
    } catch (cause) {
      setActionError(cause);
    } finally {
      setBusy(false);
    }
  };

  const failedFields = (Object.keys(errors) as ChangePasswordFormField[]).filter(
    (field) => Boolean(errors[field]),
  );
  const showSummary = attempts > 0 && (failedFields.length > 0 || Boolean(actionError));

  return (
    <div className="flex min-h-screen items-center justify-center bg-canvas font-sans">
      <div className="w-96 max-w-full rounded-lg border border-edge bg-surface p-8 shadow-md">
        <div className="mb-1 flex items-center gap-2">
          <span
            aria-hidden="true"
            className="h-5 w-0.5 shrink-0 rounded-full bg-linear-to-b from-accent to-transparent"
          />
          <h1 className="text-lg font-semibold text-fg">修改密码</h1>
        </div>
        <p className="mb-6 text-xs text-fg-muted">
          首次登录或管理员重置密码后，需要在这里设置一个新密码才能继续使用控制台。
        </p>

        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          {showSummary && failedFields.length > 0 ? (
            <p role="alert" className="text-xs text-danger">
              还有 {failedFields.length} 处需要修正：
              {failedFields.map((field) => changePasswordFieldLabel(field)).join("、")}
            </p>
          ) : null}

          <FormField
            label={changePasswordFieldLabel("currentPassword")}
            htmlFor={`${fieldPrefix}-current`}
            required
            error={errors.currentPassword}
          >
            <Input
              id={`${fieldPrefix}-current`}
              type="password"
              value={values.currentPassword}
              disabled={busy}
              invalid={Boolean(errors.currentPassword)}
              onChange={(event) => setField("currentPassword")(event.target.value)}
              autoComplete="current-password"
            />
          </FormField>

          <FormField
            label={changePasswordFieldLabel("newPassword")}
            htmlFor={`${fieldPrefix}-new`}
            required
            error={errors.newPassword}
            hint="至少 10 位，且不能与当前密码相同"
          >
            <Input
              id={`${fieldPrefix}-new`}
              type="password"
              value={values.newPassword}
              disabled={busy}
              invalid={Boolean(errors.newPassword)}
              onChange={(event) => setField("newPassword")(event.target.value)}
              autoComplete="new-password"
            />
          </FormField>

          <FormField
            label={changePasswordFieldLabel("confirmPassword")}
            htmlFor={`${fieldPrefix}-confirm`}
            required
            error={errors.confirmPassword}
          >
            <Input
              id={`${fieldPrefix}-confirm`}
              type="password"
              value={values.confirmPassword}
              disabled={busy}
              invalid={Boolean(errors.confirmPassword)}
              onChange={(event) => setField("confirmPassword")(event.target.value)}
              autoComplete="new-password"
            />
          </FormField>

          <ActionErrorNote error={actionError} />

          <Button type="submit" className="w-full" loading={busy}>
            保存并继续
          </Button>
        </form>
      </div>
    </div>
  );
}
