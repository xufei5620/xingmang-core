import { getRuntimeConfig } from "../auth/runtimeConfig";
import { oidcBearerProvider } from "../auth/session";
import { appApiConfig, type PlatformApiConfig } from "./config";

/** 网络层根本没通（DNS/连接/CORS/离线）时用的伪状态码。
 *  用 0 而不是编一个 5xx：区分「服务端拒绝了」和「压根没到服务端」，
 *  这两种情况的处置完全不同。 */
export const NETWORK_STATUS = 0;
const NETWORK_CODE = "NETWORK_UNAVAILABLE";
const BAD_RESPONSE_CODE = "BAD_RESPONSE";
const UNKNOWN_CODE = "UNKNOWN";
const UNAUTHENTICATED_CODE = "UNAUTHENTICATED";

/** oidc 模式下服务端拒绝令牌时的两句**固定**文案（oidcauth/resolver.go）。
 *
 *  后端对 OIDC 失败一律回 403 而不是 401（AUTH-SWITCH.md 第八节：仓库的错误
 *  模型里没有 401），所以只盯 401 抓不到「令牌被拒」。文案是服务端常量，
 *  **逐字相等**才算；「缺少权限 xxx」那种真正的授权失败不在此列——那是登录
 *  了但没权限，跳登录页解决不了，该照常显示无权访问。 */
const OIDC_REJECTED_MESSAGES: ReadonlySet<string> = new Set(["缺少身份", "身份令牌无效"]);

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

export type UnauthenticatedReason =
  /** 手头没有可用令牌（没登录、或续期失败会话已被清空）。 */
  | "no_token"
  /** 带着令牌去了，服务端说不认（401，或 oidc 的固定拒绝文案）。 */
  | "rejected";

/** oidc 模式的令牌来源。客户端只管「拿令牌、贴到 Authorization 头、失败时通知」，
 *  会话怎么存、怎么续、往哪跳登录都是 auth/ 目录的事。 */
export interface BearerTokenProvider {
  /** 取可用的访问令牌；没有返回 null（不抛：「没登录」不是异常，是一种状态）。 */
  getAccessToken(): Promise<string | null>;
  /** 会话失效。实现方负责清会话并跳登录页；客户端随后照常抛 ApiError。 */
  onUnauthenticated(reason: UnauthenticatedReason): void;
}

export interface ApiClientOptions {
  config: PlatformApiConfig;
  /** 注入点：测试传 mock fetch，不需要真后端。 */
  fetchImpl?: FetchLike;
  /** 传入即走 oidc：`Authorization: Bearer` 取代三个开发头。
   *  不传＝dev-header 模式，请求形状与 XM-AUTH1 之前逐字一致。 */
  auth?: BearerTokenProvider;
}

/** 开发期身份头。
 *
 *  TODO(XM-0008): 换成 OIDC —— 这三个头由 `Authorization: Bearer <token>` 取代，
 *  后端对应换掉 devHeaderResolver（httpapi/principal.go 已预留 PrincipalResolver
 *  接口，届时 handler 不用改）。生产环境后端硬拒这套头：用请求头自称身份
 *  等于没有鉴权。 */
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
  const message = body?.error?.message ?? `请求失败（HTTP ${response.status}）`;
  return new ApiError(response.status, code, message, body?.error?.request_id ?? "");
}

function isSessionRejection(err: ApiError): boolean {
  return err.status === 401 || (err.status === 403 && OIDC_REJECTED_MESSAGES.has(err.message));
}

/** 创建 API 客户端。所有请求都从这里出去，身份头只在这里注入一次。 */
export function createApiClient({ config, fetchImpl, auth }: ApiClientOptions): ApiClient {
  /** oidc 模式的身份头。取令牌可能要先续期，所以是异步的。 */
  async function bearerHeaders(provider: BearerTokenProvider): Promise<Record<string, string>> {
    const token = await provider.getAccessToken();
    if (!token) {
      // 不发请求：没有令牌的请求在 oidc 后端只会换来一句「缺少身份」，
      // 而调用方真正需要的是被带去登录页
      provider.onUnauthenticated("no_token");
      throw new ApiError(401, UNAUTHENTICATED_CODE, "登录已过期，请重新登录");
    }
    return { Authorization: `Bearer ${token}` };
  }

  async function request<T>(
    path: string,
    init: RequestInit & { headers: Record<string, string> },
    options: RequestOptions,
  ): Promise<T> {
    // 默认实现取调用时刻的 globalThis.fetch 而不是模块加载时刻：
    // 测试可以在 import 之后再替换 fetch
    const doFetch: FetchLike = fetchImpl ?? ((input, i) => globalThis.fetch(input, i));
    const url = buildUrl(config, path, options.searchParams);
    // dev-header 分支刻意**不 await**：开发头是同步拼出来的，fetch 要像以前一样
    // 在调用的同一个 tick 发出（有页面测试按这个时序数请求次数）
    const identity = auth ? await bearerHeaders(auth) : devPrincipalHeaders(config);
    const headers = { ...init.headers, ...identity };

    let response: Response;
    try {
      response = await doFetch(url, {
        ...init,
        headers,
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
      // oidc 模式下令牌被拒＝会话没了：清掉并去登录页（带上 next 回来）。
      // dev-header 模式没有这一步——那套头被拒说明后端配成了别的模式，
      // 跳登录页解决不了，让 403 原样显示出来才看得见问题
      if (auth && isSessionRejection(err)) auth.onUnauthenticated("rejected");
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
 *  此刻 window.__XM_CONFIG__ 已经就位；页面生命周期内不会再变。 */
export const apiClient: ApiClient = createApiClient({
  config: appApiConfig,
  ...(getRuntimeConfig().authMode === "oidc" ? { auth: oidcBearerProvider } : {}),
});
