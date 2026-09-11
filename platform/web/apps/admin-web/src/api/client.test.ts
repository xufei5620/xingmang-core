import { describe, expect, it, vi } from "vitest";
import {
  ApiError,
  createApiClient,
  devPrincipalHeaders,
  FeatureNotMountedError,
  looksLikeUnmountedRoute,
  NETWORK_STATUS,
  type FetchLike,
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

describe("local 模式（XM-LOGIN）：Cookie 会话 + CSRF 头", () => {
  it("GET 请求带 X-Requested-With 与 credentials:same-origin，不带开发头", async () => {
    const fetchImpl = mockFetch(jsonResponse({ items: [] }));
    await createApiClient({ config, fetchImpl, localCredentials: true }).get(
      "/api/v1/staff/accounts",
    );

    expect(fetchImpl).toHaveBeenCalledOnce();
    const call = fetchImpl.mock.calls[0];
    expect(call?.[1]?.headers).toEqual({
      Accept: "application/json",
      "X-Requested-With": "xingmang",
    });
    expect(call?.[1]?.credentials).toBe("same-origin");
  });

  it("POST 请求同样带 X-Requested-With 与 credentials，不带开发头也不带 Bearer", async () => {
    const fetchImpl = mockFetch(jsonResponse({ username: "alice" }));
    await createApiClient({ config, fetchImpl, localCredentials: true }).post(
      "/api/v1/auth/login",
      { username: "alice", password: "hunter2000" },
    );

    const call = fetchImpl.mock.calls[0];
    const headers = call?.[1]?.headers as Record<string, string>;
    expect(headers["X-Requested-With"]).toBe("xingmang");
    expect(call?.[1]?.credentials).toBe("same-origin");
    expect(headers["X-Dev-Principal-ID"]).toBeUndefined();
    expect(headers.Authorization).toBeUndefined();
  });

  it("dev-header 分支不受影响：不传 localCredentials 时请求形状逐字照旧（不带 credentials 字段）", async () => {
    const fetchImpl = mockFetch(jsonResponse({ items: [] }));
    await createApiClient({ config, fetchImpl }).get("/api/v1/services");
    const call = fetchImpl.mock.calls[0];
    expect(call?.[1]?.credentials).toBeUndefined();
    expect((call?.[1]?.headers as Record<string, string>)["X-Requested-With"]).toBeUndefined();
  });

  it("401（会话缺失/失效）触发 onLocalSessionLoss", async () => {
    const fetchImpl = mockFetch(
      jsonResponse({ error: { code: "UNAUTHENTICATED" } }, 401),
    );
    const onLocalSessionLoss = vi.fn();
    const err = await expectApiError(
      createApiClient({ config, fetchImpl, localCredentials: true, onLocalSessionLoss }).get(
        "/api/v1/staff/accounts",
      ),
    );
    expect(err.status).toBe(401);
    expect(onLocalSessionLoss).toHaveBeenCalledOnce();
  });

  it("403（登录了但没这个权限）不触发 onLocalSessionLoss——跳登录页解决不了", async () => {
    const fetchImpl = mockFetch(
      jsonResponse({ error: { code: "PERMISSION_DENIED", message: "缺少权限 staff.manage" } }, 403),
    );
    const onLocalSessionLoss = vi.fn();
    const err = await expectApiError(
      createApiClient({ config, fetchImpl, localCredentials: true, onLocalSessionLoss }).get(
        "/api/v1/staff/accounts",
      ),
    );
    expect(err.missingScope).toBe("staff.manage");
    expect(onLocalSessionLoss).not.toHaveBeenCalled();
  });

  it("不传 onLocalSessionLoss 时 401 只抛错误、不报错——登录页自己的 login()/me() 探测用这个形态", async () => {
    const fetchImpl = mockFetch(jsonResponse({ error: { code: "INVALID_CREDENTIALS" } }, 401));
    const err = await expectApiError(
      createApiClient({ config, fetchImpl, localCredentials: true }).post("/api/v1/auth/login", {
        username: "alice",
        password: "wrong-password",
      }),
    );
    expect(err.code).toBe("INVALID_CREDENTIALS");
  });
});

// ---------------------------------------------------------------------------
// XM-UX-OFFSTATE：区分「整组端点没挂载」与「这个具体资源不存在/请求失败」
// ---------------------------------------------------------------------------

describe("looksLikeUnmountedRoute：未挂载路由的结构判据", () => {
  it("chi 对未挂载路由回的纯文本 404（JSON 解析失败）判定为真", async () => {
    // 真实场景：XM_PLATFORM_USERS_MODE=off / XM_REQLOG_MODE=off 时整组端点
    // 不挂载，chi 的默认 NotFoundHandler 写纯文本，不是 JSON
    const fetchImpl = mockFetch(nonJsonResponse(404));
    const err = await expectApiError(createApiClient({ config, fetchImpl }).get("/api/v1/x"));
    expect(err.code).toBe("UNKNOWN");
    expect(looksLikeUnmountedRoute(err)).toBe(true);
  });

  it("JSON 响应体但没有 error.code 时同样判定为真（不是标准错误包）", () => {
    const err = new ApiError(404, "UNKNOWN", "请求失败（HTTP 404）");
    expect(looksLikeUnmountedRoute(err)).toBe(true);
  });

  it("已挂载路由主动写出的带 code 的 404（比如具体用户/请求不存在）判定为假", () => {
    const err = new ApiError(404, "ACTION_NOT_REGISTERED", "没有这条用户记录", "req-1");
    expect(looksLikeUnmountedRoute(err)).toBe(false);
  });

  it("非 404 状态码判定为假，即便响应体同样不是 JSON", async () => {
    const fetchImpl = mockFetch(nonJsonResponse(502));
    const err = await expectApiError(createApiClient({ config, fetchImpl }).get("/api/v1/x"));
    expect(looksLikeUnmountedRoute(err)).toBe(false);
  });

  it("非 ApiError 输入判定为假", () => {
    expect(looksLikeUnmountedRoute(new Error("boom"))).toBe(false);
    expect(looksLikeUnmountedRoute(null)).toBe(false);
    expect(looksLikeUnmountedRoute(undefined)).toBe(false);
  });
});

describe("FeatureNotMountedError：把结构信号翻译成调用方能渲染的类型", () => {
  it("保留原始 ApiError 的 status/code/message/requestId，并附上人话说明", () => {
    const cause = new ApiError(404, "UNKNOWN", "请求失败（HTTP 404）", "req-9");
    const err = new FeatureNotMountedError(cause, "用户管理在当前环境未启用（XM_PLATFORM_USERS_MODE=off）。");

    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(404);
    expect(err.code).toBe("UNKNOWN");
    expect(err.requestId).toBe("req-9");
    expect(err.description).toContain("XM_PLATFORM_USERS_MODE=off");
    // retryable/isAuthFailure 等 getter 沿用 ApiError 的实现，不必重新声明；
    // 404 既不是网络不通也不是 5xx，重试没有意义
    expect(err.retryable).toBe(false);
    expect(err.isAuthFailure).toBe(false);
  });
});
