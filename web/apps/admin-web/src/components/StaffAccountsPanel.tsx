import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { DataTableV2, PageState, formatUtcTimestamp, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge, Button, Dialog, FormField, Input, type BadgeTone } from "@xingmang/ui-primitives";
import { useId, useState } from "react";
import type { ApiClient } from "../api/client";
import {
  createStaffAccount,
  listStaffAccounts,
  resetStaffAccountPassword,
  setStaffAccountDisabled,
  setStaffAccountRoles,
  staffRoleLabel,
  STAFF_MANAGE_PERMISSION,
  STAFF_ROLE_CATALOG,
  type StaffAccount,
} from "../api/staff";
import { cachedLocalUser } from "../auth/localSession";
import {
  EMPTY_STAFF_ACCOUNT_FORM,
  MIN_PASSWORD_LENGTH,
  hasStaffAccountFormErrors,
  staffAccountFieldLabel,
  validateStaffAccountForm,
  type StaffAccountFormErrors,
  type StaffAccountFormField,
  type StaffAccountFormValues,
} from "../lib/staffAccountForm";
import { ActionErrorNote } from "./ActionErrorNote";
import { ActionResultNote, type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";

const STAFF_QUERY_KEY = ["staff", "accounts"] as const;

export interface StaffAccountsPanelProps {
  /** 可选注入点：Storybook/单测可以给一个脱离真实后端的 Action seam。 */
  client?: ApiClient;
}

/** 密码只在浏览器剪贴板 API 存在时尝试复制；不存在（旧浏览器、非安全上下文、
 *  测试环境）就什么也不做——密码已经显示在屏幕上，人仍能手动选中复制。 */
function copyToClipboard(value: string): void {
  try {
    void navigator.clipboard?.writeText(value);
  } catch {
    // 见上：复制失败不是错误，只是少一个便利
  }
}

/** 生成密码的一次性展示：复制按钮 + 明确的「只显示这一次」提示。
 *  不接受受控 open/close——调用方在自己的 Dialog 内切换到这个视图,
 *  「关闭」即代表「我已经记下了」。 */
function GeneratedPasswordReveal({ password, onDone }: { password: string; onDone: () => void }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex flex-col gap-3">
      <p className="text-xs text-fg-muted">
        这个密码只会显示这一次，请立即复制并通过安全渠道交给对方；关闭后将无法再次查看，只能重新生成。
      </p>
      <div className="flex items-center gap-2 rounded-md border border-edge bg-surface-muted px-3 py-2">
        <code className="flex-1 min-w-0 break-all font-mono text-sm text-fg">{password}</code>
        <Button
          type="button"
          size="sm"
          variant="secondary"
          onClick={() => {
            copyToClipboard(password);
            setCopied(true);
          }}
        >
          {copied ? "已复制" : "复制"}
        </Button>
      </div>
      <div className="flex justify-end">
        <Button type="button" size="sm" onClick={onDone}>
          知道了，关闭
        </Button>
      </div>
    </div>
  );
}

/** 角色多选：目录固定（api/staff.ts 的 STAFF_ROLE_CATALOG），用勾选框而不是
 *  下拉——设计系统没有 MultiSelect 原语，且角色只有五个，勾选框比多选下拉
 *  更容易一眼看清「现在选了哪几个」。 */
function RoleCheckboxGroup({
  label,
  selected,
  onToggle,
  error,
  disabled,
  idPrefix,
}: {
  label: string;
  selected: readonly string[];
  onToggle: (role: string) => void;
  error?: string;
  disabled?: boolean;
  idPrefix: string;
}) {
  return (
    <fieldset className="flex flex-col gap-1">
      <legend className="text-sm font-medium text-fg">
        {label}
        <span aria-hidden="true" className="text-danger">
          {" "}
          *
        </span>
      </legend>
      <div className="flex flex-col gap-1.5 rounded-md border border-edge p-2">
        {STAFF_ROLE_CATALOG.map((role) => {
          const id = `${idPrefix}-${role.value}`;
          return (
            <label key={role.value} htmlFor={id} className="flex items-center gap-2 text-sm text-fg">
              <input
                id={id}
                type="checkbox"
                checked={selected.includes(role.value)}
                disabled={disabled}
                onChange={() => onToggle(role.value)}
                className="size-4 rounded-sm border-edge accent-accent"
              />
              {role.label}
            </label>
          );
        })}
      </div>
      {error ? (
        <p role="alert" className="text-xs text-danger">
          {error}
        </p>
      ) : null}
    </fieldset>
  );
}

/** 全部员工账号表 + 新建账号入口（XM-LOGIN 人员与权限·账号与身份）。 */
export function StaffAccountsPanel({ client = undefined }: StaffAccountsPanelProps) {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: STAFF_QUERY_KEY,
    queryFn: ({ signal }) => listStaffAccounts({ signal }, client),
  });
  const [notice, setNotice] = useState<ActionResult | null>(null);

  const refresh = () => void queryClient.invalidateQueries({ queryKey: STAFF_QUERY_KEY });

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <p className="max-w-3xl text-xs leading-5 text-fg-muted">
          管理台自带账号密码登录（local 模式）的员工账号。新建账号时可以留空初始密码，由系统随机生成——生成的密码只会显示一次，请当场交接，本页不会再次显示它。
        </p>
        <CreateAccountDialog
          client={client}
          onCreated={(result) => {
            setNotice(result);
            refresh();
          }}
        />
      </div>

      {notice ? <ActionResultNote result={notice} onDismiss={() => setNotice(null)} /> : null}

      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
        <AccountsTable
          rows={query.data ?? []}
          client={client}
          onChanged={(result) => {
            setNotice(result);
            refresh();
          }}
        />
      </ApiStateView>
    </div>
  );
}

function CreateAccountDialog({
  client,
  onCreated,
}: {
  client?: ApiClient;
  onCreated: (result: ActionResult) => void;
}) {
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<StaffAccountFormValues>(EMPTY_STAFF_ACCOUNT_FORM);
  const [errors, setErrors] = useState<StaffAccountFormErrors>({});
  const [attempts, setAttempts] = useState(0);
  const [generatedPassword, setGeneratedPassword] = useState<string | null>(null);
  const fieldPrefix = useId();

  const mutation = useMutation({
    mutationFn: (form: StaffAccountFormValues) =>
      createStaffAccount(
        {
          username: form.username,
          displayName: form.displayName,
          roles: form.roles,
          ...(form.initialPassword ? { initialPassword: form.initialPassword } : {}),
        },
        {},
        client,
      ),
    onSuccess: (result) => {
      onCreated({ title: `已创建账号 ${values.username.trim()}`, runId: result.runId });
      if (result.initialPassword) {
        setGeneratedPassword(result.initialPassword);
      } else {
        closeAndReset();
      }
    },
  });

  const closeAndReset = () => {
    setOpen(false);
    setValues(EMPTY_STAFF_ACCOUNT_FORM);
    setErrors({});
    setAttempts(0);
    setGeneratedPassword(null);
    mutation.reset();
  };

  const toggleRole = (role: string) => {
    setValues((prev) => ({
      ...prev,
      roles: prev.roles.includes(role) ? prev.roles.filter((r) => r !== role) : [...prev.roles, role],
    }));
    setErrors((prev) => {
      if (!prev.roles) return prev;
      const next = { ...prev };
      delete next.roles;
      return next;
    });
  };

  const submit = () => {
    setAttempts((n) => n + 1);
    const found = validateStaffAccountForm(values);
    setErrors(found);
    if (hasStaffAccountFormErrors(found)) return;
    mutation.mutate(values);
  };

  const failedFields = (Object.keys(errors) as StaffAccountFormField[]).filter((f) => errors[f]);
  const showSummary = attempts > 0 && (failedFields.length > 0 || Boolean(mutation.error));

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) closeAndReset();
      }}
      trigger={<Button size="sm">新建账号</Button>}
      title={generatedPassword ? "账号已创建" : "新建账号"}
      description={
        generatedPassword ? undefined : "通过 staff.account.create@1 创建一个 local 模式的员工账号。"
      }
    >
      {generatedPassword ? (
        <GeneratedPasswordReveal password={generatedPassword} onDone={closeAndReset} />
      ) : (
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            submit();
          }}
        >
          {showSummary ? (
            <div
              role="alert"
              className="flex flex-col gap-1 rounded-md border border-danger/40 bg-danger/5 px-3 py-2 text-xs text-danger"
            >
              {failedFields.length > 0 ? (
                <span>
                  还有 {failedFields.length} 处需要修正：
                  {failedFields.map((f) => staffAccountFieldLabel(f)).join("、")}
                </span>
              ) : null}
              <ActionErrorNote error={mutation.error} permission={STAFF_MANAGE_PERMISSION} />
            </div>
          ) : null}

          <FormField
            label={staffAccountFieldLabel("username")}
            htmlFor={`${fieldPrefix}-username`}
            required
            error={errors.username}
            hint="登录用，创建后不可修改"
          >
            <Input
              id={`${fieldPrefix}-username`}
              value={values.username}
              disabled={mutation.isPending}
              invalid={Boolean(errors.username)}
              onChange={(event) => setValues((p) => ({ ...p, username: event.target.value }))}
              autoComplete="off"
              spellCheck={false}
            />
          </FormField>

          <FormField
            label={staffAccountFieldLabel("displayName")}
            htmlFor={`${fieldPrefix}-display-name`}
            required
            error={errors.displayName}
          >
            <Input
              id={`${fieldPrefix}-display-name`}
              value={values.displayName}
              disabled={mutation.isPending}
              invalid={Boolean(errors.displayName)}
              onChange={(event) => setValues((p) => ({ ...p, displayName: event.target.value }))}
            />
          </FormField>

          <RoleCheckboxGroup
            label={staffAccountFieldLabel("roles")}
            selected={values.roles}
            onToggle={toggleRole}
            error={errors.roles}
            disabled={mutation.isPending}
            idPrefix={`${fieldPrefix}-role`}
          />

          <FormField
            label={staffAccountFieldLabel("initialPassword")}
            htmlFor={`${fieldPrefix}-initial-password`}
            error={errors.initialPassword}
            hint={`留空则由系统生成一个随机密码；填写时至少 ${MIN_PASSWORD_LENGTH} 位`}
          >
            <Input
              id={`${fieldPrefix}-initial-password`}
              type="password"
              value={values.initialPassword}
              disabled={mutation.isPending}
              invalid={Boolean(errors.initialPassword)}
              onChange={(event) => setValues((p) => ({ ...p, initialPassword: event.target.value }))}
              autoComplete="new-password"
            />
          </FormField>

          <div className="flex justify-end gap-2">
            <Button type="button" size="sm" variant="secondary" onClick={closeAndReset} disabled={mutation.isPending}>
              取消
            </Button>
            <Button type="submit" size="sm" loading={mutation.isPending}>
              创建
            </Button>
          </div>
        </form>
      )}
    </Dialog>
  );
}

/** 三种状态说三句不同的话：禁用是人做的决定，锁定是登录失败次数触发的临时状态。 */
function accountStatus(row: StaffAccount): { label: string; tone: BadgeTone; title: string } {
  if (row.disabled) return { label: "已禁用", tone: "danger", title: "账号已被禁用，无法登录" };
  if (row.locked_until) {
    return { label: "已锁定", tone: "warning", title: `锁定至 ${formatUtcTimestamp(row.locked_until)}` };
  }
  return { label: "正常", tone: "success", title: "可正常登录" };
}

function AccountsTable({
  rows,
  client,
  onChanged,
}: {
  rows: readonly StaffAccount[];
  client?: ApiClient;
  onChanged: (result: ActionResult) => void;
}) {
  // 不能禁用自己当前登录的账号：那会当场把自己踢出去，且没有别人能替你重新启用
  // （如果这恰好是唯一的 admin 账号，等于把自己锁死在门外）
  const currentUsername = cachedLocalUser()?.username;

  if (rows.length === 0) {
    return (
      <PageState kind="empty" title="暂无账号" description="点击右上角「新建账号」创建第一个管理台账号。" />
    );
  }

  const columns: DataTableColumn<StaffAccount>[] = [
    {
      id: "username",
      header: "用户名",
      primary: true,
      value: (row) => row.username,
      cell: (row) => <span className="font-mono text-xs text-fg">{row.username}</span>,
    },
    {
      id: "display_name",
      header: "显示名",
      value: (row) => row.display_name,
      cell: (row) => <span className="text-sm text-fg">{row.display_name}</span>,
    },
    {
      id: "roles",
      header: "角色",
      value: (row) => row.roles.join(","),
      cell: (row) => (
        <div className="flex flex-wrap gap-1">
          {row.roles.length > 0 ? (
            row.roles.map((role) => (
              <Badge key={role} tone="neutral">
                {staffRoleLabel(role)}
              </Badge>
            ))
          ) : (
            <span className="text-xs text-fg-muted">—</span>
          )}
        </div>
      ),
    },
    {
      id: "status",
      header: "状态",
      value: (row) => accountStatus(row).label,
      cell: (row) => {
        const status = accountStatus(row);
        return (
          <Badge tone={status.tone} title={status.title}>
            {status.label}
          </Badge>
        );
      },
    },
    {
      id: "locked_until",
      header: "锁定至",
      value: (row) => row.locked_until ?? "",
      cell: (row) => (
        <span className="text-xs text-fg-muted">
          {row.locked_until ? formatUtcTimestamp(row.locked_until) : "—"}
        </span>
      ),
    },
    {
      id: "last_login_at",
      header: "最后登录",
      value: (row) => row.last_login_at ?? "",
      cell: (row) => (
        <span className="text-xs text-fg-muted">
          {row.last_login_at ? formatUtcTimestamp(row.last_login_at) : "从未登录"}
        </span>
      ),
    },
    {
      id: "actions",
      header: "操作",
      cell: (row) => (
        <div className="flex flex-wrap gap-2">
          <RolesDialog account={row} client={client} onChanged={onChanged} />
          <DisableToggleButton
            account={row}
            client={client}
            onChanged={onChanged}
            disabled={row.username === currentUsername}
          />
          <ResetPasswordDialog account={row} client={client} onChanged={onChanged} />
        </div>
      ),
    },
  ];

  return (
    <DataTableV2
      caption="员工账号列表：用户名、显示名、角色、状态、锁定至与最后登录时间"
      columns={columns}
      rows={rows}
      rowKey={(row) => row.username}
      searchable
      emptyState={<PageState kind="empty" title="没有匹配的账号" description="调整搜索条件后重试。" />}
    />
  );
}

function RolesDialog({
  account,
  client,
  onChanged,
}: {
  account: StaffAccount;
  client?: ApiClient;
  onChanged: (result: ActionResult) => void;
}) {
  const [open, setOpen] = useState(false);
  const [roles, setRoles] = useState<string[]>(account.roles);
  const [error, setError] = useState<string | undefined>(undefined);
  const fieldPrefix = useId();

  const mutation = useMutation({
    mutationFn: (next: string[]) => setStaffAccountRoles(account.username, next, {}, client),
    onSuccess: (run) => {
      setOpen(false);
      onChanged({ title: `已更新 ${account.username} 的角色`, runId: run.runId });
    },
  });

  const reset = () => {
    setRoles(account.roles);
    setError(undefined);
    mutation.reset();
  };

  const toggle = (role: string) => {
    setRoles((prev) => (prev.includes(role) ? prev.filter((r) => r !== role) : [...prev, role]));
    setError(undefined);
  };

  const submit = () => {
    if (roles.length === 0) {
      setError("至少选择一个角色");
      return;
    }
    mutation.mutate(roles);
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) reset();
      }}
      trigger={
        <Button size="sm" variant="secondary" aria-label={`修改 ${account.username} 的角色`}>
          改角色
        </Button>
      }
      title={`修改角色：${account.username}`}
      description="通过 staff.account.set_roles@1 整体替换该账号的角色集合。"
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <RoleCheckboxGroup
          label="角色"
          selected={roles}
          onToggle={toggle}
          error={error}
          disabled={mutation.isPending}
          idPrefix={`${fieldPrefix}-role`}
        />
        <ActionErrorNote error={mutation.error} permission={STAFF_MANAGE_PERMISSION} />
        <div className="flex justify-end gap-2">
          <Button type="button" size="sm" variant="secondary" onClick={() => setOpen(false)} disabled={mutation.isPending}>
            取消
          </Button>
          <Button type="submit" size="sm" loading={mutation.isPending}>
            保存
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

function DisableToggleButton({
  account,
  client,
  onChanged,
  disabled = false,
}: {
  account: StaffAccount;
  client?: ApiClient;
  onChanged: (result: ActionResult) => void;
  disabled?: boolean;
}) {
  const mutation = useMutation({
    mutationFn: () => setStaffAccountDisabled(account.username, !account.disabled, {}, client),
    onSuccess: (run) => {
      onChanged({
        title: account.disabled ? `已启用 ${account.username}` : `已禁用 ${account.username}`,
        runId: run.runId,
      });
    },
  });

  return (
    <div className="flex flex-col gap-1">
      <Button
        size="sm"
        variant={account.disabled ? "secondary" : "danger"}
        aria-label={`${account.disabled ? "启用" : "禁用"} ${account.username}`}
        title={disabled ? "不能禁用自己当前登录使用的账号" : undefined}
        disabled={disabled || mutation.isPending}
        loading={mutation.isPending}
        onClick={() => mutation.mutate()}
      >
        {account.disabled ? "启用" : "禁用"}
      </Button>
      <ActionErrorNote error={mutation.error} permission={STAFF_MANAGE_PERMISSION} />
    </div>
  );
}

function ResetPasswordDialog({
  account,
  client,
  onChanged,
}: {
  account: StaffAccount;
  client?: ApiClient;
  onChanged: (result: ActionResult) => void;
}) {
  const [open, setOpen] = useState(false);
  const [newPassword, setNewPassword] = useState("");
  const [error, setError] = useState<string | undefined>(undefined);
  const [generatedPassword, setGeneratedPassword] = useState<string | null>(null);
  const fieldPrefix = useId();

  const mutation = useMutation({
    mutationFn: () => resetStaffAccountPassword(account.username, newPassword.trim() || undefined, {}, client),
    onSuccess: (result) => {
      onChanged({ title: `已重置 ${account.username} 的密码`, runId: result.runId });
      if (result.initialPassword) {
        setGeneratedPassword(result.initialPassword);
      } else {
        closeAndReset();
      }
    },
  });

  const closeAndReset = () => {
    setOpen(false);
    setNewPassword("");
    setError(undefined);
    setGeneratedPassword(null);
    mutation.reset();
  };

  const submit = () => {
    if (newPassword && newPassword.length < MIN_PASSWORD_LENGTH) {
      setError(`至少 ${MIN_PASSWORD_LENGTH} 位；留空则由系统生成`);
      return;
    }
    mutation.mutate();
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) closeAndReset();
      }}
      trigger={
        <Button size="sm" variant="secondary" aria-label={`重置 ${account.username} 的密码`}>
          重置密码
        </Button>
      }
      title={generatedPassword ? "密码已重置" : `重置密码：${account.username}`}
      description={
        generatedPassword
          ? undefined
          : "留空新密码则由系统随机生成；通过 staff.account.reset_password@1 执行。"
      }
    >
      {generatedPassword ? (
        <GeneratedPasswordReveal password={generatedPassword} onDone={closeAndReset} />
      ) : (
        <form
          className="flex flex-col gap-3"
          onSubmit={(event) => {
            event.preventDefault();
            submit();
          }}
        >
          <FormField
            label="新密码"
            htmlFor={`${fieldPrefix}-new-password`}
            error={error}
            hint="留空则由系统生成一个随机密码"
          >
            <Input
              id={`${fieldPrefix}-new-password`}
              type="password"
              value={newPassword}
              disabled={mutation.isPending}
              invalid={Boolean(error)}
              onChange={(event) => {
                setNewPassword(event.target.value);
                setError(undefined);
              }}
              autoComplete="new-password"
            />
          </FormField>
          <ActionErrorNote error={mutation.error} permission={STAFF_MANAGE_PERMISSION} />
          <div className="flex justify-end gap-2">
            <Button type="button" size="sm" variant="secondary" onClick={() => setOpen(false)} disabled={mutation.isPending}>
              取消
            </Button>
            <Button type="submit" size="sm" loading={mutation.isPending}>
              重置
            </Button>
          </div>
        </form>
      )}
    </Dialog>
  );
}
