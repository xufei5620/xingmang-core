import type { FreshnessContract } from "@xingmang/ui-admin";
import { apiClient, type ApiClient } from "./client";
import { executeAction, type ActionRun, type ListOptions } from "./platform";

/** 渠道保障 · 检测任务 / 历史记录（主动部分）（XM-ASSURE1-ui）。
 *
 *  与 api/assurance.ts（XM-ASSURE0，被动指标）是两个独立的数据源：那边读
 *  请求审计索引的聚合，零成本、无 Kill Switch；这里读 `assurance` schema
 *  的检测任务声明/批次/结果——主动发真实请求、有真实成本、必须过 Kill
 *  Switch/预算/冷却/并发四道闸（设计稿 §0）。两者共享"渠道保障"页面壳，
 *  互不冒充。
 *
 *  权限复用 `request.read`（查询）与三个独立的 L1 Action 权限（写）：
 *  `assurance.probe.manage`（declare/cancel）、`assurance.probe.run`（run）、
 *  `assurance.probe.kill_switch`（kill_switch.set，刻意与前者分开）。 */

export const PROBE_MANAGE_PERMISSION = "assurance.probe.manage";
export const PROBE_RUN_PERMISSION = "assurance.probe.run";
export const PROBE_KILL_SWITCH_PERMISSION = "assurance.probe.kill_switch";

/** 能调用 kill_switch.set@1 的角色（XM-ASSURE1-core 交接文档 risks #3：
 *  `assurance-probe-admin`，本地登录模式下的角色名）。前端隐藏不构成安全
 *  控制（服务端仍会在没有这个权限点时拒绝），这里只是不对多数人展示一个
 *  几乎注定会 403 的控制。 */
export const PROBE_KILL_SWITCH_ROLE = "assurance-probe-admin";

/** 固定的四个探测模板（设计稿 §1.2.1，代码固化，不接受自由文本）。 */
export type ProbeTemplateKey =
  | "model_fingerprint"
  | "benchmark_set"
  | "context_length"
  | "min_viable_request";

export const PROBE_TEMPLATES: ReadonlyArray<{
  value: ProbeTemplateKey;
  label: string;
  hint: string;
}> = [
  { value: "model_fingerprint", label: "模型指纹", hint: "响应里是否出现声明模型的自我标识特征串" },
  { value: "benchmark_set", label: "基准题集", hint: "固定小题集（≤3 题）算分，并与上一次分数比较是否显著下降" },
  { value: "context_length", label: "上下文长度", hint: "固定长度输入 + 要求复述末尾片段，校验是否命中" },
  { value: "min_viable_request", label: "最小可用请求", hint: "最小 Token 请求，只看是否 200 且非空" },
];

export function isProbeTemplateKey(value: string): value is ProbeTemplateKey {
  return PROBE_TEMPLATES.some((t) => t.value === value);
}

export function probeTemplateLabel(key: string): string {
  return PROBE_TEMPLATES.find((t) => t.value === key)?.label ?? key;
}

/** max_tokens 的默认值/硬顶（设计稿 §1.2：1..512，declare@1 必填无 Schema
 *  默认值——64 只是本文件与对话框里推荐的典型值，不是后端会自动回落的值,
 *  见 XM-ASSURE1-core 交接文档偏离 #7）。 */
export const PROBE_MAX_TOKENS_DEFAULT = 64;
export const PROBE_MAX_TOKENS_CAP = 512;

/** expected_shape 的通用默认值：只做形状/长度/超时层面的浅层断言（设计稿
 *  §1.2.3 的原文示例），不按模板编造不同的"及格线"——具体的断言内容（如
 *  基准题的判分细节）由后端 templates.go 固化，本对话框不重新发明一套。 */
export const PROBE_DEFAULT_EXPECTED_SHAPE = {
  min_length: 1,
  max_length: 2000,
  must_contain: [] as string[],
  timeout_ms: 30000,
};

export interface ProbeTarget {
  channelId: string;
  externalChannelId?: string;
  model: string;
}

// --- 检测任务表（GET .../assurance/probes） ---

export interface ProbeListItem {
  declarationId: string;
  name: string;
  channelIds: string[];
  channelNames: string[];
  targetModels: string[];
  policyText: string;
  scheduleCron: string;
  lastRunAt: string | null;
  lastRunStatus: string;
  lastRunVerdict: string | null;
  /** "enabled" | "disabled" | "not_applicable_fake"。 */
  killSwitchState: string;
  canRunNow: boolean;
  /** 稳定枚举串，仅用于分支判断；tooltip 显示 cannotRunReasonText。 */
  cannotRunReason: string;
  cannotRunReasonText: string;
}

export interface ProbeListResult {
  platform: string;
  probes: ProbeListItem[];
  freshness: FreshnessContract;
  assertionDisclaimer: string;
  channelBreakdownSupported: boolean;
}

interface RawProbeListItem {
  declaration_id: string;
  name: string;
  channel_ids?: string[] | null;
  channel_names?: string[] | null;
  target_models?: string[] | null;
  policy_text: string;
  schedule_cron?: string;
  last_run_at: string | null;
  last_run_status: string;
  last_run_verdict: string | null;
  kill_switch_state: string;
  can_run_now: boolean;
  cannot_run_reason?: string;
  cannot_run_reason_text?: string;
}

interface RawProbeListBody {
  platform: string;
  probes: RawProbeListItem[] | null;
  freshness: FreshnessContract;
  assertion_disclaimer: string;
  channel_breakdown_supported: boolean;
}

function parseProbeListItem(raw: RawProbeListItem): ProbeListItem {
  return {
    declarationId: raw.declaration_id,
    name: raw.name,
    channelIds: raw.channel_ids ?? [],
    channelNames: raw.channel_names ?? [],
    targetModels: raw.target_models ?? [],
    policyText: raw.policy_text,
    scheduleCron: raw.schedule_cron ?? "",
    lastRunAt: raw.last_run_at,
    lastRunStatus: raw.last_run_status,
    lastRunVerdict: raw.last_run_verdict,
    killSwitchState: raw.kill_switch_state,
    canRunNow: raw.can_run_now,
    cannotRunReason: raw.cannot_run_reason ?? "",
    cannotRunReasonText: raw.cannot_run_reason_text ?? "",
  };
}

/** 读取某平台的检测任务表（需 request.read，与被动指标同一个 scope——
 *  设计稿 §5.1：敏感度同级，都不含 Prompt 具体输出）。 */
export async function getPlatformAssuranceProbes(
  platform: string,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ProbeListResult> {
  const body = await client.get<RawProbeListBody>(
    `/api/v1/platforms/${encodeURIComponent(platform)}/assurance/probes`,
    { ...(options.signal ? { signal: options.signal } : {}) },
  );
  return {
    platform: body.platform,
    probes: (body.probes ?? []).map(parseProbeListItem),
    freshness: body.freshness,
    assertionDisclaimer: body.assertion_disclaimer,
    channelBreakdownSupported: body.channel_breakdown_supported,
  };
}

// --- 检测历史（GET .../assurance/probe-history，主动部分） ---

export interface ProbeHistoryEntry {
  observedAt: string;
  channelId: string;
  externalChannelId: string;
  model: string;
  declarationName: string;
  promptTemplateKey: string;
  status: string;
  verdict: string;
  evidenceRef: string;
  latencyMs: number | null;
  firstTokenMs: number | null;
  measuredFirstToken: boolean;
  tokensUsed: number | null;
  httpStatus: number | null;
  errorKind: string;
}

export interface ProbeHistoryResult {
  platform: string;
  entries: ProbeHistoryEntry[];
  /** null 表示已经翻到底。不透明字符串（RFC3339 游标），不解析、不构造。 */
  nextCursor: string | null;
  freshness: FreshnessContract;
  channelBreakdownSupported: boolean;
}

interface RawProbeHistoryEntry {
  observed_at: string;
  channel_id: string;
  external_channel_id?: string;
  model: string;
  declaration_name: string;
  prompt_template_key: string;
  status: string;
  verdict: string;
  evidence_ref: string;
  latency_ms: number | null;
  first_token_ms: number | null;
  measured_first_token: boolean;
  tokens_used: number | null;
  http_status: number | null;
  error_kind?: string;
}

interface RawProbeHistoryBody {
  platform: string;
  entries: RawProbeHistoryEntry[] | null;
  next_cursor: string | null;
  freshness: FreshnessContract;
  channel_breakdown_supported: boolean;
}

function parseProbeHistoryEntry(raw: RawProbeHistoryEntry): ProbeHistoryEntry {
  return {
    observedAt: raw.observed_at,
    channelId: raw.channel_id,
    externalChannelId: raw.external_channel_id ?? "",
    model: raw.model,
    declarationName: raw.declaration_name,
    promptTemplateKey: raw.prompt_template_key,
    status: raw.status,
    verdict: raw.verdict,
    evidenceRef: raw.evidence_ref,
    latencyMs: raw.latency_ms,
    firstTokenMs: raw.first_token_ms,
    measuredFirstToken: raw.measured_first_token,
    tokensUsed: raw.tokens_used,
    httpStatus: raw.http_status,
    errorKind: raw.error_kind ?? "",
  };
}

export interface ProbeHistoryOptions extends ListOptions {
  cursor?: string;
  limit?: number;
}

/** 按时间倒序分页读取某平台的主动探测历史（需 request.read）。
 *
 *  与 getPlatformAssuranceHistory（api/assurance.ts，被动近 7 天聚合）是
 *  完全独立的端点——两张卡片各自请求、各自渲染，不合并成一次请求（设计稿
 *  §5.2），前端不应把两者的查询键混在一起。 */
export async function getPlatformAssuranceProbeHistory(
  platform: string,
  options: ProbeHistoryOptions = {},
  client: ApiClient = apiClient,
): Promise<ProbeHistoryResult> {
  const body = await client.get<RawProbeHistoryBody>(
    `/api/v1/platforms/${encodeURIComponent(platform)}/assurance/probe-history`,
    {
      searchParams: {
        cursor: options.cursor,
        limit: options.limit ? String(options.limit) : undefined,
      },
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
  return {
    platform: body.platform,
    entries: (body.entries ?? []).map(parseProbeHistoryEntry),
    nextCursor: body.next_cursor,
    freshness: body.freshness,
    channelBreakdownSupported: body.channel_breakdown_supported,
  };
}

// --- 写路径：四个 L1 Action ---

export interface DeclareProbeInput {
  platform: string;
  name: string;
  promptTemplateKey: ProbeTemplateKey;
  targetHost: string;
  targets: ProbeTarget[];
  maxTokens: number;
  /** 提供则为更新既有声明；否则新建。 */
  declarationId?: string;
  /** 更新时必填，与 declarationId 同时提供或同时省略。 */
  expectedVersion?: number;
}

function encodeTargets(targets: ProbeTarget[]): string {
  return JSON.stringify(
    targets.map((t) => ({
      channel_id: t.channelId,
      external_channel_id: t.externalChannelId ?? "",
      model: t.model,
    })),
  );
}

/** 声明（新建/更新）一条检测任务（assurance.probe.declare@1，
 *  Permission = assurance.probe.manage）。
 *
 *  targets/expected_shape 按契约编码成 JSON 字符串参数（Schema 目前只有
 *  string/int/bool/string_slice 四种字段类型，见契约文件 notes）；
 *  expected_shape 本对话框统一用 PROBE_DEFAULT_EXPECTED_SHAPE，不按模板
 *  编造不同的判分细节。 */
export function declareProbe(
  input: DeclareProbeInput,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  const params: Record<string, unknown> = {
    platform: input.platform,
    name: input.name,
    prompt_template_key: input.promptTemplateKey,
    target_host: input.targetHost,
    targets: encodeTargets(input.targets),
    max_tokens: input.maxTokens,
    expected_shape: JSON.stringify(PROBE_DEFAULT_EXPECTED_SHAPE),
  };
  if (input.declarationId) {
    params.declaration_id = input.declarationId;
    params.expected_version = input.expectedVersion;
  }
  return executeAction({ actionId: "assurance.probe.declare", version: "1", params }, options, client);
}

export interface CancelProbeInput {
  declarationId: string;
  reason: string;
}

/** 撤销一条检测任务声明（assurance.probe.cancel@1）。 */
export function cancelProbe(
  input: CancelProbeInput,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    {
      actionId: "assurance.probe.cancel",
      version: "1",
      params: { declaration_id: input.declarationId, reason: input.reason },
    },
    options,
    client,
  );
}

export interface RunProbeInput {
  declarationId: string;
  /** 防连点去重；不传则每次都是新批次。 */
  clientRunKey?: string;
}

/** 触发一次检测批次（assurance.probe.run@1）。
 *
 *  Handler 可能判定"拒绝执行"并原样返回（不是 Action 层错误）——返回值里
 *  的 result 形如 `{run_id, status:"refused"|"pending", refusal_reason?}`，
 *  调用方（对话框/表格行）需要读 `run.result` 而不是只看有没有抛错，才能
 *  区分"提交成功但被拒绝"与"提交失败"。 */
export function runProbe(
  input: RunProbeInput,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  const params: Record<string, unknown> = { declaration_id: input.declarationId };
  if (input.clientRunKey) params.client_run_key = input.clientRunKey;
  return executeAction({ actionId: "assurance.probe.run", version: "1", params }, options, client);
}

/** run@1 返回值里可能带的执行结果形状——只在调用方需要读 refused/pending
 *  区分时使用，其余场景把 ActionRun.result 当成不透明值处理即可。 */
export interface RunProbeResult {
  run_id?: string;
  status?: string;
  refusal_reason?: string;
}

export interface SetProbeKillSwitchInput {
  platform: string;
  probeEnabled: boolean;
  /** enabled=true 时必填（首次开启）；省略保留原值，显式空串清空。 */
  probeCredentialRef?: string;
}

/** 平台级探测 Kill Switch（assurance.probe.kill_switch.set@1，
 *  Permission = assurance.probe.kill_switch，与 connector.config.set@1 刻意
 *  分开授权——见契约文件 notes）。 */
export function setProbeKillSwitch(
  input: SetProbeKillSwitchInput,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  const params: Record<string, unknown> = {
    platform: input.platform,
    probe_enabled: input.probeEnabled,
  };
  if (input.probeCredentialRef !== undefined) params.probe_credential_ref = input.probeCredentialRef;
  return executeAction(
    { actionId: "assurance.probe.kill_switch.set", version: "1", params },
    options,
    client,
  );
}
