import { describe, expect, it } from "vitest";

import { shouldShowAdminReturn } from "./portal-navigation";

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
