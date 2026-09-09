/** OIDC 授权码 + PKCE（S256）客户端（XM-AUTH1）。
 *
 *  **不引入依赖**：只用 Web Crypto、fetch 与 sessionStorage。Keycloak
 *  `solov-staff` Realm 的 `xingmang-admin-web` 是 public client（CR-0001 §3）：
 *  没有 client secret，防重放全靠 PKCE + state。
 *
 *  令牌放 sessionStorage 而不是 localStorage：关掉标签页就没了，不跨标签
 *  共享，被 XSS 读走的窗口也短一截。代价是每个新标签页要重新走一遍登录——
 *  Keycloak 的 SSO 会话让这一步只是一次跳转，不用再输密码。
 *
 *  **绝不打印令牌**：本模块的任何 console 输出与错误信息都不含令牌片段
 *  （宪法 7 条）。错误信息只描述阶段（发现/交换/续期）与 HTTP 状态。
 *
 *  服务端的校验规则见 docs/modules/httpapi/AUTH-SWITCH.md 第六节：issuer
 *  精确相等、aud 或 azp 绑定 client id。本模块在发现阶段同样要求文档里的
 *  `issuer` 与配置逐字相等——不是不信 Keycloak，是把「配错 Realm」这种事
 *  在登录前就说出来，而不是登录成功后每个请求 403。 */

export interface OidcConfig {
  issuer: string;
  clientId: string;
  /** 空格分隔。必须含 openid，否则没有 id_token，顶栏就没有名字可显示。 */
  scopes: string;
  /** 授权码回调地址，必须在 Keycloak Client 的 Valid redirect URIs 里。 */
  redirectUri: string;
  /** 登出后回到哪里，必须在 Valid post logout redirect URIs 里。 */
  postLogoutRedirectUri: string;
}

export interface OidcSession {
  access_token: string;
  refresh_token?: string;
  id_token?: string;
  /** 绝对时间（epoch 毫秒）。存绝对值而不是 expires_in：读的时候才知道过没过。 */
  expires_at: number;
}

export interface OidcIdentity {
  subject: string | undefined;
  username: string | undefined;
  name: string | undefined;
  /** 顶栏显示用：优先 preferred_username（与审计里的 principal_id 一致），
   *  其次 name，再次 sub。 */
  displayName: string;
}

export interface Discovery {
  issuer: string;
  authorization_endpoint: string;
  token_endpoint: string;
  end_session_endpoint?: string;
}

export type OidcFetch = (input: string, init?: RequestInit) => Promise<Response>;

/** 注入点全部可选：应用用浏览器默认值，测试换成替身。 */
export interface OidcDeps {
  fetchImpl?: OidcFetch;
  storage?: Storage;
  crypto?: Crypto;
  now?: () => number;
  /** 整页跳转（登录/登出都要离开本站）。 */
  navigate?: (url: string) => void;
}

export interface OidcClient {
  /** 跳去 Keycloak。`next` 是登录成功后要回到的站内路径。 */
  login(next?: string): Promise<void>;
  /** 处理 /auth/callback 上的查询参数：校验 state、换令牌、存会话。 */
  handleCallback(params: URLSearchParams): Promise<{ next: string }>;
  /** 取可用的访问令牌；临近过期先续期（单飞）。没有可用令牌返回 null
   *  ——续期失败时会话已被清空，调用方据此跳登录。 */
  getAccessToken(): Promise<string | null>;
  /** 是否有会话（不管访问令牌过没过期：有 refresh_token 就还能续）。 */
  hasSession(): boolean;
  getSession(): OidcSession | null;
  getIdentity(): OidcIdentity | null;
  clearSession(): void;
  /** 清会话并跳 Keycloak 的 end_session_endpoint；发现失败就只回登录页。 */
  logout(): Promise<void>;
  discover(): Promise<Discovery>;
}

export class OidcError extends Error {
  readonly stage: "config" | "discovery" | "authorize" | "callback" | "token" | "refresh";
  constructor(stage: OidcError["stage"], message: string) {
    super(message);
    this.name = "OidcError";
    this.stage = stage;
  }
}

export const SESSION_KEY = "xm_oidc_session";
export const TXN_KEY = "xm_oidc_txn";
/** 距离过期不足这么多毫秒就先续期：后端还容忍 60s 时钟偏移，但那是给
 *  服务端之间用的，前端别把令牌用到最后一秒。 */
export const REFRESH_WINDOW_MS = 60_000;
/** 一次登录事务的有效期：state 放太久等于给攻击者留一个可猜的窗口。 */
const TXN_TTL_MS = 10 * 60_000;

interface LoginTransaction {
  state: string;
  verifier: string;
  next: string;
  created_at: number;
}

// ---------------------------------------------------------------------------
// PKCE 工具（RFC 7636）
// ---------------------------------------------------------------------------

export function base64UrlEncode(bytes: Uint8Array): string {
  let binary = "";
  for (const b of bytes) binary += String.fromCharCode(b);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function base64UrlDecode(text: string): Uint8Array {
  const padded = text.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - (text.length % 4)) % 4);
  const binary = atob(padded);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

/** 32 字节随机数 → 43 个 base64url 字符，落在 RFC 7636 要求的 43–128 之内。 */
export function generateCodeVerifier(cryptoImpl: Crypto = globalThis.crypto): string {
  const bytes = new Uint8Array(32);
  cryptoImpl.getRandomValues(bytes);
  return base64UrlEncode(bytes);
}

export async function computeCodeChallenge(
  verifier: string,
  cryptoImpl: Crypto = globalThis.crypto,
): Promise<string> {
  const digest = await cryptoImpl.subtle.digest("SHA-256", new TextEncoder().encode(verifier));
  return base64UrlEncode(new Uint8Array(digest));
}

export function generateState(cryptoImpl: Crypto = globalThis.crypto): string {
  const bytes = new Uint8Array(16);
  cryptoImpl.getRandomValues(bytes);
  return base64UrlEncode(bytes);
}

/** 只接受站内相对路径：`next` 来自 URL，放行 `https://…` 或 `//…` 就是一个
 *  开放重定向。不合法一律回工作台。 */
export function safeNextPath(raw: string | null | undefined, fallback = "/dashboard"): string {
  if (!raw) return fallback;
  if (!raw.startsWith("/") || raw.startsWith("//") || raw.startsWith("/\\")) return fallback;
  if (raw.startsWith("/login") || raw.startsWith("/auth/")) return fallback;
  return raw;
}

/** 解 JWT 载荷（**不验签**）：只用于顶栏显示名字。谁能看什么由服务端按
 *  自己验过签的令牌裁决，前端这份解析只是界面文案。 */
export function decodeJwtPayload(token: string): Record<string, unknown> | null {
  const parts = token.split(".");
  if (parts.length !== 3 || !parts[1]) return null;
  try {
    const json = new TextDecoder().decode(base64UrlDecode(parts[1]));
    const parsed: unknown = JSON.parse(json);
    return parsed && typeof parsed === "object" ? (parsed as Record<string, unknown>) : null;
  } catch {
    return null;
  }
}

export function identityFromIdToken(idToken: string | undefined): OidcIdentity | null {
  if (!idToken) return null;
  const claims = decodeJwtPayload(idToken);
  if (!claims) return null;
  const pick = (key: string): string | undefined => {
    const v = claims[key];
    return typeof v === "string" && v.trim() ? v.trim() : undefined;
  };
  const subject = pick("sub");
  const username = pick("preferred_username");
  const name = pick("name");
  const displayName = username ?? name ?? subject;
  if (!displayName) return null;
  return { subject, username, name, displayName };
}

// ---------------------------------------------------------------------------
// 客户端
// ---------------------------------------------------------------------------

function readJson<T>(storage: Storage, key: string): T | null {
  try {
    const raw = storage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : null;
  } catch {
    return null;
  }
}

function isSession(value: unknown): value is OidcSession {
  if (!value || typeof value !== "object") return false;
  const s = value as Record<string, unknown>;
  return typeof s.access_token === "string" && typeof s.expires_at === "number";
}

function formEncode(fields: Record<string, string>): string {
  const body = new URLSearchParams();
  for (const [k, v] of Object.entries(fields)) body.set(k, v);
  return body.toString();
}

/** 令牌端点的响应形状（RFC 6749 §5.1）。 */
interface TokenResponse {
  access_token?: unknown;
  refresh_token?: unknown;
  id_token?: unknown;
  expires_in?: unknown;
  error?: unknown;
}

export function createOidcClient(
  config: OidcConfig | (() => OidcConfig),
  deps: OidcDeps = {},
): OidcClient {
  const cfg = () => (typeof config === "function" ? config() : config);
  const now = deps.now ?? (() => Date.now());
  const cryptoImpl = () => deps.crypto ?? globalThis.crypto;
  const storage = () => deps.storage ?? window.sessionStorage;
  // 调用时刻再取 globalThis.fetch：测试可以在 import 之后再替换
  const doFetch: OidcFetch = deps.fetchImpl ?? ((input, init) => globalThis.fetch(input, init));
  const navigate = deps.navigate ?? ((url: string) => window.location.assign(url));

  // 发现文档缓存在实例内存里，按 issuer 键控：应用只有一个实例，页面生命周期
  // 内 issuer 不会变；测试里换 issuer 就是换 Realm，缓存自然失效
  let discoveryCache: { issuer: string; promise: Promise<Discovery> } | null = null;
  let refreshing: Promise<string | null> | null = null;

  function requireConfig(): OidcConfig {
    const c = cfg();
    if (!c.issuer.trim()) throw new OidcError("config", "OIDC issuer 未配置（XM_WEB_OIDC_ISSUER）");
    if (!c.clientId.trim())
      throw new OidcError("config", "OIDC client id 未配置（XM_WEB_OIDC_CLIENT_ID）");
    return c;
  }

  async function discover(): Promise<Discovery> {
    const c = requireConfig();
    if (discoveryCache?.issuer === c.issuer) return discoveryCache.promise;
    const attempt = (async (): Promise<Discovery> => {
      const url = `${c.issuer.replace(/\/+$/, "")}/.well-known/openid-configuration`;
      let response: Response;
      try {
        response = await doFetch(url, { headers: { Accept: "application/json" } });
      } catch {
        throw new OidcError("discovery", "无法连接身份服务（发现文档拉取失败）");
      }
      if (!response.ok) {
        throw new OidcError("discovery", `身份服务发现文档返回 HTTP ${response.status}`);
      }
      let doc: Partial<Discovery>;
      try {
        doc = (await response.json()) as Partial<Discovery>;
      } catch {
        throw new OidcError("discovery", "身份服务发现文档不是合法 JSON");
      }
      if (typeof doc.issuer !== "string" || doc.issuer !== c.issuer) {
        throw new OidcError(
          "discovery",
          `发现文档的 issuer 与配置不一致（配置：${c.issuer}）——多半是 Realm 或地址写错`,
        );
      }
      if (typeof doc.authorization_endpoint !== "string" || typeof doc.token_endpoint !== "string") {
        throw new OidcError("discovery", "发现文档缺少 authorization_endpoint / token_endpoint");
      }
      return {
        issuer: doc.issuer,
        authorization_endpoint: doc.authorization_endpoint,
        token_endpoint: doc.token_endpoint,
        ...(typeof doc.end_session_endpoint === "string"
          ? { end_session_endpoint: doc.end_session_endpoint }
          : {}),
      };
    })();
    // 失败不缓存：Keycloak 抖一下不该让整页直到刷新前都登不进
    const promise = attempt.catch((err: unknown) => {
      if (discoveryCache?.promise === promise) discoveryCache = null;
      throw err;
    });
    discoveryCache = { issuer: c.issuer, promise };
    return promise;
  }

  function getSession(): OidcSession | null {
    const value = readJson<unknown>(storage(), SESSION_KEY);
    return isSession(value) ? value : null;
  }

  function saveSession(session: OidcSession): void {
    storage().setItem(SESSION_KEY, JSON.stringify(session));
  }

  function clearSession(): void {
    try {
      storage().removeItem(SESSION_KEY);
      storage().removeItem(TXN_KEY);
    } catch {
      // 存储不可用时也没什么可清的
    }
  }

  async function requestTokens(
    stage: "token" | "refresh",
    fields: Record<string, string>,
  ): Promise<TokenResponse> {
    const discovery = await discover();
    let response: Response;
    try {
      response = await doFetch(discovery.token_endpoint, {
        method: "POST",
        headers: {
          "Content-Type": "application/x-www-form-urlencoded",
          Accept: "application/json",
        },
        body: formEncode(fields),
      });
    } catch {
      throw new OidcError(stage, "无法连接身份服务（令牌端点请求失败）");
    }
    let body: TokenResponse;
    try {
      body = (await response.json()) as TokenResponse;
    } catch {
      body = {};
    }
    if (!response.ok) {
      // 只透出 OAuth 的 error 码（invalid_grant 之类），不透出 description——
      // 那一句有时会把令牌片段带出来
      const code = typeof body.error === "string" ? body.error : `HTTP ${response.status}`;
      throw new OidcError(stage, `身份服务拒绝了${stage === "refresh" ? "续期" : "换取令牌"}请求（${code}）`);
    }
    if (typeof body.access_token !== "string" || !body.access_token) {
      throw new OidcError(stage, "身份服务的响应里没有 access_token");
    }
    return body;
  }

  function sessionFromTokens(body: TokenResponse, previous: OidcSession | null): OidcSession {
    const expiresIn = typeof body.expires_in === "number" && body.expires_in > 0 ? body.expires_in : 300;
    const refreshToken =
      typeof body.refresh_token === "string" && body.refresh_token
        ? body.refresh_token
        : previous?.refresh_token;
    const idToken =
      typeof body.id_token === "string" && body.id_token ? body.id_token : previous?.id_token;
    return {
      access_token: body.access_token as string,
      expires_at: now() + expiresIn * 1000,
      ...(refreshToken ? { refresh_token: refreshToken } : {}),
      ...(idToken ? { id_token: idToken } : {}),
    };
  }

  async function login(next?: string): Promise<void> {
    const c = requireConfig();
    const discovery = await discover();
    const verifier = generateCodeVerifier(cryptoImpl());
    const challenge = await computeCodeChallenge(verifier, cryptoImpl());
    const state = generateState(cryptoImpl());
    const txn: LoginTransaction = {
      state,
      verifier,
      next: safeNextPath(next),
      created_at: now(),
    };
    storage().setItem(TXN_KEY, JSON.stringify(txn));

    const url = new URL(discovery.authorization_endpoint);
    url.searchParams.set("response_type", "code");
    url.searchParams.set("client_id", c.clientId);
    url.searchParams.set("redirect_uri", c.redirectUri);
    url.searchParams.set("scope", c.scopes);
    url.searchParams.set("state", state);
    url.searchParams.set("code_challenge", challenge);
    url.searchParams.set("code_challenge_method", "S256");
    navigate(url.toString());
  }

  async function handleCallback(params: URLSearchParams): Promise<{ next: string }> {
    const c = requireConfig();
    // 事务一次性：先取先删。回调页刷新第二次就该失败，而不是拿同一个
    // verifier 再换一次
    const txn = readJson<LoginTransaction>(storage(), TXN_KEY);
    storage().removeItem(TXN_KEY);

    const error = params.get("error");
    if (error) {
      throw new OidcError("callback", `身份服务拒绝了登录（${error}）`);
    }
    if (!txn || typeof txn.state !== "string" || typeof txn.verifier !== "string") {
      throw new OidcError(
        "callback",
        "没有进行中的登录事务（回调页被刷新、或浏览器清空了会话存储），请重新登录",
      );
    }
    if (now() - txn.created_at > TXN_TTL_MS) {
      throw new OidcError("callback", "登录事务已超时，请重新登录");
    }
    const state = params.get("state");
    if (!state || state !== txn.state) {
      throw new OidcError("callback", "state 不匹配：回调不是本次登录发起的，已拒绝");
    }
    const code = params.get("code");
    if (!code) {
      throw new OidcError("callback", "回调里没有授权码");
    }

    const body = await requestTokens("token", {
      grant_type: "authorization_code",
      code,
      redirect_uri: c.redirectUri,
      client_id: c.clientId,
      code_verifier: txn.verifier,
    });
    saveSession(sessionFromTokens(body, null));
    return { next: safeNextPath(txn.next) };
  }

  async function refresh(session: OidcSession): Promise<string | null> {
    if (!session.refresh_token) {
      clearSession();
      return null;
    }
    const c = requireConfig();
    try {
      const body = await requestTokens("refresh", {
        grant_type: "refresh_token",
        refresh_token: session.refresh_token,
        client_id: c.clientId,
      });
      const next = sessionFromTokens(body, session);
      saveSession(next);
      return next.access_token;
    } catch {
      // 续期失败＝会话没了（refresh_token 过期/被吊销/Realm 会话被登出）。
      // 清掉，让调用方跳登录；不在这里跳，是因为 API 客户端才知道当前路径
      clearSession();
      return null;
    }
  }

  async function getAccessToken(): Promise<string | null> {
    const session = getSession();
    if (!session) return null;
    if (session.expires_at - now() > REFRESH_WINDOW_MS) return session.access_token;
    // 单飞：并发的几个请求同时发现快过期，只发一次续期，其余等同一个结果。
    // 否则 Keycloak 的 refresh_token 轮换会让第二个续期拿着已作废的旧票失败
    if (!refreshing) {
      refreshing = refresh(session).finally(() => {
        refreshing = null;
      });
    }
    return refreshing;
  }

  async function logout(): Promise<void> {
    const session = getSession();
    clearSession();
    const c = cfg();
    let discovery: Discovery | null = null;
    try {
      discovery = await discover();
    } catch {
      discovery = null;
    }
    if (!discovery?.end_session_endpoint) {
      navigate(c.postLogoutRedirectUri);
      return;
    }
    const url = new URL(discovery.end_session_endpoint);
    url.searchParams.set("post_logout_redirect_uri", c.postLogoutRedirectUri);
    url.searchParams.set("client_id", c.clientId);
    // Keycloak 有 id_token_hint 时不会再弹一屏「确认登出」
    if (session?.id_token) url.searchParams.set("id_token_hint", session.id_token);
    navigate(url.toString());
  }

  return {
    login,
    handleCallback,
    getAccessToken,
    hasSession: () => getSession() !== null,
    getSession,
    getIdentity: () => identityFromIdToken(getSession()?.id_token),
    clearSession,
    logout,
    discover,
  };
}
