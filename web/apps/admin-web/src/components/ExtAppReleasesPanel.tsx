import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  DataTableV2,
  PageState,
  formatUtcTimestamp,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge, Button, Dialog, FormField, Input, Select } from "@xingmang/ui-primitives";
import { useEffect, useId, useMemo, useRef, useState } from "react";
import {
  EXT_APPS_QUERY,
  EXT_APP_MANAGE_PERMISSION,
  EXT_APP_RELEASES_QUERY,
  RELEASE_KIND_OPTIONS,
  listExtAppReleases,
  listExtApps,
  recordExtAppRelease,
  releaseKindLabel,
  type ExtAppItem,
  type ExtAppReleaseItem,
} from "../api/extapp";
import { ActionErrorNote } from "./ActionErrorNote";
import { ActionResultNote, type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";

/** 发布记录簿（ADMIN-IA §5.4「应用与配置 → 版本与发布」，`?sub=releases`）。
 *
 *  ⚠️ **这一格不发布任何东西**。平台没有、也不会有发布或回滚端点：发布与
 *  回滚是 Platform Lifecycle Operation（宪法 2、3 条），走版本化脚本 + 人工
 *  批准。这里只把**已经发生**的发布记下来，登记这件事本身走
 *  extapp.release.record@1 与审计链。
 *
 *  「当前」标记是**算出来的**，不是存下来的：每个应用 released_at 最新的
 *  那一条就是当前版本。存一列 is_current 会与发布记录本身分叉，而它唯一的
 *  来源就是这些记录。 */
export function ExtAppReleasesPanel() {
  const queryClient = useQueryClient();
  const [result, setResult] = useState<ActionResult | null>(null);

  const releasesQuery = useQuery({
    queryKey: [EXT_APP_RELEASES_QUERY],
    queryFn: ({ signal }) => listExtAppReleases({ signal }),
  });
  // 应用清单用来把 app_id 显示成人看得懂的名字，并给登记表单填下拉。
  // 与应用目录共用同一个 queryKey：读的是同一个端点的同一份快照，分两个键
  // 只会在切页时多打一次请求。
  const appsQuery = useQuery({
    queryKey: [EXT_APPS_QUERY],
    queryFn: ({ signal }) => listExtApps({ signal }),
  });

  const apps = useMemo(() => appsQuery.data ?? [], [appsQuery.data]);
  const releases = releasesQuery.data ?? [];

  const appById = useMemo(() => {
    const map = new Map<string, ExtAppItem>();
    for (const app of apps) map.set(app.id, app);
    return map;
  }, [apps]);

  /** 每个应用的当前版本 = released_at 最新的那条记录 id。
   *
   *  直接用 `current_release.id` 而不是自己在这批记录里再排一次序：那样两处
   *  会在「同一时刻两条记录」这种边界上给出不同答案，而后端那一份才是
   *  应用目录里显示的那个。 */
  const currentReleaseIds = useMemo(() => {
    const ids = new Set<string>();
    for (const app of apps) {
      if (app.current_release) ids.add(app.current_release.id);
    }
    return ids;
  }, [apps]);

  const afterWrite = (written: ActionResult) => {
    setResult(written);
    void queryClient.invalidateQueries({ queryKey: [EXT_APP_RELEASES_QUERY] });
    // 应用目录那一列的「发布版本」也要跟着变——它来自同一批记录。
    void queryClient.invalidateQueries({ queryKey: [EXT_APPS_QUERY] });
  };

  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <p className="max-w-3xl text-xs text-fg-muted">
          记录「已经发生」的发布：哪个应用、什么时候、上了哪个版本 / 哪个提交、谁发的。
          回滚也记在这里（版本填「回滚到的那个版本」）。这一格不触发发布，也没有回滚按钮。
        </p>
        <ExtAppReleaseDialog
          apps={apps}
          onDone={(runId) => afterWrite({ title: "发布记录已登记", runId })}
        />
      </div>

      {result ? <ActionResultNote result={result} onDismiss={() => setResult(null)} /> : null}

      <ApiStateView
        isPending={releasesQuery.isPending}
        error={releasesQuery.error}
        onRetry={() => void releasesQuery.refetch()}
      >
        <DataTableV2
          caption="发布记录簿：应用、版本、提交、类型、发布时间、发布人"
          columns={releaseColumns(appById, currentReleaseIds)}
          rows={releases}
          rowKey={(r) => r.id}
          searchable
          pageSize={20}
          emptyState={
            <PageState
              kind="empty"
              title="还没有登记任何发布"
              description={`发布发生之后用右上角的「记录发布」补一条。需要 ${EXT_APP_MANAGE_PERMISSION} 才能写入。这张表是登记出来的，不会自己长出行——平台不读部署流水线。`}
            />
          }
        />
      </ApiStateView>
    </section>
  );
}

function releaseColumns(
  appById: ReadonlyMap<string, ExtAppItem>,
  currentReleaseIds: ReadonlySet<string>,
): DataTableColumn<ExtAppReleaseItem>[] {
  return [
    {
      id: "app",
      header: "应用",
      primary: true,
      value: (r) => appById.get(r.app_id)?.display_name ?? r.app_id,
      cell: (r) => {
        const app = appById.get(r.app_id);
        if (!app) {
          // 应用清单还没读到 / 读失败时，不编一个名字出来（同 lib/metrics
          // 对未登记指标的处理）：显示 id，人至少能拿它去查。
          return <span className="font-mono text-xs [overflow-wrap:anywhere]">{r.app_id}</span>;
        }
        return (
          <>
            <span className="font-medium">{app.display_name}</span>
            <p className="font-mono text-xs text-fg-muted">{app.app_key}</p>
          </>
        );
      },
    },
    {
      id: "version",
      header: "版本",
      value: (r) => r.version,
      cell: (r) => (
        <span className="text-xs">
          <span className="font-medium">{r.version}</span>
          {currentReleaseIds.has(r.id) ? (
            <Badge tone="success" className="ml-1">
              当前
            </Badge>
          ) : null}
        </span>
      ),
    },
    {
      id: "commit",
      header: "提交",
      value: (r) => r.commit_sha,
      cell: (r) =>
        r.commit_sha ? (
          <span className="font-mono text-xs [overflow-wrap:anywhere]">{r.commit_sha}</span>
        ) : (
          <Badge tone="neutral">未登记</Badge>
        ),
    },
    {
      id: "kind",
      header: "类型",
      value: (r) => r.kind,
      cell: (r) => (
        <Badge tone={r.kind === "rollback" ? "warning" : "neutral"}>
          {releaseKindLabel(r.kind)}
        </Badge>
      ),
    },
    {
      id: "released-at",
      header: "发布时间",
      value: (r) => r.released_at,
      cell: (r) => (
        <span className="text-xs tabular-nums">{formatUtcTimestamp(r.released_at)}</span>
      ),
    },
    {
      id: "released-by",
      header: "发布人",
      value: (r) => r.released_by,
      cell: (r) => <span className="text-xs">{r.released_by}</span>,
    },
    {
      id: "recorded-at",
      header: "登记时间",
      value: (r) => r.created_at,
      cell: (r) => (
        // 与「发布时间」分开显示：补记一次历史发布时两者差很远，而那正是
        // 「这条记录可信到什么程度」的线索。
        <span className="text-xs tabular-nums text-fg-muted">
          {formatUtcTimestamp(r.created_at)}
        </span>
      ),
    },
    {
      id: "notes",
      header: "备注",
      value: (r) => r.notes,
      cell: (r) =>
        r.notes ? (
          <span className="text-xs">{r.notes}</span>
        ) : (
          <span className="text-xs text-fg-muted">—</span>
        ),
    },
  ];
}

// ---------------------------------------------------------------------------
// 记录一次发布
// ---------------------------------------------------------------------------

interface ReleaseFormValues {
  app_id: string;
  version: string;
  commit_sha: string;
  kind: string;
  released_at: string;
  released_by: string;
  notes: string;
}

const EMPTY_RELEASE_FORM: ReleaseFormValues = {
  app_id: "",
  version: "",
  commit_sha: "",
  kind: "deploy",
  released_at: "",
  released_by: "",
  notes: "",
};

/** 与后端 extapp.commitPattern 同形（7~40 位小写十六进制）。 */
const COMMIT_PATTERN = /^[0-9a-f]{7,40}$/;
/** 与后端 optionalTimeParam 同一条要求：RFC3339 且**带时区**。 */
const RFC3339_PATTERN = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/;

export function validateReleaseForm(
  values: ReleaseFormValues,
): Partial<Record<keyof ReleaseFormValues, string>> {
  const errors: Partial<Record<keyof ReleaseFormValues, string>> = {};
  if (values.app_id.trim() === "") errors.app_id = "要先选一个应用";
  if (values.version.trim() === "") errors.version = "版本不能为空";
  if (values.released_by.trim() === "") errors.released_by = "发布人不能为空";

  const commit = values.commit_sha.trim().toLowerCase();
  if (commit !== "" && !COMMIT_PATTERN.test(commit)) {
    errors.commit_sha = "提交号是 7~40 位十六进制（短 SHA 或全长 SHA）";
  }
  const at = values.released_at.trim();
  if (at !== "" && !RFC3339_PATTERN.test(at)) {
    // 不带时区的时刻会被当成另一个时刻**而且不报错**——补记历史发布时，
    // 「上一版是几点上的」正是回滚复盘要问的问题。
    errors.released_at = "形如 2026-09-08T10:00:00Z，必须带时区（留空 = 现在）";
  }
  return errors;
}

export function ExtAppReleaseDialog({
  apps,
  onDone,
}: {
  apps: readonly ExtAppItem[];
  onDone: (runId: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [values, setValues] = useState<ReleaseFormValues>({ ...EMPTY_RELEASE_FORM });
  const [errors, setErrors] = useState<Partial<Record<keyof ReleaseFormValues, string>>>({});
  const [attempts, setAttempts] = useState(0);
  const fieldPrefix = useId();

  const appOptions = useMemo(
    () => [
      { value: "", label: "选择应用" },
      ...apps.map((a) => ({ value: a.id, label: `${a.display_name}（${a.app_key}）` })),
    ],
    [apps],
  );

  const mutation = useMutation({
    mutationFn: (form: ReleaseFormValues) =>
      recordExtAppRelease({
        app_id: form.app_id,
        version: form.version.trim(),
        released_by: form.released_by.trim(),
        // 提交号归一成小写，与后端做同样的事：短 SHA 常被从代码托管页面
        // 复制成大写，而校验只认小写。
        commit_sha: form.commit_sha.trim().toLowerCase(),
        kind: form.kind,
        released_at: form.released_at.trim(),
        notes: form.notes.trim(),
      }),
    onSuccess: (run) => {
      setOpen(false);
      setValues({ ...EMPTY_RELEASE_FORM });
      setErrors({});
      setAttempts(0);
      onDone(run.runId);
    },
  });

  const set = (field: keyof ReleaseFormValues) => (value: string) => {
    setValues((prev) => ({ ...prev, [field]: value }));
    setErrors((prev) => {
      if (!(field in prev)) return prev;
      const next = { ...prev };
      delete next[field];
      return next;
    });
  };

  const summaryRef = useRef<HTMLDivElement>(null);
  const failedFields = (Object.keys(errors) as (keyof ReleaseFormValues)[]).filter((f) => errors[f]);
  const showSummary = attempts > 0 && (failedFields.length > 0 || Boolean(mutation.error));
  useEffect(() => {
    if (showSummary) summaryRef.current?.focus();
  }, [attempts, mutation.error, showSummary]);

  const submit = () => {
    setAttempts((n) => n + 1);
    const found = validateReleaseForm(values);
    setErrors(found);
    if (Object.values(found).some(Boolean)) return;
    mutation.mutate(values);
  };

  const text = (field: keyof ReleaseFormValues, label: string, hint?: string, required = false) => {
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
        if (!next) {
          setErrors({});
          setAttempts(0);
          mutation.reset();
        }
      }}
      trigger={
        <Button size="sm" disabled={apps.length === 0}>
          记录发布
        </Button>
      }
      title="记录一次发布"
      description="登记一次已经发生的发布。这不会触发任何部署——发布与回滚走版本化脚本 + 人工批准（Platform Lifecycle Operation）。"
    >
      <form
        className="flex max-h-[60vh] flex-col gap-3 overflow-y-auto"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <FormField
          label="应用"
          required
          {...(errors.app_id ? { error: errors.app_id } : {})}
        >
          <Select
            options={appOptions}
            value={values.app_id}
            onValueChange={set("app_id")}
            aria-label="应用"
          />
        </FormField>

        {text("version", "版本", "回滚时填「回滚到的那个版本」", true)}
        {text("commit_sha", "提交号", "7~40 位十六进制，可留空")}

        <FormField label="类型">
          <Select
            options={RELEASE_KIND_OPTIONS}
            value={values.kind}
            onValueChange={set("kind")}
            aria-label="类型"
          />
        </FormField>

        {text("released_at", "发布时间", "形如 2026-09-08T10:00:00Z，必须带时区；留空 = 现在")}
        {text("released_by", "发布人", "执行那次发布的人，可以不是你", true)}
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
            记录
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
