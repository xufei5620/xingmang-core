import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { DataTableV2, PageState, formatUtcTimestamp, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge, Button, Dialog, FormField, Input, Select } from "@xingmang/ui-primitives";
import { useEffect, useId, useRef, useState } from "react";
import {
  listServerAssets,
  listServerServiceNotes,
  removeServerServiceNote,
  setServerServiceNote,
  SERVER_ASSETS_QUERY,
  SERVER_MANAGE_PERMISSION,
  SERVER_SERVICE_NOTES_QUERY,
  type ServerAssetItem,
  type ServerServiceNoteItem,
} from "../api/server";
import { SERVICE_KIND_OPTIONS } from "../lib/serverRegistryForm";
import { ActionErrorNote } from "./ActionErrorNote";
import { ActionResultNote, type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";

/** 服务与容器（ADMIN-IA §2.1「服务与容器」，`?tab=services`）。
 *
 *  **不扫描 Docker、不探测端口**：这只是运营手写的「这台服务器上有什么」。
 *  写路径经 server.service_note.set@1 / server.service_note.remove@1（宪法 2 条）。 */
export function ServerServiceNotesPanel() {
  const queryClient = useQueryClient();
  const [result, setResult] = useState<ActionResult | null>(null);

  const query = useQuery({
    queryKey: [SERVER_SERVICE_NOTES_QUERY],
    queryFn: ({ signal }) => listServerServiceNotes({ signal }),
  });
  const assetsQuery = useQuery({
    queryKey: [SERVER_ASSETS_QUERY],
    queryFn: ({ signal }) => listServerAssets({ signal }),
  });

  const assets = assetsQuery.data ?? [];
  const hostnameOf = (serverId: string) => assets.find((a) => a.id === serverId)?.hostname ?? "";

  const afterWrite = (written: ActionResult) => {
    setResult(written);
    void queryClient.invalidateQueries({ queryKey: [SERVER_SERVICE_NOTES_QUERY] });
  };

  const rows = query.data ?? [];

  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <p className="max-w-3xl text-xs text-fg-muted">
          从业务服务反查实际部署在哪台服务器上——手工登记，不提供重启、删除容器或任意命令执行。
        </p>
        {assets.length > 0 ? (
          <ServerServiceNoteDialog
            assets={assets}
            onDone={(runId) => afterWrite({ title: "服务已登记", runId })}
          />
        ) : (
          <Button size="sm" disabled title="先在「服务器资产」登记至少一台服务器">
            登记服务
          </Button>
        )}
      </div>

      {result ? <ActionResultNote result={result} onDismiss={() => setResult(null)} /> : null}

      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
        <DataTableV2
          caption="服务与容器登记簿：所在服务器、服务名、类型、端口与备注"
          columns={serviceNoteColumns(assets, hostnameOf, afterWrite)}
          rows={rows}
          rowKey={(n) => n.id}
          searchable
          pageSize={20}
          filters={[{ columnId: "kind", label: "类型", options: SERVICE_KIND_OPTIONS.map((o) => o.value) }]}
          emptyState={
            <PageState
              kind="empty"
              title="还没有登记任何服务"
              description={`先在「服务器资产」登记服务器，再用右上角的「登记服务」补充。需要 ${SERVER_MANAGE_PERMISSION} 才能写入。`}
            />
          }
        />
      </ApiStateView>
    </section>
  );
}

function serviceNoteColumns(
  assets: ServerAssetItem[],
  hostnameOf: (serverId: string) => string,
  onDone: (result: ActionResult) => void,
): DataTableColumn<ServerServiceNoteItem>[] {
  return [
    {
      id: "server",
      header: "所在服务器",
      value: (n) => hostnameOf(n.server_id),
      cell: (n) => <span className="text-xs font-medium">{hostnameOf(n.server_id) || "服务器已删除"}</span>,
    },
    {
      id: "service",
      header: "服务",
      primary: true,
      value: (n) => n.service_name,
      cell: (n) => <span className="font-medium">{n.service_name}</span>,
    },
    {
      id: "kind",
      header: "类型",
      value: (n) => n.service_kind,
      cell: (n) => <Badge tone="info">{kindLabel(n.service_kind)}</Badge>,
    },
    {
      id: "port",
      header: "端口",
      numeric: true,
      value: (n) => n.port,
      cell: (n) => <span className="tabular-nums text-xs">{n.port ?? "—"}</span>,
    },
    {
      id: "notes",
      header: "备注",
      value: (n) => n.notes,
      cell: (n) => (n.notes ? <span className="text-xs">{n.notes}</span> : <span className="text-xs text-fg-muted">—</span>),
    },
    {
      id: "updated",
      header: "最近更新",
      value: (n) => n.updated_at,
      cell: (n) => <span className="text-xs tabular-nums text-fg-muted">{formatUtcTimestamp(n.updated_at)}</span>,
    },
    {
      id: "actions",
      header: "操作",
      cell: (n) => (
        <div className="flex flex-wrap gap-1">
          <ServerServiceNoteDialog assets={assets} note={n} onDone={(runId) => onDone({ title: "服务已更新", runId })} />
          <ServerServiceNoteRemoveDialog note={n} onDone={(runId) => onDone({ title: "服务登记已删除", runId })} />
        </div>
      ),
    },
  ];
}

function kindLabel(kind: string): string {
  return SERVICE_KIND_OPTIONS.find((o) => o.value === kind)?.label ?? (kind || "—");
}

interface ServiceNoteFormValues {
  server_id: string;
  service_name: string;
  service_kind: string;
  port: string;
  notes: string;
}

function initialServiceNoteValues(assets: ServerAssetItem[], note?: ServerServiceNoteItem): ServiceNoteFormValues {
  if (!note) {
    return { server_id: assets[0]?.id ?? "", service_name: "", service_kind: "container", port: "", notes: "" };
  }
  return {
    server_id: note.server_id,
    service_name: note.service_name,
    service_kind: note.service_kind,
    port: note.port === null ? "" : String(note.port),
    notes: note.notes,
  };
}

function validateServiceNoteForm(values: ServiceNoteFormValues): Partial<Record<keyof ServiceNoteFormValues, string>> {
  const errors: Partial<Record<keyof ServiceNoteFormValues, string>> = {};
  if (values.server_id.trim() === "") errors.server_id = "必须选择所在服务器";
  if (values.service_name.trim() === "") errors.service_name = "服务名不能为空";
  if (values.port.trim() !== "") {
    const n = Number(values.port.trim());
    if (!/^\d+$/.test(values.port.trim()) || n <= 0 || n > 65535) errors.port = "端口必须在 1~65535 之间";
  }
  return errors;
}

export function ServerServiceNoteDialog({
  assets,
  note,
  onDone,
}: {
  assets: ServerAssetItem[];
  note?: ServerServiceNoteItem;
  onDone: (runId: string) => void;
}) {
  const editing = Boolean(note);
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<ServiceNoteFormValues>(() => initialServiceNoteValues(assets, note));
  const [errors, setErrors] = useState<Partial<Record<keyof ServiceNoteFormValues, string>>>({});
  const [attempts, setAttempts] = useState(0);
  const fieldPrefix = useId();

  const mutation = useMutation({
    mutationFn: (form: ServiceNoteFormValues) => {
      const params: Record<string, unknown> = {
        service_name: form.service_name.trim(),
        service_kind: form.service_kind,
        notes: form.notes.trim(),
      };
      if (note) params.service_note_id = note.id;
      else params.server_id = form.server_id;
      if (form.port.trim() !== "") params.port = Number(form.port.trim());
      return setServerServiceNote(params);
    },
    onSuccess: (run) => {
      setOpen(false);
      setErrors({});
      setAttempts(0);
      if (!editing) setValues(initialServiceNoteValues(assets));
      onDone(run.runId);
    },
  });

  const set = (field: keyof ServiceNoteFormValues) => (value: string) => {
    setValues((prev) => ({ ...prev, [field]: value }));
    setErrors((prev) => {
      if (!(field in prev)) return prev;
      const next = { ...prev };
      delete next[field];
      return next;
    });
  };

  const summaryRef = useRef<HTMLDivElement>(null);
  const failedFields = (Object.keys(errors) as (keyof ServiceNoteFormValues)[]).filter((f) => errors[f]);
  const showSummary = attempts > 0 && (failedFields.length > 0 || Boolean(mutation.error));
  useEffect(() => {
    if (showSummary) summaryRef.current?.focus();
  }, [attempts, mutation.error, showSummary]);

  const submit = () => {
    setAttempts((n) => n + 1);
    const found = validateServiceNoteForm(values);
    setErrors(found);
    if (Object.values(found).some(Boolean)) return;
    mutation.mutate(values);
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) {
          setValues(initialServiceNoteValues(assets, note));
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
          <Button size="sm">登记服务</Button>
        )
      }
      title={editing ? "修改服务登记" : "登记服务"}
      description={
        editing
          ? "通过 server.service_note.set@1 更新。不能更改所在服务器——要挪到另一台请删除后重新登记。"
          : "通过 server.service_note.set@1 登记。不扫描 Docker，只是手工记录。"
      }
    >
      <form
        className="flex max-h-[60vh] flex-col gap-3 overflow-y-auto"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <FormField label="所在服务器" required {...(errors.server_id ? { error: errors.server_id } : {})}>
          {editing ? (
            <Input value={assets.find((a) => a.id === values.server_id)?.hostname ?? values.server_id} readOnly aria-label="所在服务器（不可修改）" />
          ) : (
            <Select
              options={assets.map((a) => ({ value: a.id, label: a.hostname }))}
              value={values.server_id}
              onValueChange={set("server_id")}
              invalid={Boolean(errors.server_id)}
              aria-label="所在服务器"
            />
          )}
        </FormField>

        <FormField
          label="服务名"
          htmlFor={`${fieldPrefix}-service_name`}
          required
          {...(errors.service_name ? { error: errors.service_name } : {})}
        >
          <Input
            id={`${fieldPrefix}-service_name`}
            value={values.service_name}
            invalid={Boolean(errors.service_name)}
            onChange={(e) => set("service_name")(e.target.value)}
          />
        </FormField>

        <FormField label="类型" required>
          <Select options={SERVICE_KIND_OPTIONS} value={values.service_kind} onValueChange={set("service_kind")} aria-label="类型" />
        </FormField>

        <FormField label="端口" htmlFor={`${fieldPrefix}-port`} {...(errors.port ? { error: errors.port } : {})} hint="留空 = 没有单一端口">
          <Input
            id={`${fieldPrefix}-port`}
            value={values.port}
            invalid={Boolean(errors.port)}
            onChange={(e) => set("port")(e.target.value)}
            inputMode="numeric"
          />
        </FormField>

        <FormField label="备注" htmlFor={`${fieldPrefix}-notes`}>
          <Input id={`${fieldPrefix}-notes`} value={values.notes} onChange={(e) => set("notes")(e.target.value)} />
        </FormField>

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

function ServerServiceNoteRemoveDialog({ note, onDone }: { note: ServerServiceNoteItem; onDone: (runId: string) => void }) {
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const [attempted, setAttempted] = useState(false);
  const reasonId = useId();

  const mutation = useMutation({
    mutationFn: () => removeServerServiceNote({ service_note_id: note.id, reason }),
    onSuccess: (run) => {
      setOpen(false);
      setReason("");
      setAttempted(false);
      onDone(run.runId);
    },
  });

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) {
          setReason("");
          setAttempted(false);
          mutation.reset();
        }
      }}
      trigger={
        <Button size="sm" variant="secondary">
          删除
        </Button>
      }
      title={`删除服务登记 ${note.service_name}`}
      description="通过 server.service_note.remove@1 删除。删除后仍可用「登记服务」重新添加。"
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          setAttempted(true);
          if (reason.trim() === "") return;
          mutation.mutate();
        }}
      >
        <FormField label="删除原因" htmlFor={reasonId} required {...(attempted && reason.trim() === "" ? { error: "删除原因不能为空" } : {})} hint="进审计链，供事后复盘">
          <Input id={reasonId} value={reason} invalid={attempted && reason.trim() === ""} onChange={(e) => setReason(e.target.value)} />
        </FormField>
        <ActionErrorNote error={mutation.error} permission={SERVER_MANAGE_PERMISSION} />
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" size="sm" onClick={() => setOpen(false)}>
            取消
          </Button>
          <Button type="submit" size="sm" loading={mutation.isPending}>
            确认删除
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
