import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  DataTableV2,
  FreshnessBadge,
  FreshnessNote,
  PageState,
  StatTile,
  formatUtcTimestamp,
  type DataTableColumn,
} from "@xingmang/ui-admin";
import { Badge, Button, Dialog, FormField, Input, type BadgeTone } from "@xingmang/ui-primitives";
import { useState, type ReactNode } from "react";
import {
  ASSURANCE_WINDOWS,
  ASSURANCE_WINDOW_LABELS,
  getPlatformAssuranceHistory,
  getPlatformAssuranceOverview,
  type AssuranceCoverage,
  type AssuranceHistory as AssuranceHistoryData,
  type AssuranceHistoryDay,
  type AssuranceLatencyPercentiles,
  type AssuranceModelRow,
  type AssuranceOverview as AssuranceOverviewData,
  type AssuranceStatusClasses,
  type AssuranceWindow,
} from "../api/assurance";
import {
  cancelProbe,
  getPlatformAssuranceProbeHistory,
  getPlatformAssuranceProbes,
  PROBE_MANAGE_PERMISSION,
  PROBE_RUN_PERMISSION,
  runProbe,
  type ProbeHistoryEntry,
  type ProbeListItem,
} from "../api/assuranceProbes";
import type { ApiClient } from "../api/client";
import { formatCount } from "../lib/money";
import { AssuranceProbeDeclareDialog } from "./AssuranceProbeDeclareDialog";
import { AssuranceProbeKillSwitch } from "./AssuranceProbeKillSwitch";
import { ActionErrorNote } from "./ActionErrorNote";
import { ActionResultNote, type ActionResult } from "./ActionResultNote";
import { ApiStateView } from "./ApiStateView";

/** 渠道保障（原型孤儿页 `V["s2/model"]`，ADMIN-IA v3 §8.1 裁定 #1 恢复为页签）。
 *
 *  XM-ASSURE0 第一片把「保障概览」「历史记录」两个子页签接上**被动**指标
 *  ——从请求审计索引（reqlog）按窗口/按天聚合出的请求量、状态分类、延迟
 *  百分位、按模型拆分，数据源与「请求详情」页完全相同，只是这里读的是
 *  服务端聚合结果，不是逐条列表。
 *
 *  **不支持按渠道拆分**：请求审计的磁盘格式本身不采集渠道/上游字段——
 *  不是这一片没做，是这条数据源从写入那一刻起就没有这个维度（后端
 *  `connectors/reqlog` 的 `ChannelBreakdownUnsupportedReason`，服务端在每次
 *  响应里显式给出这句话，本文件原样转述，不重新编一遍）。
 *
 *  「检测任务」（主动探测）仍是纯 UI 蓝图——声明 → 探针 → 结论需要一个真正
 *  发起请求的 Action，且必须带 Kill Switch（宪法 26 条），设计与实现划给
 *  XM-ASSURE1，本片不冒充已上线。 */

// --- 共享格式化辅助（保障概览与历史记录两个子页签共用同一套口径） ---

function formatMs(v: number | null): string {
  return v === null ? "—" : `${formatCount(v)}ms`;
}

/** 成功率。total===0 时不是「0%」——那是「没有请求」，与「全部失败」
 *  是完全不同的两件事（与 lib/metrics.ts 的 renderSuccessRate24h 同一条
 *  纪律）。这里的除法只用于**展示**，不进入任何持久化或再计算，不受
 *  宪法 13 条（金额禁止 float）约束——那条铁律管的是货币，不是延迟/计数
 *  的展示态百分比。 */
function successRateText(c: AssuranceStatusClasses): string {
  const total = c.success + c.clientError + c.serverError + c.disconnected + c.other;
  if (total === 0) return "无请求";
  return `${((c.success / total) * 100).toFixed(1)}%`;
}

/** 失败构成的紧凑文案，只列非零项——4xx/5xx/连接中断三者需要运营采取的
 *  动作完全不同，合并成一个「失败」会抹掉这个区别（见 assurance.go 的
 *  StatusClassCounts 注释）。 */
function failureBreakdownText(c: AssuranceStatusClasses): string {
  const parts: string[] = [];
  if (c.clientError > 0) parts.push(`4xx ${formatCount(c.clientError)}`);
  if (c.serverError > 0) parts.push(`5xx ${formatCount(c.serverError)}`);
  if (c.disconnected > 0) parts.push(`连接中断 ${formatCount(c.disconnected)}`);
  if (c.other > 0) parts.push(`其他 ${formatCount(c.other)}`);
  return parts.length > 0 ? parts.join(" · ") : "无失败";
}

function percentileSummary(p: AssuranceLatencyPercentiles): string {
  if (p.sampleCount === 0) return "无样本";
  return `P50 ${formatMs(p.p50Ms)} · P95 ${formatMs(p.p95Ms)} · P99 ${formatMs(p.p99Ms)}`;
}

/** 「按渠道拆分」这条限制的说明条——保障概览与历史记录共用，文案取自
 *  服务端响应而不是本地硬编码第二份，避免两处各写一份、迟早对不上。 */
function ChannelBreakdownNotice({ reason }: { reason: string }) {
  return (
    <p
      role="status"
      className="flex flex-wrap items-center gap-2 rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted"
    >
      <Badge tone="neutral">按渠道拆分 · 未接入</Badge>
      <span>{reason}</span>
    </p>
  );
}

// --- 保障概览 ---

const ASSURANCE_WINDOW_OPTIONS = ASSURANCE_WINDOWS.map((value) => ({
  value,
  label: ASSURANCE_WINDOW_LABELS[value],
}));

function AssuranceWindowControl({
  value,
  onChange,
}: {
  value: AssuranceWindow;
  onChange: (next: AssuranceWindow) => void;
}) {
  return (
    <div role="group" aria-label="保障概览统计窗口" className="flex flex-wrap gap-1">
      {ASSURANCE_WINDOW_OPTIONS.map((option) => {
        const active = option.value === value;
        return (
          <button
            key={option.value}
            type="button"
            aria-pressed={active}
            onClick={() => onChange(option.value)}
            className={
              active
                ? "rounded-md border border-accent bg-accent-soft px-3 py-2 text-xs font-medium text-accent"
                : "rounded-md border border-edge px-3 py-2 text-xs text-fg-muted hover:bg-surface-muted focus:outline-2 focus:outline-accent"
            }
          >
            {option.label}
          </button>
        );
      })}
    </div>
  );
}

const MODEL_COLUMNS: DataTableColumn<AssuranceModelRow>[] = [
  {
    id: "model",
    header: "模型",
    primary: true,
    value: (m) => m.model,
    cell: (m) => (m.model ? m.model : <span className="text-fg-muted">(未知模型)</span>),
  },
  {
    id: "requests",
    header: "请求数",
    numeric: true,
    value: (m) => m.requestCount,
    cell: (m) => formatCount(m.requestCount),
  },
  {
    id: "success",
    header: "成功率",
    value: (m) => successRateText(m.statusClasses),
    cell: (m) => successRateText(m.statusClasses),
  },
  {
    id: "failures",
    header: "失败构成",
    cell: (m) => failureBreakdownText(m.statusClasses),
  },
  {
    id: "duration",
    header: "总耗时",
    headerTitle: "整次请求耗时的 P50/P95/P99",
    cell: (m) => percentileSummary(m.durationMs),
  },
  {
    id: "ttfb",
    header: "首字节",
    headerTitle: "只统计测量过首字节的请求；非流式请求通常没有这个值",
    cell: (m) =>
      m.ttfbMs.sampleCount > 0
        ? `${percentileSummary(m.ttfbMs)}（测得 ${formatCount(m.ttfbMs.sampleCount)}/${formatCount(m.requestCount)}）`
        : "未测量",
  },
];

function CoverageFooter({
  coverage,
  retentionDays,
  children,
}: {
  coverage: AssuranceCoverage;
  retentionDays: number;
  children: ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-center gap-2 text-xs text-fg-muted">
      {children}
      <span aria-hidden="true">·</span>
      <span>
        {coverage.missingDays > 0
          ? `索引覆盖不全：跨越 ${coverage.spannedDays} 天目录，其中 ${coverage.missingDays} 天缺失（记录代理未产生该日数据，或已过 ${retentionDays} 天保留期）`
          : `索引覆盖完整（保留期 ${retentionDays} 天）`}
      </span>
      {coverage.badLines > 0 ? <span>· 跳过 {formatCount(coverage.badLines)} 行坏索引</span> : null}
    </div>
  );
}

function AssuranceOverviewBody({ overview }: { overview: AssuranceOverviewData }) {
  return (
    <div className="flex flex-col gap-3">
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatTile
          label="请求量"
          value={formatCount(overview.requestCount)}
          note={`窗口 ${ASSURANCE_WINDOW_LABELS[overview.window]} · 截至 ${formatUtcTimestamp(overview.until)}`}
        />
        <StatTile
          label="成功率"
          value={successRateText(overview.statusClasses)}
          unavailable={overview.requestCount === 0}
          note={
            overview.requestCount === 0
              ? "窗口内没有请求"
              : `失败构成：${failureBreakdownText(overview.statusClasses)}`
          }
        />
        <StatTile
          label="总耗时"
          value={overview.durationMs.sampleCount > 0 ? `P50 ${formatMs(overview.durationMs.p50Ms)}` : "—"}
          unavailable={overview.durationMs.sampleCount === 0}
          note={
            overview.durationMs.sampleCount > 0
              ? `P95 ${formatMs(overview.durationMs.p95Ms)} · P99 ${formatMs(overview.durationMs.p99Ms)}`
              : "窗口内无请求"
          }
        />
        <StatTile
          label="首字节耗时"
          value={overview.ttfbMs.sampleCount > 0 ? `P50 ${formatMs(overview.ttfbMs.p50Ms)}` : "—"}
          unavailable={overview.ttfbMs.sampleCount === 0}
          note={
            overview.ttfbMs.sampleCount > 0
              ? `P95 ${formatMs(overview.ttfbMs.p95Ms)} · 已测量 ${formatCount(overview.ttfbMs.sampleCount)}/${formatCount(overview.requestCount)}`
              : "本窗口没有测得首字节的记录（通常是非流式请求，或响应在收到任何字节前就中断）"
          }
        />
      </div>

      <ChannelBreakdownNotice reason={overview.channelBreakdownReason} />

      <DataTableV2
        caption={`按模型统计（窗口 ${ASSURANCE_WINDOW_LABELS[overview.window]}）`}
        columns={MODEL_COLUMNS}
        rows={overview.models}
        rowKey={(m) => m.model || "(unknown-model)"}
        emptyState={
          <PageState
            kind="empty"
            title="窗口内没有请求"
            description="选定窗口内没有匹配到任何请求记录，换一个更长的窗口试试。"
          />
        }
      />
      {overview.modelsTruncated ? (
        <p className="text-xs text-fg-muted">
          按模型的明细已超过展示上限，只显示请求数最多的前 {formatCount(overview.models.length)} 个模型。
        </p>
      ) : null}

      <CoverageFooter coverage={overview.coverage} retentionDays={overview.retentionDays}>
        <FreshnessBadge freshness={overview.freshness} />
        <FreshnessNote freshness={overview.freshness} />
      </CoverageFooter>
    </div>
  );
}

function AssuranceOverview({ platform }: { platform: string }) {
  const [window, setWindow] = useState<AssuranceWindow>("1h");
  const query = useQuery({
    queryKey: ["assurance", "overview", platform, window],
    queryFn: ({ signal }) => getPlatformAssuranceOverview(platform, window, { signal }),
  });

  return (
    <div className="flex flex-col gap-3">
      <AssuranceWindowControl value={window} onChange={setWindow} />
      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        {query.data ? <AssuranceOverviewBody overview={query.data} /> : null}
      </ApiStateView>
    </div>
  );
}

// --- 检测任务（主动探测，XM-ASSURE1-ui 起接真实 Query/Action） ---

/** 结果列 pill：按 `last_run_status` 映射。muted（neutral）专门留给
 *  「从未运行」「已取消」这类非结果状态，不复用 warning 的黄色去表示
 *  它们（设计稿 §6.1 明确要求区分）。 */
function probeResultDisplay(item: ProbeListItem): { tone: BadgeTone; label: string } {
  switch (item.lastRunStatus) {
    case "never_run":
      return { tone: "neutral", label: "从未运行" };
    case "pending":
      return { tone: "info", label: "进行中（排队）" };
    case "running":
      return { tone: "info", label: "进行中" };
    case "refused":
      return { tone: "warning", label: item.lastRunVerdict || "已拒绝执行" };
    case "cancelled":
      return { tone: "neutral", label: "已取消" };
    case "ok":
      return { tone: "success", label: item.lastRunVerdict || "一致" };
    case "degraded":
      return { tone: "warning", label: item.lastRunVerdict || "疑似退化" };
    case "failed":
      return { tone: "danger", label: item.lastRunVerdict || "失败" };
    case "timeout":
      return { tone: "danger", label: item.lastRunVerdict || "超时" };
    default:
      return { tone: "warning", label: `未知状态（${item.lastRunStatus}）` };
  }
}

/** Kill Switch 状态徽章：与结果 pill 分开的一列信息，同样不用 warning 的
 *  黄色表示「未启用」这种非结果状态（设计稿 §6.1）。 */
function killSwitchBadge(state: string): ReactNode {
  if (state === "disabled") return <Badge tone="neutral">未启用</Badge>;
  if (state === "not_applicable_fake") return <Badge tone="neutral">fake 模式</Badge>;
  return null;
}

function CancelProbeDialog({
  item,
  client,
  onDone,
}: {
  item: ProbeListItem;
  client?: ApiClient;
  onDone: (result: ActionResult) => void;
}) {
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: () => cancelProbe({ declarationId: item.declarationId, reason: reason.trim() }, {}, client),
    onSuccess: (run) => {
      setOpen(false);
      onDone({ title: `已取消检测任务「${item.name}」`, runId: run.runId });
      void queryClient.invalidateQueries({ queryKey: ["assurance", "probes"] });
    },
  });
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (!next) {
          setReason("");
          mutation.reset();
        }
      }}
      trigger={
        <Button size="sm" variant="secondary" aria-label={`取消检测任务 ${item.name}`}>
          取消
        </Button>
      }
      title={`取消检测任务：${item.name}`}
      description="撤销这条声明（assurance.probe.cancel@1）。撤销只作用于声明本身，不影响已经在跑的批次；要恢复需要重新声明一条新的检测任务。"
    >
      <div className="flex flex-col gap-3">
        <FormField label="取消原因" htmlFor="cancel-probe-reason" required>
          <Input
            id="cancel-probe-reason"
            value={reason}
            disabled={mutation.isPending}
            onChange={(event) => setReason(event.target.value)}
          />
        </FormField>
        <ActionErrorNote error={mutation.error} permission={PROBE_MANAGE_PERMISSION} />
        <div className="flex justify-end gap-2">
          <Button type="button" size="sm" variant="secondary" onClick={() => setOpen(false)} disabled={mutation.isPending}>
            返回
          </Button>
          <Button
            type="button"
            size="sm"
            variant="danger"
            loading={mutation.isPending}
            disabled={!reason.trim()}
            onClick={() => mutation.mutate()}
          >
            确认取消
          </Button>
        </div>
      </div>
    </Dialog>
  );
}

function ProbeRowActions({
  item,
  client,
  onDone,
}: {
  item: ProbeListItem;
  client?: ApiClient;
  onDone: (result: ActionResult) => void;
}) {
  const queryClient = useQueryClient();
  const runMutation = useMutation({
    mutationFn: () => runProbe({ declarationId: item.declarationId }, {}, client),
    onSuccess: (run) => {
      const result = run.result as { status?: string; refusal_reason?: string } | undefined;
      const refused = result?.status === "refused";
      onDone({
        title: refused
          ? `「${item.name}」被拒绝执行（${result?.refusal_reason ?? "未知原因"}）`
          : `已触发「${item.name}」的检测批次`,
        runId: run.runId,
      });
      void queryClient.invalidateQueries({ queryKey: ["assurance", "probes"] });
    },
  });
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <Button
        size="sm"
        aria-label={`运行 ${item.name}`}
        title={item.canRunNow ? undefined : item.cannotRunReasonText}
        disabled={!item.canRunNow || runMutation.isPending}
        loading={runMutation.isPending}
        onClick={() => runMutation.mutate()}
      >
        运行
      </Button>
      <CancelProbeDialog item={item} client={client} onDone={onDone} />
      {runMutation.error ? <ActionErrorNote error={runMutation.error} permission={PROBE_RUN_PERMISSION} /> : null}
    </div>
  );
}

function probeColumns(client: ApiClient | undefined, onDone: (result: ActionResult) => void): DataTableColumn<ProbeListItem>[] {
  return [
    { id: "name", header: "任务", primary: true, value: (p) => p.name, cell: (p) => p.name },
    {
      id: "channels",
      header: "渠道",
      cell: (p) => (p.channelNames.length > 0 ? p.channelNames.join("、") : p.channelIds.join("、") || "—"),
    },
    {
      id: "models",
      header: "目标模型",
      cell: (p) => (p.targetModels.length > 0 ? p.targetModels.join("、") : "—"),
    },
    {
      id: "policy",
      header: "策略",
      cell: (p) => (
        <span className="flex flex-wrap items-center gap-1.5">
          <span>{p.policyText}</span>
          {killSwitchBadge(p.killSwitchState)}
        </span>
      ),
    },
    {
      id: "last-run",
      header: "最近一次",
      cell: (p) => (p.lastRunAt ? formatUtcTimestamp(p.lastRunAt) : "—"),
    },
    {
      id: "result",
      header: "结果",
      cell: (p) => {
        const d = probeResultDisplay(p);
        return <Badge tone={d.tone}>{d.label}</Badge>;
      },
    },
    {
      id: "actions",
      header: "操作",
      cell: (p) => <ProbeRowActions item={p} client={client} onDone={onDone} />,
    },
  ];
}

function AssuranceProbes({
  platform,
  serviceId,
  client,
}: {
  platform: string;
  serviceId?: string;
  client?: ApiClient;
}) {
  const query = useQuery({
    queryKey: ["assurance", "probes", platform],
    queryFn: ({ signal }) => getPlatformAssuranceProbes(platform, { signal }, client),
  });
  const [notice, setNotice] = useState<ActionResult | null>(null);
  const probes = query.data?.probes ?? [];
  // fake 模式标记：只在读到至少一行、且全部都是 not_applicable_fake 时显示——
  // 零声明时没有信号可判断真假，不猜测（宪法 12 条同一条纪律）。
  const allFake = probes.length > 0 && probes.every((p) => p.killSwitchState === "not_applicable_fake");
  const killSwitchCurrentState = probes[0]?.killSwitchState ?? null;

  return (
    <div className="flex flex-col gap-3">
      <p role="status" className="rounded-md border border-edge bg-surface-muted px-3 py-2 text-xs text-fg-muted">
        {query.data?.assertionDisclaimer ?? "检测结果为形状与延迟检测，非语义正确性保证。"}
      </p>
      {allFake ? <Badge tone="neutral">演示数据（该平台探测走 fake 模式，不产生真实调用/费用）</Badge> : null}
      {notice ? <ActionResultNote result={notice} onDismiss={() => setNotice(null)} /> : null}
      {/* 声明+Kill Switch 入口放在 DataTableV2 外层，不走它的 toolbarExtra——
          DataTableV2 空表时只渲染 emptyState、完全跳过 toolbarExtra
          （见该组件 "rows.length === 0" 分支），塞进 toolbarExtra 会导致
          "还没有一条声明"时连"发起检测"入口都看不见，是先有鸡还是先有蛋的
          死结（与 ManagedChannelTable 的"添加上游"按钮撞过的同一个坑，
          那边解法是在 emptyState 里塞第二份按钮；这里直接放外层，
          不需要维护两处相同的按钮）。 */}
      <div className="flex items-center gap-2">
        <AssuranceProbeDeclareDialog
          platform={platform}
          serviceId={serviceId}
          client={client}
          onDone={(summary) =>
            setNotice({
              title:
                summary.runResult?.status === "refused"
                  ? `已声明检测任务，但触发的批次被拒绝执行（${summary.runResult.refusal_reason ?? "未知原因"}）`
                  : "已声明检测任务并触发一次批次",
              runId: summary.declareRunId,
            })
          }
        />
        <AssuranceProbeKillSwitch platform={platform} currentState={killSwitchCurrentState} client={client} />
      </div>
      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
        <DataTableV2
          caption="检测任务"
          columns={probeColumns(client, setNotice)}
          rows={probes}
          rowKey={(p) => p.declarationId}
          emptyState={
            <PageState
              kind="empty"
              title="还没有检测任务"
              description="点击上方「发起检测」声明一条新的检测任务并立即触发一次批次。"
            />
          }
        />
        {query.data ? (
          <div className="flex flex-wrap items-center gap-2 text-xs text-fg-muted">
            <FreshnessBadge freshness={query.data.freshness} />
            <FreshnessNote freshness={query.data.freshness} />
          </div>
        ) : null}
      </ApiStateView>
    </div>
  );
}

// --- 主动检测历史（与被动近 7 天聚合各自独立渲染） ---

const PROBE_HISTORY_PAGE_SIZE = 50;

function probeHistoryResultDisplay(status: string, verdict: string): { tone: BadgeTone; label: string } {
  switch (status) {
    case "ok":
      return { tone: "success", label: verdict || "一致" };
    case "degraded":
      return { tone: "warning", label: verdict || "疑似退化" };
    case "failed":
      return { tone: "danger", label: verdict || "失败" };
    case "timeout":
      return { tone: "danger", label: verdict || "超时" };
    default:
      return { tone: "neutral", label: verdict || status };
  }
}

const PROBE_HISTORY_COLUMNS: DataTableColumn<ProbeHistoryEntry>[] = [
  {
    id: "observed-at",
    header: "时间",
    primary: true,
    value: (e) => e.observedAt,
    cell: (e) => formatUtcTimestamp(e.observedAt),
  },
  { id: "channel", header: "渠道", cell: (e) => e.channelId || "—" },
  { id: "model", header: "模型", cell: (e) => e.model || "—" },
  { id: "declaration", header: "检测项", cell: (e) => e.declarationName || e.promptTemplateKey },
  {
    id: "result",
    header: "结果",
    cell: (e) => {
      const d = probeHistoryResultDisplay(e.status, e.verdict);
      return <Badge tone={d.tone}>{d.label}</Badge>;
    },
  },
  { id: "evidence", header: "证据", cell: (e) => e.evidenceRef || "—" },
];

function AssuranceProbeHistoryCard({ platform, client }: { platform: string; client?: ApiClient }) {
  const query = useQuery({
    queryKey: ["assurance", "probe-history", platform],
    queryFn: ({ signal }) => getPlatformAssuranceProbeHistory(platform, { limit: PROBE_HISTORY_PAGE_SIZE, signal }, client),
  });
  return (
    <div className="flex flex-col gap-3">
      <h3 className="text-sm font-semibold text-fg">主动检测历史</h3>
      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
        <DataTableV2
          caption="主动检测历史：按时间倒序的逐条检测结果"
          columns={PROBE_HISTORY_COLUMNS}
          rows={query.data?.entries ?? []}
          rowKey={(e) => `${e.observedAt}:${e.channelId}:${e.model}:${e.evidenceRef}`}
          emptyState={<PageState kind="empty" title="没有主动检测历史" description="还没有任何检测批次产生过结果。" />}
        />
        {query.data ? (
          <div className="flex flex-wrap items-center gap-2 text-xs text-fg-muted">
            <FreshnessBadge freshness={query.data.freshness} />
            <FreshnessNote freshness={query.data.freshness} />
            {query.data.nextCursor ? <span>还有更多历史，未来可加「加载更多」</span> : null}
          </div>
        ) : null}
      </ApiStateView>
    </div>
  );
}

// --- 历史记录 ---

const HISTORY_COLUMNS: DataTableColumn<AssuranceHistoryDay>[] = [
  { id: "day", header: "业务日", primary: true, value: (d) => d.day, cell: (d) => d.day },
  {
    id: "coverage",
    header: "覆盖",
    value: (d) => (d.missing ? "missing" : "complete"),
    cell: (d) =>
      d.missing ? <Badge tone="warning">目录缺失</Badge> : <Badge tone="success">完整</Badge>,
  },
  {
    id: "requests",
    header: "请求量",
    numeric: true,
    value: (d) => d.requestCount,
    cell: (d) => formatCount(d.requestCount),
  },
  {
    id: "success",
    header: "成功率",
    value: (d) => successRateText(d.statusClasses),
    cell: (d) => successRateText(d.statusClasses),
  },
  { id: "failures", header: "失败构成", cell: (d) => failureBreakdownText(d.statusClasses) },
  { id: "duration", header: "总耗时", cell: (d) => percentileSummary(d.durationMs) },
  { id: "ttfb", header: "首字节", cell: (d) => percentileSummary(d.ttfbMs) },
  {
    id: "models",
    header: "涉及模型数",
    numeric: true,
    value: (d) => d.models.length,
    cell: (d) => formatCount(d.models.length),
  },
];

function AssuranceHistoryBody({ history }: { history: AssuranceHistoryData }) {
  const missingCount = history.days.filter((d) => d.missing).length;
  return (
    <div className="flex flex-col gap-3">
      <h3 className="text-sm font-semibold text-fg">被动聚合（近 7 天）</h3>
      <ChannelBreakdownNotice reason={history.channelBreakdownReason} />
      <DataTableV2
        caption="渠道保障历史：按业务日聚合的请求量/成功率/延迟"
        columns={HISTORY_COLUMNS}
        rows={history.days}
        rowKey={(d) => d.day}
        emptyState={
          <PageState kind="empty" title="没有历史数据" description="最近 7 天内没有可用的索引目录。" />
        }
      />
      <div className="flex flex-wrap items-center gap-2 text-xs text-fg-muted">
        <FreshnessBadge freshness={history.freshness} />
        <FreshnessNote freshness={history.freshness} />
        <span aria-hidden="true">·</span>
        <span>
          {missingCount > 0
            ? `近 7 天中有 ${missingCount} 天索引目录缺失（未产生数据，或已过 ${history.retentionDays} 天保留期）`
            : `近 7 天索引目录均完整（保留期 ${history.retentionDays} 天）`}
        </span>
      </div>
    </div>
  );
}

/** 历史记录子页签渲染**两张独立卡片**：被动聚合（近 7 天，ASSURE0 原样
 *  不动）与主动检测历史（本片新增）。两者数据源、写路径、风险等级完全
 *  独立，不合并成一张看起来是同一份数据的表（设计稿 §0 / §6.2）。 */
function AssuranceHistory({ platform, client }: { platform: string; client?: ApiClient }) {
  const query = useQuery({
    queryKey: ["assurance", "history", platform],
    queryFn: ({ signal }) => getPlatformAssuranceHistory(platform, { signal }),
  });
  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-3">
        <p className="text-xs text-fg-muted">
          最近 7 天的被动保障指标，按业务日（Asia/Shanghai）聚合。没有可用的降采样汇总时这是唯一的历史来源，因此固定
          7 天，不支持更长区间——不让这个 Query 被当成数据导出口。
        </p>
        <ApiStateView
          isPending={query.isPending}
          error={query.error}
          onRetry={() => void query.refetch()}
        >
          {query.data ? <AssuranceHistoryBody history={query.data} /> : null}
        </ApiStateView>
      </div>
      <hr className="border-edge" />
      <AssuranceProbeHistoryCard platform={platform} client={client} />
    </div>
  );
}

/** 按子页签 id 取内容。认不出的返回 undefined，由调用方回落到通用占位。
 *
 *  platform 是 Sub2API/NewAPI 的 serviceType（"sub2api" / "newapi"）——两个
 *  平台共用同一套组件，数据源与聚合逻辑完全对称，只是查询参数不同。
 *  serviceId 只有"检测任务"子页签用到（发起检测对话框要读渠道目录，需要
 *  恰好一个已登记 service）；client 只在单测里注入，生产路径始终省略,
 *  由各 api 函数回落到默认单例。 */
export function assuranceSubTab(
  subId: string,
  platform: string,
  serviceId?: string,
  client?: ApiClient,
): ReactNode | undefined {
  switch (subId) {
    case "overview":
      return <AssuranceOverview platform={platform} />;
    case "probes":
      return <AssuranceProbes platform={platform} serviceId={serviceId} client={client} />;
    case "history":
      return <AssuranceHistory platform={platform} client={client} />;
    default:
      return undefined;
  }
}
