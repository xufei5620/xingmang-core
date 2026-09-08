import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  DataTableV2,
  PageState,
  formatUtcTimestamp,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge, Button, Dialog, FormField, Input, Select } from "@xingmang/ui-primitives";
import { useEffect, useId, useRef, useState } from "react";
import {
  APP_STATUS_OPTIONS,
  AUTH_MODE_OPTIONS,
  EXT_APPS_QUERY,
  EXT_APP_MANAGE_PERMISSION,
  EXT_APP_RELEASES_QUERY,
  appStatusLabel,
  authModeLabel,
  listExtApps,
  retireExtApp,
  setExtApp,
  type ExtAppItem,
} from "../api/extapp";
import { ActionErrorNote } from "./ActionErrorNote";
import { ActionResultNote, type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";

/** 应用目录（ADMIN-IA §5.4「应用与配置 → 应用目录」，`?sub=catalog`）。
 *
 *  一行 = 一个我们自己部署的前端站点。**纯登记**：不请求站点、不解析域名、
 *  不查证书——「状态」是运营写下的值，不是探活结果。写路径经
 *  extapp.app.set@1 / extapp.app.retire@1（宪法 2 条）。 */
export function ExtAppCatalogPanel() {
  const queryClient = useQueryClient();
  const [result, setResult] = useState<ActionResult | null>(null);

  const query = useQuery({
    queryKey: [EXT_APPS_QUERY],
    queryFn: ({ signal }) => listExtApps({ signal }),
  });

  const afterWrite = (written: ActionResult) => {
    setResult(written);
    void queryClient.invalidateQueries({ queryKey: [EXT_APPS_QUERY] });
    // 发布记录那一格与这张表读的是同一批应用；下线一个站点之后，
    // 那边的应用名也该跟着变。
    void queryClient.invalidateQueries({ queryKey: [EXT_APP_RELEASES_QUERY] });
  };

  const rows = query.data ?? [];
  const active = rows.filter((a) => a.status === "active").length;
  const withoutRelease = rows.filter((a) => a.current_release === null).length;

  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <p className="max-w-3xl text-xs text-fg-muted">
          这里登记的是「平台自己部署的前端站点」（admin-web、console 这类），不是
          Sub2API / NewAPI 的前端，也不是页面搭建器里的「应用」。域名填主机名即可
          （形如 admin.example.com），不要带 https:// 与路径。
        </p>
        <ExtAppDialog onDone={(runId) => afterWrite({ title: "应用已登记", runId })} />
      </div>

      {result ? <ActionResultNote result={result} onDismiss={() => setResult(null)} /> : null}

      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        <div className="flex flex-col gap-3">
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
            <StatTileLite label="应用" value={String(rows.length)} note="已登记的前端站点数" />
            <StatTileLite label="在线" value={String(active)} note="登记状态为「在线」的站点数" />
            <StatTileLite
              label="没有发布记录"
              value={String(withoutRelease)}
              note="一次发布都没登记过的站点数"
              warn={withoutRelease > 0}
            />
          </div>

          <DataTableV2
            caption="应用目录：应用、域名、环境、登录方式、配置版本、发布版本、负责人、状态"
            columns={catalogColumns(afterWrite)}
            rows={rows}
            rowKey={(a) => a.id}
            searchable
            pageSize={20}
            renderExpanded={(a) => <ExtAppDetail app={a} />}
            emptyState={
              <PageState
                kind="empty"
                title="还没有登记任何前端站点"
                description={`用右上角的「登记应用」登记第一个。需要 ${EXT_APP_MANAGE_PERMISSION} 才能写入。`}
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
      <p className={`mt-1 text-xl font-semibold tabular-nums ${warn ? "text-warning" : "text-fg"}`}>
        {value}
      </p>
      <p className="mt-1 text-xs text-fg-muted">{note}</p>
    </div>
  );
}

const STATUS_TONE: Readonly<Record<string, "success" | "neutral" | "warning">> = {
  active: "success",
  planned: "warning",
  retired: "neutral",
};

function catalogColumns(onDone: (result: ActionResult) => void): DataTableColumn<ExtAppItem>[] {
  return [
    {
      id: "app",
      header: "应用",
      primary: true,
      value: (a) => `${a.display_name} ${a.app_key}`,
      cell: (a) => (
        <>
          <span className="font-medium">{a.display_name}</span>
          <p className="font-mono text-xs text-fg-muted">{a.app_key}</p>
        </>
      ),
    },
    {
      id: "domain",
      header: "域名",
      value: (a) => a.primary_domain,
      cell: (a) =>
        a.primary_domain ? (
          <span className="font-mono text-xs [overflow-wrap:anywhere]">{a.primary_domain}</span>
        ) : (
          <Badge tone="neutral">未登记</Badge>
        ),
    },
    {
      id: "environment",
      header: "环境",
      value: (a) => a.environment,
      cell: (a) => <span className="text-xs">{a.environment}</span>,
    },
    {
      id: "auth",
      header: "登录方式",
      value: (a) => a.auth_mode,
      cell: (a) =>
        a.auth_mode ? (
          <span className="text-xs">{authModeLabel(a.auth_mode)}</span>
        ) : (
          <Badge tone="neutral">未登记</Badge>
        ),
    },
    {
      // 冻结的设计产出里有这一列，但它属于页面搭建器——平台没有那个对象。
      // **留列不留数**（同 XM-0048 上游表里那三列的处理）：把列删掉会让这一页
      // 与设计稿对不上，摆一个空值又会被读成「这个应用还没有配置版本」。
      id: "config-version",
      header: "配置版本",
      value: () => "",
      cell: () => (
        <span className="text-xs text-fg-muted" title="配置版本属于页面搭建器，平台没有这个对象">
          未接入
        </span>
      ),
    },
    {
      id: "release",
      header: "发布版本",
      value: (a) => a.current_release?.version ?? "",
      cell: (a) =>
        a.current_release ? (
          <span className="text-xs">
            <span className="font-medium">{a.current_release.version}</span>
            <span className="block font-mono text-fg-muted">
              {a.current_release.commit_sha || "提交号未登记"}
            </span>
          </span>
        ) : (
          // 「没登记过发布」与「版本是空的」是两件事。
          <Badge tone="neutral">未登记发布</Badge>
        ),
    },
    {
      id: "owner",
      header: "负责人",
      value: (a) => a.owner,
      cell: (a) => <span className="text-xs">{a.owner}</span>,
    },
    {
      id: "status",
      header: "状态",
      value: (a) => a.status,
      cell: (a) => (
        <Badge tone={STATUS_TONE[a.status] ?? "neutral"}>{appStatusLabel(a.status)}</Badge>
      ),
    },
    {
      id: "actions",
      header: "详情",
      cell: (a) => (
        <div className="flex flex-wrap gap-1">
          <ExtAppDialog app={a} onDone={(runId) => onDone({ title: "应用已更新", runId })} />
          {a.status === "retired" ? null : (
            <ExtAppRetireDialog
              app={a}
              onDone={(runId) => onDone({ title: "应用已标记下线", runId })}
            />
          )}
        </div>
      ),
    },
  ];
}

/** 行展开区：把没进列的登记字段与时间戳摊开。
 *
 *  蓝图那一列叫「详情」，但这一页**不做详情页**：一个前端站点的登记只有
 *  十来个字段，为它开一条路由等于多一个要维护的地址，而展开区就地能说完。 */
function ExtAppDetail({ app }: { app: ExtAppItem }) {
  return (
    <dl className="grid grid-cols-1 gap-2 text-xs sm:grid-cols-2">
      <DetailRow label="应用键" value={app.app_key} mono />
      <DetailRow label="环境" value={app.environment} />
      <DetailRow label="域名" value={app.primary_domain || "未登记"} mono />
      <DetailRow label="登录方式" value={authModeLabel(app.auth_mode)} />
      <DetailRow label="负责人" value={app.owner} />
      <DetailRow label="状态" value={appStatusLabel(app.status)} />
      <DetailRow
        label="当前发布版本"
        value={
          app.current_release
            ? `${app.current_release.version}（${app.current_release.commit_sha || "提交号未登记"}）`
            : "未登记发布"
        }
      />
      <DetailRow
        label="当前版本发布时间"
        value={
          app.current_release ? formatUtcTimestamp(app.current_release.released_at) : "未登记发布"
        }
      />
      <DetailRow label="备注" value={app.notes || "—"} />
      <DetailRow label="登记时间" value={formatUtcTimestamp(app.created_at)} />
      <DetailRow label="最近更新" value={formatUtcTimestamp(app.updated_at)} />
    </dl>
  );
}

function DetailRow({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="flex flex-wrap items-baseline gap-2">
      <dt className="text-fg-muted">{label}</dt>
      <dd className={mono ? "font-mono text-fg [overflow-wrap:anywhere]" : "text-fg"}>{value}</dd>
    </div>
  );
}

// ---------------------------------------------------------------------------
// 登记 / 修改
// ---------------------------------------------------------------------------

interface AppFormValues {
  app_key: string;
  display_name: string;
  primary_domain: string;
  auth_mode: string;
  owner: string;
  status: string;
  notes: string;
}

const EMPTY_APP_FORM: AppFormValues = {
  app_key: "",
  display_name: "",
  primary_domain: "",
  auth_mode: "",
  owner: "",
  status: "active",
  notes: "",
};

function initialAppValues(app?: ExtAppItem): AppFormValues {
  if (!app) return { ...EMPTY_APP_FORM };
  return {
    app_key: app.app_key,
    display_name: app.display_name,
    primary_domain: app.primary_domain,
    auth_mode: app.auth_mode,
    owner: app.owner,
    status: app.status,
    notes: app.notes,
  };
}

/** 与后端 extapp.appKeyPattern 同形（core.service.instance_id 的同一条规则）。 */
const APP_KEY_PATTERN = /^[a-z0-9][a-z0-9-]{0,63}$/;
/** 与后端 extapp.hostnamePattern 同形，也与库层 CHECK 同形。 */
const HOSTNAME_PATTERN = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/;

export function validateAppForm(
  values: AppFormValues,
): Partial<Record<keyof AppFormValues, string>> {
  const errors: Partial<Record<keyof AppFormValues, string>> = {};
  const key = values.app_key.trim();
  if (key === "") {
    errors.app_key = "应用键不能为空";
  } else if (!APP_KEY_PATTERN.test(key)) {
    errors.app_key = "只能用小写字母、数字与连字符，且不以连字符开头";
  }
  if (values.display_name.trim() === "") errors.display_name = "显示名不能为空";
  if (values.owner.trim() === "") errors.owner = "负责人不能为空";

  const domain = values.primary_domain.trim().toLowerCase();
  if (domain !== "" && !HOSTNAME_PATTERN.test(domain)) {
    // 前端先拦一道，与后端说同一句话——最常见的填错是粘了一整条 URL，
    // 而那条 URL 可能带着凭据（后端会拒，但那时它已经过了一次网络）。
    errors.primary_domain = "填主机名（如 admin.example.com），不要带 https://、路径、端口或用户信息";
  }
  return errors;
}

export function ExtAppDialog({
  app,
  onDone,
}: {
  app?: ExtAppItem;
  onDone: (runId: string) => void;
}) {
  const editing = Boolean(app);
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<AppFormValues>(() => initialAppValues(app));
  const [errors, setErrors] = useState<Partial<Record<keyof AppFormValues, string>>>({});
  const [attempts, setAttempts] = useState(0);
  const fieldPrefix = useId();

  const mutation = useMutation({
    mutationFn: (form: AppFormValues) =>
      setExtApp({
        ...(app ? { app_id: app.id } : {}),
        ...form,
        // 主机名归一放在提交前，与后端做同样的事：唯一索引区分大小写，
        // 两个写法会得到两行。
        primary_domain: form.primary_domain.trim().toLowerCase(),
      }),
    onSuccess: (run) => {
      setOpen(false);
      setErrors({});
      setAttempts(0);
      if (!editing) setValues(initialAppValues());
      onDone(run.runId);
    },
  });

  const set = (field: keyof AppFormValues) => (value: string) => {
    setValues((prev) => ({ ...prev, [field]: value }));
    setErrors((prev) => {
      if (!(field in prev)) return prev;
      const next = { ...prev };
      delete next[field];
      return next;
    });
  };

  const summaryRef = useRef<HTMLDivElement>(null);
  const failedFields = (Object.keys(errors) as (keyof AppFormValues)[]).filter((f) => errors[f]);
  const showSummary = attempts > 0 && (failedFields.length > 0 || Boolean(mutation.error));
  useEffect(() => {
    if (showSummary) summaryRef.current?.focus();
  }, [attempts, mutation.error, showSummary]);

  const submit = () => {
    setAttempts((n) => n + 1);
    const found = validateAppForm(values);
    setErrors(found);
    if (Object.values(found).some(Boolean)) return;
    mutation.mutate(values);
  };

  const text = (field: keyof AppFormValues, label: string, hint?: string, required = false) => {
    const id = `${fieldPrefix}-${field}`;
    return (
      <FormField
        label={label}
        htmlFor={id}
        required={required}
        {...(errors[field] ? { error: errors[field] } : {})}
        {...(hint ? { hint } : {})}
      >
        <Input
          id={id}
          value={values[field]}
          invalid={Boolean(errors[field])}
          onChange={(e) => set(field)(e.target.value)}
        />
      </FormField>
    );
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) {
          setValues(initialAppValues(app));
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
          <Button size="sm">登记应用</Button>
        )
      }
      title={editing ? "修改应用" : "登记应用"}
      description="通过 extapp.app.set@1 整行更新。只做记录：平台不会因此部署、重启或探测这个站点。"
    >
      <form
        className="flex max-h-[60vh] flex-col gap-3 overflow-y-auto"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        {text("app_key", "应用键", "稳定标识，如 admin-web；小写字母数字与连字符", true)}
        {text("display_name", "显示名", undefined, true)}
        {text("primary_domain", "域名", "主机名，如 admin.example.com；不要带 https:// 与路径")}

        <FormField label="登录方式">
          <Select
            options={AUTH_MODE_OPTIONS}
            value={values.auth_mode}
            onValueChange={set("auth_mode")}
            aria-label="登录方式"
          />
        </FormField>

        {text("owner", "负责人", undefined, true)}

        <FormField label="状态" hint="登记值，不是探活结果">
          <Select
            options={APP_STATUS_OPTIONS}
            value={values.status}
            onValueChange={set("status")}
            aria-label="状态"
          />
        </FormField>

        {text("notes", "备注")}

        <div
          ref={summaryRef}
          tabIndex={-1}
          className="flex flex-col gap-1 outline-none focus-visible:ring-2 focus-visible:ring-accent"
        >
          {showSummary && failedFields.length > 0 ? (
            <p role="alert" className="text-xs text-danger">
              还有 {failedFields.length} 处需要修正
            </p>
          ) : null}
          <ActionErrorNote error={mutation.error} permission={EXT_APP_MANAGE_PERMISSION} />
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

// ---------------------------------------------------------------------------
// 下线
// ---------------------------------------------------------------------------

export function ExtAppRetireDialog({
  app,
  onDone,
}: {
  app: ExtAppItem;
  onDone: (runId: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | undefined>(undefined);
  const fieldId = useId();

  const mutation = useMutation({
    mutationFn: () => retireExtApp({ app_id: app.id, reason: reason.trim() }),
    onSuccess: (run) => {
      setOpen(false);
      setReason("");
      setError(undefined);
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
          setError(undefined);
          mutation.reset();
        }
      }}
      trigger={
        <Button size="sm" variant="secondary">
          下线
        </Button>
      }
      title={`把「${app.display_name}」标记为已下线`}
      description="只改登记簿里的状态。平台没有关停站点的通道——真正下线站点是 Platform Lifecycle Operation，走部署流程。"
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (reason.trim() === "") {
            setError("下线原因不能为空");
            return;
          }
          setError(undefined);
          mutation.mutate();
        }}
      >
        <FormField
          label="下线原因"
          htmlFor={fieldId}
          required
          hint="会写进审计链：事后复盘的第一个问题就是「当时为什么下线」"
          {...(error ? { error } : {})}
        >
          <Input
            id={fieldId}
            value={reason}
            invalid={Boolean(error)}
            onChange={(e) => {
              setReason(e.target.value);
              if (error) setError(undefined);
            }}
          />
        </FormField>
        <ActionErrorNote error={mutation.error} permission={EXT_APP_MANAGE_PERMISSION} />
        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" size="sm" onClick={() => setOpen(false)}>
            取消
          </Button>
          <Button type="submit" size="sm" loading={mutation.isPending}>
            标记下线
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
