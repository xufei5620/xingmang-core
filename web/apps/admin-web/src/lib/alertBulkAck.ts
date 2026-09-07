/** 批量确认的回执口径（XM-ALERTS-GAPS）。
 *
 *  抽成纯函数是因为**回执本身就是这一片的交付物**：批量写必然会部分成功，
 *  而一句「操作成功」会把两条失败说没。只有把「哪几条、为什么」做成可断言
 *  的数据，测试才钉得住它——渲染出来的一句话没法断言它说全了没有。
 *
 *  这里不发任何请求：谁能确认、失败怎么措辞、汇总怎么数，都是可以离线判定
 *  的规则，混进 useMutation 里就只能靠渲染去反推。 */
import { ACKNOWLEDGE_PERMISSION, type AlertItem } from "../api/alerts";
import { ApiError } from "../api/client";
import { canAcknowledge, describeStatus } from "./alerts";

/** 确认成功的一条。
 *
 *  runId 单独留字段：规格 §5.8 要求界面显示 action_run_id，批量不是例外——
 *  N 次写就是 N 个 run_id，回执里少一个就有一次写查不回审计事件。 */
export interface AcknowledgedEntry {
  id: string;
  /** 人认得出的名字（标题；标题为空时退回 id）。 */
  label: string;
  runId: string;
}

/** 没确认成的一条：失败与跳过共用这个形状，区别只在它落进哪个数组。 */
export interface RejectedEntry {
  id: string;
  label: string;
  /** 为什么没成。**必填**，这是回执存在的理由。 */
  reason: string;
}

export interface BulkAckSummary {
  /** 一句话的总账：成功几条、失败几条、跳过几条。 */
  headline: string;
  acknowledged: AcknowledgedEntry[];
  failed: RejectedEntry[];
  skipped: RejectedEntry[];
}

/** 回执里用来指认某一条告警的名字。
 *
 *  标题而不是 id：回执要让人认出「是哪几条没成」，而一串 UUID 认不出来。
 *  标题为空时才退回 id——那时 id 是唯一还能指认它的东西。 */
export function alertLabel(alert: AlertItem): string {
  return alert.title.trim() || alert.id;
}

export interface BulkAckPlan {
  /** 真的会发出 Action 的那些，按表格顺序。 */
  actionable: AlertItem[];
  /** 一次请求都不会发的那些，各自带原因。 */
  skipped: RejectedEntry[];
}

/** 把「选中的键」分成「要发请求的」与「不发也要交代的」。
 *
 *  跳过而不是照发：只有 OPEN / REOPENED 能确认（与后端 WHERE 子句同一条
 *  规则），把一条 RESOLVED 发出去只会换回一个必然的失败，既刷了审计也让
 *  回执里多两条读不懂的错误。但**跳过必须出现在回执里**——悄悄少确认两条
 *  和确认失败两条，对操作的人来说是同一种坏结果。
 *
 *  顺序按 items 而不是按点选顺序：回执的名单要能和表格一行行对上。 */
export function planBulkAcknowledge(
  selectedKeys: readonly string[],
  items: readonly AlertItem[],
): BulkAckPlan {
  const wanted = new Set(selectedKeys);
  const actionable: AlertItem[] = [];
  const skipped: RejectedEntry[] = [];
  const seen = new Set<string>();

  for (const alert of items) {
    if (!wanted.has(alert.id)) continue;
    seen.add(alert.id);
    if (canAcknowledge(alert)) {
      actionable.push(alert);
      continue;
    }
    skipped.push({
      id: alert.id,
      label: alertLabel(alert),
      reason: `当前状态「${describeStatus(alert.status).label}」不可确认，只有未处理与复发可以`,
    });
  }

  // 选中之后列表又刷新过（确认完自动刷新，或自动轮询转走了一条），
  // 而 DataTableV2 的选中集不会跟着 rows 一起清。这些键今天在表里没有对应
  // 的行，**必须报出来**：默默少确认几条正是这一片要修掉的那种沉默。
  for (const id of wanted) {
    if (seen.has(id)) continue;
    skipped.push({
      id,
      label: id,
      reason: "这一条已不在当前列表里，可能刚被解决或被筛掉；刷新后重新选择",
    });
  }

  return { actionable, skipped };
}

/** 把一次确认失败翻成回执里的一行原因。
 *
 *  带上错误码与 request_id 的理由同 ActionErrorNote：报障时这两样能直接对上
 *  服务端日志与审计事件（规格 §5.8、§18.4）。403 额外说清缺哪个权限——
 *  批量场景下「有几条失败了」最常见的成因就是它。 */
export function describeAckFailure(error: unknown): string {
  if (error instanceof ApiError) {
    const parts = [`${error.message}（错误码 ${error.code}）`];
    if (error.status === 403) {
      parts.push(`需要权限 ${error.missingScope ?? ACKNOWLEDGE_PERMISSION}`);
    }
    if (error.requestId) parts.push(`request_id=${error.requestId}`);
    return parts.join("，");
  }
  if (error instanceof Error && error.message) return error.message;
  return "未知错误";
}

/** 汇总一次批量确认的结果。
 *
 *  成功为零时说「0 条确认成功」而不是省略这一段：一条只写着「3 条失败」的
 *  回执，读起来像「大部分成了、个别没成」。 */
export function summarizeBulkAcknowledge(
  acknowledged: readonly AcknowledgedEntry[],
  failed: readonly RejectedEntry[],
  skipped: readonly RejectedEntry[],
): BulkAckSummary {
  const parts = [acknowledged.length > 0 ? `已确认 ${acknowledged.length} 条` : "0 条确认成功"];
  if (failed.length > 0) parts.push(`${failed.length} 条失败`);
  if (skipped.length > 0) parts.push(`${skipped.length} 条跳过`);
  return {
    headline:
      parts.join("，") + (acknowledged.length > 0 ? "；审计事件通常几秒内出现在审计页" : ""),
    acknowledged: [...acknowledged],
    failed: [...failed],
    skipped: [...skipped],
  };
}
