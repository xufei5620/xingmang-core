import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import { cachedLocalUser, changePassword, logout, me, setCachedLocalUser } from "./localSession";

function response(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

describe("auth/localSession", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    setCachedLocalUser(null);
  });

  it("me() 成功：投影字段并写入内存缓存", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          response({
            username: "alice",
            display_name: "Alice",
            roles: ["staff", "admin"],
            must_change_password: false,
          }),
        ),
      ),
    );
    const user = await me();
    expect(user).toEqual({
      username: "alice",
      display_name: "Alice",
      roles: ["staff", "admin"],
      must_change_password: false,
    });
    expect(cachedLocalUser()).toEqual(user);
  });

  it("me() 失败（401，没有有效会话）：抛出 ApiError，不写缓存", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(response({ error: { code: "UNAUTHENTICATED" } }, 401))),
    );
    await expect(me()).rejects.toBeInstanceOf(ApiError);
    expect(cachedLocalUser()).toBeNull();
  });

  it("display_name 缺失时回落到 username", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(response({ username: "carol", roles: [], must_change_password: false })),
      ),
    );
    const user = await me();
    expect(user.display_name).toBe("carol");
  });

  it("logout()：清空内存缓存，即使服务端已经没有会话（401）也不抛错", async () => {
    setCachedLocalUser({ username: "alice", display_name: "Alice", roles: [], must_change_password: false });
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(response({ error: { code: "UNAUTHENTICATED" } }, 401))),
    );
    await expect(logout()).resolves.toBeUndefined();
    expect(cachedLocalUser()).toBeNull();
  });

  it("logout()：非鉴权类错误（网络故障）仍然抛出，但缓存照样先清空", async () => {
    setCachedLocalUser({ username: "alice", display_name: "Alice", roles: [], must_change_password: false });
    vi.stubGlobal("fetch", vi.fn(() => Promise.reject(new TypeError("Failed to fetch"))));
    await expect(logout()).rejects.toBeInstanceOf(ApiError);
    expect(cachedLocalUser()).toBeNull();
  });

  it("changePassword()：请求体是 current_password/new_password 两个字段，且带 credentials", async () => {
    const fetchMock = vi.fn(() => Promise.resolve(response({})));
    vi.stubGlobal("fetch", fetchMock);
    await changePassword("old-pass-1", "new-pass-12");
    const call = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(call[0]).toContain("/api/v1/auth/password");
    expect(call[1].credentials).toBe("same-origin");
    expect(JSON.parse(call[1].body as string)).toEqual({
      current_password: "old-pass-1",
      new_password: "new-pass-12",
    });
  });

  it("changePassword()：成功后清掉内存缓存里的 must_change_password 标记", async () => {
    setCachedLocalUser({ username: "alice", display_name: "Alice", roles: [], must_change_password: true });
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response({}))));
    await changePassword("old-pass-1", "new-pass-12");
    expect(cachedLocalUser()?.must_change_password).toBe(false);
  });
});
