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
  /** **评估轮数，不是发生次数。**
   *
   *  告警引擎每 `ALERT_EVALUATE_INTERVAL_SECONDS` 秒把所有规则重跑一轮
   *  （internal/platform/jobs/alert_evaluate.go 的
   *  `DefaultAlertEvaluateInterval = 60 * time.Second`），条件仍然成立就把这
   *  条已有告警的计数 +1（db/queries/alerts.sql 的 TouchAlert：
   *  `fire_count = fire_count + 1`）。所以它数的是「这个条件连续成立了多少
   *  轮」，与上游到底出了几次事没有关系。
   *
   *  这条注释以前写着「被去重合并掉的命中次数（含首次）」——那是后端一句
   *  同样误导的注释（internal/platform/alerts/alert.go）的副本，界面上三处
   *  「触发 N 次 / 命中 N 次」全从它长出来。2026-09-08 生产上那条 669 正好
   *  等于 668 分钟 + 1，一分不差。措辞统一由 `FIRE_COUNT_MEANING` 一份说。 */
  fire_count: number;
  /** 真正的触发次数（XM-OPS-TRUTH 计划新增，**今天还不存在**）。
   *
   *  字段不在时一律只显示评估轮数，**不要 `?? 0`**：0 会被读成「一次都没
   *  触发」，而事实是「我们不知道」。判据统一走 `describeFireCount`。 */
  trigger_count?: number | null;
  /** 第一次打开这条告警的时刻（XM-OPS-TRUTH 计划新增，**今天还不存在**）。
   *
   *  `opened_at` 不是它：告警恢复后再次触发走的是 InsertAlert 新开一行
   *  （db/queries/alerts.sql 的 InsertAlert / GetLatestResolvedAlertByDedupKey），
   *  `opened_at` 每次都归零，于是抖动型告警的「已持续」系统性偏小。 */
  first_opened_at?: string | null;
  notify_status: AlertNotifyStatus;
  notify_error: string;
  notified_at: string | null;
}

/** 告警评估周期（秒）。与后端 `jobs.DefaultAlertEvaluateInterval` 同一个数，
 *  由 labels.reconcile 的「评估轮次」一组从 Go 源码抽出来逐值对账——后端改了
 *  周期而这里没跟，那一组会红。 */
export const ALERT_EVALUATE_INTERVAL_SECONDS = 60;

/** 表格里那一列的表头。**一份，两张表共用**（告警中心、平台告警面板）。 */
export const FIRE_COUNT_HEADER = "评估轮次";

/** `fire_count` 到底是什么。**一份，四处共用**（工作台待办、告警中心的列与
 *  表格说明、平台告警面板的列与表格说明）。
 *
 *  这句话曾有四份措辞不同的副本（「被去重合并掉的命中次数（含首次）」「含首次
 *  及被去重合并的重复命中」「命中 N 次」「触发 N 次」），于是同一个数在三个
 *  页面上有三种叫法，人会以为看的是三个不同的量。 */
export const FIRE_COUNT_MEANING =
  `告警每 ${ALERT_EVALUATE_INTERVAL_SECONDS} 秒重新评估一轮，条件仍然成立就加 1——` +
  "这是评估轮数，不是发生次数。";

/** 「已持续」为什么可能偏小。`first_opened_at` 缺席时挂在待办行上说明白。 */
export const ALERT_AGE_RESET_HINT =
  "「已持续」从这条告警最近一次打开算起：恢复后重新触发会新开一条，计时归零，" +
  "所以反复抖动的告警显示的时长比实际短。";

/** 计数该怎么念。
 *
 *  **两个数是不同的事实，不是替换关系**：`trigger_count` 到位后仍要显示评估
 *  轮数，否则「它已经这样多久了」这个信息就没了。字段缺席（含显式 null）时
 *  只说评估轮数——不编一个 0 冒充「没触发过」。
 *
 *  - `rounds` / `triggers` / `combined` 是整句，给没有表头的位置（工作台待办的
 *    右列、平台概览的副行）；
 *  - `figure` 只有数字，给**有表头的数字列**：表头已经写着「评估轮次」、列是
 *    右对齐 tabular-nums，格子里再写一遍「评估 N 轮」既重复又对不齐。
 *    `trigger_count` 在场时写成「M / N」（触发 / 评估），整句退到悬停里。
 *
 *  这个数**只在这一处**拼进字符串（labels.reconcile 有「取值」扫描盯着）：别处
 *  要显示它，就从这里多取一个字段，不要再开第二个出场点。 */
export function describeFireCount(
  alert: Pick<AlertItem, "fire_count" | "trigger_count">,
): { rounds: string; triggers: string | null; combined: string; figure: string } {
  const rounds = `评估 ${alert.fire_count} 轮`;
  const count = alert.trigger_count;
  if (count === undefined || count === null) {
    return { rounds, triggers: null, combined: rounds, figure: `${alert.fire_count}` };
  }
  const triggers = `触发 ${count} 次`;
  return {
    rounds,
    triggers,
    combined: `${triggers} · ${rounds}`,
    figure: `${count} / ${alert.fire_count}`,
  };
}

/** 这条告警「已持续」该从哪个时刻算，以及要不要附一句说明。
 *
 *  `first_opened_at` 在就用它（那才是真正的首次打开）；不在就退回 `opened_at`
 *  并挂 `ALERT_AGE_RESET_HINT`——把一个系统性偏小的数字不加说明地摆出来，
 *  与摆一个错数字没有区别。 */
export function alertAgeAnchor(
  alert: Pick<AlertItem, "opened_at" | "first_opened_at">,
): { since: string; hint: string | null } {
  const first = alert.first_opened_at;
  if (first === undefined || first === null || first === "") {
    return { since: alert.opened_at, hint: ALERT_AGE_RESET_HINT };
  }
  return { since: first, hint: null };
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
export interface AlertsPage {
  items: AlertItem[];
  /** 服务端说的「这一页可能不是全部」（XM-ALERTS-LIST-TRUNCATED）。
   *
   *  **不要在前端自己算**：调用方传的 limit 与真正生效的 limit 可能不是一个数
   *  （不传、或传得比服务端上界还大都会被钳），拿自己传的数去比会永远判不出
   *  截断。只有服务端知道生效值。老后端没有这个字段时按 false 处理——那时
   *  它确实没法回答这个问题，假装"可能截断"会平白吓人。 */
  truncated: boolean;
  /** 服务端实际生效的上限；没给就是 0（不显示）。 */
  limit: number;
}

/** 带信封的列表：要判断「这一屏是不是全部」时用它。 */
export async function listAlertsPage(
  options: ListAlertsOptions = {},
  client: ApiClient = apiClient,
  config: PlatformApiConfig = appApiConfig,
): Promise<AlertsPage> {
  const body = await client.get<
    ListResponse<AlertItem> & { truncated?: boolean; limit?: number }
  >("/api/v1/alerts", {
    searchParams: {
      environment: config.environment,
      status: statusParam(options.status),
      ...(options.limit === undefined ? {} : { limit: String(options.limit) }),
    },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return {
    items: body.items ?? [],
    truncated: body.truncated === true,
    limit: typeof body.limit === "number" && Number.isSafeInteger(body.limit) ? body.limit : 0,
  };
}

/** 只要条目、不关心完整性时用它（多数计数与徽章都是这一类）。 */
export async function listAlerts(
  options: ListAlertsOptions = {},
  client: ApiClient = apiClient,
  config: PlatformApiConfig = appApiConfig,
): Promise<AlertItem[]> {
  return (await listAlertsPage(options, client, config)).items;
}

/** 静默窗口此刻处在哪一态（Go 侧 httpapi/alert_silences.go 的 state 字段）。
 *
 *  **没有「已取消」**：平台今天没有撤销静默的能力，窗口只会自己到期。
 *  编一个前端状态出来，会让运营以为「取消」这件事已经做得到。 */
export type SilenceState = "active" | "scheduled" | "expired";

/** `GET /api/v1/alerts/silences` 的一条记录（httpapi/alert_silences.go silenceItem）。 */
export interface SilenceItem {
  id: string;
  /** 空串 = 全局窗口（该环境下所有规则）。翻译成中文是前端的事。 */
  rule_key: string;
  environment: string;
  reason: string;
  starts_at: string;
  ends_at: string;
  created_by: string;
  created_at: string;
  /** 由服务端按 as_of 算，**前端不要自己比时间**：一台快五分钟的机器会把
   *  刚过期的窗口显示成「生效中」，而运营据此以为告警还压着。 */
  state: SilenceState;
}

/** state 查询参数的两个取值。 */
export const SILENCE_STATE_ACTIVE = "active";
export const SILENCE_STATE_ALL = "all";

export interface ListSilencesOptions extends ListOptions {
  /** 不传 = 只看此刻生效的（服务端默认）。传 SILENCE_STATE_ALL 则含未开始与已过期。 */
  state?: typeof SILENCE_STATE_ACTIVE | typeof SILENCE_STATE_ALL;
  limit?: number;
}

export interface SilencesPage {
  items: SilenceItem[];
  /** 服务端说的「这一页可能不是全部」，理由同 AlertsPage.truncated。 */
  truncated: boolean;
  /** 服务端实际生效的上限；没给就是 0（不显示）。 */
  limit: number;
  /** 服务端判定三态所用的时刻。空串表示老后端没给——那时不显示「截至」。 */
  as_of: string;
}

/** 列出某环境下的静默窗口。
 *
 *  这条端点存在之前，静默**只能建不能看**：运营按得下去，却回答不了
 *  「现在有哪些静默生效中、是谁按的、什么时候到期」。 */
export async function listSilencesPage(
  options: ListSilencesOptions = {},
  client: ApiClient = apiClient,
  config: PlatformApiConfig = appApiConfig,
): Promise<SilencesPage> {
  const body = await client.get<
    ListResponse<SilenceItem> & { truncated?: boolean; limit?: number; as_of?: string }
  >("/api/v1/alerts/silences", {
    searchParams: {
      environment: config.environment,
      ...(options.state === undefined ? {} : { state: options.state }),
      ...(options.limit === undefined ? {} : { limit: String(options.limit) }),
    },
    ...(options.signal ? { signal: options.signal } : {}),
  });
  return {
    items: body.items ?? [],
    truncated: body.truncated === true,
    limit: typeof body.limit === "number" && Number.isSafeInteger(body.limit) ? body.limit : 0,
    as_of: typeof body.as_of === "string" ? body.as_of : "",
  };
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

/** 全部规则的键与中文名（Go 侧 alerts/rules.go 的 Rules，label 逐字取自
 *  那边每条规则的 Title）。
 *
 *  前端重复这份清单，是为了让静默对话框的下拉框有可选项——后端没有
 *  「列出规则」的端点（Foundation-A 不值得为一个静态清单开一条 API）。
 *
 *  重复就会分叉，而且**已经分叉过一次**：后端在 XM-0033 之后陆续加了
 *  upstream.version.changed / upstream.runway.low / approval.pending.too_long
 *  三条，这份清单一直停在最早的五条——于是那三类告警在界面上只显示原始
 *  英文键，静默对话框里也根本选不到它们（运营想压住「审批单挂太久」的刷屏
 *  只能整个环境全局静默）。XM-I18N-LABELS 补齐，并把「不许再分叉」变成门禁：
 *  labels.reconcile.test.ts 直接读 rules.go，键少一条、多一条、或者中文名与
 *  后端 Title 不一致，那条测试都会红。
 *
 *  拼错的 rule_key 会被后端当场拒绝（它会静默零条告警，而创建者以为
 *  已经静默了），所以这里给的是下拉而不是自由输入。 */
export const ALERT_RULES: { key: string; label: string }[] = [
  { key: "metric.sync.failed", label: "指标同步失败" },
  { key: "metric.data.stale", label: "指标数据陈旧" },
  { key: "metric.sync.consecutive_failed", label: "同步连续失败" },
  { key: "channel.token.invalid", label: "渠道 token 失效" },
  { key: "channel.balance.low", label: "渠道余额不足" },
  { key: "upstream.version.changed", label: "上游版本变化" },
  { key: "upstream.runway.low", label: "上游可用天数不足" },
  { key: "approval.pending.too_long", label: "审批单挂太久" },
  { key: "cards.sync.failed", label: "卡片同步连续失败" },
];

/** 把规则键翻成中文名；未知键原样返回。
 *
 *  未知键**不隐藏也不报错**：后端加了新规则而前端还没跟上时，
 *  列表里显示原始键仍然是有用的信息，显示成「未知规则」则是在丢事实。 */
export function ruleLabel(ruleKey: string): string {
  return ALERT_RULES.find((r) => r.key === ruleKey)?.label ?? ruleKey;
}
