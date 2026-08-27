import { formatLocalTimestamp, formatUtcTimestamp } from "@xingmang/ui-admin";
import type { BadgeTone } from "@xingmang/ui-primitives";
import type { AuditEventItem } from "../api/platform";

/** 哈希在表格里显示的前缀长度。
 *
 *  8 位十六进制（32 bit）足够肉眼比对相邻两行是否接得上，也短到不撑破表格；
 *  完整 64 位放进 title，要拿去比对时随时能取到——**不能只显示前 8 位而丢掉全量**，
 *  那样审计链就成了一个没法验证的装饰（规格 §4.4）。 */
export const HASH_PREFIX_LENGTH = 8;

/** 链首事件的 prev_hash（audit.GenesisHash：64 个 0）。 */
export const GENESIS_HASH = "0".repeat(64);

const NO_VALUE = "—";

export interface AuditRow {
  sequence: number;
  /** 表格里显示的本地时间（带时区后缀）。 */
  localTime: string;
  /** 悬停显示的权威 UTC 时间（宪法 14 条：时区必须显式）。 */
  utcTime: string;
  principalId: string;
  principalType: string;
  /** `action_id@version`——版本是动作契约的一部分，不能省。 */
  action: string;
  /** `resource_type/resource_id`；两者都没有时为「—」。 */
  resource: string;
  resultLabel: string;
  resultTone: BadgeTone;
  /** 失败时的错误码；成功时为空串。 */
  errorCode: string;
  environment: string;
  runId: string;
  requestId: string;
  hashShort: string;
  hashFull: string;
  prevHashShort: string;
  prevHashFull: string;
  /** prev_hash 是不是链首（全 0）。链首没有「上一条」，不该显示成断链。 */
  isGenesis: boolean;
  /** 有没有前/后态摘要可展开。没有就不给展开按钮，免得点开一片空白。 */
  hasDetail: boolean;
}

interface ResultDisplay {
  label: string;
  tone: BadgeTone;
}

/** 结果 → 文案与语气。
 *
 *  未知值按 warning 暴露而不是静默当成成功：后端将来新增结果类型时，
 *  宁可让人看到「未知结果」来查，也不能把一次失败渲染成绿色的成功。 */
export function describeAuditResult(result: string): ResultDisplay {
  if (result === "succeeded") return { label: "成功", tone: "success" };
  if (result === "failed") return { label: "失败", tone: "danger" };
  return { label: `未知（${result || "空"}）`, tone: "warning" };
}

function shortHash(hash: string): string {
  if (!hash) return NO_VALUE;
  return hash.slice(0, HASH_PREFIX_LENGTH);
}

function hasSummary(summary: Record<string, unknown> | null | undefined): boolean {
  return summary !== null && summary !== undefined && Object.keys(summary).length > 0;
}

/** 审计事件 → 表格行。
 *
 *  单独抽成纯函数：这层映射决定了「一次动作在页面上长什么样」，
 *  比 JSX 更值得被断言，而断言它不需要渲染整张表。 */
export function toAuditRow(event: AuditEventItem): AuditRow {
  const result = describeAuditResult(event.result);
  const resource = [event.resource_type, event.resource_id].filter(Boolean).join("/");
  return {
    sequence: event.sequence,
    localTime: formatLocalTimestamp(event.occurred_at),
    utcTime: formatUtcTimestamp(event.occurred_at),
    principalId: event.principal_id || NO_VALUE,
    principalType: event.principal_type || "",
    action: event.action_version ? `${event.action_id}@${event.action_version}` : event.action_id,
    resource: resource || NO_VALUE,
    resultLabel: result.label,
    resultTone: result.tone,
    // 成功事件不显示错误码：后端可能带一个空串，渲染出来会变成一列空白噪音
    errorCode: event.result === "succeeded" ? "" : (event.error_code ?? ""),
    environment: event.environment || "",
    runId: event.action_run_id || "",
    requestId: event.request_id || "",
    hashShort: shortHash(event.event_hash),
    hashFull: event.event_hash || "",
    prevHashShort: shortHash(event.prev_hash),
    prevHashFull: event.prev_hash || "",
    isGenesis: event.prev_hash === GENESIS_HASH,
    hasDetail: hasSummary(event.before_summary) || hasSummary(event.after_summary),
  };
}

/** 摘要 → 展开区里显示的 JSON 文本。
 *
 *  null 与空对象要分开说：新建资源没有前态（null），
 *  和「前态是个空对象」在审计上不是一回事（registry/actions.go 里也是这么记的）。 */
export function formatSummary(summary: Record<string, unknown> | null | undefined): string {
  if (summary === null || summary === undefined) return "（无）";
  return JSON.stringify(summary, null, 2);
}
