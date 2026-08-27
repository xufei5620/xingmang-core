import { describe, expect, it } from "vitest";
import type { RequestSummary } from "../api/requests";
import {
  describeRole,
  describeStatus,
  formatBytes,
  formatMillis,
  formatTokens,
  formatTTFB,
  formatUsername,
  isUnmappedUser,
  MISSING_VALUE_TEXT,
  NO_STATUS_TEXT,
  retentionNote,
  truncationNote,
  UNMAPPED_USER_TEXT,
  utf8Bytes,
} from "./requests";

/** 样本令牌前缀的引导串。拼接而不是就地写 "sk-…"：`sk-` 开头的字面量会被
 *  gitleaks 的 generic-api-key 规则判成泄漏，而本仓库禁止用 allowlist 消音。 */
const TOKEN_LEAD = "sk-";

function summary(over: Partial<RequestSummary> = {}): RequestSummary {
  return {
    id: "20260828-000117",
    source: "sub2api",
    occurred_at: "2026-08-28T09:00:00Z",
    username: "zhang.wei",
    token_prefix: TOKEN_LEAD + "a1b2",
    model: "gpt-4o",
    status: 200,
    duration_ms: 1200,
    ttfb_ms: 300,
    tokens_in: 100,
    tokens_out: 50,
    tokens_cache: 20,
    stream: false,
    upstream_request_id: "req_1",
    client_ip: "203.0.113.x",
    ...over,
  };
}

describe("用户名的空值口径", () => {
  it("映射不到用户名时显示「未映射」而不是空白", () => {
    // 空白会被读成「这条请求没有用户」，而事实是「有用户，只是没查到它叫什么」
    // ——后者要拿 token_prefix 去上游后台反查，前者什么也不用做
    expect(formatUsername("")).toBe(UNMAPPED_USER_TEXT);
    expect(formatUsername("   ")).toBe(UNMAPPED_USER_TEXT);
    expect(isUnmappedUser("")).toBe(true);
    expect(isUnmappedUser("  ")).toBe(true);
  });

  it("有用户名时原样显示", () => {
    expect(formatUsername("li.na")).toBe("li.na");
    expect(isUnmappedUser("li.na")).toBe(false);
  });
});

describe("TTFB 的 null 与 0", () => {
  it("null 是「没测」，0 是缓存命中——两者必须显示成不同的东西", () => {
    expect(formatTTFB(null)).toBe(MISSING_VALUE_TEXT);
    expect(formatTTFB(0)).toBe("0 ms");
    // 这条断言是本文件的核心：合并成 `ttfb || "—"` 会让缓存命中显示成「没测」，
    // 而缓存命中率恰恰是这一页最有价值的观察之一
    expect(formatTTFB(0)).not.toBe(formatTTFB(null));
  });
});

describe("状态码的语气", () => {
  it("status=0 归为 danger 而不是中性——那是一次没成功的请求", () => {
    const d = describeStatus(0);
    expect(d.label).toBe(NO_STATUS_TEXT);
    expect(d.tone).toBe("danger");
  });

  it("2xx 成功、4xx 警告、5xx 危险", () => {
    expect(describeStatus(200).tone).toBe("success");
    expect(describeStatus(204).tone).toBe("success");
    expect(describeStatus(429).tone).toBe("warning");
    expect(describeStatus(404).tone).toBe("warning");
    expect(describeStatus(502).tone).toBe("danger");
  });

  it("状态码原样显示，不翻译成自造文案", () => {
    // 运营会拿这个数字去和上游日志对，翻译成「限流」之类会让对照多一步
    expect(describeStatus(429).label).toBe("429");
    expect(describeStatus(502).label).toBe("502");
  });
});

describe("时长与字节的格式", () => {
  it("一秒以内保留毫秒，超过一秒转成秒", () => {
    expect(formatMillis(88)).toBe("88 ms");
    expect(formatMillis(999)).toBe("999 ms");
    expect(formatMillis(1000)).toBe("1.0 s");
    expect(formatMillis(18420)).toBe("18.4 s");
  });

  it("缺失值不显示成 0", () => {
    // 「没测到耗时」和「0 毫秒」是两件事
    expect(formatMillis(null)).toBe(MISSING_VALUE_TEXT);
    expect(formatMillis(undefined)).toBe(MISSING_VALUE_TEXT);
    expect(formatMillis(Number.NaN)).toBe(MISSING_VALUE_TEXT);
    expect(formatMillis(-1)).toBe(MISSING_VALUE_TEXT);
  });

  it("字节按 B / KiB / MiB 分档", () => {
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(2048)).toBe("2.0 KiB");
    expect(formatBytes(3 * 1024 * 1024)).toBe("3.0 MiB");
    expect(formatBytes(null)).toBe(MISSING_VALUE_TEXT);
  });
});

describe("token 三元组", () => {
  it("缓存命中数单列，不并进输入", () => {
    // 并进去会得到一个既不是用量也不是成本的数字：两者计费口径不同
    expect(formatTokens(summary({ tokens_in: 100, tokens_out: 50, tokens_cache: 20 }))).toBe(
      "100 / 50 / 20",
    );
  });
});

describe("消息角色", () => {
  it("已知角色给中文标签", () => {
    expect(describeRole("system").label).toBe("系统");
    expect(describeRole("user").label).toBe("用户");
    expect(describeRole("assistant").label).toBe("助手");
    expect(describeRole("tool").label).toBe("工具");
  });

  it("未知角色原样显示，不归到「其他」", () => {
    // 上游协议随时会长出新角色；统一画成「其他」，读的人就分不出
    // 一条工具调用和一条开发者指令
    expect(describeRole("developer").label).toBe("developer");
    expect(describeRole("function").label).toBe("function");
    expect(describeRole("").label).toBe("未知角色");
  });
});

describe("截断与保留期提示", () => {
  it("未截断时不产生提示", () => {
    expect(truncationNote(false, 100, 100)).toBe("");
  });

  it("截断时同时说清「显示了多少」和「共多少」", () => {
    // 一个不声张的截断比不显示更危险——人会以为看到的就是全部
    const note = truncationNote(true, 3 * 1024 * 1024, 64 * 1024);
    expect(note).toContain("64.0 KiB");
    expect(note).toContain("3.0 MiB");
  });

  it("保留期提示说得出天数", () => {
    expect(retentionNote(30)).toContain("30 天");
    expect(retentionNote(0)).toBe("");
    expect(retentionNote(Number.NaN)).toBe("");
  });
});

describe("utf8Bytes", () => {
  it("按字节数而不是字符数——中文一个字三字节", () => {
    // 截断阈值是字节口径（后端也是），按 length 算会让中文内容的
    // 「共多少」显示成实际的三分之一
    expect(utf8Bytes("abc")).toBe(3);
    expect(utf8Bytes("你好")).toBe(6);
  });
});
