import { describe, expect, it, vi } from "vitest";
import {
  ApiError,
  createApiClient,
  devPrincipalHeaders,
  NETWORK_STATUS,
  type FetchLike,
  type UnauthenticatedReason,
} from "./client";
import type { PlatformApiConfig } from "./config";

const config: PlatformApiConfig = {
  baseUrl: "",
  principalId: "dev-operator",
  principalType: "HUMAN",
  scopes: ["registry.read", "ops.read"],
};

/** 手写最小 Response 替身：只用到 ok/status/json 三样，
 *  不依赖运行环境是否提供 fetch/Response（jsdom 本身并不提供）。 */
function fakeResponse(status: number, json: () => Promise<unknown>): Response {
  return { ok: status >= 200 && status < 300, status, json } as unknown as Response;
}

function jsonResponse(body: unknown, status = 200): Response {
  return fakeResponse(status, () => Promise.resolve(body));
}

function nonJsonResponse(status: number): Response {
  return fakeResponse(status, () => Promise.reject(new SyntaxError("Unexpected token <")));
}

function mockFetch(...responses: Response[]) {
  const fn = vi.fn<FetchLike>();
  for (const r of responses) fn.mockResolvedValueOnce(r);
  return fn;
}

/** 单独一个「拒绝」的 mock。
 *  不和 mockFetch 合并成 `instanceof Error` 判断：jsdom 里 DOMException
 *  **不是** Error 的实例，那样写会把中止异常当成一个 Response 喂进客户端。 */
function mockFetchRejecting(reason: unknown) {
  return vi.fn<FetchLike>().mockRejectedValue(reason);
}

async function expectApiError(promise: Promise<unknown>): Promise<ApiError> {
  const err = await promise.then(
    () => null,
    (e: unknown) => e,
  );
  expect(err).toBeInstanceOf(ApiError);
  return err as ApiError;
}

describe("devPrincipalHeaders：开发期身份头", () => {
  it("三个头齐全，scope 用逗号拼接", () => {
    expect(devPrincipalHeaders(config)).toEqual({
      "X-Dev-Principal-ID": "dev-operator",
      "X-Dev-Principal-Type": "HUMAN",
      "X-Dev-Scopes": "registry.read,ops.read",
    });
  });

  it("MACHINE 身份类型原样透传（宪法 6 条：人机身份分域）", () => {
    const headers = devPrincipalHeaders({ ...config, principalType: "MACHINE" });
    expect(headers["X-Dev-Principal-Type"]).toBe("MACHINE");
  });
});

describe("createApiClient：每个请求都注入身份头", () => {
  it("GET 请求带上三个身份头与 Accept", async () => {
    const fetchImpl = mockFetch(jsonResponse({ items: [] }));
    await createApiClient({ config, fetchImpl }).get("/api/v1/metrics");

    expect(fetchImpl).toHaveBeenCalledOnce();
    const call = fetchImpl.mock.calls[0];
    expect(call).toBeDefined();
    expect(call?.[0]).toBe("/api/v1/metrics");
    expect(call?.[1]?.method).toBe("GET");
    expect(call?.[1]?.headers).toEqual({
      Accept: "application/json",
      "X-Dev-Principal-ID": "dev-operator",
      "X-Dev-Principal-Type": "HUMAN",
      "X-Dev-Scopes": "registry.read,ops.read",
    });
  });

  it("每一次请求都重新注入，不只是第一次", async () => {
    const fetchImpl = mockFetch(jsonResponse({ items: [] }), jsonResponse({ items: [] }));
    const client = createApiClient({ config, fetchImpl });
    await client.get("/api/v1/metrics");
    await client.get("/api/v1/services");
    expect(fetchImpl).toHaveBeenCalledTimes(2);
    for (const call of fetchImpl.mock.calls) {
      expect(call[1]?.headers).toMatchObject({ "X-Dev-Principal-ID": "dev-operator" });
    }
  });

  it("baseUrl 与查询参数拼接正确；undefined 的参数不进 URL", async () => {
    const fetchImpl = mockFetch(jsonResponse({ items: [] }));
    const client = createApiClient({
      config: { ...config, baseUrl: "https://ops.example.com" },
      fetchImpl,
    });
    await client.get("/api/v1/services", {
      searchParams: { environment: "staging", missing: undefined },
    });
    expect(fetchImpl.mock.calls[0]?.[0]).toBe(
      "https://ops.example.com/api/v1/services?environment=staging",
    );
  });

  it("解析出的 JSON 原样返回", async () => {
    const fetchImpl = mockFetch(jsonResponse({ items: [{ id: "a" }] }));
    const body = await createApiClient({ config, fetchImpl }).get<{ items: Array<{ id: string }> }>(
      "/api/v1/services",
    );
    expect(body.items[0]?.id).toBe("a");
  });
});

describe("createApiClient：POST（写路径）", () => {
  it("请求体只有 params——后端 DisallowUnknownFields，多一个字段就是 400", async () => {
    const fetchImpl = mockFetch(jsonResponse({ action_run_id: "run-1" }));
    await createApiClient({ config, fetchImpl }).post(
      "/api/v1/actions/registry.service.create/versions/1/execute",
      { params: { instance_id: "sub2api-dev" } },
      { requestId: "req-abc" },
    );

    const init = fetchImpl.mock.calls[0]?.[1];
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({ params: { instance_id: "sub2api-dev" } });
  });

  it("request_id 走 X-Request-ID 头而不是请求体（httpapi/middleware.go 读的是头）", async () => {
    const fetchImpl = mockFetch(jsonResponse({ action_run_id: "run-1" }));
    await createApiClient({ config, fetchImpl }).post("/x", { params: {} }, { requestId: "req-7" });

    const headers = fetchImpl.mock.calls[0]?.[1]?.headers as Record<string, string>;
    expect(headers["X-Request-ID"]).toBe("req-7");
    expect(headers["Content-Type"]).toBe("application/json");
    expect(headers["X-Dev-Principal-ID"]).toBe("dev-operator");
    expect(String(fetchImpl.mock.calls[0]?.[1]?.body)).not.toContain("request_id");
  });

  it("没给 requestId 就不带这个头，让后端自己生成一个", async () => {
    const fetchImpl = mockFetch(jsonResponse({}));
    await createApiClient({ config, fetchImpl }).post("/x", {});
    const headers = fetchImpl.mock.calls[0]?.[1]?.headers as Record<string, string>;
    expect("X-Request-ID" in headers).toBe(false);
  });

  it("写路径的错误映射与读路径一致（403 抠得出权限名）", async () => {
    const fetchImpl = mockFetch(
      jsonResponse(
        {
          error: {
            code: "PERMISSION_DENIED",
            message: "缺少权限 registry.service.manage",
            request_id: "req-9",
          },
        },
        403,
      ),
    );
    const api = await expectApiError(createApiClient({ config, fetchImpl }).post("/x", {}));
    expect(api.missingScope).toBe("registry.service.manage");
    expect(api.requestId).toBe("req-9");
  });
});

describe("createApiClient：错误映射", () => {
  it("403 抓出缺少的权限名，供界面直接提示", async () => {
    const fetchImpl = mockFetch(
      jsonResponse(
        { error: { code: "PERMISSION_DENIED", message: "缺少权限 ops.read", request_id: "req-1" } },
        403,
      ),
    );
    const api = await expectApiError(createApiClient({ config, fetchImpl }).get("/api/v1/metrics"));
    expect(api.status).toBe(403);
    expect(api.code).toBe("PERMISSION_DENIED");
    expect(api.missingScope).toBe("ops.read");
    expect(api.requestId).toBe("req-1");
    expect(api.isAuthFailure).toBe(true);
    expect(api.retryable).toBe(false);
  });

  it("跨环境 403 没有权限名时不硬凑一个", async () => {
    const fetchImpl = mockFetch(
      jsonResponse(
        {
          error: {
            code: "PERMISSION_DENIED",
            message: "不允许跨环境读取：调用者身份属于 staging",
          },
        },
        403,
      ),
    );
    const api = await expectApiError(createApiClient({ config, fetchImpl }).get("/api/v1/metrics"));
    expect(api.missingScope).toBeUndefined();
    expect(api.message).toContain("不允许跨环境读取");
  });

  it("401 同样归为鉴权失败", async () => {
    const fetchImpl = mockFetch(
      jsonResponse({ error: { code: "UNAUTHENTICATED", message: "缺少身份" } }, 401),
    );
    const api = await expectApiError(createApiClient({ config, fetchImpl }).get("/api/v1/metrics"));
    expect(api.isAuthFailure).toBe(true);
    expect(api.retryable).toBe(false);
  });

  it("错误体不是 JSON 时保留状态码，不伪装成业务错误", async () => {
    const fetchImpl = mockFetch(nonJsonResponse(502));
    const api = await expectApiError(createApiClient({ config, fetchImpl }).get("/api/v1/metrics"));
    expect(api.status).toBe(502);
    expect(api.code).toBe("UNKNOWN");
    expect(api.retryable).toBe(true);
  });

  it("网络不通用状态码 0，与「服务端拒绝了」区分开", async () => {
    const fetchImpl = mockFetchRejecting(new TypeError("Failed to fetch"));
    const api = await expectApiError(createApiClient({ config, fetchImpl }).get("/api/v1/metrics"));
    expect(api.status).toBe(NETWORK_STATUS);
    expect(api.code).toBe("NETWORK_UNAVAILABLE");
    expect(api.retryable).toBe(true);
    expect(api.isAuthFailure).toBe(false);
  });

  it("主动取消原样抛出 AbortError，不包装成网络故障", async () => {
    const fetchImpl = mockFetchRejecting(new DOMException("aborted", "AbortError"));
    const err = await createApiClient({ config, fetchImpl })
      .get("/api/v1/metrics")
      .then(
        () => null,
        (e: unknown) => e,
      );
    expect(err).toBeInstanceOf(DOMException);
    expect((err as DOMException).name).toBe("AbortError");
  });

  it("2xx 但响应不是 JSON 时给出明确错误", async () => {
    const fetchImpl = mockFetch(nonJsonResponse(200));
    const api = await expectApiError(createApiClient({ config, fetchImpl }).get("/api/v1/metrics"));
    expect(api.code).toBe("BAD_RESPONSE");
  });
});

// ---------------------------------------------------------------------------
// XM-AUTH1：oidc 模式（Authorization: Bearer 取代三个开发头）
// ---------------------------------------------------------------------------

function bearerAuth(token: string | null = "tok-1") {
  return {
    getAccessToken: vi.fn(() => Promise.resolve(token)),
    onUnauthenticated: vi.fn<(reason: UnauthenticatedReason) => void>(),
  };
}

describe("createApiClient：oidc 模式注入 Bearer", () => {
  it("GET 只带 Accept + Authorization，没有任何 X-Dev-* 头", async () => {
    const fetchImpl = mockFetch(jsonResponse({ items: [] }));
    const auth = bearerAuth();
    await createApiClient({ config, fetchImpl, auth }).get("/api/v1/metrics");

    expect(fetchImpl.mock.calls[0]?.[1]?.headers).toEqual({
      Accept: "application/json",
      Authorization: "Bearer tok-1",
    });
    expect(auth.getAccessToken).toHaveBeenCalledOnce();
    expect(auth.onUnauthenticated).not.toHaveBeenCalled();
  });

  it("POST 同样换成 Bearer，X-Request-ID 照旧", async () => {
    const fetchImpl = mockFetch(jsonResponse({ action_run_id: "run-1" }));
    await createApiClient({ config, fetchImpl, auth: bearerAuth() }).post(
      "/x",
      { params: {} },
      { requestId: "req-7" },
    );
    const headers = fetchImpl.mock.calls[0]?.[1]?.headers as Record<string, string>;
    expect(headers.Authorization).toBe("Bearer tok-1");
    expect(headers["X-Request-ID"]).toBe("req-7");
    expect(Object.keys(headers).some((k) => k.startsWith("X-Dev-"))).toBe(false);
  });

  it("每次请求都重新取令牌：续期后的新令牌能用上", async () => {
    const fetchImpl = mockFetch(jsonResponse({}), jsonResponse({}));
    const auth = bearerAuth();
    auth.getAccessToken.mockResolvedValueOnce("tok-1").mockResolvedValueOnce("tok-2");
    const client = createApiClient({ config, fetchImpl, auth });
    await client.get("/a");
    await client.get("/b");
    expect((fetchImpl.mock.calls[1]?.[1]?.headers as Record<string, string>).Authorization).toBe(
      "Bearer tok-2",
    );
  });

  it("没有可用令牌：不发请求，通知会话失效（no_token），抛 401 ApiError", async () => {
    const fetchImpl = mockFetch(jsonResponse({}));
    const auth = bearerAuth(null);
    const api = await expectApiError(createApiClient({ config, fetchImpl, auth }).get("/a"));
    expect(fetchImpl).not.toHaveBeenCalled();
    expect(auth.onUnauthenticated).toHaveBeenCalledWith("no_token");
    expect(api.status).toBe(401);
    expect(api.code).toBe("UNAUTHENTICATED");
    expect(api.isAuthFailure).toBe(true);
  });

  it("服务端 401：通知会话失效（rejected），错误照常抛出", async () => {
    const fetchImpl = mockFetch(
      jsonResponse({ error: { code: "UNAUTHENTICATED", message: "缺少身份" } }, 401),
    );
    const auth = bearerAuth();
    const api = await expectApiError(createApiClient({ config, fetchImpl, auth }).get("/a"));
    expect(auth.onUnauthenticated).toHaveBeenCalledWith("rejected");
    expect(api.status).toBe(401);
  });

  it("后端对 OIDC 失败回的是 403「身份令牌无效」/「缺少身份」：同样按会话失效处理", async () => {
    for (const message of ["身份令牌无效", "缺少身份"]) {
      const fetchImpl = mockFetch(
        jsonResponse({ error: { code: "PERMISSION_DENIED", message } }, 403),
      );
      const auth = bearerAuth();
      await expectApiError(createApiClient({ config, fetchImpl, auth }).get("/a"));
      expect(auth.onUnauthenticated).toHaveBeenCalledWith("rejected");
    }
  });

  it("真正的授权失败（缺少权限 xxx）不跳登录：登录了也没用，该显示无权访问", async () => {
    const fetchImpl = mockFetch(
      jsonResponse({ error: { code: "PERMISSION_DENIED", message: "缺少权限 audit.read" } }, 403),
    );
    const auth = bearerAuth();
    const api = await expectApiError(createApiClient({ config, fetchImpl, auth }).get("/a"));
    expect(auth.onUnauthenticated).not.toHaveBeenCalled();
    expect(api.missingScope).toBe("audit.read");
  });

  it("dev-header 模式（不传 auth）收到 401/403 不做任何跳转，与以前一致", async () => {
    const fetchImpl = mockFetch(
      jsonResponse({ error: { code: "PERMISSION_DENIED", message: "缺少身份" } }, 403),
    );
    const api = await expectApiError(createApiClient({ config, fetchImpl }).get("/a"));
    expect(api.status).toBe(403);
    expect(fetchImpl.mock.calls[0]?.[1]?.headers).toMatchObject({
      "X-Dev-Principal-ID": "dev-operator",
    });
  });
});
