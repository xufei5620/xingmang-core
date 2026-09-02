import {
  ApiError,
  apiClient,
  FeatureNotMountedError,
  looksLikeUnmountedRoute,
  type ApiClient,
} from "./client";
import type {
  FreshnessContract,
  PeriodGranularity,
  PeriodRange,
} from "@xingmang/ui-admin";
import type { ListOptions } from "./platform";

/** 一个**可能缺席**的金额。
 *
 *  `minor_units` 为 null 表示上游没给这个数——与 0 是相反的两件事。
 *  界面据此显示「—」而不是「¥0.00」（宪法 12 条）。
 *
 *  它是**字符串**而不是 number:JSON number 在 JS 里是 float64，超过 2^53 的
 *  最小单位金额会静默丢精度（宪法 13 条：金额禁止 float）。解析用 BigInt。 */
export interface AmountBody {
  minor_units: string | null;
  currency: string;
}

/** 一个可能缺席的计数。理由同 AmountBody。 */
export interface CountBody {
  value: number | null;
}

/** 账号状态。`unknown` 是上游给了我们不认识的值——
 *  **不回落成正常**：把一个被停用的账号显示成正常，人会据此排除掉真正的原因。 */
export type PlatformUserStatus = "active" | "limited" | "disabled" | "unknown";

export interface PlatformUserItem {
  id: string;
  username: string;
  /** 已在**连接器**侧打码。空串=上游没记；`invalid-contact`=记了但形状不认识。 */
  email_masked: string;
  status: PlatformUserStatus;
  balance: AmountBody;
  period_recharge: AmountBody;
  period_consumed: AmountBody;
  /** 近 30 天消费。**滚动窗口，不随所选区间变**——原型把它和「区间消费」
   *  并排放，正是为了看出「今天花得少，但一个月来一直很稳」。 */
  last_30d_consumed: AmountBody;
  /** null 表示从未活跃——与「很久以前活跃过」不是一回事。 */
  last_active_at: string | null;
  token_prefix: string;
}

/** 统计粒度。与后端 `platformusers.Granularity` 逐字对应。 */
export type { PeriodGranularity } from "@xingmang/ui-admin";

/** 服务端回显的统计区间。
 *
 *  `from` / `to` 是**闭区间**业务日。回显它们而不只回粒度：粒度是「周」时，
 *  人要能看见到底是哪七天——跨月那几天最容易理解错。 */
export type PeriodBody = PeriodRange;

/** 区间合计，**带覆盖率**。
 *
 *  合计只加得动「上游给得出流水」的那些用户，所以它可能是一个**下界**。
 *  `covered_users < total_users` 时界面必须说出来，否则一个下界会被读成全量
 *  （宪法 12 条：禁止裸数字冒充完整数据）。 */
export interface PeriodTotalsBody {
  recharge: AmountBody;
  consumed: AmountBody;
  covered_users: number;
  total_users: number;
  /** covered === total 的便捷判定，避免各处自己比一遍比错。 */
  complete: boolean;
}

export interface PlatformUserPage {
  items: PlatformUserItem[];
  next_cursor: string;
  total_count: CountBody;
  total_balance: AmountBody;
  /** 今日活跃用户数（原型第 1 格的副行）。null = 上游没给。 */
  active_today: CountBody;
  period_totals: PeriodTotalsBody;
  period: PeriodBody;
  data_source: string;
  freshness: FreshnessContract;
}

/** 每日消费序列的只读 DTO。金额与计数都允许未知，不能把缺失日当作 0。 */
export interface DailyUsagePointBody {
  day: string;
  consumed: AmountBody;
  requests: CountBody;
}

export interface DailyUsageSeriesBody {
  from: string;
  to: string;
  points: DailyUsagePointBody[];
  coverage: { expected_days: number; covered_days: number; complete: boolean };
  snapshot: {
    observed_at: string;
    source: string;
    watermark: string;
    is_partial: boolean;
  };
}

/** 列表排序键。与后端 `platformusers.SortKey` 逐字对应。 */
export type PlatformUserSort =
  | "balance_desc"
  | "consumed_desc"
  | "last_active_desc"
  | "username_asc";

export interface ListPlatformUsersOptions extends ListOptions {
  q?: string;
  status?: PlatformUserStatus | "";
  sort?: PlatformUserSort;
  /** 统计区间的锚点业务日（YYYY-MM-DD）。不传 = 服务端的「今天」。 */
  day?: string;
  /** 统计粒度。不传 = day。 */
  granularity?: PeriodGranularity;
  limit?: number;
  cursor?: string;
}

export interface DailyUsageOptions extends ListOptions {
  day?: string;
  days?: number;
}

/** 读取指定用户按业务日排列的消费序列；服务端负责 CST 日期解释。
 *
 *  与 `listPlatformUsers` 同一判据：`XM_PLATFORM_USERS_MODE=off` 时这条端点
 *  与用户管理其余端点一起整组不挂载，chi 的默认 404 没有可解析的 `error.code`；
 *  具体这个用户没有日消费记录时，后端把连接器的 `ErrNotFound` 翻译成带
 *  `error.code`（`ACTION_NOT_REGISTERED`）的结构化 404（platformusers/
 *  service.go `translateError`），结构不同，不会被误判成「未接入」。 */
export async function listPlatformUserDailyUsage(
  platform: string,
  userId: string,
  options: DailyUsageOptions = {},
  client: ApiClient = apiClient,
): Promise<DailyUsageSeriesBody> {
  const segment = encodePlatformUserIdSegment(userId);
  try {
    return await client.get<DailyUsageSeriesBody>(
      `/api/v1/platforms/${encodeURIComponent(platform)}/users/${segment}/daily-usage`,
      {
        searchParams: {
          ...(options.day ? { day: options.day } : {}),
          ...(options.days === undefined ? {} : { days: String(options.days) }),
        },
        ...(options.signal ? { signal: options.signal } : {}),
      },
    );
  } catch (error) {
    if (looksLikeUnmountedRoute(error)) {
      throw new FeatureNotMountedError(error, USERS_NOT_MOUNTED_DESCRIPTION);
    }
    throw error;
  }
}

export interface KeyMetadataBody {
  id: string;
  prefix: string;
  status: string;
  created_at: string | null;
  last_used_at: string | null;
  today_peak_rpm: CountBody;
}

export interface KeyMetadataPageBody {
  items: KeyMetadataBody[];
  next_cursor: string;
  snapshot: {
    observed_at: string;
    source: string;
    watermark: string;
    is_partial: boolean;
  };
}

export interface KeyMetadataOptions extends ListOptions {
  limit?: number;
  cursor?: string;
}

/** 读取元数据-only API Key 列表。响应永远没有完整 Key、secret 或 credential 字段。
 *
 *  未接入判据与 `listPlatformUserDailyUsage` 逐字相同：整组不挂载时是没有
 *  `error.code` 的纯文本 404，具体用户没有 Key 记录时是带 `error.code`
 *  （`ACTION_NOT_REGISTERED`）的结构化 404，两者不会混淆。 */
export async function listPlatformUserKeys(
  platform: string,
  userId: string,
  options: KeyMetadataOptions = {},
  client: ApiClient = apiClient,
): Promise<KeyMetadataPageBody> {
  const segment = encodePlatformUserIdSegment(userId);
  try {
    return await client.get<KeyMetadataPageBody>(
      `/api/v1/platforms/${encodeURIComponent(platform)}/users/${segment}/keys`,
      {
        searchParams: {
          ...(options.limit === undefined ? {} : { limit: String(options.limit) }),
          ...(options.cursor ? { cursor: options.cursor } : {}),
        },
        ...(options.signal ? { signal: options.signal } : {}),
      },
    );
  } catch (error) {
    if (looksLikeUnmountedRoute(error)) {
      throw new FeatureNotMountedError(error, USERS_NOT_MOUNTED_DESCRIPTION);
    }
    throw error;
  }
}

/** `platform.users.read` —— 读取用户清单需要的权限。
 *
 *  **不复用 ops.read**：那看到的是聚合数字，这是逐用户的资金明细
 *  （internal/platform/platformusers/permissions.go）。 */
export const USERS_READ_PERMISSION = "platform.users.read";

/** 用户 ID 的 canonical 路径段前缀。
 *
 * 不能只靠 `encodeURIComponent`:它会把 `.` / `..` 原样留下，而浏览器会在
 * React Router 看到前先做 dot-segment 归一化。统一加结构前缀并把 UTF-8 字节
 * 编成小写 hex 后，任何合法结果都不可能等于 `.` 或 `..`，且与原 ID 双射。 */
const USER_ID_SEGMENT_PREFIX = "u-";

export function encodePlatformUserIdSegment(userId: string): string {
  if (userId.length === 0) throw new Error("用户 ID 不能为空");
  const bytes = new TextEncoder().encode(userId);
  // TextEncoder 会把未配对代理项替换成 U+FFFD；那不是无损编码，必须拒绝。
  if (new TextDecoder("utf-8", { fatal: true }).decode(bytes) !== userId) {
    throw new Error("用户 ID 不是可无损编码的 Unicode 字符串");
  }
  if (bytes.byteLength > 512) throw new Error("用户 ID 超过 512 字节");
  const hex = Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
  return `${USER_ID_SEGMENT_PREFIX}${hex}`;
}

/** 严格解码 canonical 路径段；任何裸 ID、空 payload、非小写 hex 或非法 UTF-8
 * 都返回 null，由路由在发起 platformusers Query 前 fail closed。 */
export function decodePlatformUserIdSegment(segment: string): string | null {
  if (!segment.startsWith(USER_ID_SEGMENT_PREFIX)) return null;
  const hex = segment.slice(USER_ID_SEGMENT_PREFIX.length);
  if (hex.length === 0 || hex.length % 2 !== 0 || !/^[0-9a-f]+$/.test(hex)) return null;

  const bytes = new Uint8Array(hex.length / 2);
  if (bytes.byteLength > 512) return null;
  for (let index = 0; index < bytes.length; index += 1) {
    bytes[index] = Number.parseInt(hex.slice(index * 2, index * 2 + 2), 16);
  }

  try {
    const userId = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
    if (userId.length === 0 || encodePlatformUserIdSegment(userId) !== segment) return null;
    return userId;
  } catch {
    return null;
  }
}

interface PlatformUserDetailResponse {
  ref: { platform: string; id: string };
  user: PlatformUserItem;
  registered_at: string | null;
  period: PeriodBody;
  snapshot: {
    observed_at: string;
    source: string;
    watermark: string;
    is_partial: boolean;
  };
  capabilities: string[];
}

/** 用户管理「未接入」的说明文案（用户清单、用户详情共用同一句话——它们是
 *  同一条链路的一体两面，理由不该分叉）。
 *
 *  `XM_PLATFORM_USERS_MODE=off` 时后端整组不挂载 `/platforms/{platform}/users*`
 *  （cmd/platform-api/platformusers.go buildPlatformUsers），前端据此分辨
 *  「没接」与「坏了」，不能显示成加载失败。 */
const USERS_NOT_MOUNTED_DESCRIPTION =
  "用户管理在当前环境未启用（XM_PLATFORM_USERS_MODE=off）。接入真实用户数据源后会自动出现，无需手动开启。";

/** Read the v2 canonical detail resource. A 404 with a parseable error
 * envelope is a proven not-found result; a 404 without one means the whole
 * endpoint group isn't mounted (see USERS_NOT_MOUNTED_DESCRIPTION above), and
 * all other API errors stay errors so the page can preserve retry semantics. */
export async function getPlatformUser(
  platform: string,
  userId: string,
  options: LookupPlatformUserOptions = {},
  client: ApiClient = apiClient,
): Promise<PlatformUserLookupResult> {
  const segment = encodePlatformUserIdSegment(userId);
  try {
    const body = await client.get<PlatformUserDetailResponse>(
      `/api/v1/platforms/${encodeURIComponent(platform)}/users/${segment}`,
      {
        searchParams: {
          ...(options.day ? { day: options.day } : {}),
          ...(options.granularity ? { granularity: options.granularity } : {}),
        },
        ...(options.signal ? { signal: options.signal } : {}),
      },
    );
    const observedMs = new Date(body.snapshot.observed_at).getTime();
    const staleness = Number.isFinite(observedMs)
      ? Math.max(0, Math.floor((Date.now() - observedMs) / 1000))
      : null;
    const page: PlatformUserPage = {
      items: [body.user],
      next_cursor: "",
      total_count: { value: 1 },
      total_balance: body.user.balance,
      active_today: { value: null },
      period_totals: {
        recharge: body.user.period_recharge,
        consumed: body.user.period_consumed,
        covered_users: body.user.period_consumed.minor_units === null ? 0 : 1,
        total_users: 1,
        complete: body.user.period_consumed.minor_units !== null,
      },
      period: body.period,
      data_source: body.snapshot.source,
      freshness: {
        state: body.snapshot.is_partial ? "partial" : "fresh",
        staleness_seconds: staleness,
        threshold_seconds: 60,
        is_partial: body.snapshot.is_partial,
        observed_at: body.snapshot.observed_at || null,
        last_success: body.snapshot.observed_at || null,
        last_error_code: "",
      },
    };
    return { kind: "found", user: body.user, page, pagesScanned: 1 };
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) {
      // 未挂载路由的 404 没有可解析的 error.code；具体用户 id 不存在的 404
      // 一定带着后端 WriteError 写出的 code（比如 ACTION_NOT_REGISTERED）。
      // 只有前者是「这条链路没接」，后者仍然是「这个用户没有这条记录」。
      if (looksLikeUnmountedRoute(error)) {
        throw new FeatureNotMountedError(error, USERS_NOT_MOUNTED_DESCRIPTION);
      }
      return { kind: "notFound", pagesScanned: 1 };
    }
    throw error;
  }
}

/** 哪些平台有终端用户清单。
 *
 *  与后端 `platformusers.SupportsPlatform` 逐字对应。CPA 的「用户」是代理商
 *  (语义不同)，服务器根本没有终端用户——给它们挂一个永远空的用户页签,
 *  等于告诉运营「这个平台没有用户」，而事实是我们压根没接。 */
const PLATFORMS_WITH_USERS = new Set(["sub2api", "newapi"]);

export function platformHasUsers(serviceType: string): boolean {
  return PLATFORMS_WITH_USERS.has(serviceType);
}

/** 读取某平台的一页终端用户。
 *
 *  **不传 environment**：后端一律用调用者身份自己的环境（跨环境读取是硬拒绝）。
 *  这批数据逐用户可还原一家客户的经营规模，多一个可写的入参就多一个将来被
 *  放宽成「跨环境看板」的口子。 */
export async function listPlatformUsers(
  platform: string,
  options: ListPlatformUsersOptions = {},
  client: ApiClient = apiClient,
): Promise<PlatformUserPage> {
  let body: PlatformUserPage;
  try {
    body = await client.get<PlatformUserPage>(
      `/api/v1/platforms/${encodeURIComponent(platform)}/users`,
      {
        searchParams: {
          ...(options.q ? { q: options.q } : {}),
          ...(options.status ? { status: options.status } : {}),
          ...(options.sort ? { sort: options.sort } : {}),
          // 区间**不传默认值**：「今天」必须由服务端按 CST +08:00 解释。
          // 前端拿浏览器本地日期去填的话，一个在 UTC-5 的运营看到的「今天」
          // 会比账面业务日早一天（宪法 14 条）。
          ...(options.day ? { day: options.day } : {}),
          ...(options.granularity ? { granularity: options.granularity } : {}),
          ...(options.limit === undefined ? {} : { limit: String(options.limit) }),
          ...(options.cursor ? { cursor: options.cursor } : {}),
        },
        ...(options.signal ? { signal: options.signal } : {}),
      },
    );
  } catch (error) {
    // XM_PLATFORM_USERS_MODE=off 时这条端点整组不挂载：chi 的默认 404 没有
    // 可解析的 error.code，用它区分「没接」与「这次请求恰好失败了」
    if (looksLikeUnmountedRoute(error)) {
      throw new FeatureNotMountedError(error, USERS_NOT_MOUNTED_DESCRIPTION);
    }
    throw error;
  }
  return {
    ...body,
    items: body.items ?? [],
  };
}

/** 详情页用的精确查找结果。
 *
 * `notFound` 只在服务端过滤结果已经彻底耗尽时出现；`incomplete` 表示搜索结果
 * 仍可能在未扫描的游标后面，调用方必须显示「无法确认」，不能伪装成 404。 */
export type PlatformUserLookupResult =
  | {
      kind: "found";
      user: PlatformUserItem;
      page: PlatformUserPage;
      pagesScanned: number;
    }
  | { kind: "notFound"; pagesScanned: number }
  | {
      kind: "incomplete";
      reason: "pageLimit" | "cursorCycle";
      pagesScanned: number;
      lastPage: PlatformUserPage;
    };

/** 单次按 ID 查找最多披露 5 × 200 条过滤结果。
 *
 * `200` 是 platformusers v1 的既有 MaxLimit；`5` 是详情深链的客户端扫描预算。
 * 达到预算仍有游标时返回 `incomplete`，由页面明确提示并允许重试。这样既不把
 * 一个模糊搜索结果误当目标，也不因未扫完就给出假的 Not Found。 */
const USER_LOOKUP_PAGE_SIZE = 200;
const USER_LOOKUP_MAX_PAGES = 5;

export type LookupPlatformUserOptions = Pick<
  ListPlatformUsersOptions,
  "day" | "granularity" | "signal"
>;

/** 只用 platformusers v1 的列表 Query 按不透明 ID 精确查找一个用户。
 *
 * 后端的 `q` 同时匹配用户名 / ID / 令牌前缀，因此任何一页的第一条都不一定
 * 是目标；唯一可接受的命中是 `item.id === userId`。 */
export async function lookupPlatformUserExact(
  platform: string,
  userId: string,
  options: LookupPlatformUserOptions = {},
  client: ApiClient = apiClient,
): Promise<PlatformUserLookupResult> {
  let cursor = "";
  const seenCursors = new Set<string>();

  for (let pagesScanned = 1; pagesScanned <= USER_LOOKUP_MAX_PAGES; pagesScanned += 1) {
    const page = await listPlatformUsers(
      platform,
      {
        q: userId,
        limit: USER_LOOKUP_PAGE_SIZE,
        ...(cursor ? { cursor } : {}),
        ...(options.day ? { day: options.day } : {}),
        ...(options.granularity ? { granularity: options.granularity } : {}),
        ...(options.signal ? { signal: options.signal } : {}),
      },
      client,
    );

    const exact = page.items.find((item) => item.id === userId);
    if (exact) return { kind: "found", user: exact, page, pagesScanned };

    const nextCursor = page.next_cursor;
    if (!nextCursor) return { kind: "notFound", pagesScanned };

    if (seenCursors.has(nextCursor)) {
      return { kind: "incomplete", reason: "cursorCycle", pagesScanned, lastPage: page };
    }
    if (pagesScanned === USER_LOOKUP_MAX_PAGES) {
      return { kind: "incomplete", reason: "pageLimit", pagesScanned, lastPage: page };
    }

    seenCursors.add(nextCursor);
    cursor = nextCursor;
  }

  // 循环上限是常量，运行时不可达；保留 fail-closed 分支，避免未来重构把
  // `USER_LOOKUP_MAX_PAGES` 改成 0 后悄悄回落为 Not Found。
  throw new Error("用户精确查找没有执行任何分页请求");
}

/** 状态的展示口径。
 *
 *  抽成纯函数是为了能直接断言这些规则本身：一个把 disabled 显示成绿色「正常」
 *  的映射，在截图上和正确实现长得几乎一样，只有测试看得出来。 */
export function describeUserStatus(status: string): {
  label: string;
  tone: "success" | "warning" | "danger" | "neutral";
  hint: string;
} {
  switch (status) {
    case "active":
      return { label: "正常", tone: "success", hint: "账号可正常调用" };
    case "limited":
      // 原型逐格写的是「注意」而不是「受限」：这一列的作用是让人一眼挑出
      // 该看的账号，而不是复述上游的状态机（上游语义各异，说明留在 hint 里）
      return { label: "注意", tone: "warning", hint: "余额不足或被风控标记，上游语义各异" };
    case "disabled":
      return { label: "停用", tone: "neutral", hint: "账号已停用，不参与调用" };
    default:
      // 不认识的状态按需关注处理，不按正常：把一个被停用的账号显示成正常,
      // 人会据此排除掉真正的故障原因（与渠道令牌状态的「未知」同一条）
      return { label: "未知", tone: "warning", hint: `上游给了一个前端不认识的状态：${status}` };
  }
}

/** 打码邮箱的展示口径。
 *
 *  三种取值要分开说：有值、上游没记、记了但形状不认识。压成一个「—」
 *  会把「我们没解析出来」这条要有人看一眼的信号盖掉。 */
export const UNPARSED_CONTACT = "invalid-contact";

export function describeMaskedEmail(raw: string): { text: string; hint?: string } {
  if (raw === "") return { text: "—", hint: "上游没有记录联系方式" };
  if (raw === UNPARSED_CONTACT) {
    return {
      text: "格式不认识",
      hint: "上游记了联系方式，但形状不是我们认得的邮箱；为安全起见整条不显示",
    };
  }
  return { text: raw, hint: "邮箱已在服务端打码，平台不持有明文" };
}
