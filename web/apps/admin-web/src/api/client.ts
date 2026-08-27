import { appApiConfig, type PlatformApiConfig } from "./config";

/** 网络层根本没通（DNS/连接/CORS/离线）时用的伪状态码。
 *  用 0 而不是编一个 5xx：区分「服务端拒绝了」和「压根没到服务端」，
 *  这两种情况的处置完全不同。 */
export const NETWORK_STATUS = 0;
const NETWORK_CODE = "NETWORK_UNAVAILABLE";
const BAD_RESPONSE_CODE = "BAD_RESPONSE";
const UNKNOWN_CODE = "UNKNOWN";

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

export interface ApiClientOptions {
  config: PlatformApiConfig;
  /** 注入点：测试传 mock fetch，不需要真后端。 */
  fetchImpl?: FetchLike;
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

/** 创建 API 客户端。所有请求都从这里出去，身份头只在这里注入一次。 */
export function createApiClient({ config, fetchImpl }: ApiClientOptions): ApiClient {
  async function request<T>(path: string, init: RequestInit, options: RequestOptions): Promise<T> {
    // 默认实现取调用时刻的 globalThis.fetch 而不是模块加载时刻：
    // 测试可以在 import 之后再替换 fetch
    const doFetch: FetchLike = fetchImpl ?? ((input, i) => globalThis.fetch(input, i));
    const url = buildUrl(config, path, options.searchParams);

    let response: Response;
    try {
      response = await doFetch(url, {
        ...init,
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

    if (!response.ok) throw await toApiError(response);

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
          headers: { Accept: "application/json", ...devPrincipalHeaders(config) },
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
            ...devPrincipalHeaders(config),
            ...(options.requestId ? { "X-Request-ID": options.requestId } : {}),
          },
          body: JSON.stringify(body),
        },
        options,
      );
    },
  };
}

/** 应用默认客户端。 */
export const apiClient: ApiClient = createApiClient({ config: appApiConfig });
