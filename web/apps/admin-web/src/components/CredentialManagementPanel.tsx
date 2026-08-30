import { useMutation, useQuery } from "@tanstack/react-query";
import {
  DataTableV2,
  PageState,
  formatUtcTimestamp,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Button, Dialog, FormField, Input } from "@xingmang/ui-primitives";
import { useEffect, useId, useRef, useState } from "react";
import { ApiError, type ApiClient } from "../api/client";
import {
  CREDENTIAL_MANAGE_PERMISSION,
  fingerprintPrefix,
  listCredentials,
  revokeCredential,
  rotateCredential,
  upsertCredential,
  type CredentialMetadata,
  type CredentialRevokeInput,
  type CredentialValueInput,
} from "../api/credentials";
import { appApiConfig, type PlatformApiConfig } from "../api/config";
import { ActionErrorNote } from "./ActionErrorNote";
import { ActionResultNote, type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";
import {
  EMPTY_CREDENTIAL_FORM,
  buildCredentialParams,
  credentialFieldLabel,
  validateCredentialForm,
  validateRevokeReason,
  type CredentialFormErrors,
  type CredentialFormValues,
} from "../lib/credentialForm";

export interface CredentialManagementPanelProps {
  /** 可选注入点：Storybook/单测可以给一个脱离真实后端的 Action seam。 */
  client?: ApiClient;
  config?: PlatformApiConfig;
}

type WriteMode = "upsert" | "rotate";

interface WriteVariables {
  mode: WriteMode;
  values: CredentialFormValues;
}

/** 设置页的凭据登记簿 + 粘贴即保存表单。
 *
 *  这个组件只认识两类东西：Query 返回的四个安全元数据字段，以及 Action 的
 *  action_run_id。SecretProvider、文件权限和连接器现场解析属于后端片，留在
 * 这里的 client/config 注入点只为预览与契约测试服务。 */
export function CredentialManagementPanel({
  client = undefined,
  config = appApiConfig,
}: CredentialManagementPanelProps) {
  const query = useQuery({
    queryKey: ["credentials", config.environment],
    queryFn: ({ signal }) => listCredentials({ signal }, client, config),
  });
  const [mode, setMode] = useState<WriteMode>("upsert");
  const [form, setForm] = useState<CredentialFormValues>(EMPTY_CREDENTIAL_FORM);
  const [errors, setErrors] = useState<CredentialFormErrors>({});
  const [notice, setNotice] = useState<ActionResult | null>(null);
  const [actionError, setActionError] = useState<unknown>(null);
  const lastSubmittedSecret = useRef("");
  const fieldPrefix = useId();

  const writeMutation = useMutation<
    { runId: string; result: unknown },
    unknown,
    WriteVariables
  >({
    mutationFn: ({ mode: nextMode, values }) => {
      const params = buildCredentialParams(values);
      lastSubmittedSecret.current = params.secretValue;
      const input: CredentialValueInput = params;
      return nextMode === "rotate"
        ? rotateCredential(input, {}, client)
        : upsertCredential(input, {}, client);
    },
    onSuccess: (run, variables) => {
      lastSubmittedSecret.current = "";
      setActionError(null);
      setErrors({});
      // 引用保留在表单里便于连续轮换；值永远清空，不做成功后的回显。
      setForm({ credentialRef: variables.values.credentialRef.trim(), secretValue: "" });
      setNotice({
        title: variables.mode === "rotate" ? "已轮换凭据" : "已保存凭据",
        runId: run.runId,
      });
      void query.refetch();
      // TanStack Query 会暂存 mutation variables；清掉它，避免 secret_value
      // 在前端 mutation 状态里比请求生命周期更久。
      writeMutation.reset();
    },
    onError: (error) => {
      // 后端契约要求错误不回显秘密；这里再做一层本地防护，避免错误代理误把
      // 本次粘贴值带回页面。输入框也在失败后清空，要求用户重新粘贴。
      const secret = lastSubmittedSecret.current;
      lastSubmittedSecret.current = "";
      setForm((previous) => ({ ...previous, secretValue: "" }));
      setActionError(redactActionError(error, secret));
      writeMutation.reset();
    },
  });

  const setField = (field: keyof CredentialFormValues, value: string) => {
    setForm((previous) => ({ ...previous, [field]: value }));
    setErrors((previous) => {
      if (!(field in previous)) return previous;
      const next = { ...previous };
      delete next[field];
      return next;
    });
    setActionError(null);
  };

  const resetForm = () => {
    setMode("upsert");
    setForm(EMPTY_CREDENTIAL_FORM);
    setErrors({});
    setActionError(null);
    lastSubmittedSecret.current = "";
  };

  const edit = (credential: CredentialMetadata) => {
    // 只把引用带入编辑态；旧值不可读，因此值输入框必须为空。
    setMode("rotate");
    setForm({ credentialRef: credential.credential_ref, secretValue: "" });
    setErrors({});
    setActionError(null);
  };

  const submit = () => {
    const found = validateCredentialForm(form);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setNotice(null);
    writeMutation.mutate({ mode, values: form });
  };

  const list = query.error
    ? isCredentialQueryUnavailable(query.error)
      ? (
          <PageState
            kind="unavailable"
            title="凭据登记簿尚未接入"
            description="后端 Credential Query 尚未接线；此处不填充样例引用，粘贴表单仅作为 Action 预览边界。"
          />
        )
      : (
          <ApiStateView
            isPending={query.isPending}
            error={query.error}
            onRetry={() => void query.refetch()}
          >
            <></>
          </ApiStateView>
        )
    : (
        <ApiStateView
          isPending={query.isPending}
          error={query.error}
          onRetry={() => void query.refetch()}
        >
          <CredentialTable rows={query.data ?? []} onEdit={edit} onRevoked={(run, credentialRef) => {
            // 正在轮换的同一引用被吊销后，退出编辑态，避免表单继续指向已撤销对象。
            if (form.credentialRef.trim() === credentialRef) resetForm();
            setNotice({ title: "已吊销凭据", runId: run.runId });
            void query.refetch();
          }} client={client} editingDisabled={writeMutation.isPending} />
        </ApiStateView>
      );

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <div className="rounded-lg border border-edge bg-surface px-4 py-3 shadow-sm">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 className="text-base font-semibold text-fg">凭据引用与轮换</h2>
            <p className="mt-1 max-w-4xl text-xs leading-5 text-fg-muted">
              平台只保存 CredentialRef；值经 Action 写入仓库外 SecretProvider。保存、轮换或吊销后，值不会回读到页面、响应或审计摘要。
            </p>
          </div>
          <span className="rounded-full border border-edge px-2 py-1 text-xs text-fg-muted">值不可读回</span>
        </div>
      </div>

      {notice ? <ActionResultNote result={notice} onDismiss={() => setNotice(null)} /> : null}

      <div className="grid min-w-0 grid-cols-1 gap-4 xl:grid-cols-[minmax(0,1.5fr)_minmax(19rem,0.8fr)]">
        <section className="min-w-0" aria-labelledby={`${fieldPrefix}-list-title`}>
          <div className="mb-2 flex items-baseline justify-between gap-3">
            <h3 id={`${fieldPrefix}-list-title`} className="text-sm font-semibold text-fg">已登记引用</h3>
            <span className="text-xs text-fg-muted">仅显示四项安全元数据</span>
          </div>
          {list}
        </section>

        <CredentialForm
          mode={mode}
          form={form}
          errors={errors}
          actionError={actionError}
          pending={writeMutation.isPending}
          onChange={setField}
          onSubmit={submit}
          onReset={resetForm}
          fieldPrefix={fieldPrefix}
        />
      </div>
    </div>
  );
}

function CredentialTable({
  rows,
  onEdit,
  onRevoked,
  client,
  editingDisabled,
}: {
  rows: readonly CredentialMetadata[];
  onEdit: (credential: CredentialMetadata) => void;
  onRevoked: (run: { runId: string; result: unknown }, credentialRef: string) => void;
  client?: ApiClient;
  editingDisabled: boolean;
}) {
  if (rows.length === 0) {
    return (
      <PageState
        kind="empty"
        title="暂无凭据引用"
        description="当前环境没有已登记的 CredentialRef；后端 Query/SecretProvider 接线完成后，保存成功的引用会出现在这里。"
      />
    );
  }

  const columns: DataTableColumn<CredentialMetadata>[] = [
    {
      id: "credential_ref",
      header: "CredentialRef",
      primary: true,
      value: (row) => row.credential_ref,
      cell: (row) => <span className="font-mono text-xs break-all text-fg">{row.credential_ref}</span>,
    },
    {
      id: "scope",
      header: "scope",
      value: (row) => row.scope,
      cell: (row) => <span className="font-mono text-xs text-fg">{row.scope || "—"}</span>,
    },
    {
      id: "updated_at",
      header: "更新时间",
      value: (row) => row.updated_at,
      cell: (row) => (
        <span className="text-xs text-fg-muted">
          {row.updated_at ? formatUtcTimestamp(row.updated_at) : "—"}
        </span>
      ),
    },
    {
      id: "fingerprint",
      header: "指纹",
      value: (row) => row.fingerprint,
      cell: (row) => (
        <span className="font-mono text-xs text-fg-muted" title="仅显示指纹前八位">
          {fingerprintPrefix(row.fingerprint)}
        </span>
      ),
    },
    {
      id: "actions",
      header: "操作",
      cell: (row) => (
        <div className="flex flex-wrap gap-2">
          <Button
            size="sm"
            variant="secondary"
            aria-label="修改凭据"
            disabled={editingDisabled}
            onClick={() => onEdit(row)}
          >
            修改
          </Button>
          <RevokeDialog
            credentialRef={row.credential_ref}
            client={client}
            onRevoked={onRevoked}
            disabled={editingDisabled}
          />
        </div>
      ),
    },
  ];

  return (
    <DataTableV2
      caption="凭据引用列表：CredentialRef、scope、更新时间与指纹前缀"
      columns={columns}
      rows={rows}
      rowKey={(row) => row.credential_ref}
      searchable
      emptyState={
        <PageState
          kind="empty"
          title="没有匹配的凭据引用"
          description="调整搜索条件后重试。"
        />
      }
    />
  );
}

function CredentialForm({
  mode,
  form,
  errors,
  actionError,
  pending,
  onChange,
  onSubmit,
  onReset,
  fieldPrefix,
}: {
  mode: WriteMode;
  form: CredentialFormValues;
  errors: CredentialFormErrors;
  actionError: unknown;
  pending: boolean;
  onChange: (field: keyof CredentialFormValues, value: string) => void;
  onSubmit: () => void;
  onReset: () => void;
  fieldPrefix: string;
}) {
  const editing = mode === "rotate";
  const [attempts, setAttempts] = useState(0);
  const summaryRef = useRef<HTMLDivElement>(null);
  const failedFields = (Object.keys(errors) as Array<keyof CredentialFormValues>).filter(
    (field) => Boolean(errors[field]),
  );
  const showSummary = attempts > 0 && (failedFields.length > 0 || Boolean(actionError));
  useEffect(() => {
    if (showSummary) summaryRef.current?.focus();
  }, [showSummary, attempts]);

  const reset = () => {
    setAttempts(0);
    onReset();
  };

  return (
    <section className="rounded-lg border border-edge bg-surface p-4 shadow-sm" aria-labelledby={`${fieldPrefix}-form-title`}>
      <div className="mb-3 flex items-start justify-between gap-3">
        <div>
          <h3 id={`${fieldPrefix}-form-title`} className="text-sm font-semibold text-fg">
            {editing ? "轮换凭据" : "添加凭据"}
          </h3>
          <p className="mt-1 text-xs leading-5 text-fg-muted">
            {editing
              ? "只填写新值；现有值不可读取。提交会调用 credential.rotate。"
              : "粘贴一次即保存；提交会调用 credential.upsert。"}
          </p>
        </div>
        {editing ? (
          <Button size="sm" variant="ghost" onClick={reset} type="button" disabled={pending}>
            添加新的
          </Button>
        ) : null}
      </div>

      <form
        className="flex flex-col gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          setAttempts((count) => count + 1);
          onSubmit();
        }}
      >
        {showSummary ? (
          <div
            ref={summaryRef}
            role="alert"
            aria-label="请检查凭据表单"
            tabIndex={-1}
            className="rounded-md border border-danger/40 bg-danger/5 px-3 py-2 text-xs text-danger outline-none focus-visible:outline-2 focus-visible:outline-danger"
          >
            <span>
              请检查凭据表单
              {failedFields.length > 0
                ? `：${failedFields.map((field) => credentialFieldLabel(field)).join("、")}`
                : "：Action 未完成"}
            </span>
            {actionError ? (
              <span className="mt-1 block">
                <ActionErrorNote error={actionError} permission={CREDENTIAL_MANAGE_PERMISSION} />
              </span>
            ) : null}
          </div>
        ) : null}

        <FormField
          label={credentialFieldLabel("credentialRef")}
          htmlFor={`${fieldPrefix}-credential-ref`}
          required
          error={errors.credentialRef}
          hint="形如 secret://sub2api/readonly；只保存引用名，不是密钥正文"
        >
          <Input
            id={`${fieldPrefix}-credential-ref`}
            value={form.credentialRef}
            readOnly={editing}
            disabled={pending}
            invalid={Boolean(errors.credentialRef)}
            onChange={(event) => onChange("credentialRef", event.target.value)}
            autoComplete="off"
            spellCheck={false}
          />
        </FormField>

        <FormField
          label={credentialFieldLabel("secretValue")}
          htmlFor={`${fieldPrefix}-secret-value`}
          required
          error={errors.secretValue}
          hint="只在本次请求内暂存；保存后不会再次显示，失败后也会清空"
        >
          <Input
            id={`${fieldPrefix}-secret-value`}
            type="password"
            disabled={pending}
            value={form.secretValue}
            invalid={Boolean(errors.secretValue)}
            onChange={(event) => onChange("secretValue", event.target.value)}
            autoComplete="new-password"
            spellCheck={false}
          />
        </FormField>

        <div className="flex flex-wrap justify-end gap-2">
          {editing ? (
            <Button type="button" size="sm" variant="secondary" onClick={reset} disabled={pending}>
              取消
            </Button>
          ) : null}
          <Button type="submit" size="sm" loading={pending}>
            {editing ? "轮换并保存" : "保存凭据"}
          </Button>
        </div>
      </form>
    </section>
  );
}

function RevokeDialog({
  credentialRef,
  client,
  onRevoked,
  disabled = false,
}: {
  credentialRef: string;
  client?: ApiClient;
  onRevoked: (run: { runId: string; result: unknown }, credentialRef: string) => void;
  disabled?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const [reasonError, setReasonError] = useState<string | undefined>();
  const fieldId = useId();
  const mutation = useMutation({
    mutationFn: (input: CredentialRevokeInput) => revokeCredential(input, {}, client),
    onSuccess: (run) => {
      setOpen(false);
      setReason("");
      setReasonError(undefined);
      onRevoked(run, credentialRef);
    },
  });

  const submit = () => {
    const error = validateRevokeReason(reason);
    setReasonError(error);
    if (error) return;
    mutation.mutate({ credentialRef, reason });
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) {
          setReason("");
          setReasonError(undefined);
          mutation.reset();
        }
      }}
      trigger={
        <Button size="sm" variant="danger" aria-label="吊销凭据" disabled={disabled}>
          吊销
        </Button>
      }
      title="吊销凭据"
      description="吊销只携带 CredentialRef 与审计理由；值不会进入该请求。"
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          submit();
        }}
      >
        <p className="rounded-md border border-edge bg-surface-muted px-3 py-2 font-mono text-xs text-fg-muted break-all">
          {credentialRef}
        </p>
        <FormField
          label="吊销原因"
          htmlFor={fieldId}
          required
          error={reasonError}
          hint="原因会作为审计摘要的一部分保存，不要填写凭据值"
        >
          <Input
            id={fieldId}
            aria-label="吊销原因"
            value={reason}
            invalid={Boolean(reasonError)}
            onChange={(event) => {
              setReason(event.target.value);
              setReasonError(undefined);
            }}
            autoComplete="off"
          />
        </FormField>
        <ActionErrorNote error={mutation.error} permission={CREDENTIAL_MANAGE_PERMISSION} />
        <div className="flex justify-end gap-2">
          <Button type="button" size="sm" variant="secondary" onClick={() => setOpen(false)}>
            取消
          </Button>
          <Button type="submit" size="sm" variant="danger" loading={mutation.isPending}>
            确认吊销
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

function isCredentialQueryUnavailable(error: unknown): boolean {
  if (error instanceof ApiError) {
    return error.code === "ACTION_NOT_REGISTERED" || error.code === "NOT_IMPLEMENTED";
  }
  return error instanceof Error && /尚未接入|未实现/.test(error.message);
}

function redactActionError(error: unknown, secret: string): unknown {
  if (!secret) return error;
  if (error instanceof ApiError && error.message.includes(secret)) {
    return new ApiError(
      error.status,
      error.code,
      "凭据 Action 失败；请检查 CredentialRef、权限与 SecretProvider 状态",
      error.requestId,
    );
  }
  if (error instanceof Error && error.message.includes(secret)) {
    return new Error("凭据 Action 失败；请检查 CredentialRef、权限与 SecretProvider 状态");
  }
  return error;
}
