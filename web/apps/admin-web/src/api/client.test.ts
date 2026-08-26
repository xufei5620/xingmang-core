import { describe, expect, it, vi } from "vitest";
import {
  ApiError,
  createApiClient,
  devPrincipalHeaders,
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
