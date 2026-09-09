import { describe, expect, it } from "vitest";

import { isAdminAreaPath, shouldShowAdminReturn } from "./portal-navigation";

describe("portal admin return navigation", () => {
  it("is hidden from ordinary users and inside the admin workspace", () => {
    expect(shouldShowAdminReturn(false, "user", false)).toBe(false);
    expect(shouldShowAdminReturn(false, undefined, false)).toBe(false);
    expect(shouldShowAdminReturn(false, undefined, true)).toBe(false);
    expect(shouldShowAdminReturn(true, "admin", false)).toBe(false);
  });

  it("survives switching to user mode for fresh and stale administrators", () => {
    expect(shouldShowAdminReturn(false, "admin", false)).toBe(true);
    expect(shouldShowAdminReturn(false, "user", true)).toBe(true);
  });
});

describe("isAdminAreaPath (XM-INV-HIDE-ADMIN-LOGIN)", () => {
  it("is false for every user-facing path, including the user embed's landing path", () => {
    expect(isAdminAreaPath("/")).toBe(false);
    expect(isAdminAreaPath("/orders")).toBe(false);
    expect(isAdminAreaPath("/profiles")).toBe(false);
    expect(isAdminAreaPath("/records")).toBe(false);
  });

  it("is true for the admin area root and every admin sub-page", () => {
    expect(isAdminAreaPath("/admin")).toBe(true);
    expect(isAdminAreaPath("/admin/payment-candidates")).toBe(true);
    expect(isAdminAreaPath("/admin/eligibility-freezes")).toBe(true);
    expect(isAdminAreaPath("/admin/refund-cases")).toBe(true);
    expect(isAdminAreaPath("/admin/source-health")).toBe(true);
    expect(isAdminAreaPath("/admin/settings")).toBe(true);
  });

  it("does not get confused by a hypothetical non-admin path sharing the /admin prefix", () => {
    // Documents the known limitation this shares with App.tsx's own
    // pathname.startsWith("/admin") checks (DataProvider, embeddedUserMode):
    // there is no such route today, so this is not a live bug, just the
    // same prefix-match convention the rest of the app already relies on.
    expect(isAdminAreaPath("/administration")).toBe(true);
  });
});
