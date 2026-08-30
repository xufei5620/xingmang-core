import { describe, expect, it, vi, type Mock } from "vitest";
import {
  computeCodeChallenge,
  createOidcClient,
  decodeJwtPayload,
  generateCodeVerifier,
  generateState,
  identityFromIdToken,
  OidcError,
  REFRESH_WINDOW_MS,
  safeNextPath,
  SESSION_KEY,
  TXN_KEY,
  type OidcConfig,
  type OidcFetch,
  type OidcSession,
} from "./oidc";

// vitest 的 jsdom 环境保留了 Node 的 WebCrypto（含 subtle.digest），PKCE 的 SHA-256 直接用它
const cryptoImpl = globalThis.crypto;

function memoryStorage(): Storage {
  const map = new Map<string, string>();
  return {
    get length() {
      return map.size;
    },
    clear: () => map.clear(),
    getItem: (key) => map.get(key) ?? null,
    key: (index) => [...map.keys()][index] ?? null,
    removeItem: (key) => {
      map.delete(key);
    },
    setItem: (key, value) => {
      map.set(key, String(value));
    },
  };
}

const config: OidcConfig = {
  issuer: "https://auth.example.test/realms/solov-staff",
  clientId: "xingmang-admin-web",
  scopes: "openid profile email",
  redirectUri: "https://admin.example.test/auth/callback",
  postLogoutRedirectUri: "https://admin.example.test/login",
};

const discoveryDoc = {
  issuer: config.issuer,
  authorization_endpoint: `${config.issuer}/protocol/openid-connect/auth`,
  token_endpoint: `${config.issuer}/protocol/openid-connect/token`,
  end_session_endpoint: `${config.issuer}/protocol/openid-connect/logout`,
};

function jsonResponse(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

function base64Url(text: string): string {
  return btoa(text).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

/** 未验签的假 id_token：只有载荷有意义，签名段随便填。 */
function makeIdToken(claims: Record<string, unknown>): string {
  return `${base64Url(JSON.stringify({ alg: "RS256", kid: "k1" }))}.${base64Url(
    JSON.stringify(claims),
  )}.sig`;
}

function tokenBody(overrides: Record<string, unknown> = {}) {
  return {
    access_token: "at-1",
    refresh_token: "rt-1",
    expires_in: 300,
    token_type: "Bearer",
    id_token: makeIdToken({ sub: "u-1", preferred_username: "alice", name: "Alice" }),
    ...overrides,
  };
}

type TokenHandler = (fields: URLSearchParams) => Response;

/** 按 URL 分发的 Keycloak 替身：发现文档 + 令牌端点。 */
function fakeKeycloak(
  onToken: TokenHandler = () => jsonResponse(tokenBody()),
  discovery: unknown = discoveryDoc,
) {
  return vi.fn<OidcFetch>(async (url, init) => {
    if (url.endsWith("/.well-known/openid-configuration")) return jsonResponse(discovery);
    if (url === discoveryDoc.token_endpoint) {
      return onToken(new URLSearchParams(String(init?.body)));
    }
    return jsonResponse({ error: "not_found" }, 404);
  });
}

const NOW = 1_700_000_000_000;

function setup(
  options: { fetchImpl?: Mock<OidcFetch>; now?: () => number; storage?: Storage } = {},
) {
  const storage = options.storage ?? memoryStorage();
  const navigate = vi.fn<(url: string) => void>();
  const fetchImpl = options.fetchImpl ?? fakeKeycloak();
  const client = createOidcClient(config, {
    fetchImpl,
    storage,
    crypto: cryptoImpl,
    navigate,
    now: options.now ?? (() => NOW),
  });
  return { client, storage, navigate, fetchImpl };
}

function tokenCalls(fetchImpl: ReturnType<typeof fakeKeycloak>) {
  return fetchImpl.mock.calls.filter(([url]) => url === discoveryDoc.token_endpoint);
}

function discoveryCalls(fetchImpl: ReturnType<typeof fakeKeycloak>) {
  return fetchImpl.mock.calls.filter(([url]) => url.endsWith("/.well-known/openid-configuration"));
}

/** 走完 login()，把事务与跳转地址取出来。 */
async function startLogin(client: ReturnType<typeof setup>["client"], storage: Storage, navigate: ReturnType<typeof setup>["navigate"], next?: string) {
  await client.login(next);
  const txn = JSON.parse(storage.getItem(TXN_KEY) ?? "null") as {
    state: string;
    verifier: string;
    next: string;
  };
  const target = new URL(String(navigate.mock.calls[0]?.[0]));
  return { txn, target };
}

async function expectOidcError(promise: Promise<unknown>): Promise<OidcError> {
  const err = await promise.then(
    () => null,
    (e: unknown) => e,
  );
  expect(err).toBeInstanceOf(OidcError);
  return err as OidcError;
}

describe("PKCE 工具（RFC 7636）", () => {
  it("code_verifier 是 43 个 base64url 字符（32 字节随机数），每次不同", () => {
    const a = generateCodeVerifier(cryptoImpl);
    const b = generateCodeVerifier(cryptoImpl);
    expect(a).toMatch(/^[A-Za-z0-9_-]{43}$/);
    expect(b).toMatch(/^[A-Za-z0-9_-]{43}$/);
    expect(a).not.toBe(b);
  });

  it("code_challenge = base64url(SHA-256(verifier))，对上 RFC 7636 附录 B 的向量", async () => {
    const challenge = await computeCodeChallenge(
      "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk",
      cryptoImpl,
    );
    expect(challenge).toBe("E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM");
  });

  it("state 也是不可猜的随机串", () => {
    expect(generateState(cryptoImpl)).toMatch(/^[A-Za-z0-9_-]{22}$/);
    expect(generateState(cryptoImpl)).not.toBe(generateState(cryptoImpl));
  });
});

describe("safeNextPath：登录后回跳只接受站内路径", () => {
  it("站内相对路径原样放行", () => {
    expect(safeNextPath("/platforms/sub2api?tab=users")).toBe("/platforms/sub2api?tab=users");
  });

  it("绝对地址、协议相对地址、登录页自身都回工作台（开放重定向）", () => {
    expect(safeNextPath("https://evil.example/x")).toBe("/dashboard");
    expect(safeNextPath("//evil.example/x")).toBe("/dashboard");
    expect(safeNextPath("/login?next=/x")).toBe("/dashboard");
    expect(safeNextPath("/auth/callback")).toBe("/dashboard");
    expect(safeNextPath(null)).toBe("/dashboard");
    expect(safeNextPath("")).toBe("/dashboard");
  });
});

describe("login()：发现 + 跳转授权端点", () => {
  it("带着 PKCE S256 与 state 跳到 authorization_endpoint，事务存在 sessionStorage", async () => {
    const { client, storage, navigate } = setup();
    const { txn, target } = await startLogin(client, storage, navigate, "/alerts?x=1");

    expect(`${target.origin}${target.pathname}`).toBe(discoveryDoc.authorization_endpoint);
    expect(target.searchParams.get("response_type")).toBe("code");
    expect(target.searchParams.get("client_id")).toBe("xingmang-admin-web");
    expect(target.searchParams.get("redirect_uri")).toBe(config.redirectUri);
    expect(target.searchParams.get("scope")).toBe("openid profile email");
    expect(target.searchParams.get("code_challenge_method")).toBe("S256");
    expect(target.searchParams.get("state")).toBe(txn.state);
    expect(target.searchParams.get("code_challenge")).toBe(
      await computeCodeChallenge(txn.verifier, cryptoImpl),
    );
    // verifier 只留在本地，绝不出现在跳转地址里
    expect(target.toString()).not.toContain(txn.verifier);
    expect(txn.next).toBe("/alerts?x=1");
  });

  it("发现文档缓存在内存里：两次登录只拉一次", async () => {
    const { client, fetchImpl } = setup();
    await client.login();
    await client.login();
    expect(discoveryCalls(fetchImpl)).toHaveLength(1);
  });

  it("发现文档的 issuer 与配置不一致就拒绝，不跳转（配错 Realm 在登录前说出来）", async () => {
    const fetchImpl = fakeKeycloak(undefined, { ...discoveryDoc, issuer: `${config.issuer}-test` });
    const { client, navigate } = setup({ fetchImpl });
    const err = await expectOidcError(client.login());
    expect(err.stage).toBe("discovery");
    expect(navigate).not.toHaveBeenCalled();
  });

  it("发现失败不缓存：身份服务恢复后下一次登录能成功", async () => {
    let failing = true;
    const fetchImpl = vi.fn<OidcFetch>(async (url, init) => {
      if (failing) throw new TypeError("Failed to fetch");
      return fakeKeycloak()(url, init);
    });
    const { client, navigate } = setup({ fetchImpl });
    await expectOidcError(client.login());
    failing = false;
    await client.login();
    expect(navigate).toHaveBeenCalledOnce();
  });

  it("没配 issuer / client id 时给出明确错误", async () => {
    const client = createOidcClient(
      { ...config, issuer: "" },
      { fetchImpl: fakeKeycloak(), storage: memoryStorage(), crypto: cryptoImpl, navigate: vi.fn() },
    );
    const err = await expectOidcError(client.login());
    expect(err.stage).toBe("config");
    expect(err.message).toContain("XM_WEB_OIDC_ISSUER");
  });
});

describe("handleCallback()：校验 state、换令牌", () => {
  it("state 匹配时用 code_verifier 换令牌（public client，无 secret），会话落 sessionStorage", async () => {
    const { client, storage, navigate, fetchImpl } = setup();
    const { txn } = await startLogin(client, storage, navigate, "/audit");

    const result = await client.handleCallback(
      new URLSearchParams({ code: "code-1", state: txn.state }),
    );
    expect(result).toEqual({ next: "/audit" });

    const [call] = tokenCalls(fetchImpl);
    expect(call).toBeDefined();
    const init = call?.[1];
    expect(init?.method).toBe("POST");
    expect((init?.headers as Record<string, string>)["Content-Type"]).toBe(
      "application/x-www-form-urlencoded",
    );
    const fields = new URLSearchParams(String(init?.body));
    expect(fields.get("grant_type")).toBe("authorization_code");
    expect(fields.get("code")).toBe("code-1");
    expect(fields.get("redirect_uri")).toBe(config.redirectUri);
    expect(fields.get("client_id")).toBe("xingmang-admin-web");
    expect(fields.get("code_verifier")).toBe(txn.verifier);
    expect(fields.has("client_secret")).toBe(false);

    const session = JSON.parse(storage.getItem(SESSION_KEY) ?? "null") as OidcSession;
    expect(session.access_token).toBe("at-1");
    expect(session.refresh_token).toBe("rt-1");
    expect(session.expires_at).toBe(NOW + 300_000);
    expect(session.id_token).toBeDefined();
    // 事务一次性：换完就删
    expect(storage.getItem(TXN_KEY)).toBeNull();
    expect(client.hasSession()).toBe(true);
    expect(client.getIdentity()?.displayName).toBe("alice");
  });

  it("state 不匹配：拒绝、不发令牌请求、事务作废", async () => {
    const { client, storage, navigate, fetchImpl } = setup();
    await startLogin(client, storage, navigate);

    const err = await expectOidcError(
      client.handleCallback(new URLSearchParams({ code: "code-1", state: "forged" })),
    );
    expect(err.stage).toBe("callback");
    expect(err.message).toContain("state 不匹配");
    expect(tokenCalls(fetchImpl)).toHaveLength(0);
    expect(storage.getItem(TXN_KEY)).toBeNull();
    expect(client.hasSession()).toBe(false);
  });

  it("身份服务带 error 回来（用户取消 / 被拒）时如实报错", async () => {
    const { client, storage, navigate } = setup();
    const { txn } = await startLogin(client, storage, navigate);
    const err = await expectOidcError(
      client.handleCallback(new URLSearchParams({ error: "access_denied", state: txn.state })),
    );
    expect(err.message).toContain("access_denied");
  });

  it("没有进行中的事务（回调页刷新）时拒绝，而不是拿着 code 硬换", async () => {
    const { client, fetchImpl } = setup();
    const err = await expectOidcError(
      client.handleCallback(new URLSearchParams({ code: "c", state: "s" })),
    );
    expect(err.message).toContain("没有进行中的登录事务");
    expect(tokenCalls(fetchImpl)).toHaveLength(0);
  });

  it("令牌端点拒绝（invalid_grant）时不存会话，错误里只有 OAuth 错误码", async () => {
    const fetchImpl = fakeKeycloak(() =>
      jsonResponse({ error: "invalid_grant", error_description: "Code not valid" }, 400),
    );
    const { client, storage, navigate } = setup({ fetchImpl });
    const { txn } = await startLogin(client, storage, navigate);
    const err = await expectOidcError(
      client.handleCallback(new URLSearchParams({ code: "bad", state: txn.state })),
    );
    expect(err.stage).toBe("token");
    expect(err.message).toContain("invalid_grant");
    expect(err.message).not.toContain("Code not valid");
    expect(storage.getItem(SESSION_KEY)).toBeNull();
  });
});

function seedSession(storage: Storage, session: Partial<OidcSession> = {}) {
  storage.setItem(
    SESSION_KEY,
    JSON.stringify({
      access_token: "at-old",
      refresh_token: "rt-old",
      id_token: makeIdToken({ sub: "u-1", preferred_username: "alice" }),
      expires_at: NOW + 5 * 60_000,
      ...session,
    } satisfies OidcSession),
  );
}

describe("getAccessToken()：续期", () => {
  it("没有会话返回 null，不发请求", async () => {
    const { client, fetchImpl } = setup();
    expect(await client.getAccessToken()).toBeNull();
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("离过期还早就直接给当前令牌，不发请求", async () => {
    const { client, storage, fetchImpl } = setup();
    seedSession(storage);
    expect(await client.getAccessToken()).toBe("at-old");
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("距过期不足 60s 先用 refresh_token 续期，新会话落盘并保留旧 id_token", async () => {
    const fetchImpl = fakeKeycloak(() =>
      jsonResponse({ access_token: "at-new", refresh_token: "rt-new", expires_in: 300 }),
    );
    const { client, storage } = setup({ fetchImpl });
    seedSession(storage, { expires_at: NOW + REFRESH_WINDOW_MS - 1 });

    expect(await client.getAccessToken()).toBe("at-new");

    const fields = new URLSearchParams(String(tokenCalls(fetchImpl)[0]?.[1]?.body));
    expect(fields.get("grant_type")).toBe("refresh_token");
    expect(fields.get("refresh_token")).toBe("rt-old");
    expect(fields.get("client_id")).toBe("xingmang-admin-web");

    const session = JSON.parse(storage.getItem(SESSION_KEY) ?? "null") as OidcSession;
    expect(session.access_token).toBe("at-new");
    expect(session.refresh_token).toBe("rt-new");
    expect(session.expires_at).toBe(NOW + 300_000);
    expect(session.id_token).toBeDefined();
  });

  it("单飞：并发取令牌只发一次续期请求，大家拿同一个结果", async () => {
    const fetchImpl = fakeKeycloak(() =>
      jsonResponse({ access_token: "at-new", refresh_token: "rt-new", expires_in: 300 }),
    );
    const { client, storage } = setup({ fetchImpl });
    seedSession(storage, { expires_at: NOW + 1000 });

    const results = await Promise.all([
      client.getAccessToken(),
      client.getAccessToken(),
      client.getAccessToken(),
    ]);
    expect(results).toEqual(["at-new", "at-new", "at-new"]);
    expect(tokenCalls(fetchImpl)).toHaveLength(1);
  });

  it("续期被拒（refresh_token 过期/吊销）：清会话、返回 null，让调用方去登录", async () => {
    const fetchImpl = fakeKeycloak(() => jsonResponse({ error: "invalid_grant" }, 400));
    const { client, storage } = setup({ fetchImpl });
    seedSession(storage, { expires_at: NOW + 1000 });

    expect(await client.getAccessToken()).toBeNull();
    expect(storage.getItem(SESSION_KEY)).toBeNull();
    expect(client.hasSession()).toBe(false);
  });

  it("没有 refresh_token 又快过期：同样清会话返回 null", async () => {
    const { client, storage, fetchImpl } = setup();
    seedSession(storage, { expires_at: NOW + 1000, refresh_token: undefined });
    expect(await client.getAccessToken()).toBeNull();
    expect(tokenCalls(fetchImpl)).toHaveLength(0);
    expect(client.hasSession()).toBe(false);
  });
});

describe("logout()", () => {
  it("清会话并跳 end_session_endpoint，带 post_logout_redirect_uri / client_id / id_token_hint", async () => {
    const { client, storage, navigate } = setup();
    seedSession(storage);
    const idToken = client.getSession()?.id_token;

    await client.logout();

    expect(storage.getItem(SESSION_KEY)).toBeNull();
    const target = new URL(String(navigate.mock.calls[0]?.[0]));
    expect(`${target.origin}${target.pathname}`).toBe(discoveryDoc.end_session_endpoint);
    expect(target.searchParams.get("post_logout_redirect_uri")).toBe(config.postLogoutRedirectUri);
    expect(target.searchParams.get("client_id")).toBe("xingmang-admin-web");
    expect(target.searchParams.get("id_token_hint")).toBe(idToken);
  });

  it("身份服务不可达时也把本地会话清掉，只回登录页", async () => {
    const fetchImpl = vi.fn<OidcFetch>(() => Promise.reject(new TypeError("Failed to fetch")));
    const { client, storage, navigate } = setup({ fetchImpl });
    seedSession(storage);

    await client.logout();

    expect(storage.getItem(SESSION_KEY)).toBeNull();
    expect(navigate).toHaveBeenCalledWith(config.postLogoutRedirectUri);
  });
});

describe("id_token 解析（只用于显示，不验签）", () => {
  it("preferred_username 优先，其次 name，再次 sub", () => {
    expect(
      identityFromIdToken(makeIdToken({ sub: "u", preferred_username: "alice", name: "Alice" }))
        ?.displayName,
    ).toBe("alice");
    expect(identityFromIdToken(makeIdToken({ sub: "u", name: "Alice" }))?.displayName).toBe(
      "Alice",
    );
    expect(identityFromIdToken(makeIdToken({ sub: "u" }))?.displayName).toBe("u");
  });

  it("不是三段式 JWT 或载荷不是 JSON 时返回 null，而不是抛出", () => {
    expect(decodeJwtPayload("garbage")).toBeNull();
    expect(decodeJwtPayload("a.b.c")).toBeNull();
    expect(identityFromIdToken(undefined)).toBeNull();
    expect(identityFromIdToken(makeIdToken({}))).toBeNull();
  });
});
