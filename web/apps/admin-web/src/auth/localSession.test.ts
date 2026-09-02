import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import {
  cachedLocalUser,
  changePassword,
  completeTotpLogin,
  login,
  logout,
  me,
  setCachedLocalUser,
  type LocalUser,
} from "./localSession";

function response(body: unknown, status = 200): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response;
}

/** 测试专用的完整 LocalUser 构造：只需要覆盖关心的字段，其余用安全默认值
 *  填满，避免每个用例都要抄一遍 TOTP 那四个字段。 */
function testUser(overrides: Partial<LocalUser> = {}): LocalUser {
  return {
    username: "alice",
    display_name: "Alice",
    roles: [],
    must_change_password: false,
    totp_enrolled: false,
    must_enroll_totp: false,
    totp_enrolled_at: null,
    recovery_codes_remaining: null,
    ...overrides,
  };
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
      totp_enrolled: false,
      must_enroll_totp: false,
      totp_enrolled_at: null,
      recovery_codes_remaining: null,
    });
    expect(cachedLocalUser()).toEqual(user);
  });

  it("me() 投影 TOTP 状态字段", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          response({
            username: "carol",
            display_name: "Carol",
            roles: ["admin"],
            must_change_password: false,
            totp_enrolled: true,
            must_enroll_totp: false,
            totp_enrolled_at: "2026-09-02T00:00:00Z",
            recovery_codes_remaining: 7,
          }),
        ),
      ),
    );
    const user = await me();
    expect(user.totp_enrolled).toBe(true);
    expect(user.totp_enrolled_at).toBe("2026-09-02T00:00:00Z");
    expect(user.recovery_codes_remaining).toBe(7);
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
    setCachedLocalUser(testUser());
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(response({ error: { code: "UNAUTHENTICATED" } }, 401))),
    );
    await expect(logout()).resolves.toBeUndefined();
    expect(cachedLocalUser()).toBeNull();
  });

  it("logout()：非鉴权类错误（网络故障）仍然抛出，但缓存照样先清空", async () => {
    setCachedLocalUser(testUser());
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
    setCachedLocalUser(testUser({ must_change_password: true }));
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(response({}))));
    await changePassword("old-pass-1", "new-pass-12");
    expect(cachedLocalUser()?.must_change_password).toBe(false);
  });

  it("login()：未启用 TOTP 时一步完成，返回 kind=authenticated 并写缓存", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          response({ username: "dave", display_name: "Dave", roles: ["staff"], must_change_password: false }),
        ),
      ),
    );
    const outcome = await login("dave", "correct-password-123");
    expect(outcome.kind).toBe("authenticated");
    if (outcome.kind !== "authenticated") throw new Error("unreachable");
    expect(outcome.user.username).toBe("dave");
    expect(cachedLocalUser()?.username).toBe("dave");
  });

  it("login()：已启用 TOTP 时返回 kind=totp_required，不写缓存", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(response({ requires_totp: true, temp_token: "temp-abc", username: "carol" })),
      ),
    );
    const outcome = await login("carol", "correct-password-123");
    expect(outcome.kind).toBe("totp_required");
    if (outcome.kind !== "totp_required") throw new Error("unreachable");
    expect(outcome.challenge).toEqual({ tempToken: "temp-abc", username: "carol" });
    expect(cachedLocalUser()).toBeNull();
  });

  it("completeTotpLogin()：请求体带 temp_token 与 code，成功后写缓存", async () => {
    const fetchMock = vi.fn(() =>
      Promise.resolve(
        response({
          username: "carol",
          display_name: "Carol",
          roles: ["admin"],
          must_change_password: false,
          totp_enrolled: true,
        }),
      ),
    );
    vi.stubGlobal("fetch", fetchMock);
    const user = await completeTotpLogin("temp-abc", { code: "123456" });
    const call = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(call[0]).toContain("/api/v1/auth/login/totp");
    expect(JSON.parse(call[1].body as string)).toEqual({ temp_token: "temp-abc", code: "123456" });
    expect(user.username).toBe("carol");
    expect(cachedLocalUser()?.username).toBe("carol");
  });

  it("completeTotpLogin()：恢复码分支发 recovery_code 字段而不是 code", async () => {
    const fetchMock = vi.fn(() => Promise.resolve(response({ username: "carol", roles: [] })));
    vi.stubGlobal("fetch", fetchMock);
    await completeTotpLogin("temp-abc", { recoveryCode: "ABCDE-FGHIJ" });
    const call = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(JSON.parse(call[1].body as string)).toEqual({
      temp_token: "temp-abc",
      recovery_code: "ABCDE-FGHIJ",
    });
  });
});
