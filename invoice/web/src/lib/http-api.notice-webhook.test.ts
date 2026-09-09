import { afterEach, describe, expect, it, vi } from "vitest";

import {
  httpInvoiceApi,
  mapAdminSettings,
  requiredEligibilityStartAt,
  type BackendSystemSettings,
} from "./http-api";

// XM-INV-NOTICE-WEBHOOK-SETTING：地址从宿主机文件搬进管理端设置。
//
// 这一组钉的是**地址只进不出**，以及老后端没有这个字段时不能显示成"已配置"。

function stubWindowTimers() {
  vi.stubGlobal("window", {
    setTimeout: globalThis.setTimeout.bind(globalThis),
    clearTimeout: globalThis.clearTimeout.bind(globalThis),
  });
}

function stubFetch() {
  stubWindowTimers();
  const calls: Array<{ url: string; method?: string; body?: string }> = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input.toString();
      calls.push({
        url,
        method: init?.method,
        body: typeof init?.body === "string" ? init.body : undefined,
      });
      // 写操作要 CSRF 令牌，而令牌只在建立会话时拿到。先让 getSession 走一遍。
      if (url.endsWith("/api/v1/auth/session")) {
        return new Response(
          JSON.stringify({
            authenticated: true,
            csrf_token: "c".repeat(40),
            user: {
              id: "admin-1",
              display_name: "admin",
              email: "a@example.com",
              email_verified: true,
              role: "admin",
            },
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        );
      }
      return new Response(JSON.stringify({ configured: true }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  return calls;
}

/** 建立会话拿到 CSRF 令牌；随后清空调用记录，让断言只看要测的那几发。 */
async function withSession(calls: Array<{ url: string }>) {
  await httpInvoiceApi.getSession();
  calls.length = 0;
}

function backendSettings(
  noticeWebhook?: BackendSystemSettings["notice_webhook"],
): BackendSystemSettings {
  return {
    revision: 3,
    issuer_configured: true,
    issuer_name: "示例公司",
    service_item: "技术服务",
    minimum_request_minor: 20000,
    eligibility_start_at: requiredEligibilityStartAt,
    eligibility_policy_version: 1,
    eligibility_timezone: "Asia/Shanghai",
    eligibility_rule: "payment_and_usage_at_or_after",
    smtp: {
      from_address: "a@example.com",
      from_name: "n",
      host: "smtp.qq.com",
      port: 587,
      starttls: true,
      credential_configured: true,
      test_recipient_masked: "tes***@example.com",
    },
    admin_access: { cidrs: ["127.0.0.1/32"], current_ip: "127.0.0.1" },
    ...(noticeWebhook ? { notice_webhook: noticeWebhook } : {}),
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("通知地址的读", () => {
  it("后端没有这个字段时算未配置，不能显示成已配置", () => {
    const settings = mapAdminSettings(backendSettings());
    expect(settings.noticeWebhook.configured).toBe(false);
    expect(settings.noticeWebhook.fingerprint).toBe("");
    // null 而不是零时刻——"1970 年更新的"会被读成一次真实更新。
    expect(settings.noticeWebhook.updatedAt).toBeNull();
  });

  it("configured 只认布尔 true，不认真值", () => {
    const settings = mapAdminSettings(
      backendSettings({ configured: "yes" as unknown as boolean }),
    );
    expect(settings.noticeWebhook.configured).toBe(false);
  });

  it("已配置时带出指纹与更新信息，且里面没有地址", () => {
    const settings = mapAdminSettings(
      backendSettings({
        configured: true,
        fingerprint: "sha256:0123456789abcdef",
        updated_by: "admin-1",
        updated_at: "2026-09-06T08:00:00Z",
      }),
    );
    expect(settings.noticeWebhook.configured).toBe(true);
    expect(settings.noticeWebhook.fingerprint).toBe("sha256:0123456789abcdef");
    expect(settings.noticeWebhook.updatedBy).toBe("admin-1");
    expect(settings.noticeWebhook.updatedAt).toBe("2026-09-06T08:00:00Z");
    // 展示面里不该有任何地址片段。
    expect(JSON.stringify(settings.noticeWebhook)).not.toContain("qyapi");
    expect(JSON.stringify(settings.noticeWebhook)).not.toContain("key=");
  });
});

describe("通知地址的写", () => {
  it("保存打的是 admin 的 PUT，地址进 body 不进 URL", async () => {
    const calls = stubFetch();
    await withSession(calls);
    const address = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=super-secret";
    await httpInvoiceApi.saveNoticeWebhook(address);
    expect(calls[0]?.url).toBe("/api/v1/admin/settings/notice-webhook");
    expect(calls[0]?.method).toBe("PUT");
    // **地址绝不能进 URL**：URL 会进访问日志、进 Referer、进浏览器历史。
    expect(calls[0]?.url).not.toContain("key=");
    expect(calls[0]?.body).toContain(address);
  });

  it("清除是 DELETE，测试是 POST 到 /test，都不带地址", async () => {
    const calls = stubFetch();
    await withSession(calls);
    await httpInvoiceApi.clearNoticeWebhook();
    await httpInvoiceApi.sendNoticeWebhookTest();
    expect(calls[0]?.method).toBe("DELETE");
    expect(calls[0]?.url).toBe("/api/v1/admin/settings/notice-webhook");
    expect(calls[1]?.method).toBe("POST");
    expect(calls[1]?.url).toBe("/api/v1/admin/settings/notice-webhook/test");
    expect(calls[1]?.body).toBe("{}");
  });
});
