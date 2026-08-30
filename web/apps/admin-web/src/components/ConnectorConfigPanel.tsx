import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { PageState, formatUtcTimestamp } from "@xingmang/ui-admin";
import { Badge, Button, FormField, Input, Select } from "@xingmang/ui-primitives";
import { useEffect, useId, useRef, useState } from "react";
import type { ApiClient } from "../api/client";
import {
  CONNECTOR_CONFIG_QUERY_KEY,
  CONNECTOR_MANAGE_PERMISSION,
  CONNECTOR_PLATFORMS,
  isConnectorPlatform,
  listConnectorConfigs,
  setConnectorConfig,
  type ConnectorConfig,
  type ConnectorMode,
  type ConnectorPlatform,
} from "../api/connectors";
import { CREDENTIAL_QUERY_KEYS, listExpectedCredentials } from "../api/credentials";
import type { ActionRun } from "../api/platform";
import {
  CONNECTOR_MODE_OPTIONS,
  connectorFieldLabel,
  connectorFormFromConfig,
  validateConnectorForm,
  type ConnectorFormErrors,
  type ConnectorFormField,
  type ConnectorFormValues,
} from "../lib/connectorForm";
import { PLATFORM_CATALOG } from "../lib/platforms";
import { ActionErrorNote } from "./ActionErrorNote";
import { ActionResultNote, type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";

export interface ConnectorConfigPanelProps {
  /** 可选注入点：单测给一个脱离真实后端的 client。 */
  client?: ApiClient;
}

function platformLabel(platform: string): string {
  return PLATFORM_CATALOG.find((spec) => spec.serviceType === platform)?.label ?? platform;
}

/** 各平台的接入模式（fake / real）表单。
 *
 *  读 GET /connectors/config 显示数据库里当前生效的值；写走
 *  connector.config.set@1。切换生效靠 worker 每轮同步前重读配置，不需要重启——
 *  这句话印在页面上，免得有人保存完去找重启按钮。 */
export function ConnectorConfigPanel({ client }: ConnectorConfigPanelProps) {
  const queryClient = useQueryClient();
  const configs = useQuery({
    queryKey: CONNECTOR_CONFIG_QUERY_KEY,
    queryFn: ({ signal }) => listConnectorConfigs({ signal }, client),
  });
  // 预期清单只用来给 CredentialRef 一个默认值；读不到就留空，不挡住整块表单
  const expected = useQuery({
    queryKey: CREDENTIAL_QUERY_KEYS.expected,
    queryFn: ({ signal }) => listExpectedCredentials({ signal }, client),
  });
  const [notice, setNotice] = useState<ActionResult | null>(null);
  const titleId = useId();

  const onSaved = (run: ActionRun, platform: ConnectorPlatform, mode: ConnectorMode) => {
    setNotice({
      title: `已保存 ${platformLabel(platform)} 接入配置（${mode}）`,
      runId: run.runId,
    });
    void queryClient.invalidateQueries({ queryKey: CONNECTOR_CONFIG_QUERY_KEY });
  };

  const unknownPlatforms = (configs.data ?? [])
    .map((item) => item.platform)
    .filter((platform) => !isConnectorPlatform(platform));

  return (
    <section className="flex min-w-0 flex-col gap-3" aria-labelledby={titleId}>
      <div className="rounded-lg border border-edge bg-surface px-4 py-3 shadow-sm">
        <h2 id={titleId} className="text-base font-semibold text-fg">
          接入模式
        </h2>
        <p className="mt-1 max-w-4xl text-xs leading-5 text-fg-muted">
          每个平台一条配置：fake 用演示数据，real 只读连接真实上游。保存后 worker
          在下一轮同步周期（≤5 分钟）内自动切换，无需重启。
        </p>
      </div>

      {notice ? <ActionResultNote result={notice} onDismiss={() => setNotice(null)} /> : null}

      <ApiStateView
        isPending={configs.isPending || expected.isPending}
        error={configs.error}
        onRetry={() => void configs.refetch()}
      >
        {expected.isError ? (
          <p className="text-xs text-fg-muted">
            预期凭据清单读取失败，CredentialRef 没有默认值，需要手动填写。
          </p>
        ) : null}
        <div className="grid min-w-0 grid-cols-1 gap-4 xl:grid-cols-2">
          {CONNECTOR_PLATFORMS.map((platform) => {
            const current = (configs.data ?? []).find((item) => item.platform === platform);
            const defaultCredentialRef =
              (expected.data ?? []).find((item) => item.platform === platform)?.credential_ref ?? "";
            return (
              <ConnectorForm
                // 版本变了就整块重挂：表单初值来自数据库，别让保存前的草稿盖住别人刚改的值
                key={`${platform}:${current?.version ?? 0}`}
                platform={platform}
                current={current}
                defaultCredentialRef={defaultCredentialRef}
                client={client}
                onSaved={onSaved}
              />
            );
          })}
        </div>
        {unknownPlatforms.length > 0 ? (
          <p className="text-xs text-fg-muted">
            数据库里还有前端不认识的平台配置：{unknownPlatforms.join("、")}；本页不提供它们的表单。
          </p>
        ) : null}
      </ApiStateView>
    </section>
  );
}

function ConnectorForm({
  platform,
  current,
  defaultCredentialRef,
  client,
  onSaved,
}: {
  platform: ConnectorPlatform;
  current: ConnectorConfig | undefined;
  defaultCredentialRef: string;
  client?: ApiClient;
  onSaved: (run: ActionRun, platform: ConnectorPlatform, mode: ConnectorMode) => void;
}) {
  const label = platformLabel(platform);
  const [form, setForm] = useState<ConnectorFormValues>(() =>
    connectorFormFromConfig(current, defaultCredentialRef),
  );
  const [errors, setErrors] = useState<ConnectorFormErrors>({});
  const [attempts, setAttempts] = useState(0);
  const summaryRef = useRef<HTMLDivElement>(null);
  const fieldPrefix = useId();

  const mutation = useMutation({
    mutationFn: (values: ConnectorFormValues) => setConnectorConfig({ platform, ...values }, {}, client),
    onSuccess: (run, values) => {
      setErrors({});
      onSaved(run, platform, values.mode);
    },
  });

  const failedFields = (Object.keys(errors) as ConnectorFormField[]).filter((field) =>
    Boolean(errors[field]),
  );
  const showSummary = attempts > 0 && (failedFields.length > 0 || Boolean(mutation.error));
  useEffect(() => {
    if (showSummary) summaryRef.current?.focus();
  }, [showSummary, attempts]);

  const setField = (field: ConnectorFormField, value: string) => {
    setForm((previous) => ({ ...previous, [field]: value }));
    setErrors((previous) => {
      if (!(field in previous)) return previous;
      const next = { ...previous };
      delete next[field];
      return next;
    });
    mutation.reset();
  };

  const submit = () => {
    const found = validateConnectorForm(form);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    mutation.mutate(form);
  };

  const real = form.mode === "real";

  return (
    <section
      className="flex min-w-0 flex-col gap-3 rounded-lg border border-edge bg-surface p-4 shadow-sm"
      aria-labelledby={`${fieldPrefix}-title`}
    >
      <div className="flex items-start justify-between gap-3">
        <h3 id={`${fieldPrefix}-title`} className="text-sm font-semibold text-fg">
          {label}
        </h3>
        {current ? (
          <Badge tone={current.mode === "real" ? "info" : "neutral"} title="数据库里当前生效的模式">
            当前 {current.mode || "—"}
          </Badge>
        ) : (
          <Badge tone="warning" title="数据库里没有这个平台的接入配置记录">
            未配置
          </Badge>
        )}
      </div>

      <CurrentConfig current={current} />

      <form
        className="flex flex-col gap-3"
        onSubmit={(event) => {
          event.preventDefault();
          setAttempts((count) => count + 1);
          submit();
        }}
      >
        {showSummary ? (
          <div
            ref={summaryRef}
            role="alert"
            aria-label={`请检查 ${label} 接入配置`}
            tabIndex={-1}
            className="rounded-md border border-danger/40 bg-danger/5 px-3 py-2 text-xs text-danger outline-none focus-visible:outline-2 focus-visible:outline-danger"
          >
            <span>
              请检查接入配置
              {failedFields.length > 0
                ? `：${failedFields.map((field) => connectorFieldLabel(field)).join("、")}`
                : "：Action 未完成"}
            </span>
            {mutation.error ? (
              <span className="mt-1 block">
                <ActionErrorNote error={mutation.error} permission={CONNECTOR_MANAGE_PERMISSION} />
              </span>
            ) : null}
          </div>
        ) : null}

        <FormField label={connectorFieldLabel("mode")} required error={errors.mode}>
          <Select
            aria-label={`${label} 接入模式`}
            options={[...CONNECTOR_MODE_OPTIONS]}
            value={form.mode}
            disabled={mutation.isPending}
            onValueChange={(value) => setField("mode", value === "real" ? "real" : "fake")}
          />
        </FormField>

        <FormField
          label={connectorFieldLabel("endpoint")}
          htmlFor={`${fieldPrefix}-endpoint`}
          required={real}
          error={errors.endpoint}
          hint="形如 https://api.example.com；必须是 https，fake 模式可留空"
        >
          <Input
            id={`${fieldPrefix}-endpoint`}
            value={form.endpoint}
            disabled={mutation.isPending}
            invalid={Boolean(errors.endpoint)}
            onChange={(event) => setField("endpoint", event.target.value)}
            autoComplete="off"
            spellCheck={false}
          />
        </FormField>

        <FormField
          label={connectorFieldLabel("targetAllowlist")}
          htmlFor={`${fieldPrefix}-allowlist`}
          required={real}
          error={errors.targetAllowlist}
          hint="英文逗号分隔的主机名；必须包含上游地址的主机（ADR-004）"
        >
          <Input
            id={`${fieldPrefix}-allowlist`}
            value={form.targetAllowlist}
            disabled={mutation.isPending}
            invalid={Boolean(errors.targetAllowlist)}
            onChange={(event) => setField("targetAllowlist", event.target.value)}
            autoComplete="off"
            spellCheck={false}
          />
        </FormField>

        <FormField
          label={connectorFieldLabel("credentialRef")}
          htmlFor={`${fieldPrefix}-credential-ref`}
          required={real}
          error={errors.credentialRef}
          hint={
            defaultCredentialRef
              ? `默认取该平台的预期引用 ${defaultCredentialRef}；只填引用名，不是密钥正文`
              : "形如 secret://<scope>/<name>；只填引用名，不是密钥正文"
          }
        >
          <Input
            id={`${fieldPrefix}-credential-ref`}
            value={form.credentialRef}
            disabled={mutation.isPending}
            invalid={Boolean(errors.credentialRef)}
            onChange={(event) => setField("credentialRef", event.target.value)}
            autoComplete="off"
            spellCheck={false}
          />
        </FormField>

        <div className="flex flex-wrap items-center justify-end gap-2">
          <Button type="submit" size="sm" loading={mutation.isPending} aria-label={`保存 ${label} 接入配置`}>
            保存接入配置
          </Button>
        </div>
      </form>
    </section>
  );
}

/** 数据库里当前生效的一行。没有记录就说没有，不拿表单初值冒充。 */
function CurrentConfig({ current }: { current: ConnectorConfig | undefined }) {
  if (!current) {
    return (
      <PageState
        kind="empty"
        compact
        title="数据库里尚无这条接入配置"
        description="保存一次后，这里会显示生效值、版本与更新时间。"
      />
    );
  }
  return (
    <dl
      className="grid grid-cols-[6.5rem_minmax(0,1fr)] gap-x-3 gap-y-1 rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs"
      aria-label="数据库里当前生效的接入配置"
    >
      <dt className="text-fg-muted">当前模式</dt>
      <dd className="font-mono text-fg">{current.mode || "—"}</dd>
      <dt className="text-fg-muted">上游地址</dt>
      <dd className="font-mono break-all text-fg">{current.endpoint || "—"}</dd>
      <dt className="text-fg-muted">允许主机</dt>
      <dd className="font-mono break-all text-fg">
        {current.target_allowlist.length > 0 ? current.target_allowlist.join(", ") : "—"}
      </dd>
      <dt className="text-fg-muted">CredentialRef</dt>
      <dd className="font-mono break-all text-fg">{current.credential_ref || "—"}</dd>
      <dt className="text-fg-muted">版本</dt>
      <dd className="font-mono text-fg">{current.version > 0 ? `v${current.version}` : "—"}</dd>
      <dt className="text-fg-muted">更新时间</dt>
      <dd className="text-fg">
        {current.updated_at ? formatUtcTimestamp(current.updated_at) : "—"}
        {current.updated_by ? <span className="text-fg-muted"> · {current.updated_by}</span> : null}
      </dd>
    </dl>
  );
}
