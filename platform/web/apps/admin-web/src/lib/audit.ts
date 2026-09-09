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

// --- 相邻行的链关系 ---

/** 本页两条相邻可见事件之间的关系。
 *
 *  这一层存在的原因是一个真实的误报（Codex #6）：后端的哈希链是**全局**的，
 *  而本页按 environment 过滤，于是 6 / 4 / 2 这样的序号缺口是完全正常的。
 *  在有缺口的两行之间比 prev_hash 与 event_hash，链再正确也必然不相等——
 *  「肉眼验链」于是会在一条健康的链上稳定地喊断链。
 *
 *  所以：**只有序号真的相邻（差 1）时才比对**，其余情形老老实实说
 *  「这里看不到上一条」，而不是把一个比不了的比较结果渲染成结论。 */
export type ChainLink =
  | { kind: "genesis" }
  /** 序号连续，可以就地比对。 */
  | { kind: "adjacent"; matches: boolean }
  /** 中间隔着 hidden 条本页看不到的事件（其他环境）。 */
  | { kind: "gap"; hidden: number }
  /** 本页没有序号相邻的上一条（翻到页尾，或数据乱序）。 */
  | { kind: "unknown" };

/** 判断某行与它下面那一行（列表倒序，所以是序号更小的那条）的链关系。 */
export function chainLinkBetween(
  row: Pick<AuditEventItem, "sequence" | "prev_hash">,
  older: Pick<AuditEventItem, "sequence" | "event_hash"> | undefined,
): ChainLink {
  if (row.prev_hash === GENESIS_HASH) return { kind: "genesis" };
  if (older === undefined) return { kind: "unknown" };
  const step = row.sequence - older.sequence;
  if (step === 1) return { kind: "adjacent", matches: row.prev_hash === older.event_hash };
  if (step > 1) return { kind: "gap", hidden: step - 1 };
  // step <= 0：序号没有变小，说明数据不是按倒序来的。这时候任何比对结论都不可信
  return { kind: "unknown" };
}

export interface ChainLinkDisplay {
  label: string;
  detail: string;
  /** neutral 表示「这不是异常信号」——缺口不该被染成红色。 */
  tone: "neutral" | "success" | "danger";
}

export function describeChainLink(link: ChainLink): ChainLinkDisplay {
  switch (link.kind) {
    case "genesis":
      return { label: "链首", detail: "链首事件，prev_hash 为全 0", tone: "neutral" };
    case "adjacent":
      return link.matches
        ? {
            label: "相连",
            detail: "序号连续，且本行 prev_hash 等于下一行的事件哈希",
            tone: "success",
          }
        : {
            label: "与相邻行对不上",
            detail:
              "序号连续，但本行 prev_hash 与下一行的事件哈希不同；请用 audit-verify 复核整条链",
            tone: "danger",
          };
    case "gap":
      return {
        label: `中间有 ${link.hidden} 条其他环境事件`,
        detail:
          "哈希链是全局的，本页按环境过滤后出现序号缺口属正常；相邻两行的哈希本就不必相等",
        tone: "neutral",
      };
    case "unknown":
      return {
        label: "上一条不在本页",
        detail: "本页没有序号相邻的上一条事件，无法在这里比对",
        tone: "neutral",
      };
  }
}

/** 摘要 → 展开区里显示的 JSON 文本。
 *
 *  null 与空对象要分开说：新建资源没有前态（null），
 *  和「前态是个空对象」在审计上不是一回事（registry/actions.go 里也是这么记的）。 */
export function formatSummary(summary: Record<string, unknown> | null | undefined): string {
  if (summary === null || summary === undefined) return "（无）";
  return JSON.stringify(summary, null, 2);
}
