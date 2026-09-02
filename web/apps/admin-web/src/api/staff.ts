import { apiClient, type ApiClient } from "./client";
import { executeAction, type ActionRun, type ListOptions } from "./platform";

/** `GET /api/v1/staff/accounts` 的一条记录（XM-LOGIN）。
 *
 *  账号是人，不是某一个环境下的业务数据——不按 environment 过滤，
 *  这份清单在所有环境下看到的都是同一批账号。 */
export interface StaffAccount {
  username: string;
  display_name: string;
  roles: string[];
  disabled: boolean;
  must_change_password: boolean;
  /** null 表示当前未锁定。 */
  locked_until: string | null;
  /** null 表示从未登录过。 */
  last_login_at: string | null;
  created_at: string;
  /** XM-AUTH-TOTP0：是否已激活 TOTP（totp_enrolled_at 非空）。 */
  totp_enrolled: boolean;
  /** 语义与 must_change_password 完全对称：持有需要 TOTP 的角色、且尚未
   *  激活时为真。 */
  must_enroll_totp: boolean;
  totp_enrolled_at: string | null;
}

interface ItemsResponse {
  items?: unknown;
}

interface StaffAccountRaw {
  username?: unknown;
  display_name?: unknown;
  roles?: unknown;
  disabled?: unknown;
  must_change_password?: unknown;
  locked_until?: unknown;
  last_login_at?: unknown;
  created_at?: unknown;
  totp_enrolled?: unknown;
  must_enroll_totp?: unknown;
  totp_enrolled_at?: unknown;
}

function str(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function strOrNull(value: unknown): string | null {
  return typeof value === "string" && value ? value : null;
}

function rolesOf(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value.filter((r): r is string => typeof r === "string" && r.length > 0);
}

/** 把服务端一行投影成页面需要的字段；缺字段一律回落到安全默认值而不是抛错——
 *  一行数据形状有点偏差不该让整张表打不开。 */
function projectStaffAccount(raw: unknown): StaffAccount {
  const row = (raw && typeof raw === "object" ? raw : {}) as StaffAccountRaw;
  const username = str(row.username);
  return {
    username,
    display_name: str(row.display_name) || username,
    roles: rolesOf(row.roles),
    disabled: row.disabled === true,
    must_change_password: row.must_change_password === true,
    locked_until: strOrNull(row.locked_until),
    last_login_at: strOrNull(row.last_login_at),
    created_at: str(row.created_at),
    totp_enrolled: row.totp_enrolled === true,
    must_enroll_totp: row.must_enroll_totp === true,
    totp_enrolled_at: strOrNull(row.totp_enrolled_at),
  };
}

/** 读取全部员工账号（需要 staff.manage）。 */
export async function listStaffAccounts(
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<StaffAccount[]> {
  const body = await client.get<ItemsResponse>("/api/v1/staff/accounts", {
    ...(options.signal ? { signal: options.signal } : {}),
  });
  if (!Array.isArray(body?.items)) return [];
  return body.items.map(projectStaffAccount);
}

/** Action 内核要求三段 ID；产品卡片里的短名仍写作 staff.account.create 等。 */
export const STAFF_ACTION_IDS = {
  create: "staff.account.create",
  setRoles: "staff.account.set_roles",
  setDisabled: "staff.account.set_disabled",
  resetPassword: "staff.account.reset_password",
} as const;

/** 四个 Action 声明的 Permission（一致，都是「人员与权限」这一块）。 */
export const STAFF_MANAGE_PERMISSION = "staff.manage";

export interface CreateStaffAccountInput {
  username: string;
  displayName: string;
  roles: string[];
  /** 留空＝让服务端生成一个初始密码（随回执一起回来，只显示一次）。 */
  initialPassword?: string;
}

/** 写操作的回执：run_id + 可能随之生成的初始密码（仅创建/重置密码两个 Action 会有）。 */
export interface StaffActionResult {
  runId: string;
  initialPassword: string | null;
}

function extractInitialPassword(result: unknown): string | null {
  if (!result || typeof result !== "object") return null;
  const value = (result as Record<string, unknown>).initial_password;
  return typeof value === "string" && value ? value : null;
}

/** 新建员工账号（staff.account.create@1）。roles 以逗号拼接传给后端。 */
export async function createStaffAccount(
  input: CreateStaffAccountInput,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<StaffActionResult> {
  const run = await executeAction(
    {
      actionId: STAFF_ACTION_IDS.create,
      version: "1",
      params: {
        username: input.username.trim(),
        display_name: input.displayName.trim(),
        roles: input.roles.join(","),
        ...(input.initialPassword ? { initial_password: input.initialPassword } : {}),
      },
    },
    options,
    client,
  );
  return { runId: run.runId, initialPassword: extractInitialPassword(run.result) };
}

/** 改角色（staff.account.set_roles@1）。传入的角色集合会整体替换现有角色。 */
export function setStaffAccountRoles(
  username: string,
  roles: string[],
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    {
      actionId: STAFF_ACTION_IDS.setRoles,
      version: "1",
      params: { username: username.trim(), roles: roles.join(",") },
    },
    options,
    client,
  );
}

/** 禁用/启用账号（staff.account.set_disabled@1）。 */
export function setStaffAccountDisabled(
  username: string,
  disabled: boolean,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<ActionRun> {
  return executeAction(
    {
      actionId: STAFF_ACTION_IDS.setDisabled,
      version: "1",
      params: { username: username.trim(), disabled },
    },
    options,
    client,
  );
}

/** 重置密码（staff.account.reset_password@1）。不填新密码＝让服务端生成一个。 */
export async function resetStaffAccountPassword(
  username: string,
  newPassword: string | undefined,
  options: ListOptions = {},
  client: ApiClient = apiClient,
): Promise<StaffActionResult> {
  const run = await executeAction(
    {
      actionId: STAFF_ACTION_IDS.resetPassword,
      version: "1",
      params: {
        username: username.trim(),
        ...(newPassword ? { new_password: newPassword } : {}),
      },
    },
    options,
    client,
  );
  return { runId: run.runId, initialPassword: extractInitialPassword(run.result) };
}

/** 角色目录：内部名 → 中文标签。顺序即表单里勾选框的展示顺序。 */
export const STAFF_ROLE_CATALOG: readonly { value: string; label: string }[] = [
  { value: "staff", label: "员工" },
  { value: "admin", label: "管理员" },
  { value: "auditor", label: "审计" },
  { value: "credential-admin", label: "凭据管理员" },
  { value: "key-metadata-reader", label: "Key 元数据只读" },
  { value: "request-content-reader", label: "请求正文查看" },
];

const ROLE_LABELS: ReadonlyMap<string, string> = new Map(
  STAFF_ROLE_CATALOG.map((r) => [r.value, r.label]),
);

/** 角色内部名 → 中文标签；不认识的角色原样显示内部名（不吞掉，方便发现新角色）。 */
export function staffRoleLabel(role: string): string {
  return ROLE_LABELS.get(role) ?? role;
}
