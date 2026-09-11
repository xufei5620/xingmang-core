import { afterEach, describe, expect, it, vi } from "vitest";

import {
  httpInvoiceApi,
  mapAdminSettings,
  requiredEligibilityStartAt,
  type BackendSystemSettings,
} from "./http-api";

// XM-INV-SMTP-TEST-RECIPIENT-SETTING：测试收件邮箱从服务器环境变量搬进管理端。
//
// 这一组钉两件事：①**不传与传空串含义不同**——不传是「保持不变」，传空串是
// 「清空，回到环境变量兜底」；真值判断会把这两者混成一个，于是每保存一次 SMTP
// 主机就顺手把收件人抹掉。②设置响应里**没有明文**，空遮蔽值是合法的「还没配」，
// 不能让整个设置页拒绝解析。

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
      if (url.endsWith("/invoice-api/v1/auth/session")) {
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
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  return calls;
}

/** 建立会话拿到 CSRF 令牌；随后清空调用记录，让断言只看要测的那一发。 */
async function withSession(calls: Array<{ url: string }>) {
  await httpInvoiceApi.getSession();
  calls.length = 0;
}

function settingsPayload(
  smtp: Partial<BackendSystemSettings["smtp"]> = {},
): BackendSystemSettings {
  return {
    revision: 3,
    issuer_configured: true,
    issuer_name: "示例科技有限公司",
    service_item: "技术服务",
    minimum_request_minor: 20000,
    eligibility_start_at: requiredEligibilityStartAt,
    eligibility_policy_version: 1,
    eligibility_timezone: "Asia/Shanghai",
    eligibility_rule: "payment_and_usage_at_or_after",
    smtp: {
      from_address: "billing@example.com",
      from_name: "发票中心",
      host: "smtp.qq.com",
      port: 587,
      starttls: true,
      credential_configured: true,
      test_recipient_masked: "ops***@example.com",
      ...smtp,
    },
    admin_access: { cidrs: ["127.0.0.1/32"], current_ip: "127.0.0.1" },
  } as BackendSystemSettings;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("SMTP 测试收件邮箱", () => {
  it("不传收件人时请求体里没有 test_recipient（保持库里现值）", async () => {
    const calls = stubFetch();
    await withSession(calls);
    await httpInvoiceApi.saveSMTPSettings({
      revision: 3,
      fromAddress: "billing@example.com",
      fromName: "发票中心",
      host: "smtp.qq.com",
      port: 587,
      startTLS: true,
    });
    const write = calls.find((call) => call.method === "PUT");
    expect(write).toBeDefined();
    const body = JSON.parse(write!.body ?? "{}");
    expect("test_recipient" in body).toBe(false);
  });

  it("传空串时请求体里带着空的 test_recipient（明确清空）", async () => {
    const calls = stubFetch();
    await withSession(calls);
    await httpInvoiceApi.saveSMTPSettings({
      revision: 3,
      fromAddress: "billing@example.com",
      fromName: "发票中心",
      host: "smtp.qq.com",
      port: 587,
      startTLS: true,
      testRecipient: "",
    });
    const write = calls.find((call) => call.method === "PUT");
    const body = JSON.parse(write!.body ?? "{}");
    expect(body.test_recipient).toBe("");
  });

  it("传地址时原样带过去", async () => {
    const calls = stubFetch();
    await withSession(calls);
    await httpInvoiceApi.saveSMTPSettings({
      revision: 3,
      fromAddress: "billing@example.com",
      fromName: "发票中心",
      host: "smtp.qq.com",
      port: 587,
      startTLS: true,
      testRecipient: "ops@example.com",
    });
    const write = calls.find((call) => call.method === "PUT");
    const body = JSON.parse(write!.body ?? "{}");
    expect(body.test_recipient).toBe("ops@example.com");
  });

  it("空遮蔽值是合法的「还没配」，不抛错", () => {
    const mapped = mapAdminSettings(
      settingsPayload({ test_recipient_masked: "" }),
    );
    expect(mapped.smtp.testRecipientMasked).toBe("");
    expect(mapped.smtp.testRecipientManaged).toBe(false);
  });

  it("后端说由后台管理时映射成 true", () => {
    const mapped = mapAdminSettings(
      settingsPayload({ test_recipient_managed: true }),
    );
    expect(mapped.smtp.testRecipientManaged).toBe(true);
  });

  it("老后端没有 test_recipient_managed 时按 false 处理，不会误报「已由后台管理」", () => {
    const mapped = mapAdminSettings(settingsPayload());
    expect(mapped.smtp.testRecipientManaged).toBe(false);
    expect(mapped.smtp.testRecipientMasked).toBe("ops***@example.com");
  });

  it("遮蔽值形状不对仍然拒绝解析", () => {
    expect(() =>
      mapAdminSettings(settingsPayload({ test_recipient_masked: "ops@x.com" })),
    ).toThrow();
  });
});
