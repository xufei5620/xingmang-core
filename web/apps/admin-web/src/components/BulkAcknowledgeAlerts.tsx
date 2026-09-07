import { Button } from "@xingmang/ui-primitives";
import { useState } from "react";
import { acknowledgeAlert, type AlertItem } from "../api/alerts";
import {
  alertLabel,
  describeAckFailure,
  planBulkAcknowledge,
  summarizeBulkAcknowledge,
  type AcknowledgedEntry,
  type BulkAckSummary,
  type RejectedEntry,
} from "../lib/alertBulkAck";

export interface BulkAcknowledgeAlertsProps {
  /** DataTableV2 选中的行键（= alert.id）。 */
  selectedKeys: readonly string[];
  /** 当前表里的告警，用来把键翻回对象并判断谁能确认。 */
  items: AlertItem[];
  onCompleted: (summary: BulkAckSummary) => void;
}

/** 批量确认（alerts.alert.acknowledge@1）。
 *
 *  **这里曾经写着「批量确认随 Foundation-B（XM-0030）上线」，那句话从写下的
 *  那天起就是错的。** 确认动作声明的是 `RiskLevel: action.L0`
 *  （internal/platform/alerts/actions.go），而 L0 根本不进审批通道——单条确认
 *  一直可用就是证据。挡住批量的从来不是审批，只是没人把循环写出来。
 *
 *  仍然只走 Action：批量不是一条新的写通道，是同一个 Action 调 N 次
 *  （宪法 2 条）。所以没有「批量确认」端点，也不该有。 */
export function BulkAcknowledgeAlerts({
  selectedKeys,
  items,
  onCompleted,
}: BulkAcknowledgeAlertsProps) {
  const [running, setRunning] = useState(false);
  const plan = planBulkAcknowledge(selectedKeys, items);

  const execute = async () => {
    setRunning(true);
    const acknowledged: AcknowledgedEntry[] = [];
    const failed: RejectedEntry[] = [];
    // 串行而不是 Promise.all：一次点击并发 N 个 POST，会把内核与审计写成一个
    // 尖峰，而这一页的批量规模是几条到几十条，串行慢的那点没人察觉。
    // 顺序也让回执的名单和表格一行行对得上。
    for (const alert of plan.actionable) {
      try {
        const outcome = await acknowledgeAlert(alert.id);
        acknowledged.push({ id: alert.id, label: alertLabel(alert), runId: outcome.runId });
      } catch (error) {
        // 一条失败**不中断**后面的：中途停下会留下一个「谁确认了、谁没有」
        // 都说不清的半截状态，而这正是批量操作最难善后的形态。
        failed.push({ id: alert.id, label: alertLabel(alert), reason: describeAckFailure(error) });
      }
    }
    setRunning(false);
    onCompleted(summarizeBulkAcknowledge(acknowledged, failed, plan.skipped));
  };

  return (
    <>
      <Button
        size="sm"
        variant="secondary"
        loading={running}
        disabled={plan.actionable.length === 0}
        onClick={() => void execute()}
      >
        批量确认 {plan.actionable.length} 条
      </Button>
      {/* 会跳过几条要在点之前就说，不能等回执：人是按「我选了 9 条」来判断
          这一步做没做完的，回执里才第一次出现「其实只发了 7 条」已经晚了。 */}
      {plan.skipped.length > 0 ? (
        <span className="text-fg-muted">
          {plan.actionable.length === 0
            ? `选中的 ${plan.skipped.length} 条都不可确认（只有未处理与复发可以）`
            : `另有 ${plan.skipped.length} 条不可确认，将跳过`}
        </span>
      ) : null}
    </>
  );
}

/** 批量确认的回执。
 *
 *  **不说「操作成功」**：批量写必然部分成功，一句笼统的成功会把失败说没。
 *  三段分开列——成功的带 run_id（规格 §5.8），失败与跳过的逐条写清是哪条、
 *  为什么。名单放在可滚动的框里，几十条也不会把表格顶到屏幕外。 */
export function BulkAckReceipt({ summary }: { summary: BulkAckSummary }) {
  const clean = summary.failed.length === 0 && summary.skipped.length === 0;
  return (
    <div
      role="status"
      className={`mb-3 rounded-md border px-3 py-2 text-xs ${
        clean ? "border-success bg-success/10 text-success" : "border-warning bg-warning/10 text-fg"
      }`}
    >
      <p className="font-medium">{summary.headline}</p>

      {summary.failed.length > 0 ? (
        <div className="mt-2">
          <p className="font-medium text-danger">确认失败 {summary.failed.length} 条</p>
          <ul className="mt-1 max-h-40 list-disc space-y-1 overflow-y-auto pl-4">
            {summary.failed.map((entry) => (
              <li key={entry.id} className="break-all text-danger">
                {entry.label}：{entry.reason}
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      {summary.skipped.length > 0 ? (
        <div className="mt-2">
          <p className="font-medium">跳过 {summary.skipped.length} 条（没有发出请求）</p>
          <ul className="mt-1 max-h-40 list-disc space-y-1 overflow-y-auto pl-4">
            {summary.skipped.map((entry) => (
              <li key={entry.id} className="break-all text-fg-muted">
                {entry.label}：{entry.reason}
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      {summary.acknowledged.length > 0 ? (
        <div className="mt-2">
          <p className="font-medium">已确认 {summary.acknowledged.length} 条，各自的 run_id：</p>
          <ul className="mt-1 max-h-40 space-y-1 overflow-y-auto">
            {summary.acknowledged.map((entry) => (
              <li key={entry.id} className="break-all">
                {entry.label}：run_id={entry.runId}
              </li>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  );
}
