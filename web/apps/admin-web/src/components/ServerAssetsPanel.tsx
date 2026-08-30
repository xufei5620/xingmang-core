import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { DataTableV2, PageState, formatUtcTimestamp, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge, Button, Dialog, FormField, Input, Select } from "@xingmang/ui-primitives";
import { useEffect, useId, useRef, useState } from "react";
import {
  listServerAssets,
  listServerSuppliers,
  retireServerAsset,
  setServerAsset,
  SERVER_ASSETS_QUERY,
  SERVER_MANAGE_PERMISSION,
  SERVER_SUPPLIERS_QUERY,
  type ServerAssetItem,
  type ServerSupplierItem,
} from "../api/server";
import { formatMinorUnits } from "../lib/money";
import {
  ASSET_STATUS_OPTIONS,
  BILLING_CYCLE_OPTIONS,
  CURRENCY_OPTIONS,
  isExpiringSoon,
  minorUnitsToDecimal,
  parseDecimalToMinorUnits,
} from "../lib/serverRegistryForm";
import { ActionErrorNote } from "./ActionErrorNote";
import { ActionResultNote, type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";

/** 服务器资产（ADMIN-IA §2.1「服务器资产」，`?tab=assets`）。
 *
 *  拍板「服务器只做记录」：本页只读写 core.server_asset 登记簿，不装
 *  Server Agent、不做任何 SSH/docker 探测——所有实时字段（连接状态、资源
 *  水位）仍在 ServerDetailPage 的蓝图态里保持「未接入」，本页不冒充它们。
 *
 *  写路径全部经 server.asset.set@1 / server.asset.retire@1（宪法 2 条）。 */
export function ServerAssetsPanel() {
  const queryClient = useQueryClient();
  const [result, setResult] = useState<ActionResult | null>(null);

  const query = useQuery({
    queryKey: [SERVER_ASSETS_QUERY],
    queryFn: ({ signal }) => listServerAssets({ signal }),
  });
  const suppliersQuery = useQuery({
    queryKey: [SERVER_SUPPLIERS_QUERY],
    queryFn: ({ signal }) => listServerSuppliers({ signal }),
  });

  const suppliers = suppliersQuery.data ?? [];
  const supplierName = (id: string) => suppliers.find((s) => s.id === id)?.name ?? "";

  const afterWrite = (written: ActionResult) => {
    setResult(written);
    void queryClient.invalidateQueries({ queryKey: [SERVER_ASSETS_QUERY] });
  };

  const rows = query.data ?? [];
  const activeCount = rows.filter((a) => a.status === "active").length;
  const expiringCount = rows.filter((a) => isExpiringSoon(a.expires_at)).length;
  const costByCurrency = new Map<string, bigint>();
  for (const a of rows) {
    if (!a.monthly_cost_minor_units || !a.currency) continue;
    if (!/^-?\d+$/.test(a.monthly_cost_minor_units)) continue;
    const prev = costByCurrency.get(a.currency) ?? 0n;
    costByCurrency.set(a.currency, prev + BigInt(a.monthly_cost_minor_units));
  }

  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <p className="max-w-3xl text-xs text-fg-muted">
          一行代表一台实际服务器，由运营手工登记（不是 Agent 上报）。规格、月付成本与到期日均为登记值；
          连接状态与资源水位等实时字段仍在服务器详情页显示「未接入」。
        </p>
        <ServerAssetDialog
          suppliers={suppliers}
          onDone={(runId) => afterWrite({ title: "服务器资产已登记", runId })}
        />
      </div>

      {result ? <ActionResultNote result={result} onDismiss={() => setResult(null)} /> : null}

      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
        <div className="flex flex-col gap-3">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
            <StatTileLite label="服务器资产" value={String(rows.length)} note="已登记台数" />
            <StatTileLite label="使用中" value={String(activeCount)} note="状态为 active 的台数" />
            <StatTileLite
              label="30 天内到期"
              value={String(expiringCount)}
              note="按到期日排期，含已过期"
              warn={expiringCount > 0}
            />
            <StatTileLite
              label="月成本合计"
              value={
                costByCurrency.size === 0
                  ? "—"
                  : [...costByCurrency.entries()].map(([c, v]) => formatMinorUnits(v, c)).join(" + ")
              }
              note={costByCurrency.size === 0 ? "尚无已登记成本的资产" : "按币种分别合计，不做汇率折算"}
            />
          </div>

          <DataTableV2
            caption="服务器资产登记簿：主机名、IP、机房/供应商、规格、状态、月付成本与到期日"
            columns={assetColumns(supplierName, afterWrite, suppliers)}
            rows={rows}
            rowKey={(a) => a.id}
            searchable
            pageSize={20}
            filters={[
              { columnId: "status", label: "状态", options: ASSET_STATUS_OPTIONS.map((o) => o.value) },
            ]}
            emptyState={
              <PageState
                kind="empty"
                title="还没有登记任何服务器资产"
                description={`用右上角的「登记服务器资产」登记第一台。需要 ${SERVER_MANAGE_PERMISSION} 才能写入。`}
              />
            }
          />
        </div>
      </ApiStateView>
    </section>
  );
}

function StatTileLite({
  label,
  value,
  note,
  warn = false,
}: {
  label: string;
  value: string;
  note: string;
  warn?: boolean;
}) {
  return (
    <div className="rounded-lg border border-edge bg-surface p-3 shadow-sm">
      <p className="text-xs text-fg-muted">{label}</p>
      <p className={`mt-1 text-xl font-semibold tabular-nums ${warn ? "text-warning" : "text-fg"}`}>{value}</p>
      <p className="mt-1 text-xs text-fg-muted">{note}</p>
    </div>
  );
}

function assetColumns(
  supplierName: (id: string) => string,
  onDone: (result: ActionResult) => void,
  suppliers: ServerSupplierItem[],
): DataTableColumn<ServerAssetItem>[] {
  return [
    {
      id: "hostname",
      header: "主机名 / IP",
      primary: true,
      value: (a) => `${a.hostname} ${a.ip_addresses.join(" ")}`,
      cell: (a) => (
        <>
          <span className="font-medium">{a.hostname}</span>
          <p className="font-mono text-xs break-all text-fg-muted">
            {a.ip_addresses.length > 0 ? a.ip_addresses.join(", ") : "未登记 IP"}
          </p>
        </>
      ),
    },
    {
      id: "location",
      header: "机房 / 供应商",
      value: (a) => `${a.datacenter} ${supplierName(a.supplier_id)}`,
      cell: (a) => (
        <span className="text-xs">
          {a.datacenter || <Badge tone="neutral">机房未登记</Badge>}
          <span className="block text-fg-muted">
            {a.supplier_id ? supplierName(a.supplier_id) || "供应商已删除" : "供应商未登记"}
          </span>
        </span>
      ),
    },
    {
      id: "spec",
      header: "规格",
      value: (a) => `${a.vcpu ?? ""} ${a.memory_gb ?? ""} ${a.disk_gb ?? ""}`,
      cell: (a) =>
        a.vcpu === null && a.memory_gb === null && a.disk_gb === null ? (
          <Badge tone="neutral">未登记</Badge>
        ) : (
          <span className="text-xs tabular-nums">
            {a.vcpu ?? "—"} vCPU / {a.memory_gb ?? "—"} GB 内存 / {a.disk_gb ?? "—"} GB 磁盘
          </span>
        ),
    },
    {
      id: "purpose",
      header: "用途",
      value: (a) => a.purpose,
      cell: (a) => (a.purpose ? <span className="text-xs">{a.purpose}</span> : <Badge tone="neutral">未登记</Badge>),
    },
    {
      id: "status",
      header: "状态",
      value: (a) => a.status,
      cell: (a) => <Badge tone={statusTone(a.status)}>{statusLabel(a.status)}</Badge>,
    },
    {
      id: "cost",
      header: "月付成本 / 周期",
      numeric: true,
      value: (a) => (a.monthly_cost_minor_units && /^-?\d+$/.test(a.monthly_cost_minor_units) ? BigInt(a.monthly_cost_minor_units) : null),
      cell: (a) => (
        <span className="text-xs tabular-nums">
          {a.monthly_cost_minor_units ? formatMinorUnits(a.monthly_cost_minor_units, a.currency) : "—"}
          <span className="block text-fg-muted">{cycleLabel(a.billing_cycle)}</span>
        </span>
      ),
    },
    {
      id: "expires",
      header: "到期日",
      value: (a) => a.expires_at,
      cell: (a) =>
        a.expires_at ? (
          <span className={`text-xs tabular-nums ${isExpiringSoon(a.expires_at) ? "font-medium text-warning" : ""}`}>
            {a.expires_at}
            {isExpiringSoon(a.expires_at) ? <Badge tone="warning" className="ml-1">30 天内</Badge> : null}
          </span>
        ) : (
          <Badge tone="neutral">未登记</Badge>
        ),
    },
    {
      id: "notes",
      header: "备注",
      value: (a) => a.notes,
      cell: (a) => (a.notes ? <span className="text-xs">{a.notes}</span> : <span className="text-xs text-fg-muted">—</span>),
    },
    {
      id: "updated",
      header: "最近更新",
      value: (a) => a.updated_at,
      cell: (a) => <span className="text-xs tabular-nums text-fg-muted">{formatUtcTimestamp(a.updated_at)}</span>,
    },
    {
      id: "actions",
      header: "操作",
      cell: (a) => (
        <div className="flex flex-wrap gap-1">
          <ServerAssetDialog
            suppliers={suppliers}
            asset={a}
            onDone={(runId) => onDone({ title: "服务器资产已更新", runId })}
          />
          {a.status !== "retired" ? (
            <ServerAssetRetireDialog asset={a} onDone={(runId) => onDone({ title: "服务器资产已退役", runId })} />
          ) : null}
        </div>
      ),
    },
  ];
}

function statusLabel(status: string): string {
  return ASSET_STATUS_OPTIONS.find((o) => o.value === status)?.label ?? (status || "—");
}

function statusTone(status: string): "success" | "neutral" | "warning" {
  if (status === "active") return "success";
  if (status === "retired") return "neutral";
  return "warning";
}

function cycleLabel(cycle: string): string {
  return BILLING_CYCLE_OPTIONS.find((o) => o.value === cycle)?.label ?? (cycle || "周期未登记");
}

// ---------------------------------------------------------------------------
// 登记 / 修改对话框
// ---------------------------------------------------------------------------

interface AssetFormValues {
  hostname: string;
  ip_addresses: string;
  datacenter: string;
  supplier_id: string;
  vcpu: string;
  memory_gb: string;
  disk_gb: string;
  purpose: string;
  status: string;
  monthly_cost: string;
  currency: string;
  billing_cycle: string;
  expires_at: string;
  notes: string;
}

const EMPTY_ASSET_FORM: AssetFormValues = {
  hostname: "",
  ip_addresses: "",
  datacenter: "",
  supplier_id: "",
  vcpu: "",
  memory_gb: "",
  disk_gb: "",
  purpose: "",
  status: "active",
  monthly_cost: "",
  currency: "",
  billing_cycle: "",
  expires_at: "",
  notes: "",
};

function initialAssetValues(asset?: ServerAssetItem): AssetFormValues {
  if (!asset) return { ...EMPTY_ASSET_FORM };
  return {
    hostname: asset.hostname,
    ip_addresses: asset.ip_addresses.join(", "),
    datacenter: asset.datacenter,
    supplier_id: asset.supplier_id,
    vcpu: asset.vcpu === null ? "" : String(asset.vcpu),
    memory_gb: asset.memory_gb === null ? "" : String(asset.memory_gb),
    disk_gb: asset.disk_gb === null ? "" : String(asset.disk_gb),
    purpose: asset.purpose,
    status: asset.status || "active",
    monthly_cost:
      asset.monthly_cost_minor_units && asset.currency
        ? minorUnitsToDecimal(asset.monthly_cost_minor_units, asset.currency)
        : "",
    currency: asset.currency,
    billing_cycle: asset.billing_cycle,
    expires_at: asset.expires_at,
    notes: asset.notes,
  };
}

function validateAssetForm(values: AssetFormValues): Partial<Record<keyof AssetFormValues, string>> {
  const errors: Partial<Record<keyof AssetFormValues, string>> = {};
  if (values.hostname.trim() === "") errors.hostname = "主机名不能为空";
  for (const [field, label] of [
    ["vcpu", "vCPU"],
    ["memory_gb", "内存"],
    ["disk_gb", "磁盘"],
  ] as const) {
    const raw = values[field].trim();
    if (raw !== "" && (!/^\d+$/.test(raw) || Number(raw) <= 0)) {
      errors[field] = `${label} 必须是正整数`;
    }
  }
  if (values.monthly_cost.trim() !== "") {
    if (values.currency === "") {
      errors.currency = "填了月付成本就必须选择币种";
    } else {
      const parsed = parseDecimalToMinorUnits(values.monthly_cost, values.currency);
      if (!parsed.ok) errors.monthly_cost = parsed.error;
    }
  } else if (values.currency !== "") {
    errors.monthly_cost = "选了币种就必须填月付成本（或把币种清空）";
  }
  if (values.expires_at.trim() !== "" && !/^\d{4}-\d{2}-\d{2}$/.test(values.expires_at.trim())) {
    errors.expires_at = "必须形如 2026-09-30";
  }
  return errors;
}

function buildAssetParams(assetId: string | undefined, values: AssetFormValues): Record<string, unknown> {
  const params: Record<string, unknown> = {
    hostname: values.hostname.trim(),
    ip_addresses: values.ip_addresses
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean),
    datacenter: values.datacenter.trim(),
    purpose: values.purpose.trim(),
    status: values.status,
    billing_cycle: values.billing_cycle,
    expires_at: values.expires_at.trim(),
    notes: values.notes.trim(),
  };
  if (assetId) params.asset_id = assetId;
  if (values.supplier_id) params.supplier_id = values.supplier_id;
  for (const [field, key] of [
    ["vcpu", "vcpu"],
    ["memory_gb", "memory_gb"],
    ["disk_gb", "disk_gb"],
  ] as const) {
    const raw = values[field].trim();
    if (raw !== "") params[key] = Number(raw);
  }
  const money = parseDecimalToMinorUnits(values.monthly_cost, values.currency);
  if (money.ok && money.minor !== undefined) {
    params.monthly_cost_minor_units = money.minor;
    params.currency = values.currency;
  }
  return params;
}

export function ServerAssetDialog({
  suppliers,
  asset,
  onDone,
}: {
  suppliers: ServerSupplierItem[];
  asset?: ServerAssetItem;
  onDone: (runId: string) => void;
}) {
  const editing = Boolean(asset);
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<AssetFormValues>(() => initialAssetValues(asset));
  const [errors, setErrors] = useState<Partial<Record<keyof AssetFormValues, string>>>({});
  const [attempts, setAttempts] = useState(0);
  const fieldPrefix = useId();

  const mutation = useMutation({
    mutationFn: (form: AssetFormValues) => setServerAsset(buildAssetParams(asset?.id, form)),
    onSuccess: (run) => {
      setOpen(false);
      setErrors({});
      setAttempts(0);
      if (!editing) setValues(initialAssetValues());
      onDone(run.runId);
    },
  });

  const set = (field: keyof AssetFormValues) => (value: string) => {
    setValues((prev) => ({ ...prev, [field]: value }));
    setErrors((prev) => {
      if (!(field in prev)) return prev;
      const next = { ...prev };
      delete next[field];
      return next;
    });
  };

  const summaryRef = useRef<HTMLDivElement>(null);
  const failedFields = (Object.keys(errors) as (keyof AssetFormValues)[]).filter((f) => errors[f]);
  const showSummary = attempts > 0 && (failedFields.length > 0 || Boolean(mutation.error));
  useEffect(() => {
    if (showSummary) summaryRef.current?.focus();
  }, [attempts, mutation.error, showSummary]);

  const submit = () => {
    setAttempts((n) => n + 1);
    const found = validateAssetForm(values);
    setErrors(found);
    if (Object.values(found).some(Boolean)) return;
    mutation.mutate(values);
  };

  const text = (field: keyof AssetFormValues, label: string, hint?: string, required = false) => {
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
          setValues(initialAssetValues(asset));
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
          <Button size="sm">登记服务器资产</Button>
        )
      }
      title={editing ? "修改服务器资产" : "登记服务器资产"}
      description={
        editing
          ? "通过 server.asset.set@1 整行更新。表单里看到什么就写进去什么——留空的字段会被清空。"
          : "通过 server.asset.set@1 登记一台服务器。只做记录，不会触发任何探测。"
      }
    >
      <form
        className="flex max-h-[65vh] flex-col gap-3 overflow-y-auto"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        {text("hostname", "主机名", "唯一标识（同环境不能重复登记）", true)}
        {text("ip_addresses", "IP 地址", "多个用逗号分隔，如 203.0.113.9, 10.0.0.5")}
        {text("datacenter", "机房 / 区域")}

        <FormField label="供应商" hint="留空 = 未登记供应商">
          <Select
            options={[{ value: "", label: "未登记" }, ...suppliers.map((s) => ({ value: s.id, label: s.name }))]}
            value={values.supplier_id}
            onValueChange={set("supplier_id")}
            aria-label="供应商"
          />
        </FormField>

        <div className="grid grid-cols-3 gap-2">
          {text("vcpu", "vCPU")}
          {text("memory_gb", "内存 GB")}
          {text("disk_gb", "磁盘 GB")}
        </div>

        {text("purpose", "用途")}

        <FormField label="状态">
          <Select options={ASSET_STATUS_OPTIONS} value={values.status} onValueChange={set("status")} aria-label="状态" />
        </FormField>

        <div className="grid grid-cols-2 gap-2">
          <FormField
            label="月付成本"
            htmlFor={`${fieldPrefix}-monthly_cost`}
            {...(errors.monthly_cost ? { error: errors.monthly_cost } : {})}
            hint="十进制，如 99.90；留空 = 未登记"
          >
            <Input
              id={`${fieldPrefix}-monthly_cost`}
              value={values.monthly_cost}
              invalid={Boolean(errors.monthly_cost)}
              onChange={(e) => set("monthly_cost")(e.target.value)}
              inputMode="decimal"
            />
          </FormField>
          <FormField label="币种" {...(errors.currency ? { error: errors.currency } : {})}>
            <Select
              options={CURRENCY_OPTIONS}
              value={values.currency}
              onValueChange={set("currency")}
              invalid={Boolean(errors.currency)}
              aria-label="币种"
            />
          </FormField>
        </div>

        <FormField label="计费周期">
          <Select options={BILLING_CYCLE_OPTIONS} value={values.billing_cycle} onValueChange={set("billing_cycle")} aria-label="计费周期" />
        </FormField>

        {text("expires_at", "到期日", "形如 2026-09-30；留空 = 未登记")}
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

function ServerAssetRetireDialog({ asset, onDone }: { asset: ServerAssetItem; onDone: (runId: string) => void }) {
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const [attempted, setAttempted] = useState(false);
  const reasonId = useId();

  const mutation = useMutation({
    mutationFn: () => retireServerAsset({ asset_id: asset.id, reason }),
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
          退役
        </Button>
      }
      title={`退役 ${asset.hostname}`}
      description="通过 server.asset.retire@1 只改状态，不动其它字段。退役后仍保留在登记簿里，可以随时改回。"
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
        <FormField
          label="退役原因"
          htmlFor={reasonId}
          required
          {...(attempted && reason.trim() === "" ? { error: "退役原因不能为空" } : {})}
          hint="进审计链，供事后复盘"
        >
          <Input
            id={reasonId}
            value={reason}
            invalid={attempted && reason.trim() === ""}
            onChange={(e) => setReason(e.target.value)}
          />
        </FormField>
        <ActionErrorNote error={mutation.error} permission={SERVER_MANAGE_PERMISSION} />
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" size="sm" onClick={() => setOpen(false)}>
            取消
          </Button>
          <Button type="submit" size="sm" loading={mutation.isPending}>
            确认退役
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
