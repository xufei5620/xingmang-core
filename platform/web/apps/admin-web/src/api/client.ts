import { getRuntimeConfig } from "../auth/runtimeConfig";
import { onLocalSessionLoss } from "../auth/session";
import { appApiConfig, type PlatformApiConfig } from "./config";
import { httpStatusText } from "../lib/labels";

/** 网络层根本没通（DNS/连接/CORS/离线）时用的伪状态码。
 *  用 0 而不是编一个 5xx：区分「服务端拒绝了」和「压根没到服务端」，
 *  这两种情况的处置完全不同。 */
export const NETWORK_STATUS = 0;
const NETWORK_CODE = "NETWORK_UNAVAILABLE";
const BAD_RESPONSE_CODE = "BAD_RESPONSE";
const UNKNOWN_CODE = "UNKNOWN";

/** local 模式的 CSRF 头（后端契约：非 GET 请求必须带；GET 上带着也无害，
 *  所以统一发出，不必按 method 分支）。 */
const LOCAL_CSRF_HEADER: Readonly<Record<string, string>> = { "X-Requested-With": "xingmang" };

/** 后端 403 文案形如「缺少权限 registry.read」（httpapi/authz.go）。
 *  从里面把 scope 名抠出来，才能在界面上告诉人「缺哪个权限」而不是干瞪眼。 */
const MISSING_SCOPE_PATTERN = /缺少权限\s*([A-Za-z0-9._-]+)/;

/** 统一错误体（规格 §18.4：`{error:{code,message,request_id}}`）。 */
interface ErrorEnvelope {
  error?: { code?: string; message?: string; request_id?: string };
}

/** 平台 API 错误。带上 code 与 request_id，报障时能直接对上服务端日志。 */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId: string;
  /** 403 时缺少的权限名；解析不出来则为 undefined。 */
  readonly missingScope: string | undefined;

  constructor(status: number, code: string, message: string, requestId = "") {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.requestId = requestId;
    this.missingScope = MISSING_SCOPE_PATTERN.exec(message)?.[1];
  }

  /** 是否值得重试。权限/参数类错误重试多少次都是同一个答案，只会刷日志。 */
  get retryable(): boolean {
    return this.status === NETWORK_STATUS || this.status >= 500;
  }

  /** 是否是鉴权类失败（未认证或无权限）。 */
  get isAuthFailure(): boolean {
    return this.status === 401 || this.status === 403;
  }
}

/** 「这条链路在当前环境未启用」：一整组端点因为对应的连接器/模式=off 没有被
 *  挂载进路由（router.go 的 `if d.PlatformUsers != nil` / `if d.RequestLogs
 *  != nil`），不是「这一次请求失败了」。
 *
 *  调用方只应在明知某个端点组按环境变量可选挂载时才把 404 包装成这个类型
 *  ——具体资源 id 的 404（比如某个用户不存在）不会经过这条路径：那条路由
 *  本来就已经挂载，只是这一个 id 没有记录，与「整组端点都不存在」不是同一件
 *  事。见 api/users.ts、api/requests.ts 里 `looksLikeUnmountedRoute` 的调用点。
 *
 *  `description` 是给人看的完整说明（含哪个环境变量、接入后会怎样），由
 *  调用方按自己的链路填，这一层不猜。 */
export class FeatureNotMountedError extends ApiError {
  readonly description: string;

  constructor(cause: ApiError, description: string) {
    super(cause.status, cause.code, cause.message, cause.requestId);
    this.name = "FeatureNotMountedError";
    this.description = description;
  }
}

/** 404 的响应体不是平台标准错误包（解析不出 `error.code`）。
 *
 *  chi 对没有挂载的路由返回纯文本 404（`"404 page not found"`），这与业务
 *  handler 主动写出的 `{error:{code,message}}` 结构不同（见 WriteError，
 *  internal/platform/httpapi/response.go）——用这层**结构**差异，而不是猜测
 *  响应文案，去分辨「这条链路根本没挂载」与「这个具体资源不存在」。
 *
 *  **只是一个结构信号，不是判定本身**：只有明知按环境变量可选挂载的端点组
 *  （platformusers、requests）的调用方才该把它读成「未接入」；其它端点的 404
 *  没有这层歧义，不要在别处套用这条判据。 */
export function looksLikeUnmountedRoute(error: unknown): error is ApiError {
  return error instanceof ApiError && error.status === 404 && error.code === UNKNOWN_CODE;
}

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

export interface RequestOptions {
  /** 值为 undefined 的项不会拼进 URL——「不传」和「传空串」对后端不是一回事。 */
  searchParams?: Record<string, string | undefined>;
  signal?: AbortSignal;
}

export interface PostOptions extends RequestOptions {
  /** 幂等/追踪用的请求 ID，作为 `X-Request-ID` 发出。
   *
   *  **不放进请求体**：后端 ExecuteActionHandler 用 `DisallowUnknownFields`
   *  解析 `{"params":{…}}`，多一个 request_id 字段会直接 400；它真正的来源是
   *  RequestID 中间件读的 `X-Request-ID` 头（httpapi/middleware.go）。 */
  requestId?: string;
}

export interface ApiClient {
  get<T>(path: string, options?: RequestOptions): Promise<T>;
  post<T>(path: string, body: unknown, options?: PostOptions): Promise<T>;
}

export interface ApiClientOptions {
  config: PlatformApiConfig;
  /** 注入点：测试传 mock fetch，不需要真后端。 */
  fetchImpl?: FetchLike;
  /** local 模式（XM-LOGIN）：会话是 HttpOnly Cookie，不发开发头也不发 Bearer。
   *  true 时每个请求都带 `credentials:"same-origin"` 与 `X-Requested-With: xingmang`
   *  （CSRF 防护，GET 上带着也无害）。 */
  localCredentials?: boolean;
  /** local 模式下收到 401（会话缺失/失效）时触发；不传＝只抛错误、不跳转——
   *  auth/localSession.ts 的登录页探测请求用这个「只抛不跳」的形态，
   *  避免刚提交错误密码就被这层再跳一次登录页。 */
  onLocalSessionLoss?: () => void;
}

/** 仅供显式非生产开发模式使用的身份头。 */
export function devPrincipalHeaders(config: PlatformApiConfig): Record<string, string> {
  return {
    "X-Dev-Principal-ID": config.principalId,
    "X-Dev-Principal-Type": config.principalType,
    "X-Dev-Scopes": config.scopes.join(","),
  };
}

function buildUrl(config: PlatformApiConfig, path: string, params?: RequestOptions["searchParams"]) {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params ?? {})) {
    if (value !== undefined) query.set(key, value);
  }
  const qs = query.toString();
  return `${config.baseUrl}${path}${qs ? `?${qs}` : ""}`;
}

async function toApiError(response: Response): Promise<ApiError> {
  let body: ErrorEnvelope | null = null;
  try {
    body = (await response.json()) as ErrorEnvelope;
  } catch {
    // 错误体不是 JSON（反代返回的 HTML 错误页等）：保留状态码，别把解析失败
    // 伪装成业务错误
    body = null;
  }
  const code = body?.error?.code ?? UNKNOWN_CODE;
  // 兜底文案带上状态码的中文：这一句正是「响应不是平台错误包」时人唯一
  // 能看到的东西，而 404 / 502 这些数字对读不懂英文的人也说不出所以然。
  // 状态码本身逐字保留在前面（httpStatusText 的形制）。
  const message = body?.error?.message ?? `请求失败（${httpStatusText(response.status)}）`;
  return new ApiError(response.status, code, message, body?.error?.request_id ?? "");
}

/** 创建 API 客户端。所有请求都从这里出去，身份头只在这里注入一次。 */
export function createApiClient({
  config,
  fetchImpl,
  localCredentials,
  onLocalSessionLoss,
}: ApiClientOptions): ApiClient {
  async function request<T>(
    path: string,
    init: RequestInit & { headers: Record<string, string> },
    options: RequestOptions,
  ): Promise<T> {
    // 默认实现取调用时刻的 globalThis.fetch 而不是模块加载时刻：
    // 测试可以在 import 之后再替换 fetch
    const doFetch: FetchLike = fetchImpl ?? ((input, i) => globalThis.fetch(input, i));
    const url = buildUrl(config, path, options.searchParams);
    // dev-header／local 分支刻意**不 await**：两者的身份头都是同步拼出来的，
    // fetch 要像以前一样在调用的同一个 tick 发出（有页面测试按这个时序数请求次数）
    const identity = localCredentials ? LOCAL_CSRF_HEADER : devPrincipalHeaders(config);
    const headers = { ...init.headers, ...identity };

    let response: Response;
    try {
      response = await doFetch(url, {
        ...init,
        headers,
        // local 模式的会话是 HttpOnly Cookie：必须显式带 credentials，
        // 否则同源请求默认也不带 Cookie（fetch 的 credentials 默认值是
        // "same-origin"，但显式写出来不依赖这个默认值，其它两种模式不需要它）
        ...(localCredentials ? { credentials: "same-origin" as RequestCredentials } : {}),
        ...(options.signal ? { signal: options.signal } : {}),
      });
    } catch (cause) {
      // AbortError 是调用方主动取消（切页面/换查询），原样抛出，
      // 不要包装成「网络不可用」在界面上吓人
      if (cause instanceof DOMException && cause.name === "AbortError") throw cause;
      throw new ApiError(
        NETWORK_STATUS,
        NETWORK_CODE,
        "无法连接平台 API，请检查服务是否启动或网络是否可达",
      );
    }

    if (!response.ok) {
      const err = await toApiError(response);
      // local 模式只认 401：403 是「登录了但没这个权限」（缺少权限 xxx），
      // 再跳一次登录页解决不了，应该照常显示「无权访问」。而且这里**不带**
      // 「传了 onLocalSessionLoss 才跳」之外的例外——登录页自己的 login()/me()
      // 探测用的是没接 onLocalSessionLoss 的另一个客户端实例（见
      // auth/localSession.ts），所以这里始终可以无条件按 401 判断，不用再
      // 额外区分「是不是登录请求本身」
      if (localCredentials && err.status === 401) onLocalSessionLoss?.();
      throw err;
    }

    try {
      return (await response.json()) as T;
    } catch {
      throw new ApiError(response.status, BAD_RESPONSE_CODE, "平台 API 返回的不是合法 JSON");
    }
  }

  return {
    get<T>(path: string, options: RequestOptions = {}): Promise<T> {
      return request<T>(
        path,
        {
          method: "GET",
          headers: { Accept: "application/json" },
        },
        options,
      );
    },

    /** 写路径。目前只有 Action 执行入口用它（ADR-003：写操作唯一入口）。 */
    post<T>(path: string, body: unknown, options: PostOptions = {}): Promise<T> {
      return request<T>(
        path,
        {
          method: "POST",
          headers: {
            Accept: "application/json",
            "Content-Type": "application/json",
            ...(options.requestId ? { "X-Request-ID": options.requestId } : {}),
          },
          body: JSON.stringify(body),
        },
        options,
      );
    },
  };
}

/** 应用默认客户端。模式在模块加载时定一次：/app-config.js 在业务包之前同步执行，
 *  此刻 window.__XM_CONFIG__ 已经就位；页面生命周期内不会再变。
 *
 *  local 模式下这里接了 onLocalSessionLoss（会跳登录页）：所有业务只读/写路径
 *  （员工账号列表、Action 执行……）都经过这个单例，会话过期就该被带回登录页。
 *  auth/localSession.ts 的 login()/me() 探测用的是另一个**不接**这个回调的客户端
 *  实例，理由见该文件顶部注释。 */
export const apiClient: ApiClient = createApiClient({
  config: appApiConfig,
  ...(getRuntimeConfig().authMode === "local"
    ? { localCredentials: true, onLocalSessionLoss }
    : {}),
});
