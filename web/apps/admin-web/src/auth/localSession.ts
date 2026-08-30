/** local 模式（XM-LOGIN）：管理台自带账号密码登录。
 *
 *  会话是服务端签发的 HttpOnly Cookie（`xm_session`）：前端拿不到、也不需要
 *  拿到令牌本身，只需要在每次请求上带 `credentials:"same-origin"` 让浏览器
 *  自动附上 Cookie。登录态因此天然不能被 XSS 读走（HttpOnly），代价是前端
 *  没法像 oidc 那样自己判断「有没有会话」——唯一的真相来源是服务端对
 *  GET /api/v1/auth/me 的回答，这也是 RequireAuth 每次挂载都要问一次的原因。
 *
 *  这个模块只做四件事（login/me/logout/changePassword）与一份**内存**当前
 *  用户缓存——不进 localStorage/sessionStorage：关掉标签页就该重新问一次
 *  服务端，而不是拿着一份可能已经过期的本地回忆自称「已登录」。
 *
 *  **本模块内部用的 authClient 是一个独立的 ApiClient 实例，不接
 *  onLocalSessionLoss**（对比 api/client.ts 的应用单例）：login() 在密码
 *  写错时会正常收到 401 INVALID_CREDENTIALS——那不是「会话失效」，是「这次
 *  登录尝试被拒绝」，此时人正停在登录页上，不该被通用兜底再跳一次登录页
 *  （那会打断登录页自己正要显示的错误文案）。me() 同理：RequireAuth 自己
 *  根据这次探测的结果决定去哪，不需要另一层再跳一次。 */
import { ApiError, createApiClient, type ApiClient } from "../api/client";
import { appApiConfig } from "../api/config";

export interface LocalUser {
  username: string;
  display_name: string;
  roles: string[];
  must_change_password: boolean;
}

interface LocalUserResponse {
  username?: unknown;
  display_name?: unknown;
  roles?: unknown;
  must_change_password?: unknown;
}

function projectLocalUser(body: LocalUserResponse): LocalUser {
  const username = typeof body.username === "string" ? body.username : "";
  const displayName = typeof body.display_name === "string" && body.display_name ? body.display_name : username;
  const roles = Array.isArray(body.roles)
    ? body.roles.filter((r): r is string => typeof r === "string" && r.length > 0)
    : [];
  return {
    username,
    display_name: displayName,
    roles,
    must_change_password: body.must_change_password === true,
  };
}

// 惰性构造：真正调用 createApiClient() 推迟到第一次实际发请求时才发生，
// 不在模块顶层求值期间读取任何跨文件的值——client.ts / session.ts /
// localSession.ts 三者互相引用（顶栏要显示 local 用户名、client.ts 的单例
// 要接 local 模式的会话失效回调），惰性构造让这圈引用只在「谁的 export
// 先写好」层面存在，不产生「谁的模块体先跑完」这种求值顺序依赖。
let _authClient: ApiClient | null = null;
function authClient(): ApiClient {
  if (!_authClient) _authClient = createApiClient({ config: appApiConfig, localCredentials: true });
  return _authClient;
}

let cachedUser: LocalUser | null = null;

/** RequireAuth／顶栏读取「当前是谁」的唯一入口：有值就不必再问服务端一次。 */
export function cachedLocalUser(): LocalUser | null {
  return cachedUser;
}

export function setCachedLocalUser(user: LocalUser | null): void {
  cachedUser = user;
}

export async function login(username: string, password: string): Promise<LocalUser> {
  const body = await authClient().post<LocalUserResponse>("/api/v1/auth/login", {
    username,
    password,
  });
  const user = projectLocalUser(body);
  cachedUser = user;
  return user;
}

/** 探测当前会话；401/403＝没有有效会话，原样把 ApiError 抛给调用方判断。 */
export async function me(): Promise<LocalUser> {
  const body = await authClient().get<LocalUserResponse>("/api/v1/auth/me");
  const user = projectLocalUser(body);
  cachedUser = user;
  return user;
}

export async function logout(): Promise<void> {
  try {
    await authClient().post("/api/v1/auth/logout", {});
  } catch (cause) {
    // 已经没有会话时后端多半也是 401——退出的目的（清掉本地状态、回登录页）
    // 仍然达成，不必把这类错误显示给人看
    if (!(cause instanceof ApiError) || !cause.isAuthFailure) throw cause;
  } finally {
    cachedUser = null;
  }
}

export async function changePassword(currentPassword: string, newPassword: string): Promise<void> {
  await authClient().post("/api/v1/auth/password", {
    current_password: currentPassword,
    new_password: newPassword,
  });
  // 改密成功后本地标记清掉，避免 RequireAuth 下一次渲染还拿着旧缓存把人
  // 拽回改密页——服务端已经不再要求强制改密了
  if (cachedUser) cachedUser = { ...cachedUser, must_change_password: false };
}
