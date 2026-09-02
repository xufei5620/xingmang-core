import { describe, expect, it, vi } from "vitest";
import type { ApiClient } from "./client";
import { issueConsoleAssertion } from "./consoleAssertion";

function fakeClient(response: unknown): { client: ApiClient; post: ReturnType<typeof vi.fn> } {
  const post = vi.fn(async () => response);
  return { client: { get: vi.fn(), post } as unknown as ApiClient, post };
}

describe("issueConsoleAssertion（CR-0006 XM-INVCON1）", () => {
  it("POST 到断言签发端点，请求体只带 scope", async () => {
    const { client, post } = fakeClient({ assertion: "jws-value", expires_at: "2026-09-15T10:04:00Z" });
    const result = await issueConsoleAssertion("sub2api", client);
    expect(post).toHaveBeenCalledWith("/api/v1/auth/console-assertion", { scope: "sub2api" });
    expect(result).toEqual({ assertion: "jws-value", expiresAt: "2026-09-15T10:04:00Z" });
  });

  it.each(["sub2api", "newapi", "global"] as const)("%s 三个 scope 都原样透传", async (scope) => {
    const { client, post } = fakeClient({ assertion: "x", expires_at: "2026-09-15T10:04:00Z" });
    await issueConsoleAssertion(scope, client);
    expect(post).toHaveBeenCalledWith("/api/v1/auth/console-assertion", { scope });
  });

  it("响应体形状不对时不抛异常，字段回落成空串", async () => {
    const { client } = fakeClient({});
    const result = await issueConsoleAssertion("sub2api", client);
    expect(result).toEqual({ assertion: "", expiresAt: "" });
  });
});
