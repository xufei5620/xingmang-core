import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { DataTableV2, PageState, formatUtcTimestamp, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge, Button, Dialog, FormField, Input, Select } from "@xingmang/ui-primitives";
import { useEffect, useId, useRef, useState } from "react";
import {
  listServerDomains,
  setServerDomain,
  SERVER_DOMAINS_QUERY,
  SERVER_MANAGE_PERMISSION,
  type ServerDomainItem,
} from "../api/server";
import { CERT_SOURCE_OPTIONS, isExpiringSoon } from "../lib/serverRegistryForm";
import { ActionErrorNote } from "./ActionErrorNote";
import { ActionResultNote, type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";

/** 域名与证书（ADMIN-IA §2.1「域名与证书」，`?tab=domains`）。
 *
 *  只做记录：证书健康度不探测，来源与到期日全部手工登记。写路径经
 *  server.domain.set@1（宪法 2 条）。 */
export function ServerDomainsPanel() {
  const queryClient = useQueryClient();
  const [result, setResult] = useState<ActionResult | null>(null);

  const query = useQuery({
    queryKey: [SERVER_DOMAINS_QUERY],
    queryFn: ({ signal }) => listServerDomains({ signal }),
  });

  const afterWrite = (written: ActionResult) => {
    setResult(written);
    void queryClient.invalidateQueries({ queryKey: [SERVER_DOMAINS_QUERY] });
  };

  const rows = query.data ?? [];
  const domainExpiring = rows.filter((d) => isExpiringSoon(d.expires_at)).length;
  const certExpiring = rows.filter((d) => isExpiringSoon(d.cert_expires_at)).length;

  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <p className="max-w-3xl text-xs text-fg-muted">
          域名、注册商、DNS 托管与证书生命周期由运营手工登记；证书来源与到期日不做任何探测，
          全部依赖登记值的准确性。
        </p>
        <ServerDomainDialog onDone={(runId) => afterWrite({ title: "域名已登记", runId })} />
      </div>

      {result ? <ActionResultNote result={result} onDismiss={() => setResult(null)} /> : null}

      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
        <div className="flex flex-col gap-3">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
            <StatTileLite label="域名" value={String(rows.length)} note="已登记的域名数" />
            <StatTileLite
              label="域名 30 天内到期"
              value={String(domainExpiring)}
              note="按域名到期日排期，含已过期"
              warn={domainExpiring > 0}
            />
            <StatTileLite
              label="证书 30 天内到期"
              value={String(certExpiring)}
              note="按证书到期日排期，含已过期"
              warn={certExpiring > 0}
            />
          </div>

          <DataTableV2
            caption="域名与证书登记簿：域名、注册商、DNS 托管、到期日、证书来源与证书到期日"
            columns={domainColumns(afterWrite)}
            rows={rows}
            rowKey={(d) => d.id}
            searchable
            pageSize={20}
            emptyState={
              <PageState
                kind="empty"
                title="还没有登记任何域名"
                description={`用右上角的「登记域名」登记第一个。需要 ${SERVER_MANAGE_PERMISSION} 才能写入。`}
              />
            }
          />
        </div>
      </ApiStateView>
    </section>
  );
}

function StatTileLite({ label, value, note, warn = false }: { label: string; value: string; note: string; warn?: boolean }) {
  return (
    <div className="rounded-lg border border-edge bg-surface p-3 shadow-sm">
      <p className="text-xs text-fg-muted">{label}</p>
      <p className={`mt-1 text-xl font-semibold tabular-nums ${warn ? "text-warning" : "text-fg"}`}>{value}</p>
      <p className="mt-1 text-xs text-fg-muted">{note}</p>
    </div>
  );
}

function domainColumns(onDone: (result: ActionResult) => void): DataTableColumn<ServerDomainItem>[] {
  return [
    {
      id: "domain",
      header: "域名",
      primary: true,
      value: (d) => `${d.domain_name} ${d.registrar}`,
      cell: (d) => (
        <>
          <span className="font-medium">{d.domain_name}</span>
          <p className="text-xs text-fg-muted">{d.registrar || "注册商未登记"}</p>
        </>
      ),
    },
    {
      id: "dns",
      header: "DNS 托管",
      value: (d) => d.dns_provider,
      cell: (d) => (d.dns_provider ? <span className="text-xs">{d.dns_provider}</span> : <Badge tone="neutral">未登记</Badge>),
    },
    {
      id: "expires",
      header: "到期日",
      value: (d) => d.expires_at,
      cell: (d) =>
        d.expires_at ? (
          <span className={`text-xs tabular-nums ${isExpiringSoon(d.expires_at) ? "font-medium text-warning" : ""}`}>
            {d.expires_at}
            {isExpiringSoon(d.expires_at) ? <Badge tone="warning" className="ml-1">30 天内</Badge> : null}
          </span>
        ) : (
          <Badge tone="neutral">未登记</Badge>
        ),
    },
    {
      id: "cert",
      header: "证书来源 / 到期",
      value: (d) => `${d.cert_source} ${d.cert_expires_at}`,
      cell: (d) => (
        <span className="text-xs">
          {certSourceLabel(d.cert_source)}
          <span className={`block tabular-nums ${isExpiringSoon(d.cert_expires_at) ? "font-medium text-warning" : "text-fg-muted"}`}>
            {d.cert_expires_at || "证书到期日未登记"}
            {isExpiringSoon(d.cert_expires_at) ? <Badge tone="warning" className="ml-1">30 天内</Badge> : null}
          </span>
        </span>
      ),
    },
    {
      id: "bound",
      header: "绑定服务",
      value: (d) => d.bound_service_note,
      cell: (d) =>
        d.bound_service_note ? <span className="text-xs">{d.bound_service_note}</span> : <span className="text-xs text-fg-muted">—</span>,
    },
    {
      id: "updated",
      header: "最近更新",
      value: (d) => d.updated_at,
      cell: (d) => <span className="text-xs tabular-nums text-fg-muted">{formatUtcTimestamp(d.updated_at)}</span>,
    },
    {
      id: "actions",
      header: "操作",
      cell: (d) => <ServerDomainDialog domain={d} onDone={(runId) => onDone({ title: "域名已更新", runId })} />,
    },
  ];
}

function certSourceLabel(source: string): string {
  return CERT_SOURCE_OPTIONS.find((o) => o.value === source)?.label ?? (source || "证书来源未登记");
}

interface DomainFormValues {
  domain_name: string;
  registrar: string;
  dns_provider: string;
  expires_at: string;
  cert_source: string;
  cert_expires_at: string;
  bound_service_note: string;
}

const EMPTY_DOMAIN_FORM: DomainFormValues = {
  domain_name: "",
  registrar: "",
  dns_provider: "",
  expires_at: "",
  cert_source: "",
  cert_expires_at: "",
  bound_service_note: "",
};

function initialDomainValues(domain?: ServerDomainItem): DomainFormValues {
  if (!domain) return { ...EMPTY_DOMAIN_FORM };
  return {
    domain_name: domain.domain_name,
    registrar: domain.registrar,
    dns_provider: domain.dns_provider,
    expires_at: domain.expires_at,
    cert_source: domain.cert_source,
    cert_expires_at: domain.cert_expires_at,
    bound_service_note: domain.bound_service_note,
  };
}

const DATE_PATTERN = /^\d{4}-\d{2}-\d{2}$/;

function validateDomainForm(values: DomainFormValues): Partial<Record<keyof DomainFormValues, string>> {
  const errors: Partial<Record<keyof DomainFormValues, string>> = {};
  if (values.domain_name.trim() === "") errors.domain_name = "域名不能为空";
  if (values.expires_at.trim() !== "" && !DATE_PATTERN.test(values.expires_at.trim())) {
    errors.expires_at = "必须形如 2026-09-30";
  }
  if (values.cert_expires_at.trim() !== "" && !DATE_PATTERN.test(values.cert_expires_at.trim())) {
    errors.cert_expires_at = "必须形如 2026-09-30";
  }
  return errors;
}

export function ServerDomainDialog({ domain, onDone }: { domain?: ServerDomainItem; onDone: (runId: string) => void }) {
  const editing = Boolean(domain);
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<DomainFormValues>(() => initialDomainValues(domain));
  const [errors, setErrors] = useState<Partial<Record<keyof DomainFormValues, string>>>({});
  const [attempts, setAttempts] = useState(0);
  const fieldPrefix = useId();

  const mutation = useMutation({
    mutationFn: (form: DomainFormValues) =>
      setServerDomain({ ...(domain ? { domain_id: domain.id } : {}), ...form }),
    onSuccess: (run) => {
      setOpen(false);
      setErrors({});
      setAttempts(0);
      if (!editing) setValues(initialDomainValues());
      onDone(run.runId);
    },
  });

  const set = (field: keyof DomainFormValues) => (value: string) => {
    setValues((prev) => ({ ...prev, [field]: value }));
    setErrors((prev) => {
      if (!(field in prev)) return prev;
      const next = { ...prev };
      delete next[field];
      return next;
    });
  };

  const summaryRef = useRef<HTMLDivElement>(null);
  const failedFields = (Object.keys(errors) as (keyof DomainFormValues)[]).filter((f) => errors[f]);
  const showSummary = attempts > 0 && (failedFields.length > 0 || Boolean(mutation.error));
  useEffect(() => {
    if (showSummary) summaryRef.current?.focus();
  }, [attempts, mutation.error, showSummary]);

  const submit = () => {
    setAttempts((n) => n + 1);
    const found = validateDomainForm(values);
    setErrors(found);
    if (Object.values(found).some(Boolean)) return;
    mutation.mutate(values);
  };

  const text = (field: keyof DomainFormValues, label: string, hint?: string, required = false) => {
    const id = `${fieldPrefix}-${field}`;
    return (
      <FormField label={label} htmlFor={id} required={required} {...(errors[field] ? { error: errors[field] } : {})} {...(hint ? { hint } : {})}>
        <Input id={id} value={values[field]} invalid={Boolean(errors[field])} onChange={(e) => set(field)(e.target.value)} />
      </FormField>
    );
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) {
          setValues(initialDomainValues(domain));
        } else {
          setErrors({});
          setAttempts(0);
          mutation.reset();
        }
      }}
      trigger={
        editing ? (
          <Button size="sm" variant="secondary">
            修改
          </Button>
        ) : (
          <Button size="sm">登记域名</Button>
        )
      }
      title={editing ? "修改域名" : "登记域名"}
      description="通过 server.domain.set@1 整行更新。证书健康度不探测，全部手工登记。"
    >
      <form
        className="flex max-h-[60vh] flex-col gap-3 overflow-y-auto"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        {text("domain_name", "域名", undefined, true)}
        {text("registrar", "注册商")}
        {text("dns_provider", "DNS 托管")}
        {text("expires_at", "到期日", "形如 2026-09-30")}

        <FormField label="证书来源">
          <Select options={CERT_SOURCE_OPTIONS} value={values.cert_source} onValueChange={set("cert_source")} aria-label="证书来源" />
        </FormField>
        {text("cert_expires_at", "证书到期日", "形如 2026-09-30")}
        {text("bound_service_note", "绑定服务备注", "哪个服务在用这个域名")}

        <div ref={summaryRef} tabIndex={-1} className="flex flex-col gap-1 outline-none focus-visible:ring-2 focus-visible:ring-accent">
          {showSummary && failedFields.length > 0 ? (
            <p role="alert" className="text-xs text-danger">
              还有 {failedFields.length} 处需要修正
            </p>
          ) : null}
          <ActionErrorNote error={mutation.error} permission={SERVER_MANAGE_PERMISSION} />
        </div>

        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" size="sm" onClick={() => setOpen(false)}>
            取消
          </Button>
          <Button type="submit" size="sm" loading={mutation.isPending}>
            {editing ? "保存" : "登记"}
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
