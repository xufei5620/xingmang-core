import { describe, expect, it } from "vitest";

import {
  mapAdminSettings,
  mapInvoicePolicy,
  mapLot,
  invoiceDocumentPath,
  mapProfile,
  profileMutationBody,
  requiredEligibilityStartAt,
  type BackendFundingLot,
  type BackendInvoicePolicy,
  type BackendSystemSettings,
} from "./http-api";
import {
  canUserCancelInvoice,
  eligibilityStartLabel,
  invoicePDFSizeAllowed,
  maxInvoicePDFBytes,
  normalizeIssuedAt,
  replaceDirectRequestAfterMutation,
  userCancellationLabel,
} from "./workflow";

describe("invoice profile HTTP contract", () => {
  const backendProfile = {
    id: "40000000-0000-4000-8000-000000000001",
    principal_id: "20000000-0000-4000-8000-000000000001",
    type: "enterprise" as const,
    title: "测试企业",
    tax_id: "91310000TEST000001",
    email: "invoice@example.com",
    email_verified: true,
    is_default: true,
    revision: 7,
  };

  it("preserves the server CAS revision through mapping and mutation", () => {
    const profile = mapProfile(backendProfile);
    expect(profile.revision).toBe(7);
    expect(profileMutationBody(profile)).toMatchObject({
      id: backendProfile.id,
      revision: 7,
      tax_id: backendProfile.tax_id,
      is_default: true,
    });
  });

  it("rejects an invalid profile revision instead of creating a blind update", () => {
    expect(() => mapProfile({ ...backendProfile, revision: 0 })).toThrow(
      "开票资料版本无效",
    );
  });
});

describe("request recovery and document routes", () => {
  it("allows cancellation only while the backend can release reservations", () => {
    expect(
      canUserCancelInvoice({ status: "submitted", workflowStatus: "pending_review" }),
    ).toBe(true);
    expect(
      canUserCancelInvoice({ status: "returned", workflowStatus: "needs_changes" }),
    ).toBe(true);
    expect(
      canUserCancelInvoice({ status: "reviewing", workflowStatus: "approved" }),
    ).toBe(false);
    expect(
      userCancellationLabel({ workflowStatus: "needs_changes" }),
    ).toBe("取消并重新申请");
  });

  it("updates a direct email-linked record after cancellation", () => {
    const current = { id: "request-1", status: "pending" };
    const cancelled = { id: "request-1", status: "cancelled" };
    expect(replaceDirectRequestAfterMutation(current, cancelled)).toBe(
      cancelled,
    );
    expect(
      replaceDirectRequestAfterMutation(
        { id: "request-2", status: "pending" },
        cancelled,
      ),
    ).toEqual({ id: "request-2", status: "pending" });
  });

  it("keeps administrator downloads on the administrator authorization edge", () => {
    expect(invoiceDocumentPath("request/unsafe", true)).toBe(
      "/api/v1/admin/invoice-requests/request%2Funsafe/document",
    );
    expect(invoiceDocumentPath("request-1", false)).toBe(
      "/api/v1/user/invoice-requests/request-1/document",
    );
  });

  it("normalizes a real issue timestamp and rejects invalid input", () => {
    expect(normalizeIssuedAt("2026-08-24T10:30:00Z")).toBe(
      "2026-08-24T10:30:00.000Z",
    );
    expect(() => normalizeIssuedAt("not-a-date")).toThrow(
      "请输入有效的实际开票时间",
    );
  });

  it("matches the backend 20 MiB upload boundary", () => {
    expect(invoicePDFSizeAllowed(maxInvoicePDFBytes)).toBe(true);
    expect(invoicePDFSizeAllowed(maxInvoicePDFBytes + 1)).toBe(false);
    expect(invoicePDFSizeAllowed(0)).toBe(false);
  });
});

describe("immutable invoice eligibility policy contract", () => {
  const policy: BackendInvoicePolicy = {
    minimum_request_minor: 20_000,
    service_item: "技术服务",
    eligibility_start_at: requiredEligibilityStartAt,
    eligibility_policy_version: 1,
    eligibility_timezone: "Asia/Shanghai",
    eligibility_rule: "payment_and_usage_at_or_after",
  };

  it("maps only the exact immutable payment-and-usage boundary", () => {
    expect(mapInvoicePolicy(policy)).toMatchObject({
      eligibilityStartAt: "2026-08-31T16:00:00Z",
      eligibilityPolicyVersion: 1,
      eligibilityTimezone: "Asia/Shanghai",
    });
    for (const invalid of [
      { ...policy, eligibility_start_at: "2026-08-31T15:59:59.999999Z" },
      { ...policy, eligibility_start_at: "2026-08-31T16:00:00.000001Z" },
      { ...policy, eligibility_policy_version: 2 },
      { ...policy, eligibility_timezone: "UTC" },
      { ...policy, eligibility_rule: "payment_only" },
    ]) {
      expect(() => mapInvoicePolicy(invalid as BackendInvoicePolicy)).toThrow(
        "开票生效时间策略无效",
      );
    }
    expect(eligibilityStartLabel(requiredEligibilityStartAt)).toContain(
      "2026/09/01 00:00:00",
    );
  });

  it("keeps the same immutable policy in administrator settings", () => {
    const settings: BackendSystemSettings = {
      revision: 3,
      issuer_configured: true,
      issuer_name: "测试主体",
      service_item: "技术服务",
      minimum_request_minor: 20_000,
      eligibility_start_at: requiredEligibilityStartAt,
      eligibility_policy_version: 1,
      eligibility_timezone: "Asia/Shanghai",
      eligibility_rule: "payment_and_usage_at_or_after",
      smtp: {
        from_address: "invoice@example.com",
        from_name: "发票中心",
        host: "smtp.example.com",
        port: 587,
        starttls: true,
        credential_configured: true,
        test_recipient_masked: "tes***@example.com",
      },
      admin_access: { cidrs: ["203.0.113.8/32"], current_ip: "203.0.113.8" },
    };
    expect(mapAdminSettings(settings)).toMatchObject({
      revision: 3,
      eligibilityStartAt: requiredEligibilityStartAt,
      eligibilityPolicyVersion: 1,
      smtp: { testRecipientMasked: "tes***@example.com" },
    });
    expect(() =>
      mapAdminSettings({ ...settings, eligibility_policy_version: 0 }),
    ).toThrow("系统开票生效策略无效");
  });

  it("accepts closed-set noninvoiceable reasons and rejects unknown ones", () => {
    const lot: BackendFundingLot = {
      id: "50000000-0000-4000-8000-000000000001",
      source: "sub2api",
      source_instance_id: "10000000-0000-4000-8000-000000000001",
      source_label: "SoloV API",
      display_reference: "PAY-00000001",
      completed_at: requiredEligibilityStartAt,
      original_paid_minor: 30_000,
      consumed_cash_minor: 0,
      reserved_minor: 0,
      issued_minor: 0,
      available_minor: 0,
      eligibility_kind: "subscription",
      eligibility_status: "active",
      reason_code: "SUBSCRIPTION_USAGE_UNSUPPORTED",
      verification: "verified",
      refund_frozen: false,
    };
    expect(mapLot(lot)).toMatchObject({
      availableMinor: 0,
      reasonCode: "SUBSCRIPTION_USAGE_UNSUPPORTED",
      description: "订阅消费暂缺可核验关联证据（不可开票）",
    });
    expect(() => mapLot({ ...lot, reason_code: "UNKNOWN" } as unknown as BackendFundingLot)).toThrow(
      "充值记录包含无效的资金账本状态",
    );
  });
});
