/** 告警中心的 API 层（XM-0033，规格 §9.3 / §9.4）。
 *
 *  单独一个模块而不是继续堆进 platform.ts：告警有自己的一组类型、
 *  一组状态语义和两个写路径，混进那个已经 280 行的文件只会让两边都更难读。
 *  写路径仍然复用 platform.ts 的 executeAction——写操作唯一入口是 Action
 *  内核（宪法 2 条），前端这边也只该有一条通道。 */
import { apiClient, type ApiClient } from "./client";
import { appApiConfig, type PlatformApiConfig } from "./config";
import { executeAction, type ActionRun, type ListOptions } from "./platform";

/** 告警状态，五个值逐字对齐规格 §9.3（Go 侧 alerts.Status）。 */
export type AlertStatus = "OPEN" | "ACKNOWLEDGED" | "SILENCED" | "RESOLVED" | "REOPENED";

/** 严重度（Go 侧 alerts.Severity）。 */
export type AlertSeverity = "info" | "warning" | "critical";

/** 投递状态（Go 侧 alerts.NotifyStatus）。
 *
 *  它与 status **正交**：一条 OPEN 的告警完全可能还没投递出去，
 *  而那正是最危险的组合——有人以为「告警会通知我」。界面必须分开显示。 */
export type AlertNotifyStatus = "pending" | "delivered" | "failed";

/** `GET /api/v1/alerts` 的一条记录（httpapi/alerts.go alertItem）。 */
export interface AlertItem {
  id: string;
  rule_key: string;
  dedup_key: string;
  severity: AlertSeverity;
  status: AlertStatus;
  title: string;
  detail: string;
  environment: string;
  /** 空串表示这条告警与具体指标无关（渠道类规则也会填所依据的聚合指标）。 */
  source_metric_key: string;
  opened_at: string;
  last_seen_at: string;
  /** null 表示还没人确认 / 还没解决——不要用零值时间冒充（规格 §9.1 同一条纪律）。 */
  acknowledged_at: string | null;
  resolved_at: string | null;
  /** 被去重合并掉的命中次数（含首次），最小为 1。 */
  fire_count: number;
  notify_status: AlertNotifyStatus;
  notify_error: string;
  notified_at: string | null;
}

interface ListResponse<T> {
  items: T[] | null;
}

/** 「活跃」的四个状态，与后端 alerts.ActiveStatusStrings 一致。
 *
 *  SILENCED 算活跃是关键：静默不是终态，窗口过期后它会转回 OPEN。
 *  把它从这份清单里去掉，界面上就会出现「静默期间告警凭空消失、
 *  窗口一过又凭空出现」。 */
export const ACTIVE_ALERT_STATUSES: AlertStatus[] = [
  "OPEN",
  "ACKNOWLEDGED",
  "SILENCED",
  "REOPENED",
];

/** status 查询参数的特殊值：返回**含已解决**的最近告警。 */
export const ALERT_STATUS_ALL = "all";

export interface ListAlertsOptions extends ListOptions {
  /** 不传 = 全部活跃告警（页面默认）。传 ALERT_STATUS_ALL 则含已解决。 */
  status?: AlertStatus[] | typeof ALERT_STATUS_ALL;
  limit?: number;
}

function statusParam(status: ListAlertsOptions["status"]): string | undefined {
  if (status === undefined) return undefined;
  if (status === ALERT_STATUS_ALL) return ALERT_STATUS_ALL;
  return status.length > 0 ? status.join(",") : undefined;
}

/** 列出某环境下的告警。
 *
 *  environment 由 client 侧的配置决定（不传则用调用者身份自己的环境）——
 *  跨环境读取在后端是硬拒绝，前端不该假装能选。 */
export async function listAlerts(
  options: ListAlertsOptions = {},
  client: ApiClient = apiClient,
  config: PlatformApiConfig = appApiConfig,
): Promise<AlertItem[]> {
  const body = await client.get<ListResponse<AlertItem>>("/api/v1/alerts", {
    searchParams: {
      environment: config.environment,
      status: statusParam(options.status),
      ...(options.limit === undefined ? {} : { limit: String(options.limit) }),
    },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return body.items ?? [];
}

/** alerts.alert.acknowledge@1 声明的 Permission（alerts/permissions.go）。 */
export const ACKNOWLEDGE_PERMISSION = "alerts.alert.manage";

/** alerts.silence.create@1 声明的 Permission。
 *
 *  与确认**分开**授予是有意的：确认只是说「我看见了」，告警仍留在列表里；
 *  静默是让它不再出现也不再投递。两件事的爆炸半径差一个量级。 */
export const SILENCE_PERMISSION = "alerts.silence.manage";

/** 确认一条告警（alerts.alert.acknowledge@1，L0）。 */
export function acknowledgeAlert(
  alertId: string,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    { actionId: "alerts.alert.acknowledge", version: "1", params: { alert_id: alertId } },
    options,
    client,
  );
}

export interface CreateSilenceParams {
  /** 空串 = 全局静默（该环境下所有规则）。这是一个明确的选择，不是漏填。 */
  rule_key: string;
  duration_minutes: number;
  reason: string;
}

/** 创建静默窗口（alerts.silence.create@1，L1）。 */
export function createSilence(
  params: CreateSilenceParams,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    {
      actionId: "alerts.silence.create",
      version: "1",
      // 逐字段展开而不是直接传 params：后端 Schema 是白名单语义，
      // 多一个字段就是 400。展开让「多传了什么」在这里就看得见。
      params: {
        rule_key: params.rule_key,
        duration_minutes: params.duration_minutes,
        reason: params.reason,
      },
    },
    options,
    client,
  );
}

/** 第一批规则的键与中文名（Go 侧 alerts/rules.go 的 RuleKeys）。
 *
 *  前端重复这份清单，是为了让静默对话框的下拉框有可选项——后端没有
 *  「列出规则」的端点（Foundation-A 不值得为一个静态清单开一条 API）。
 *  重复的代价由 alerts.test.ts 里那条断言兜住：它对着后端错误文案里
 *  列出的键做形态校验，键名改了会在集成测试里显形。
 *
 *  拼错的 rule_key 会被后端当场拒绝（它会静默零条告警，而创建者以为
 *  已经静默了），所以这里给的是下拉而不是自由输入。 */
export const ALERT_RULES: { key: string; label: string }[] = [
  { key: "metric.sync.failed", label: "指标同步失败" },
  { key: "metric.data.stale", label: "指标数据陈旧" },
  { key: "metric.sync.consecutive_failed", label: "同步连续失败" },
  { key: "channel.token.invalid", label: "渠道 token 失效" },
  { key: "channel.balance.low", label: "渠道余额不足" },
];

/** 把规则键翻成中文名；未知键原样返回。
 *
 *  未知键**不隐藏也不报错**：后端加了新规则而前端还没跟上时，
 *  列表里显示原始键仍然是有用的信息，显示成「未知规则」则是在丢事实。 */
export function ruleLabel(ruleKey: string): string {
  return ALERT_RULES.find((r) => r.key === ruleKey)?.label ?? ruleKey;
}
