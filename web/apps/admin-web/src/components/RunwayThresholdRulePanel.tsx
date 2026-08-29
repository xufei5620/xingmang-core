import { useMutation, useQuery } from "@tanstack/react-query";
import { formatUtcTimestamp, PageState } from "@xingmang/ui-admin";
import { Badge, Button, FormField, Input } from "@xingmang/ui-primitives";
import { useEffect, useMemo, useState } from "react";
import {
  getRunwayThresholds,
  listRunwayThresholdHistory,
  previewRunwayThresholds,
  RUNWAY_THRESHOLD_HISTORY_QUERY,
  RUNWAY_THRESHOLD_PREVIEW_QUERY,
  RUNWAY_THRESHOLD_QUERY,
  type RunwayImpactItem,
  type RunwayImpactPreview,
  type RunwayThresholdDraft,
} from "../api/runwayThresholds";
import { appApiConfig } from "../api/config";
import { ApiStateView } from "./ApiStateView";
import { describeSeverity, describeStatus } from "../lib/alerts";
import { RUNWAY_REASON_TEXT } from "../lib/runway";

const TRANSITION_LABEL: Record<string, string> = {
  would_open: "将打开",
  would_escalate: "将升级",
  would_deescalate: "将降级",
  would_resolve: "将恢复",
  unchanged: "不变",
  current_inconsistent: "当前不一致",
};

const CONSISTENCY_LABEL: Record<string, string> = {
  missing_active_alert: "当前分类已进入告警档，但没有活跃 R5 告警",
  unexpected_active_alert: "当前分类不应有活跃 R5 告警",
  severity_mismatch: "活跃 R5 严重度与当前分类不一致",
  duplicate_active_alert: "同一账号存在多个活跃 R5 告警",
};

function parseDraftValue(raw: string): number | null {
  if (!/^\d+$/.test(raw.trim())) return null;
  const value = Number(raw);
  return Number.isSafeInteger(value) && value > 0 ? value : null;
}

function validateDraft(values: Record<keyof RunwayThresholdDraft, string>): string | null {
  const critical = parseDraftValue(values.criticalDays);
  const warning = parseDraftValue(values.warningDays);
  const serious = parseDraftValue(values.seriousDays);
  if (critical === null || warning === null || serious === null) return "三个阈值都必须是正整数";
  if (!(critical < warning && warning < serious)) return "必须满足 0 < critical < warning < serious";
  return null;
}

function toDraft(values: Record<keyof RunwayThresholdDraft, string>): RunwayThresholdDraft | null {
  const criticalDays = parseDraftValue(values.criticalDays);
  const warningDays = parseDraftValue(values.warningDays);
  const seriousDays = parseDraftValue(values.seriousDays);
  if (criticalDays === null || warningDays === null || seriousDays === null) return null;
  return { criticalDays, warningDays, seriousDays };
}

function draftsEqual(left: RunwayThresholdDraft | null, right: RunwayThresholdDraft | null): boolean {
  if (!left || !right) return left === right;
  return left.criticalDays === right.criticalDays && left.warningDays === right.warningDays && left.seriousDays === right.seriousDays;
}

function levelLabel(level: string): string {
  return ({ critical: "Critical", warning: "Warning", serious: "Serious", healthy: "Healthy" } as Record<string, string>)[level] ?? "未知";
}

function transitionTone(transition: string): "danger" | "warning" | "success" | "neutral" {
  if (transition === "would_escalate" || transition === "current_inconsistent") return "danger";
  if (transition === "would_open") return "warning";
  if (transition === "would_resolve" || transition === "would_deescalate") return "success";
  return "neutral";
}

function observationIsStale(observedAt: string | null, evaluationAt: string): boolean {
  if (!observedAt || !evaluationAt) return false;
  const observed = Date.parse(observedAt);
  const evaluated = Date.parse(evaluationAt);
  return Number.isFinite(observed) && Number.isFinite(evaluated) && evaluated - observed > 30 * 60 * 1000;
}

function unknownReasonLabel(reason: string): string {
  return RUNWAY_REASON_TEXT[reason] ?? `${reason}（前端暂不认识）`;
}

export interface RunwayThresholdRulePanelProps {
  /** 便于 Storybook/单测注入 API 配置；生产默认使用当前应用身份。 */
  environment?: string;
}

/**
 * R5 可用天数规则的内联只读/预览面。
 *
 * C3d 阶段刻意没有提交按钮、Drawer、Action catalog 或 manage scope：
 * 先让运营看懂阈值会影响哪些上游，再等待 Foundation-B/C3c 的审批写链。
 */
export function RunwayThresholdRulePanel({ environment = appApiConfig.environment }: RunwayThresholdRulePanelProps) {
  const config = useMemo(() => ({ ...appApiConfig, environment }), [environment]);
  const currentQuery = useQuery({
    queryKey: [RUNWAY_THRESHOLD_QUERY, environment ?? ""],
    queryFn: ({ signal }) => getRunwayThresholds(undefined, config, { signal }),
    retry: false,
  });
  const historyQuery = useQuery({
    queryKey: [RUNWAY_THRESHOLD_HISTORY_QUERY, environment ?? ""],
    queryFn: ({ signal }) => listRunwayThresholdHistory({ limit: 20, signal }, undefined, config),
    enabled: currentQuery.isSuccess,
    retry: false,
  });
  const [draft, setDraft] = useState<Record<keyof RunwayThresholdDraft, string>>({
    criticalDays: "", warningDays: "", seriousDays: "",
  });
  const [baseSnapshot, setBaseSnapshot] = useState<RunwayThresholdDraft | null>(null);
  const [baseRevision, setBaseRevision] = useState<number | null>(null);
  const [validationError, setValidationError] = useState<string | null>(null);
  const [lastPreviewDraft, setLastPreviewDraft] = useState<RunwayThresholdDraft | null>(null);
  const previewMutation = useMutation({
    mutationKey: [RUNWAY_THRESHOLD_PREVIEW_QUERY, environment ?? ""],
    mutationFn: ({ value, limit }: { value: RunwayThresholdDraft; limit?: number }) =>
      previewRunwayThresholds(value, limit === undefined ? {} : { limit }, undefined, config),
    onSuccess: (_result, variables) => setLastPreviewDraft(variables.value),
  });

  useEffect(() => {
    setBaseSnapshot(null);
    setBaseRevision(null);
    setValidationError(null);
    setLastPreviewDraft(null);
    previewMutation.reset();
  }, [environment]);

  const currentDraft = toDraft(draft);
  const draftDirty = !draftsEqual(currentDraft, baseSnapshot);
  const revisionChanged = Boolean(currentQuery.data && baseRevision !== null && currentQuery.data.revision !== baseRevision);

  useEffect(() => {
    if (!currentQuery.data) return;
    const next: RunwayThresholdDraft = {
      criticalDays: currentQuery.data.criticalDays,
      warningDays: currentQuery.data.warningDays,
      seriousDays: currentQuery.data.seriousDays,
    };
    if (baseSnapshot === null || (revisionChanged && !draftDirty)) {
      setDraft({ criticalDays: String(next.criticalDays), warningDays: String(next.warningDays), seriousDays: String(next.seriousDays) });
      setBaseSnapshot(next);
      setBaseRevision(currentQuery.data.revision);
      setValidationError(null);
      setLastPreviewDraft(null);
      previewMutation.reset();
    }
  }, [currentQuery.data, baseSnapshot, baseRevision, revisionChanged, draftDirty]);

  const updateDraft = (key: keyof RunwayThresholdDraft, value: string) => {
    const next = { ...draft, [key]: value };
    setDraft(next);
    setValidationError(validateDraft(next));
  };

  const preview = (limit?: number) => {
    const invalid = validateDraft(draft);
    if (invalid) {
      setValidationError(invalid);
      return;
    }
    const next = toDraft(draft);
    if (next) previewMutation.mutate({ value: next, limit });
  };

  const refreshAll = () => {
    void Promise.all([currentQuery.refetch(), historyQuery.refetch()]);
    setLastPreviewDraft(null);
    previewMutation.reset();
  };

  const previewStale = Boolean(
    previewMutation.data &&
      (Boolean(lastPreviewDraft && !draftsEqual(currentDraft, lastPreviewDraft)) ||
        revisionChanged ||
        Boolean(currentQuery.data && previewMutation.data.currentRevision !== currentQuery.data.revision)),
  );

  return (
    <section className="flex min-w-0 flex-col gap-4">
      <ApiStateView isPending={currentQuery.isPending} error={currentQuery.error} onRetry={() => void currentQuery.refetch()}>
        {currentQuery.data ? (
          <div className="flex min-w-0 flex-col gap-4">
            <CurrentThresholdCard snapshot={currentQuery.data} onRefresh={refreshAll} refreshing={currentQuery.isFetching || historyQuery.isFetching} />
            <ThresholdEditor
              draft={draft}
              validationError={validationError}
              dirty={draftDirty}
              hasPreview={Boolean(lastPreviewDraft)}
              revisionChanged={revisionChanged}
              onResetDraft={() => {
                if (!currentQuery.data) return;
                const next = { criticalDays: currentQuery.data.criticalDays, warningDays: currentQuery.data.warningDays, seriousDays: currentQuery.data.seriousDays };
                setDraft({ criticalDays: String(next.criticalDays), warningDays: String(next.warningDays), seriousDays: String(next.seriousDays) });
                setBaseSnapshot(next);
                setBaseRevision(currentQuery.data.revision);
                setValidationError(null);
                setLastPreviewDraft(null);
                previewMutation.reset();
              }}
              onChange={updateDraft}
              onPreview={preview}
              previewing={previewMutation.isPending}
            />
            <FoundationGate />
            <RuleDeclaration />
            <PreviewSection preview={previewMutation.data} isPending={previewMutation.isPending} error={previewMutation.error} onRetry={() => preview()} stale={previewStale} />
            {historyQuery.isSuccess ? <HistorySection items={historyQuery.data.items} hasMore={historyQuery.data.hasMore} /> : null}
            {historyQuery.isError ? <ApiStateView isPending={false} error={historyQuery.error} onRetry={() => void historyQuery.refetch()} compact><span /></ApiStateView> : null}
          </div>
        ) : null}
      </ApiStateView>
    </section>
  );
}

function CurrentThresholdCard({ snapshot, onRefresh, refreshing }: { snapshot: { environment: string; criticalDays: number; warningDays: number; seriousDays: number; revision: number; source: string; updatedAt: string; updatedBy: string; reason: string }; onRefresh: () => void; refreshing: boolean }) {
  return (
    <section className="rounded-xl border border-edge bg-surface p-4 shadow-sm">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <p className="text-xs font-medium uppercase tracking-wide text-fg-muted">R5 · 上游可用天数</p>
          <h2 className="mt-1 text-base font-semibold text-fg">当前阈值快照</h2>
          <p className="mt-1 text-xs leading-5 text-fg-muted">服务端环境：<span className="font-mono text-fg">{snapshot.environment || "未知"}</span> · revision 会同时用于看板分档与下一轮告警评估。</p>
        </div>
        <div className="flex flex-wrap items-center justify-end gap-2">
          <Badge tone="success">revision {snapshot.revision}</Badge>
          <Badge tone={snapshot.source === "database" ? "success" : "warning"}>{snapshot.source || "来源未知"}</Badge>
          <Button type="button" size="sm" variant="ghost" onClick={onRefresh} loading={refreshing}>重新读取</Button>
        </div>
      </div>
      <div className="mt-4 grid grid-cols-1 gap-3 sm:grid-cols-3">
        <ThresholdMetric label="Critical" value={snapshot.criticalDays} accent="danger" note="≤ 此天数进入最紧告警档" />
        <ThresholdMetric label="Warning" value={snapshot.warningDays} accent="warning" note="≤ 此天数进入告警档" />
        <ThresholdMetric label="Serious" value={snapshot.seriousDays} accent="info" note="仅展示关注色，不创建 R5 通知" />
      </div>
      <div className="mt-4 flex flex-wrap gap-x-5 gap-y-1 border-t border-edge pt-3 text-xs text-fg-muted">
        <span>更新时间：{formatUtcTimestamp(snapshot.updatedAt || null)}</span>
        <span>变更人：{snapshot.updatedBy || "—"}</span>
        <span>理由：{snapshot.reason || "—"}</span>
      </div>
    </section>
  );
}

function ThresholdMetric({ label, value, accent, note }: { label: string; value: number; accent: "danger" | "warning" | "info"; note: string }) {
  const classes = accent === "danger" ? "border-danger/30 bg-danger/5" : accent === "warning" ? "border-warning/30 bg-warning/5" : "border-accent/20 bg-accent-soft/40";
  return <div className={`rounded-lg border p-3 ${classes}`}><div className="flex items-baseline justify-between"><span className="text-xs font-semibold text-fg-muted">{label}</span><span className="font-mono text-2xl font-semibold tabular-nums text-fg">{value}<span className="ml-1 text-xs font-normal text-fg-muted">天</span></span></div><p className="mt-1 text-xs text-fg-muted">{note}</p></div>;
}

function ThresholdEditor({ draft, validationError, dirty, hasPreview, revisionChanged, onResetDraft, onChange, onPreview, previewing }: { draft: Record<keyof RunwayThresholdDraft, string>; validationError: string | null; dirty: boolean; hasPreview: boolean; revisionChanged: boolean; onResetDraft: () => void; onChange: (key: keyof RunwayThresholdDraft, value: string) => void; onPreview: (limit?: number) => void; previewing: boolean }) {
  return (
    <section className="rounded-xl border border-edge bg-surface p-4 shadow-sm">
      <form noValidate onSubmit={(event) => { event.preventDefault(); onPreview(); }}>
      <div className="flex flex-wrap items-start justify-between gap-3"><div><h2 className="text-sm font-semibold text-fg">影响预览</h2><p className="mt-1 text-xs text-fg-muted">调整草稿后预览会打开、升级、降级或恢复哪些上游；不会写入配置。</p></div><Button type="submit" size="sm" loading={previewing}>预览影响</Button></div>
      <div className="mt-4 grid grid-cols-1 gap-3 sm:grid-cols-3">
        <FormField label="Critical 天数" htmlFor="runway-critical" hint="最紧告警档；包含边界值"><Input id="runway-critical" aria-describedby={validationError ? "runway-order-error" : undefined} type="number" min={1} step={1} inputMode="numeric" value={draft.criticalDays} invalid={Boolean(validationError)} onChange={(event) => onChange("criticalDays", event.target.value)} /></FormField>
        <FormField label="Warning 天数" htmlFor="runway-warning" hint="告警档上界；包含边界值"><Input id="runway-warning" aria-describedby={validationError ? "runway-order-error" : undefined} type="number" min={1} step={1} inputMode="numeric" value={draft.warningDays} invalid={Boolean(validationError)} onChange={(event) => onChange("warningDays", event.target.value)} /></FormField>
        <FormField label="Serious 天数" htmlFor="runway-serious" hint="只改变展示关注色，不创建通知"><Input id="runway-serious" aria-describedby={validationError ? "runway-order-error" : undefined} type="number" min={1} step={1} inputMode="numeric" value={draft.seriousDays} invalid={Boolean(validationError)} onChange={(event) => onChange("seriousDays", event.target.value)} /></FormField>
      </div>
      {dirty && !validationError ? <p className="mt-3 text-xs text-fg-muted">{hasPreview ? "草稿已预览，但尚未写入配置。" : "草稿已修改；点击“预览影响”后才会请求服务端评估。"}</p> : null}
      {revisionChanged ? <div role="alert" className="mt-3 flex flex-wrap items-center justify-between gap-2 rounded-md border border-warning/40 bg-warning/5 px-3 py-2 text-xs text-fg"><span>服务端当前 revision 已变化；保留你的草稿，但请先重新读取基线再预览。</span><Button type="button" size="sm" variant="secondary" onClick={onResetDraft}>采用最新当前值</Button></div> : null}
      {validationError ? <p id="runway-order-error" role="alert" className="mt-3 rounded-md border border-danger/30 bg-danger/5 px-3 py-2 text-xs text-danger">{validationError}</p> : null}
      </form>
    </section>
  );
}

function FoundationGate() {
  return <div className="flex items-start gap-3 rounded-lg border border-warning/40 bg-warning/5 px-3 py-3 text-xs text-fg"><span aria-hidden="true" className="mt-0.5 text-warning">●</span><div><p className="font-semibold">Foundation-B / C3c 尚未开放</p><p className="mt-1 leading-5 text-fg-muted">当前页面只提供读取、历史与影响预览。阈值写入需要获批的 L2 Action 与居中确认流程；本页不会读取 manage scope，也不会发送写请求。</p></div></div>;
}

function RuleDeclaration() {
  return <section className="rounded-xl border border-edge bg-surface-muted/40 p-4"><h2 className="text-sm font-semibold text-fg">规则说明</h2><dl className="mt-3 grid grid-cols-1 gap-2 text-xs leading-5 sm:grid-cols-2"><div><dt className="text-fg-muted">数据来源</dt><dd className="text-fg">上游余额快照 ÷ 近 7 个完整业务日的日均消耗</dd></div><div><dt className="text-fg-muted">可通知档</dt><dd className="text-fg">Critical / Warning（天数 ≤ 对应阈值）</dd></div><div><dt className="text-fg-muted">仅展示档</dt><dd className="text-fg">Serious 只改变关注色，不创建或投递 R5 通知</dd></div><div><dt className="text-fg-muted">恢复条件</dt><dd className="text-fg">下一轮评估不再满足告警条件；未知余额不会被当成健康</dd></div></dl></section>;
}

function PreviewSection({ preview, isPending, error, onRetry, stale }: { preview: RunwayImpactPreview | undefined; isPending: boolean; error: unknown; onRetry: () => void; stale: boolean }) {
  if (error) return <ApiStateView isPending={isPending} error={error} onRetry={onRetry} compact><span /></ApiStateView>;
  if (isPending) return <PageState kind="loading" compact />;
  if (!preview) return <section className="rounded-xl border border-dashed border-edge bg-surface-muted/30 p-5 text-center text-xs text-fg-muted">点击“预览影响”后，这里会显示影响对象和当前告警一致性。</section>;

  const counts = [
    { label: "将打开", value: preview.counts.wouldOpen },
    { label: "将升级", value: preview.counts.wouldEscalate },
    { label: "将降级", value: preview.counts.wouldDeescalate },
    { label: "将恢复", value: preview.counts.wouldResolve },
    { label: "不变", value: preview.counts.unchanged },
    { label: "当前不一致", value: preview.counts.currentInconsistent },
  ];
  return (
    <section className="rounded-xl border border-edge bg-surface p-4 shadow-sm">
      <div aria-live="polite">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h2 className="text-sm font-semibold text-fg">预览结果</h2>
            <p className="mt-1 text-xs text-fg-muted">评估时间 {formatUtcTimestamp(preview.evaluationAt || null)} · 当前 revision {preview.currentRevision}</p>
          </div>
          <Badge tone={preview.coverage.known === preview.coverage.total ? "success" : "warning"}>
            覆盖 {preview.coverage.known} / {preview.coverage.total} 个计量型上游
          </Badge>
        </div>
        {stale ? <p role="status" className="mt-3 rounded-md border border-warning/40 bg-warning/5 px-3 py-2 text-xs text-fg">草稿已变化，当前结果是旧预览；请重新预览后再作判断。</p> : null}
        {preview.counts.currentInconsistent > 0 ? <p role="alert" className="mt-3 rounded-md border border-danger/40 bg-danger/5 px-3 py-2 text-xs text-fg">发现 {preview.counts.currentInconsistent} 个当前不一致对象。请先核查 Worker 评估任务与活跃 R5 告警，不要把它们当成阈值变化效果。</p> : null}
        <div className="mt-4 grid grid-cols-2 gap-2 sm:grid-cols-3 xl:grid-cols-6">
          {counts.map((item) => <div key={item.label} className="rounded-md border border-edge bg-surface-muted/40 p-2"><p className="text-xs text-fg-muted">{item.label}</p><p className="mt-1 text-lg font-semibold tabular-nums text-fg">{item.value}</p></div>)}
        </div>
        {Object.keys(preview.coverage.unknownReasons).length > 0 ? <p className="mt-3 text-xs text-fg-muted">未知原因：{Object.entries(preview.coverage.unknownReasons).map(([key, value]) => `${unknownReasonLabel(key)} ×${value}`).join(" · ")}</p> : null}
      </div>
      <ImpactTable items={preview.items} hasMore={preview.hasMore} evaluationAt={preview.evaluationAt} />
    </section>
  );
}

function ImpactTable({ items, hasMore, evaluationAt }: { items: RunwayImpactItem[]; hasMore: boolean; evaluationAt: string }) {
  return <div className="mt-4 overflow-x-auto rounded-lg border border-edge"><table className="w-full min-w-180 text-left text-xs"><caption className="sr-only">可用天数阈值影响对象</caption><thead className="bg-surface-muted text-fg-muted"><tr><th scope="col" className="px-3 py-2 font-medium">上游账号</th><th scope="col" className="px-3 py-2 font-medium">当前 / 提议</th><th scope="col" className="px-3 py-2 font-medium">影响</th><th scope="col" className="px-3 py-2 font-medium">一致性</th><th scope="col" className="px-3 py-2 font-medium">余额观测</th></tr></thead><tbody>{items.length === 0 ? <tr><td colSpan={5} className="px-3 py-6 text-center text-fg-muted">没有变化或当前不一致对象</td></tr> : items.map((item) => { const staleObservation = observationIsStale(item.observedAt, evaluationAt); const severity = item.currentAlertSeverity ? describeSeverity(item.currentAlertSeverity) : null; const status = item.currentAlertStatus ? describeStatus(item.currentAlertStatus) : null; return <tr key={item.accountId} className="border-t border-edge align-top"><td className="px-3 py-2"><p className="font-medium text-fg">{item.name || item.accountId}</p><p className="mt-1 break-all font-mono text-fg-muted">{item.accountId}</p>{item.days === null ? <p className="mt-1 text-fg-muted">可用天数未知</p> : <p className="mt-1 tabular-nums text-fg">{item.days} 天</p>}</td><td className="px-3 py-2 text-fg-muted">{item.oldLevel ? `${levelLabel(item.oldLevel)} → ${levelLabel(item.newLevel)}` : "—"}</td><td className="px-3 py-2"><Badge tone={transitionTone(item.transition)}>{TRANSITION_LABEL[item.transition] ?? item.transition}</Badge></td><td className="px-3 py-2 text-fg-muted">{item.consistencyReason ? <span className="text-danger">{CONSISTENCY_LABEL[item.consistencyReason] ?? item.consistencyReason}</span> : severity && status ? <Badge tone={severity.tone} title={`${severity.hint}；${status.hint}`}>{severity.label} · {status.label}</Badge> : "—"}</td><td className="px-3 py-2 font-mono text-fg-muted"><span>{item.observedAt ? formatUtcTimestamp(item.observedAt) : "未知"}</span>{staleObservation ? <span className="ml-2 inline-flex rounded-sm border border-warning/40 bg-warning/5 px-1.5 py-0.5 font-sans text-[11px] text-fg" title="余额观测早于预览评估时间超过 30 分钟">观测偏旧</span> : null}</td></tr>; })}</tbody></table>{hasMore ? <p className="border-t border-edge px-3 py-2 text-xs text-fg-muted">对象超过服务端返回上限，仅显示当前页；完整分页契约待后续切片。</p> : null}</div>;
}

function HistorySection({ items, hasMore }: { items: Array<{ revision: number; criticalDays: number; warningDays: number; seriousDays: number; changedAt: string; changedBy: string; reason: string; requestId: string; changeSource: string }>; hasMore: boolean }) {
  return <section className="rounded-xl border border-edge bg-surface p-4 shadow-sm"><div className="flex items-baseline justify-between gap-3"><div><h2 className="text-sm font-semibold text-fg">最近变更</h2><p className="mt-1 text-xs text-fg-muted">历史只读展示；每个 revision 都能回到当时的三档值。</p></div>{hasMore ? <span className="text-xs text-fg-muted">还有更早记录</span> : null}</div>{items.length === 0 ? <p className="mt-3 rounded-md border border-dashed border-edge px-3 py-5 text-center text-xs text-fg-muted">暂无阈值历史；完成 bootstrap 后会在这里显示 revision 记录。</p> : <div className="mt-3 overflow-x-auto rounded-lg border border-edge"><table className="w-full min-w-220 text-left text-xs"><caption className="sr-only">runway 阈值历史</caption><thead className="bg-surface-muted text-fg-muted"><tr><th scope="col" className="px-3 py-2 font-medium">Revision</th><th scope="col" className="px-3 py-2 font-medium">阈值</th><th scope="col" className="px-3 py-2 font-medium">来源 / 时间</th><th scope="col" className="px-3 py-2 font-medium">变更理由</th><th scope="col" className="px-3 py-2 font-medium">Request ID</th></tr></thead><tbody>{items.map((item) => <tr key={item.revision} className="border-t border-edge align-top"><td className="px-3 py-2 font-mono font-semibold text-fg">{item.revision}</td><td className="px-3 py-2 tabular-nums text-fg">{item.criticalDays} / {item.warningDays} / {item.seriousDays}</td><td className="px-3 py-2 text-fg-muted"><Badge tone="neutral">{item.changeSource || "未知来源"}</Badge><span className="mt-1 block">{formatUtcTimestamp(item.changedAt || null)}</span><span className="mt-1 block">{item.changedBy || "—"}</span></td><td className="px-3 py-2 text-fg-muted">{item.reason || "—"}</td><td className="px-3 py-2 break-all font-mono text-fg-muted">{item.requestId || "—"}</td></tr>)}</tbody></table></div>}</section>;
}
