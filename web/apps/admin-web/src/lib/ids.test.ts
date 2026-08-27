import { afterEach, describe, expect, it, vi } from "vitest";
import { isSafeRequestId, newRequestId } from "./ids";

describe("newRequestId", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("生成的 ID 落在后端 safeRequestID 的字符集里", () => {
    // 不匹配的值会被服务端静默换掉，界面上显示的 ID 就和日志里的对不上
    for (let i = 0; i < 20; i += 1) {
      expect(isSafeRequestId(newRequestId())).toBe(true);
    }
  });

  it("每次都不一样（同一个 ID 会把两次操作在日志里混成一次）", () => {
    const ids = new Set(Array.from({ length: 50 }, () => newRequestId()));
    expect(ids.size).toBe(50);
  });

  it("没有 randomUUID 时退到 getRandomValues，仍然合法且唯一", () => {
    const real = globalThis.crypto;
    vi.stubGlobal("crypto", {
      getRandomValues: (buf: Uint8Array) => real.getRandomValues(buf),
    });
    const id = newRequestId();
    expect(id).toMatch(/^req-[0-9a-f]{32}$/);
    expect(isSafeRequestId(id)).toBe(true);
  });

  it("两条随机源都没有时也要给出一个合法 ID——不带 ID 比带一个弱 ID 更糟", () => {
    vi.stubGlobal("crypto", undefined);
    const id = newRequestId();
    expect(id.startsWith("req-")).toBe(true);
    expect(isSafeRequestId(id)).toBe(true);
  });
});

describe("isSafeRequestId", () => {
  it("逐字对齐 httpapi/middleware.go 的 ^[A-Za-z0-9._:-]{1,128}$", () => {
    expect(isSafeRequestId("a1b2-c3.d4:e5_f")).toBe(true); // 点/冒号/短横/下划线都在集合里
    expect(isSafeRequestId("a1b2 c3")).toBe(false); // 空格不在
    expect(isSafeRequestId("")).toBe(false);
    expect(isSafeRequestId("a".repeat(128))).toBe(true);
    expect(isSafeRequestId("a".repeat(129))).toBe(false);
    expect(isSafeRequestId("有中文")).toBe(false);
    expect(isSafeRequestId("a\nb")).toBe(false); // 换行会伪造日志行
  });
});
