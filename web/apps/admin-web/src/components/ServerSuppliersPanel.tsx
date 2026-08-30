import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { DataTableV2, PageState, formatUtcTimestamp, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge, Button, Dialog, FormField, Input } from "@xingmang/ui-primitives";
import { useEffect, useId, useRef, useState } from "react";
import {
  listServerAssets,
  listServerSuppliers,
  setServerSupplier,
  SERVER_ASSETS_QUERY,
  SERVER_MANAGE_PERMISSION,
  SERVER_SUPPLIERS_QUERY,
  type ServerSupplierItem,
} from "../api/server";
import { ActionErrorNote } from "./ActionErrorNote";
import { ActionResultNote, type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";

/** 供应商与采购（ADMIN-IA §2.1「供应商与采购」，`?tab=suppliers`）。
 *
 *  只存联系方式，不存密码：购买账号密码走 CredentialRef 才是安全的做法
 *  （宪法 7 条），但本片尚未把凭据字段接进这张表——供应商门户账号今天还是
 *  一句备注，见 XM-SERVER0 handoff 的 follow_ups。
 *
 *  写路径经 server.supplier.set@1（宪法 2 条）。 */
export function ServerSuppliersPanel() {
  const queryClient = useQueryClient();
  const [result, setResult] = useState<ActionResult | null>(null);

  const query = useQuery({
    queryKey: [SERVER_SUPPLIERS_QUERY],
    queryFn: ({ signal }) => listServerSuppliers({ signal }),
  });
  // 「关联服务器」这一格要数每个供应商挂了几台资产，读一次资产列表在内存里分组
  const assetsQuery = useQuery({
    queryKey: [SERVER_ASSETS_QUERY],
    queryFn: ({ signal }) => listServerAssets({ signal }),
  });

  const afterWrite = (written: ActionResult) => {
    setResult(written);
    void queryClient.invalidateQueries({ queryKey: [SERVER_SUPPLIERS_QUERY] });
  };

  const rows = query.data ?? [];
  const assetCountBySupplier = new Map<string, number>();
  for (const a of assetsQuery.data ?? []) {
    if (!a.supplier_id) continue;
    assetCountBySupplier.set(a.supplier_id, (assetCountBySupplier.get(a.supplier_id) ?? 0) + 1);
  }

  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <p className="max-w-3xl text-xs text-fg-muted">
          供应商与购买账号由平台自己登记（不是上游读来的）。购买账号密码只保存为密钥引用
          （CredentialRef），此处不回显；供应商门户的采购权限与服务器 Root 权限是两套东西，不得互相顶替。
        </p>
        <ServerSupplierDialog onDone={(runId) => afterWrite({ title: "供应商已登记", runId })} />
      </div>

      {result ? <ActionResultNote result={result} onDismiss={() => setResult(null)} /> : null}

      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
        <DataTableV2
          caption="服务器供应商登记簿：名称、官网、控制台地址、联系方式、关联服务器台数"
          columns={supplierColumns(assetCountBySupplier, afterWrite)}
          rows={rows}
          rowKey={(s) => s.id}
          searchable
          pageSize={20}
          emptyState={
            <PageState
              kind="empty"
              title="还没有登记任何供应商"
              description={`用右上角的「登记供应商」登记第一个。需要 ${SERVER_MANAGE_PERMISSION} 才能写入。`}
            />
          }
        />
      </ApiStateView>
    </section>
  );
}

function supplierColumns(
  assetCountBySupplier: Map<string, number>,
  onDone: (result: ActionResult) => void,
): DataTableColumn<ServerSupplierItem>[] {
  return [
    {
      id: "name",
      header: "供应商",
      primary: true,
      value: (s) => `${s.name} ${s.website}`,
      cell: (s) => (
        <>
          <span className="font-medium">{s.name}</span>
          {s.website ? (
            <p className="font-mono text-xs break-all text-fg-muted">{s.website}</p>
          ) : (
            <Badge tone="neutral" className="mt-0.5">
              官网未登记
            </Badge>
          )}
        </>
      ),
    },
    {
      id: "console",
      header: "控制台地址",
      value: (s) => s.console_url,
      cell: (s) =>
        s.console_url ? (
          <span className="font-mono text-xs break-all">{s.console_url}</span>
        ) : (
          <Badge tone="neutral">未登记</Badge>
        ),
    },
    {
      id: "contact",
      header: "联系人 / 联系方式",
      value: (s) => `${s.contact_name} ${s.contact_info}`,
      cell: (s) => (
        <span className="text-xs">
          {s.contact_name || <Badge tone="neutral">未登记</Badge>}
          <span className="block text-fg-muted">{s.contact_info || "—"}</span>
        </span>
      ),
    },
    {
      id: "assets",
      header: "关联服务器",
      numeric: true,
      value: (s) => assetCountBySupplier.get(s.id) ?? 0,
      cell: (s) => <span className="tabular-nums text-xs">{assetCountBySupplier.get(s.id) ?? 0} 台</span>,
    },
    {
      id: "notes",
      header: "备注",
      value: (s) => s.notes,
      cell: (s) => (s.notes ? <span className="text-xs">{s.notes}</span> : <span className="text-xs text-fg-muted">—</span>),
    },
    {
      id: "updated",
      header: "最近更新",
      value: (s) => s.updated_at,
      cell: (s) => <span className="text-xs tabular-nums text-fg-muted">{formatUtcTimestamp(s.updated_at)}</span>,
    },
    {
      id: "actions",
      header: "操作",
      cell: (s) => (
        <ServerSupplierDialog supplier={s} onDone={(runId) => onDone({ title: "供应商已更新", runId })} />
      ),
    },
  ];
}

interface SupplierFormValues {
  name: string;
  website: string;
  console_url: string;
  contact_name: string;
  contact_info: string;
  notes: string;
}

const EMPTY_SUPPLIER_FORM: SupplierFormValues = {
  name: "",
  website: "",
  console_url: "",
  contact_name: "",
  contact_info: "",
  notes: "",
};

function initialSupplierValues(supplier?: ServerSupplierItem): SupplierFormValues {
  if (!supplier) return { ...EMPTY_SUPPLIER_FORM };
  return {
    name: supplier.name,
    website: supplier.website,
    console_url: supplier.console_url,
    contact_name: supplier.contact_name,
    contact_info: supplier.contact_info,
    notes: supplier.notes,
  };
}

const HTTPS_PATTERN = /^https:\/\/[^@\s]+$/;

function validateSupplierForm(values: SupplierFormValues): Partial<Record<keyof SupplierFormValues, string>> {
  const errors: Partial<Record<keyof SupplierFormValues, string>> = {};
  if (values.name.trim() === "") errors.name = "供应商名称不能为空";
  if (values.website.trim() !== "" && !HTTPS_PATTERN.test(values.website.trim())) {
    errors.website = "必须是 https 且不含 user:pass@ 段";
  }
  if (values.console_url.trim() !== "" && !HTTPS_PATTERN.test(values.console_url.trim())) {
    errors.console_url = "必须是 https 且不含 user:pass@ 段";
  }
  return errors;
}

export function ServerSupplierDialog({
  supplier,
  onDone,
}: {
  supplier?: ServerSupplierItem;
  onDone: (runId: string) => void;
}) {
  const editing = Boolean(supplier);
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<SupplierFormValues>(() => initialSupplierValues(supplier));
  const [errors, setErrors] = useState<Partial<Record<keyof SupplierFormValues, string>>>({});
  const [attempts, setAttempts] = useState(0);
  const fieldPrefix = useId();

  const mutation = useMutation({
    mutationFn: (form: SupplierFormValues) =>
      setServerSupplier({ ...(supplier ? { supplier_id: supplier.id } : {}), ...form }),
    onSuccess: (run) => {
      setOpen(false);
      setErrors({});
      setAttempts(0);
      if (!editing) setValues(initialSupplierValues());
      onDone(run.runId);
    },
  });

  const set = (field: keyof SupplierFormValues) => (value: string) => {
    setValues((prev) => ({ ...prev, [field]: value }));
    setErrors((prev) => {
      if (!(field in prev)) return prev;
      const next = { ...prev };
      delete next[field];
      return next;
    });
  };

  const summaryRef = useRef<HTMLDivElement>(null);
  const failedFields = (Object.keys(errors) as (keyof SupplierFormValues)[]).filter((f) => errors[f]);
  const showSummary = attempts > 0 && (failedFields.length > 0 || Boolean(mutation.error));
  useEffect(() => {
    if (showSummary) summaryRef.current?.focus();
  }, [attempts, mutation.error, showSummary]);

  const submit = () => {
    setAttempts((n) => n + 1);
    const found = validateSupplierForm(values);
    setErrors(found);
    if (Object.values(found).some(Boolean)) return;
    mutation.mutate(values);
  };

  const text = (field: keyof SupplierFormValues, label: string, hint?: string, required = false) => {
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
          setValues(initialSupplierValues(supplier));
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
          <Button size="sm">登记供应商</Button>
        )
      }
      title={editing ? "修改供应商" : "登记供应商"}
      description="通过 server.supplier.set@1 整行更新。只存联系方式，不存密码。"
    >
      <form
        className="flex max-h-[60vh] flex-col gap-3 overflow-y-auto"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        {text("name", "供应商名称", undefined, true)}
        {text("website", "官网", "必须 https")}
        {text("console_url", "控制台地址", "必须 https")}
        {text("contact_name", "联系人")}
        {text("contact_info", "联系方式")}
        {text("notes", "备注")}

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
