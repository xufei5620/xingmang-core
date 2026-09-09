import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Badge, Button, Dialog, FormField, Input } from "@xingmang/ui-primitives";
import { useEffect, useId, useRef, useState } from "react";
import {
  confirmPlatformChannelBinding,
  removePlatformChannelBinding,
  PLATFORM_CHANNEL_BINDING_MANAGE_PERMISSION,
  type PlatformChannelRow,
} from "../api/platformChannels";
import { CHANNEL_BINDING_HISTORY_QUERY_KEY } from "../api/platformChannelBindings";
import { ActionErrorNote } from "./ActionErrorNote";
import { ActionResultNote, type ActionResult } from "./ActionResultNote";
import { ChannelBindingHistory } from "./ChannelBindingHistory";

const CANDIDATE_LABELS: Record<string, { label: string; tone: "success" | "warning" | "danger" | "neutral" }> = {
  unmapped: { label: "未映射", tone: "neutral" },
  candidate: { label: "待确认", tone: "warning" },
  conflict: { label: "冲突", tone: "danger" },
  orphan: { label: "孤儿记录", tone: "warning" },
};

/** 「上游映射」卡片：候选/冲突/绑定的确认与解绑，从渠道管理列表页搬过来
 *  （2026-09-02 裁定，见 `ManagedChannelTable.tsx` 文件头）。
 *
 *  两个 Action 都已经在后端注册好、只是之前没有前端 UI
 *  （`internal/platform/finance/channel_binding_actions.go`：L1、只认
 *  HUMAN 主体、权限 `finance.platform_channel_binding.manage`）；这里第一次
 *  把它们接上，不是重新设计一套新的绑定逻辑。 */
export function ChannelBindingCard({
  serviceId,
  row,
  onDone,
}: {
  serviceId: string;
  row: PlatformChannelRow;
  onDone: () => void;
}) {
  const [result, setResult] = useState<ActionResult | null>(null);
  const afterWrite = (written: ActionResult) => {
    setResult(written);
    onDone();
  };

  const shown = CANDIDATE_LABELS[row.candidate.state] ?? { label: row.candidate.state, tone: "neutral" as const };

  return (
    <section className="min-w-0 rounded-lg border border-edge bg-surface p-4 shadow-sm">
      <header className="mb-3 flex flex-wrap items-baseline justify-between gap-2">
        <h3 className="text-sm font-semibold text-fg">上游映射</h3>
        <p className="text-xs text-fg-muted">这条渠道映射到哪个上游账号；确认与解绑都走 L1 Action，带理由进审计链</p>
      </header>

      {result ? <ActionResultNote result={result} onDismiss={() => setResult(null)} /> : null}

      <dl className="grid grid-cols-1 gap-x-6 gap-y-3 text-sm sm:grid-cols-2">
        <div>
          <dt className="text-xs text-fg-muted">当前状态</dt>
          <dd className="mt-0.5">
            <Badge tone={row.binding ? "success" : shown.tone}>{row.binding ? "已绑定" : shown.label}</Badge>
          </dd>
        </div>
        <div>
          <dt className="text-xs text-fg-muted">证据充分度</dt>
          <dd className="mt-0.5 text-fg">{evidenceLabel(row.candidate.evidenceStatus)}</dd>
        </div>
        {row.binding ? (
          <>
            <div>
              <dt className="text-xs text-fg-muted">绑定的上游账号</dt>
              <dd className="mt-0.5 font-mono text-xs break-all text-fg">{row.binding.upstreamAccountId}</dd>
            </div>
            <div>
              <dt className="text-xs text-fg-muted">生效时间</dt>
              <dd className="mt-0.5 text-fg">{row.binding.validFrom || "—"}</dd>
            </div>
          </>
        ) : (
          <div className="sm:col-span-2">
            <dt className="text-xs text-fg-muted">候选上游账号</dt>
            <dd className="mt-0.5 text-fg">
              {row.candidate.upstreamAccountIds.length > 0
                ? row.candidate.upstreamAccountIds.join("、")
                : "没有候选，需要人工输入上游账号 id"}
            </dd>
          </div>
        )}
        {row.candidate.reasonCodes.length > 0 ? (
          <div className="sm:col-span-2">
            <dt className="text-xs text-fg-muted">判定原因</dt>
            <dd className="mt-0.5 text-fg">{row.candidate.reasonCodes.join("、")}</dd>
          </div>
        ) : null}
        {row.conflicts.length > 0 ? (
          <div className="sm:col-span-2">
            <dt className="text-xs text-danger">冲突</dt>
            <dd className="mt-0.5 text-danger">{row.conflicts.join("、")}</dd>
          </div>
        ) : null}
        {row.candidate.platformAssignmentMissing ? (
          <p className="text-xs text-warning sm:col-span-2">平台归属尚未登记，确认绑定前请先核实这条渠道属于哪个自营平台。</p>
        ) : null}
      </dl>

      <div className="mt-3 flex flex-wrap gap-2">
        <ConfirmBindingDialog serviceId={serviceId} row={row} onDone={(runId) => afterWrite({ title: row.binding ? "上游映射已更新" : "上游映射已确认", runId })} />
        {row.binding ? (
          <RemoveBindingDialog serviceId={serviceId} row={row} bindingId={row.binding.id} onDone={(runId) => afterWrite({ title: "上游映射已解除", runId })} />
        ) : null}
      </div>

      {/* 绑定历史放在这张卡里、动作按钮下面：它说的是同一件事的过去时。
          单独摆成一张卡会让人把「现在绑在谁身上」和「以前绑过谁」当成两块
          互不相干的信息，而改绑之前最该看的恰恰是上一次为什么这么绑 */}
      <ChannelBindingHistory serviceId={serviceId} externalChannelId={row.channelRef.externalChannelId} />
    </section>
  );
}

function evidenceLabel(status: string): string {
  switch (status) {
    case "sufficient":
      return "证据充分";
    case "conflicting":
      return "证据冲突";
    case "insufficient":
    default:
      return "证据不足";
  }
}

function ConfirmBindingDialog({
  serviceId,
  row,
  onDone,
}: {
  serviceId: string;
  row: PlatformChannelRow;
  onDone: (runId: string) => void;
}) {
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [accountId, setAccountId] = useState("");
  const [reason, setReason] = useState("");
  const [errors, setErrors] = useState<{ accountId?: string; reason?: string }>({});
  const [attempts, setAttempts] = useState(0);
  const fieldPrefix = useId();

  const mutation = useMutation({
    mutationFn: (input: { accountId: string; reason: string }) =>
      confirmPlatformChannelBinding({
        serviceId,
        externalChannelId: row.channelRef.externalChannelId,
        upstreamAccountId: input.accountId.trim(),
        reason: input.reason.trim(),
        ...(row.binding ? { expectedBindingId: row.binding.id } : {}),
      }),
    onSuccess: (run) => {
      setOpen(false);
      setErrors({});
      setAttempts(0);
      setReason("");
      void queryClient.invalidateQueries({ queryKey: ["platform-channels"] });
      // 确认/改绑会往 finance.platform_channel_binding 插一行，历史因此多一条
      void queryClient.invalidateQueries({ queryKey: [CHANNEL_BINDING_HISTORY_QUERY_KEY] });
      onDone(run.runId);
    },
  });

  const summaryRef = useRef<HTMLDivElement>(null);
  const showSummary = attempts > 0 && (Object.keys(errors).length > 0 || Boolean(mutation.error));
  useEffect(() => {
    if (showSummary) summaryRef.current?.focus();
  }, [attempts, mutation.error, showSummary]);

  const submit = () => {
    setAttempts((n) => n + 1);
    const found: { accountId?: string; reason?: string } = {};
    if (!accountId.trim()) found.accountId = "上游账号 id 必填";
    if (!reason.trim()) found.reason = "必须写明为什么确认这条映射：改绑之后成本会归到不同的上游账号上";
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    mutation.mutate({ accountId, reason });
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) {
          setAccountId(row.binding?.upstreamAccountId ?? row.candidate.upstreamAccountIds[0] ?? "");
          setReason("");
        } else {
          setErrors({});
          setAttempts(0);
          mutation.reset();
        }
      }}
      trigger={<Button size="sm">{row.binding ? "改绑" : "确认绑定"}</Button>}
      title={row.binding ? "改绑上游账号" : "确认上游映射"}
      description="通过 finance.platform_channel_binding.set@1 写入，需要人工理由，记入审计链。"
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        {row.candidate.upstreamAccountIds.length > 0 ? (
          <p className="text-xs text-fg-muted">
            候选：{row.candidate.upstreamAccountIds.join("、")}
          </p>
        ) : null}
        <FormField
          label="上游账号 id"
          htmlFor={`${fieldPrefix}-account`}
          required
          {...(errors.accountId ? { error: errors.accountId } : {})}
          hint="finance.upstream_account 的主键；可以从候选里挑一个，也可以手填一个登记簿里已有的账号 id"
        >
          <Input
            value={accountId}
            invalid={Boolean(errors.accountId)}
            onChange={(e) => {
              setAccountId(e.target.value);
              setErrors((prev) => ({ ...prev, accountId: undefined }));
            }}
          />
        </FormField>
        <FormField
          label="理由"
          htmlFor={`${fieldPrefix}-reason`}
          required
          {...(errors.reason ? { error: errors.reason } : {})}
          hint="会进审计链，写清楚凭什么认定是这个上游账号"
        >
          <Input
            value={reason}
            invalid={Boolean(errors.reason)}
            onChange={(e) => {
              setReason(e.target.value);
              setErrors((prev) => ({ ...prev, reason: undefined }));
            }}
          />
        </FormField>

        <div ref={summaryRef} tabIndex={-1} className="outline-none focus-visible:ring-2 focus-visible:ring-accent">
          <ActionErrorNote error={mutation.error} permission={PLATFORM_CHANNEL_BINDING_MANAGE_PERMISSION} />
        </div>

        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" size="sm" onClick={() => setOpen(false)}>
            取消
          </Button>
          <Button type="submit" size="sm" loading={mutation.isPending}>
            保存
          </Button>
        </div>
      </form>
    </Dialog>
  );
}

function RemoveBindingDialog({
  serviceId,
  row,
  bindingId,
  onDone,
}: {
  serviceId: string;
  row: PlatformChannelRow;
  bindingId: string;
  onDone: (runId: string) => void;
}) {
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | undefined>(undefined);
  const [attempts, setAttempts] = useState(0);
  const fieldPrefix = useId();

  const mutation = useMutation({
    mutationFn: (input: { reason: string }) =>
      removePlatformChannelBinding({
        serviceId,
        externalChannelId: row.channelRef.externalChannelId,
        expectedBindingId: bindingId,
        reason: input.reason.trim(),
      }),
    onSuccess: (run) => {
      setOpen(false);
      setError(undefined);
      setAttempts(0);
      setReason("");
      void queryClient.invalidateQueries({ queryKey: ["platform-channels"] });
      // 解绑不插新行，但它会让「当前生效」那个标记从这条渠道上消失——
      // 历史列表照样要重取，否则那一行会继续标着「当前生效」
      void queryClient.invalidateQueries({ queryKey: [CHANNEL_BINDING_HISTORY_QUERY_KEY] });
      onDone(run.runId);
    },
  });

  const submit = () => {
    setAttempts((n) => n + 1);
    if (!reason.trim()) {
      setError("必须写明为什么解绑：没有理由的解绑在事后复盘时与手滑不可区分");
      return;
    }
    setError(undefined);
    mutation.mutate({ reason });
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) {
          setReason("");
        } else {
          setError(undefined);
          setAttempts(0);
          mutation.reset();
        }
      }}
      trigger={
        <Button size="sm" variant="secondary">
          解绑
        </Button>
      }
      title="解除上游映射"
      description="通过 finance.platform_channel_binding.remove@1 写入，需要人工理由，记入审计链。"
    >
      <form
        className="flex flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          submit();
        }}
      >
        <p className="rounded-md border border-edge bg-surface-muted px-2 py-1.5 text-xs text-fg-muted">
          解绑之后这条渠道会回到未映射状态，成本与毛利在确认新的映射之前算不出来。
        </p>
        <FormField
          label="理由"
          htmlFor={`${fieldPrefix}-reason`}
          required
          {...(error ? { error } : {})}
          hint="会进审计链"
        >
          <Input
            value={reason}
            invalid={Boolean(error)}
            onChange={(e) => {
              setReason(e.target.value);
              setError(undefined);
            }}
          />
        </FormField>

        {attempts > 0 ? (
          <ActionErrorNote error={mutation.error} permission={PLATFORM_CHANNEL_BINDING_MANAGE_PERMISSION} />
        ) : null}

        <div className="flex justify-end gap-2">
          <Button type="button" variant="secondary" size="sm" onClick={() => setOpen(false)}>
            取消
          </Button>
          <Button type="submit" size="sm" variant="secondary" loading={mutation.isPending}>
            确认解绑
          </Button>
        </div>
      </form>
    </Dialog>
  );
}
