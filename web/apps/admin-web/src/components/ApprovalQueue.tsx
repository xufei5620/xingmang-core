import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { formatLocalTimestamp, PageHeader, PageState } from "@xingmang/ui-admin";
import { Badge, Button, Dialog, FormField, Input, Select } from "@xingmang/ui-primitives";
import { useState } from "react";
import {
  APPROVAL_DECIDE_PERMISSION,
  cancelApproval,
  decideApproval,
  executeApproval,
  listApprovals,
  type ApprovalItem,
  type ApprovalStatus,
  type ApprovalVerdict,
} from "../api/approvals";
import { currentPrincipalId } from "../auth/session";
import {
  abilitiesFor,
  displayStatus,
  groupByRisk,
  paramRows,
  statusFullLabel,
  statusHint,
  statusLabel,
  statusTone,
  voteProgress,
} from "../lib/approvals";
import { APPROVAL_VERDICTS, principalTypeHint, principalTypeText } from "../lib/labels";
import { ActionErrorNote } from "./ActionErrorNote";
import { ApiStateView } from "./ApiStateView";
import { RiskBadge } from "./RiskBadge";

const STATUS_FILTER_ALL = "all";
/** 筛选项由**同一份**对照表生成，不再手抄一遍。
 *
 *  顺序照队列里最常用到最少用：先看要处理的，再看已经落定的。 */
const STATUS_FILTER_ORDER: ApprovalStatus[] = [
  "PENDING",
  "APPROVED",
  "EXECUTED",
  "REJECTED",
  "EXPIRED",
  "CANCELLED",
];
const STATUS_FILTER_OPTIONS = [
  { value: STATUS_FILTER_ALL, label: "全部状态" },
  ...STATUS_FILTER_ORDER.map((status) => ({ value: status, label: statusFullLabel(status) })),
];

/** 审批队列（操作与审批页「待审批」子页签，XM-0030b-ui）。
 *
 *  这一格的三条纪律：
 *
 *  1. **不按权限藏按钮。** scope 在任何鉴权模式下都不下发到前端，藏与不藏都是
 *     猜（见 lib/approvals.ts 的 abilitiesFor 注释）。按钮照常给，服务端拒绝
 *     之后就地把原因显示出来。
 *  2. **过期按 expires_at 算，不照抄 status。** `ExpirePending` 定时任务在
 *     XM-0030c 之前没人调，库里会一直停在 PENDING。
 *  3. **执行不让改参数。** 参数在审批那一刻就冻结了，改参数等于换一件事、
 *     要重新提交——所以执行确认框里参数是只读的展示，不是表单。 */
export function ApprovalQueue() {
  const queryClient = useQueryClient();
  const [statusFilter, setStatusFilter] = useState<string>("PENDING");

  const status = statusFilter === STATUS_FILTER_ALL ? undefined : (statusFilter as ApprovalStatus);
  const query = useQuery({
    queryKey: ["approvals", statusFilter],
    queryFn: ({ signal }) =>
      listApprovals({ ...(status ? { status } : {}), ...(signal ? { signal } : {}) }),
  });

  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["approvals"] });
  // 「现在」在这一层取一次，传给所有纯函数。每个卡片各自 new Date() 会让
  // 同一屏里的过期判定出现毫秒级的不一致，也让测试无从固定时间。
  const now = new Date();
  const principalId = currentPrincipalId();
  const groups = groupByRisk(query.data?.items ?? []);

  return (
    <section className="flex flex-col gap-3">
      <PageHeader
        title="待审批"
        description="L2 及以上风险等级的 Action 由内核受理成审批单，批准后由人显式触发执行（宪法 9 条：L3/L4 必须审批；宪法 10 条：AI 不作为第二审批人）。执行用的参数始终取自单上冻结的那一份——改参数等于换一件事，要重新提交。"
        onRefresh={() => void query.refetch()}
        refreshing={query.isFetching}
      />
      <div className="flex items-end gap-2">
        <Select
          aria-label="按状态筛选审批单"
          value={statusFilter}
          onValueChange={setStatusFilter}
          options={STATUS_FILTER_OPTIONS}
        />
      </div>
      <ApiStateView
        isPending={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
      >
        {groups.length === 0 ? (
          <PageState
            kind="empty"
            title={statusFilter === "PENDING" ? "没有待审批的动作" : "没有符合条件的审批单"}
            description="L2 及以上的调用会在这里排队；L0/L1 直接执行，不经过审批。"
          />
        ) : (
          <div className="flex flex-col gap-4">
            {query.data?.truncated ? (
              <p role="status" className="text-xs text-fg-muted">
                已取满 {query.data.limit} 条，这一屏可能不是全部。审批队列长到这个量级
                通常意味着没人在看单——该处理的是积压，不是翻页。
              </p>
            ) : null}
            {groups.map((group) => (
              <section key={group.riskLevel} className="flex flex-col gap-2">
                <h3 className="flex items-center gap-2 text-sm font-medium text-fg">
                  <RiskBadge level={group.riskLevel} />
                  <span>{group.items.length} 张</span>
                </h3>
                <ul className="flex flex-col gap-2">
                  {group.items.map((item) => (
                    <li key={item.id}>
                      <ApprovalCard
                        item={item}
                        now={now}
                        principalId={principalId}
                        onChanged={refresh}
                      />
                    </li>
                  ))}
                </ul>
              </section>
            ))}
          </div>
        )}
      </ApiStateView>
    </section>
  );
}

function ApprovalCard({
  item,
  now,
  principalId,
  onChanged,
}: {
  item: ApprovalItem;
  now: Date;
  principalId: string;
  onChanged: () => void;
}) {
  const status = displayStatus(item, now);
  return (
    <article className="flex flex-col gap-1 rounded-md border border-edge px-3 py-2">
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-mono text-xs [overflow-wrap:anywhere]">
          {item.action_id}@{item.action_version}
        </span>
        <Badge tone={statusTone(status)} title={statusHint(status)}>
          {statusLabel(status)}
        </Badge>
        <ApprovalDetailDialog
          item={item}
          now={now}
          principalId={principalId}
          onChanged={onChanged}
        />
      </div>
      <p className="text-xs text-fg">{item.reason}</p>
      <p className="text-xs text-fg-muted">
        {item.requester_id} 提交于 {formatLocalTimestamp(item.created_at)} · {voteProgress(item, now)}
        {status === "PENDING" ? ` · ${formatLocalTimestamp(item.expires_at)} 到期` : null}
      </p>
    </article>
  );
}

function ApprovalDetailDialog({
  item,
  now,
  principalId,
  onChanged,
}: {
  item: ApprovalItem;
  now: Date;
  principalId: string;
  onChanged: () => void;
}) {
  const [comment, setComment] = useState("");
  const status = displayStatus(item, now);
  const abilities = abilitiesFor(item, principalId, now);

  const vote = useMutation({
    mutationFn: (verdict: ApprovalVerdict) => decideApproval(item.id, verdict, comment),
    onSuccess: onChanged,
  });
  const run = useMutation({
    mutationFn: () => executeApproval(item.id),
    onSuccess: onChanged,
  });
  const withdraw = useMutation({
    mutationFn: () => cancelApproval(item.id),
    onSuccess: onChanged,
  });
  const busy = vote.isPending || run.isPending || withdraw.isPending;

  return (
    <Dialog
      trigger={
        <Button variant="ghost" size="sm">
          详情
        </Button>
      }
      title={`${item.action_id}@${item.action_version}`}
    >
      <div className="flex flex-col gap-3">
        <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
          <dt className="text-fg-muted">状态</dt>
          <dd>
            <Badge tone={statusTone(status)} title={statusHint(status)}>
              {statusLabel(status)}
            </Badge>
            {status !== item.status ? (
              // 库里说 PENDING、其实已经过期。说出这个差别，否则人会以为
              // 「还能批」而实际上服务端在执行那一刻会拒。
              <span className="ml-2 text-fg-muted">
                （库中仍记为{statusLabel(item.status)}——过期清理任务尚未上线）
              </span>
            ) : null}
          </dd>
          <dt className="text-fg-muted">风险等级</dt>
          <dd>
            <RiskBadge level={item.risk_level} />
          </dd>
          <dt className="text-fg-muted">提交人</dt>
          <dd>
            {item.requester_id}（
            <span title={principalTypeHint(item.requester_type)}>
              {principalTypeText(item.requester_type)}
            </span>
            ）
          </dd>
          <dt className="text-fg-muted">理由</dt>
          <dd>{item.reason}</dd>
          <dt className="text-fg-muted">票数</dt>
          <dd>{voteProgress(item, now)}</dd>
          <dt className="text-fg-muted">提交时间</dt>
          <dd>{formatLocalTimestamp(item.created_at)}</dd>
          <dt className="text-fg-muted">到期时间</dt>
          <dd>{formatLocalTimestamp(item.expires_at)}</dd>
          {item.action_run_id ? (
            <>
              <dt className="text-fg-muted">执行记录</dt>
              <dd className="font-mono [overflow-wrap:anywhere]">{item.action_run_id}</dd>
            </>
          ) : null}
        </dl>

        <section className="flex flex-col gap-1">
          <h4 className="text-xs font-medium text-fg">参数（审批时冻结，执行时原样使用）</h4>
          {paramRows(item.params).length === 0 ? (
            <p className="text-xs text-fg-muted">这个动作不带参数。</p>
          ) : (
            <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5 text-xs">
              {paramRows(item.params).map((row) => (
                <div key={row.key} className="contents">
                  <dt className="font-mono text-fg-muted [overflow-wrap:anywhere]">{row.key}</dt>
                  <dd className="font-mono [overflow-wrap:anywhere]">{row.value}</dd>
                </div>
              ))}
            </dl>
          )}
          <p className="font-mono text-xs text-fg-muted [overflow-wrap:anywhere]">
            params_hash: {item.params_hash}
          </p>
        </section>

        <section className="flex flex-col gap-1">
          <h4 className="text-xs font-medium text-fg">已投的票</h4>
          {item.decisions.length === 0 ? (
            <p className="text-xs text-fg-muted">还没有人投票。</p>
          ) : (
            <ul className="flex flex-col gap-1">
              {item.decisions.map((d) => (
                <li key={`${d.approver_id}-${d.created_at}`} className="text-xs">
                  <Badge
                    tone={d.verdict === "APPROVE" ? "success" : "danger"}
                    title={APPROVAL_VERDICTS[d.verdict]?.hint}
                  >
                    {APPROVAL_VERDICTS[d.verdict]?.label ?? d.verdict}
                  </Badge>{" "}
                  {d.approver_id}
                  {d.privileged ? "（特权票）" : ""} · {formatLocalTimestamp(d.created_at)}
                  {d.comment ? ` · ${d.comment}` : ""}
                </li>
              ))}
            </ul>
          )}
        </section>

        {abilities.canVote ? (
          <section className="flex flex-col gap-2">
            <FormField label="意见（可选）">
              <Input
                value={comment}
                onChange={(event) => setComment(event.target.value)}
                placeholder="写给其他审批人和日后翻审计的人看"
              />
            </FormField>
            <div className="flex gap-2">
              <Button
                disabled={busy}
                loading={vote.isPending}
                onClick={() => vote.mutate("APPROVE")}
              >
                同意
              </Button>
              <Button
                variant="ghost"
                disabled={busy}
                loading={vote.isPending}
                onClick={() => vote.mutate("REJECT")}
              >
                驳回
              </Button>
            </div>
            <ActionErrorNote error={vote.error} permission={APPROVAL_DECIDE_PERMISSION} />
          </section>
        ) : abilities.voteBlockedReason ? (
          <p className="text-xs text-fg-muted">{abilities.voteBlockedReason}</p>
        ) : null}

        {abilities.canExecute ? (
          <section className="flex flex-col gap-1">
            <p className="text-xs text-fg-muted">
              执行会按上面冻结的参数跑这个 Action，且只能跑一次——失败也不会回到
              可重跑状态，重跑要重新提交审批。所需权限由该 Action 自己声明，不是
              approval.decide。
            </p>
            <div>
              <Button disabled={busy} loading={run.isPending} onClick={() => run.mutate()}>
                执行
              </Button>
            </div>
            <ActionErrorNote error={run.error} />
          </section>
        ) : null}

        {abilities.canCancel ? (
          <section className="flex flex-col gap-1">
            <div>
              <Button
                variant="ghost"
                disabled={busy}
                loading={withdraw.isPending}
                onClick={() => withdraw.mutate()}
              >
                撤回
              </Button>
            </div>
            <ActionErrorNote error={withdraw.error} />
          </section>
        ) : null}
      </div>
    </Dialog>
  );
}
