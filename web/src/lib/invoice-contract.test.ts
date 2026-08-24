import { describe, expect, it } from "vitest";

import {
  invoiceDocumentPath,
  mapProfile,
  profileMutationBody,
} from "./http-api";
import {
  canUserCancelInvoice,
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
