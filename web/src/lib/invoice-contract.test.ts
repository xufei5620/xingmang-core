import { describe, expect, it } from "vitest";

import {
  mapAdminSettings,
  mapInvoicePolicy,
  mapLot,
  mapPlatformLoginOutcome,
  mapSourceAccount,
  invoiceDocumentPath,
  mapProfile,
  platformLoginBody,
  platformLoginTwoFABody,
  profileMutationBody,
  requiredEligibilityStartAt,
  type BackendFundingLot,
  type BackendInvoicePolicy,
  type BackendSourceAccount,
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

describe("source account HTTP contract", () => {
  const backendAccount: BackendSourceAccount = {
    id: "50000000-0000-4000-8000-000000000001",
    source_type: "newapi",
    source_instance_id: "60000000-0000-4000-8000-000000000001",
    source_name: "SoloV 模型平台",
    external_user_id_masked: "n***8",
    binding_status: "verified",
  };

  it("passes through a real last_observed_at timestamp unchanged", () => {
    expect(
      mapSourceAccount({
        ...backendAccount,
        last_observed_at: "2026-08-15T09:30:00Z",
      }).lastObservedAt,
    ).toBe("2026-08-15T09:30:00Z");
  });

  // Regression: a source account that has never synced arrives on the wire
  // as a real-looking sentinel timestamp (Go's zero time.Time, or a
  // COALESCE-to-epoch fallback) rather than an absent field. Rendering it
  // literally produced "最近同步 1/01/01 08:05" in production.
  it.each([
    ["Go's zero time.Time", "0001-01-01T00:00:00Z"],
    ["a COALESCE-to-epoch fallback", "1970-01-01T00:00:00Z"],
  ])("normalizes %s to undefined", (_label, sentinel) => {
    expect(
      mapSourceAccount({ ...backendAccount, last_observed_at: sentinel })
        .lastObservedAt,
    ).toBeUndefined();
  });

  it("normalizes a missing last_observed_at to undefined", () => {
    expect(
      mapSourceAccount({ ...backendAccount, last_observed_at: undefined })
        .lastObservedAt,
    ).toBeUndefined();
  });

  // XM-INV-LOT-REASON-CONTRACT R10. `source_type` used to be a closed list
  // (sub2api | newapi) that threw INVALID_SOURCE_ACCOUNT for anything else --
  // and getSourceAccounts() maps every row, so one row on a third platform
  // (or a backend one deploy ahead of this bundle) rejected the WHOLE accounts
  // request. An empty accounts list on a first load is the binding wizard.
  // Same prescription as eligibility_status: shape check, keep the raw value,
  // label it 「未识别的平台」; malformed is still refused.
  describe("an unrecognised but well-formed source_type", () => {
    const unknown = { ...backendAccount, source_type: "thirdapi" };

    it("does not throw, and keeps the raw platform code on the row", () => {
      expect(() => mapSourceAccount(unknown)).not.toThrow();
      expect(mapSourceAccount(unknown).source).toBe("thirdapi");
    });

    it("labels the row 「未识别的平台」 when the backend sent no display name", () => {
      expect(
        mapSourceAccount({ ...unknown, source_name: "" }).sourceLabel,
      ).toBe("未识别的平台");
    });

    it("prefers the backend's own display name over the fallback label", () => {
      expect(
        mapSourceAccount({ ...unknown, source_name: "第三平台" }).sourceLabel,
      ).toBe("第三平台");
    });

    it("keeps the row's binding status and identity, so the panel can list it", () => {
      const row = mapSourceAccount(unknown);
      expect(row.status).toBe("verified");
      expect(row.externalUserIdMasked).toBe("n***8");
      expect(row.id).toBe(backendAccount.id);
    });

    it("still resolves the known platforms to their fixed labels", () => {
      expect(
        mapSourceAccount({ ...backendAccount, source_type: "sub2api", source_name: "" })
          .sourceLabel,
      ).toBe("SoloV API");
      expect(
        mapSourceAccount({ ...backendAccount, source_type: "newapi", source_name: "" })
          .sourceLabel,
      ).toBe("SoloV 模型平台");
    });
  });

  it.each([
    ["an empty string", ""],
    ["an upper-case code", "SUB2API"],
    ["a code with a space", "new api"],
    ["a code with a hyphen", "sub2-api"],
    ["a code starting with a digit", "3rdapi"],
    ["a code over the length cap", `a${"b".repeat(63)}`],
  ])("still refuses %s as a source_type", (_label, sourceType) => {
    expect(() =>
      mapSourceAccount({ ...backendAccount, source_type: sourceType }),
    ).toThrow("源账号包含无法识别的平台类型。");
    try {
      mapSourceAccount({ ...backendAccount, source_type: sourceType });
    } catch (error) {
      expect((error as { code?: string }).code).toBe("INVALID_SOURCE_ACCOUNT");
    }
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

  // Renamed and rewritten by XM-INV-LOT-REASON-CONTRACT. This test used to end
  // with `expect(() => mapLot({...lot, reason_code: "UNKNOWN"})).toThrow(...)`,
  // which pinned exactly the behaviour that took the user's invoice page down:
  // any reason_code outside the bundle's hand-copied list threw, and one such
  // lot rejected the whole orders response. That assertion is intentionally
  // reversed below, not deleted -- the closed set was a real decision once, and
  // this is the record of it being overturned for the second time (the first
  // was freeze_reason, after migration 0016; see
  // http-api.eligibility-freeze-reason-tolerance.test.ts).
  //
  // What replaces it is not "no validation". A well-formed but unknown code is
  // accepted and flagged degraded; a malformed one is still rejected; and an
  // unknown status still cannot carry invoiceable money.
  it("renders unknown-but-well-formed noninvoiceable reasons instead of rejecting the response", () => {
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
      eligibilityDegraded: false,
    });

    const unknown = mapLot({
      ...lot,
      reason_code: "UNKNOWN",
    } as unknown as BackendFundingLot);
    expect(unknown.reasonCode).toBe("UNKNOWN");
    expect(unknown.eligibilityDegraded).toBe(true);
    expect(unknown.availableMinor).toBe(0);
    expect(unknown.description).toBe("账本状态待确认（UNKNOWN）");

    // Malformed codes are still refused -- the check became a shape check, not
    // an absent one.
    expect(() =>
      mapLot({ ...lot, reason_code: "not upper case" } as unknown as BackendFundingLot),
    ).toThrow("充值记录包含无效的资金账本状态");
  });
});

describe("platform login outcome mapping", () => {
  const tempTokenFixture = ["01234567", "89abcdef"].join("");

  it("maps a direct success with no two-factor step", () => {
    expect(mapPlatformLoginOutcome({ ok: true })).toEqual({ ok: true });
  });

  it("maps a two-factor-required response and preserves the temp token", () => {
    expect(
      mapPlatformLoginOutcome({
        ok: true,
        requires_two_fa: true,
        temp_token: tempTokenFixture,
      }),
    ).toEqual({ ok: true, requiresTwoFA: true, tempToken: tempTokenFixture });
  });

  it("rejects a two-factor response with a missing or malformed temp token", () => {
    expect(() =>
      mapPlatformLoginOutcome({ ok: true, requires_two_fa: true }),
    ).toThrow("登录服务返回了无法识别的验证状态");
    expect(() =>
      mapPlatformLoginOutcome({
        ok: true,
        requires_two_fa: true,
        temp_token: "short",
      }),
    ).toThrow("登录服务返回了无法识别的验证状态");
  });
});

describe("platform login auto-detect request body", () => {
  it("omits platform on the initial login so the backend auto-detects it", () => {
    expect(
      platformLoginBody({ identifier: "person@example.com", password: "x" }),
    ).toEqual({ identifier: "person@example.com", password: "x" });
  });

  it("still forwards an explicit platform for the ops/test override path", () => {
    expect(
      platformLoginBody({
        platform: "sub2api",
        identifier: "person@example.com",
        password: "x",
      }),
    ).toEqual({
      platform: "sub2api",
      identifier: "person@example.com",
      password: "x",
    });
  });

  it("omits platform on the 2FA step so the backend uses the remembered platform", () => {
    const tempTokenFixture = ["01234567", "89abcdef"].join("");
    expect(
      platformLoginTwoFABody({ tempToken: tempTokenFixture, code: "123456" }),
    ).toEqual({ temp_token: tempTokenFixture, code: "123456" });
  });
});
