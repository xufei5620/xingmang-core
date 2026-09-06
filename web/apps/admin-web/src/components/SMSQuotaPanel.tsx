import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { DataTableV2, formatUtcTimestamp, type DataTableColumn } from "@xingmang/ui-admin";
import { Badge, Button, FormField, Input } from "@xingmang/ui-primitives";
import { useId, useState } from "react";
import { listSMSQuotas, setSMSQuota, type SMSQuota } from "../api/sms";
import { ActionErrorNote } from "./ActionErrorNote";
import { ApiStateView } from "./ApiStateView";
import type { ActionResult } from "./ActionResultNote";

const QUOTAS_QUERY = "sms-quotas";

/** 接入配额（XM-SMS4 #3）。
 *
 *  机器身份要号是花真钱，而且没有人在页面前面点确认：一个循环里的 bug 能在十
 *  分钟内买光余额。所以机器**必须先在这里登记**——没有配额行就一次都调不动，
 *  与「新装环境两家默认都是关的」同一条纪律。人不受这套约束。 */
export function SMSQuotaPanel({ onWrite }: { onWrite: (r: ActionResult) => void }) {
  const queryClient = useQueryClient();
  const query = useQuery({ queryKey: [QUOTAS_QUERY], queryFn: () => listSMSQuotas() });
  const quotas = query.data ?? [];

  const [consumer, setConsumer] = useState("");
  const [dailyRequests, setDailyRequests] = useState("100");
  const [spendCap, setSpendCap] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [error, setError] = useState<unknown>(null);
  const formId = useId();

  const save = useMutation({
    mutationFn: () =>
      setSMSQuota({
        consumer: consumer.trim(),
        daily_requests: Number(dailyRequests) || 0,
        ...(spendCap.trim() ? { daily_spend_cap: spendCap.trim() } : {}),
        enabled,
      }),
    onSuccess: (run) => {
      onWrite({ runId: run.runId, title: `已登记 ${consumer.trim()} 的配额` });
      setError(null);
      void queryClient.invalidateQueries({ queryKey: [QUOTAS_QUERY] });
    },
    onError: setError,
  });

  function edit(q: SMSQuota) {
    setConsumer(q.consumer);
    setDailyRequests(String(q.daily_requests));
    setSpendCap(q.daily_spend_cap ?? "");
    setEnabled(q.enabled);
    setError(null);
  }

  const columns: DataTableColumn<SMSQuota>[] = [
    { id: "consumer", header: "消费者", primary: true, cell: (r) => r.consumer, value: (r) => r.consumer },
    {
      id: "enabled",
      header: "状态",
      cell: (r) => <Badge tone={r.enabled ? "success" : "neutral"}>{r.enabled ? "已启用" : "已停用"}</Badge>,
      value: (r) => (r.enabled ? "已启用" : "已停用"),
    },
    {
      id: "requests",
      header: "今日号数",
      numeric: true,
      cell: (r) => `${r.used_numbers} / ${r.daily_requests}`,
      value: (r) => r.used_numbers,
    },
    {
      id: "spend",
      header: "今日花费",
      cell: (r) =>
        r.used_spend?.length
          ? r.used_spend.map((s) => `${s.amount}${s.currency ? ` (${s.currency})` : ""}`).join(" · ")
          : "—",
    },
    { id: "cap", header: "花费上限", numeric: true, cell: (r) => r.daily_spend_cap || "不限" },
    { id: "updated", header: "更新时间", cell: (r) => (r.updated_at ? formatUtcTimestamp(r.updated_at) : "—") },
    {
      id: "ops",
      header: "操作",
      cell: (r) => (
        <Button variant="secondary" size="sm" onClick={() => edit(r)}>
          编辑
        </Button>
      ),
    },
  ];

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <section className="border-edge flex min-w-0 flex-col gap-3 rounded-md border p-3">
        <div>
          <h3 className="text-sm font-semibold">登记消费者</h3>
          <p className="text-fg-muted text-xs">
            机器身份（SERVICE）要号<strong>必须先登记</strong>：没有这里的行就一次都调不动。
            人不受配额约束。接入方式见 <code>docs/modules/sms/INTEGRATION.md</code>。
          </p>
        </div>
        <div className="grid gap-3 md:grid-cols-3">
          <FormField label="消费者" htmlFor={`${formId}-consumer`} hint="principal ID，与审计里那一列同源。">
            <Input
              id={`${formId}-consumer`}
              aria-label="消费者"
              value={consumer}
              onChange={(e) => setConsumer(e.target.value)}
            />
          </FormField>
          <FormField label="日号数" htmlFor={`${formId}-requests`} hint="按号数算，不是调用次数。0 = 一次都不许。">
            <Input
              id={`${formId}-requests`}
              aria-label="日号数"
              inputMode="numeric"
              value={dailyRequests}
              onChange={(e) => setDailyRequests(e.target.value)}
            />
          </FormField>
          <FormField
            label="日花费上限"
            htmlFor={`${formId}-cap`}
            hint="止损线：到线之后不再放行，最多超出一次请求。留空 = 不限。"
          >
            <Input
              id={`${formId}-cap`}
              aria-label="日花费上限"
              inputMode="decimal"
              value={spendCap}
              onChange={(e) => setSpendCap(e.target.value)}
            />
          </FormField>
        </div>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          启用（停用后这个消费者立刻调不动）
        </label>
        {error ? <ActionErrorNote error={error} /> : null}
        <div className="flex items-center gap-2">
          <Button size="sm" disabled={!consumer.trim() || save.isPending} onClick={() => save.mutate()}>
            {save.isPending ? "保存中…" : "保存配额"}
          </Button>
        </div>
      </section>

      <ApiStateView isPending={query.isPending} error={query.error} onRetry={() => void query.refetch()}>
        <DataTableV2
          caption="已登记的消费者"
          columns={columns}
          rows={quotas}
          rowKey={(r) => r.consumer}
          emptyState={
            <p className="text-fg-muted text-sm">
              还没有登记任何消费者。没有登记的机器身份调用会被拒绝，这是刻意的默认。
            </p>
          }
        />
      </ApiStateView>
    </div>
  );
}
