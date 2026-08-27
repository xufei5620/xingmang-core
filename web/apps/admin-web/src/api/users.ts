import { apiClient, type ApiClient } from "./client";
import type { FreshnessContract } from "@xingmang/ui-admin";
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
  /** null 表示从未活跃——与「很久以前活跃过」不是一回事。 */
  last_active_at: string | null;
  token_prefix: string;
}

export interface PlatformUserPage {
  items: PlatformUserItem[];
  next_cursor: string;
  total_count: CountBody;
  total_balance: AmountBody;
  data_source: string;
  freshness: FreshnessContract;
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
  limit?: number;
  cursor?: string;
}

/** `platform.users.read` —— 读取用户清单需要的权限。
 *
 *  **不复用 ops.read**：那看到的是聚合数字，这是逐用户的资金明细
 *  （internal/platform/platformusers/permissions.go）。 */
export const USERS_READ_PERMISSION = "platform.users.read";

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
  const body = await client.get<PlatformUserPage>(
    `/api/v1/platforms/${encodeURIComponent(platform)}/users`,
    {
      searchParams: {
        ...(options.q ? { q: options.q } : {}),
        ...(options.status ? { status: options.status } : {}),
        ...(options.sort ? { sort: options.sort } : {}),
        ...(options.limit === undefined ? {} : { limit: String(options.limit) }),
        ...(options.cursor ? { cursor: options.cursor } : {}),
      },
      ...(options.signal ? { signal: options.signal } : {}),
    },
  );
  return {
    ...body,
    items: body.items ?? [],
  };
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
      return { label: "受限", tone: "warning", hint: "余额不足或被风控标记，上游语义各异" };
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
