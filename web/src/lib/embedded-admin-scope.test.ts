import { describe, expect, it } from "vitest";

import {
  ADMIN_AUTH_POPUP_MESSAGE,
  appendEmbeddedAdminParams,
  isAdminAuthPopupCompleteMessage,
  isAdminAuthPopupReturn,
  isAdminNavItemVisible,
  parseEmbeddedAdminMode,
  parseEmbeddedAdminScope,
  resolvePlatformSourceInstanceId,
  withAdminAuthPopupReturnParam,
} from "./embedded-admin-scope";

describe("parseEmbeddedAdminMode", () => {
  it("recognizes ui_mode=embedded_admin", () => {
    expect(
      parseEmbeddedAdminMode(new URLSearchParams("ui_mode=embedded_admin")),
    ).toBe(true);
  });

  it("rejects every other ui_mode value, including the user embed's", () => {
    expect(
      parseEmbeddedAdminMode(new URLSearchParams("ui_mode=embedded")),
    ).toBe(false);
    expect(parseEmbeddedAdminMode(new URLSearchParams(""))).toBe(false);
  });
});

describe("parseEmbeddedAdminScope", () => {
  it("is always unscoped when embedded-admin mode itself is off", () => {
    expect(
      parseEmbeddedAdminScope(new URLSearchParams("platform=sub2api"), false),
    ).toBeNull();
    expect(
      parseEmbeddedAdminScope(new URLSearchParams("scope=global"), false),
    ).toBeNull();
  });

  it("recognizes both platform values", () => {
    expect(
      parseEmbeddedAdminScope(new URLSearchParams("platform=sub2api"), true),
    ).toEqual({ kind: "platform", platform: "sub2api" });
    expect(
      parseEmbeddedAdminScope(new URLSearchParams("platform=newapi"), true),
    ).toEqual({ kind: "platform", platform: "newapi" });
  });

  it("recognizes the global scope", () => {
    expect(
      parseEmbeddedAdminScope(new URLSearchParams("scope=global"), true),
    ).toEqual({ kind: "global" });
  });

  it("prefers scope=global over a platform param if both are somehow present", () => {
    expect(
      parseEmbeddedAdminScope(
        new URLSearchParams("scope=global&platform=sub2api"),
        true,
      ),
    ).toEqual({ kind: "global" });
  });

  it("resolves an unrecognized or missing combination to null, not a guess", () => {
    expect(parseEmbeddedAdminScope(new URLSearchParams(""), true)).toBeNull();
    expect(
      parseEmbeddedAdminScope(new URLSearchParams("platform=admin"), true),
    ).toBeNull();
    expect(
      parseEmbeddedAdminScope(new URLSearchParams("scope=all"), true),
    ).toBeNull();
  });
});

describe("appendEmbeddedAdminParams", () => {
  it("leaves the path byte-for-byte untouched outside embedded-admin mode", () => {
    expect(
      appendEmbeddedAdminParams("/admin/refund-cases", false, {
        kind: "platform",
        platform: "sub2api",
      }),
    ).toBe("/admin/refund-cases");
    expect(appendEmbeddedAdminParams("/admin", false, null)).toBe("/admin");
  });

  it("appends ui_mode alone when the scope did not resolve", () => {
    expect(appendEmbeddedAdminParams("/admin", true, null)).toBe(
      "/admin?ui_mode=embedded_admin",
    );
  });

  it("appends the platform for platform scope", () => {
    expect(
      appendEmbeddedAdminParams("/admin/eligibility-freezes", true, {
        kind: "platform",
        platform: "newapi",
      }),
    ).toBe("/admin/eligibility-freezes?ui_mode=embedded_admin&platform=newapi");
  });

  it("appends scope=global for global scope", () => {
    expect(
      appendEmbeddedAdminParams("/admin/settings", true, { kind: "global" }),
    ).toBe("/admin/settings?ui_mode=embedded_admin&scope=global");
  });

  it("uses & when the path already carries a query string", () => {
    expect(
      appendEmbeddedAdminParams("/admin?view=issued", true, {
        kind: "platform",
        platform: "sub2api",
      }),
    ).toBe("/admin?view=issued&ui_mode=embedded_admin&platform=sub2api");
  });
});

describe("isAdminNavItemVisible", () => {
  it("shows every item unscoped (standalone, and an unresolved embedded scope)", () => {
    for (const key of [
      "review",
      "payment-candidates",
      "eligibility-freezes",
      "refund-cases",
      "source-health",
      "settings",
      "return-to-user",
    ] as const) {
      expect(isAdminNavItemVisible(key, null)).toBe(true);
    }
  });

  it("Sub2API platform mode has no payment-candidates queue and no settings/return-to-user", () => {
    const scope = { kind: "platform", platform: "sub2api" } as const;
    expect(isAdminNavItemVisible("review", scope)).toBe(true);
    expect(isAdminNavItemVisible("eligibility-freezes", scope)).toBe(true);
    expect(isAdminNavItemVisible("refund-cases", scope)).toBe(true);
    expect(isAdminNavItemVisible("source-health", scope)).toBe(true);
    expect(isAdminNavItemVisible("payment-candidates", scope)).toBe(false);
    expect(isAdminNavItemVisible("settings", scope)).toBe(false);
    expect(isAdminNavItemVisible("return-to-user", scope)).toBe(false);
  });

  it("NewAPI platform mode additionally shows payment-candidates", () => {
    const scope = { kind: "platform", platform: "newapi" } as const;
    expect(isAdminNavItemVisible("payment-candidates", scope)).toBe(true);
    expect(isAdminNavItemVisible("settings", scope)).toBe(false);
  });

  it("global scope shows only settings and source-health", () => {
    const scope = { kind: "global" } as const;
    expect(isAdminNavItemVisible("settings", scope)).toBe(true);
    expect(isAdminNavItemVisible("source-health", scope)).toBe(true);
    expect(isAdminNavItemVisible("review", scope)).toBe(false);
    expect(isAdminNavItemVisible("payment-candidates", scope)).toBe(false);
    expect(isAdminNavItemVisible("eligibility-freezes", scope)).toBe(false);
    expect(isAdminNavItemVisible("refund-cases", scope)).toBe(false);
    expect(isAdminNavItemVisible("return-to-user", scope)).toBe(false);
  });
});

describe("resolvePlatformSourceInstanceId", () => {
  const items = [
    { sourceType: "sub2api", sourceInstanceId: "10000000-0000-4000-8000-000000000001" },
    { sourceType: "newapi", sourceInstanceId: "10000000-0000-4000-8000-000000000002" },
  ] as const;

  it("finds the matching platform's source instance", () => {
    expect(resolvePlatformSourceInstanceId("sub2api", items)).toBe(
      "10000000-0000-4000-8000-000000000001",
    );
    expect(resolvePlatformSourceInstanceId("newapi", items)).toBe(
      "10000000-0000-4000-8000-000000000002",
    );
  });

  it("returns null (never a guess) when unresolved", () => {
    expect(resolvePlatformSourceInstanceId("newapi", [])).toBeNull();
  });
});

describe("admin auth popup handshake", () => {
  it("recognizes only the exact versioned message shape", () => {
    expect(isAdminAuthPopupCompleteMessage(ADMIN_AUTH_POPUP_MESSAGE)).toBe(true);
    expect(
      isAdminAuthPopupCompleteMessage({ ...ADMIN_AUTH_POPUP_MESSAGE, version: 2 }),
    ).toBe(false);
    expect(
      isAdminAuthPopupCompleteMessage({ ...ADMIN_AUTH_POPUP_MESSAGE, source: "evil" }),
    ).toBe(false);
    expect(
      isAdminAuthPopupCompleteMessage({ ...ADMIN_AUTH_POPUP_MESSAGE, type: "other" }),
    ).toBe(false);
  });

  it("rejects non-object and unrelated payloads", () => {
    expect(isAdminAuthPopupCompleteMessage(null)).toBe(false);
    expect(isAdminAuthPopupCompleteMessage(undefined)).toBe(false);
    expect(isAdminAuthPopupCompleteMessage("auth-complete")).toBe(false);
    expect(isAdminAuthPopupCompleteMessage({})).toBe(false);
  });

  it("round-trips the return marker through a bare and an already-querystring path", () => {
    expect(withAdminAuthPopupReturnParam("/admin")).toBe(
      "/admin?embedded_admin_auth_popup=1",
    );
    expect(
      isAdminAuthPopupReturn(
        new URL(
          "https://invoice.solov.cc" + withAdminAuthPopupReturnParam("/admin"),
        ).searchParams,
      ),
    ).toBe(true);

    const withQuery = "/admin?ui_mode=embedded_admin&platform=sub2api";
    expect(withAdminAuthPopupReturnParam(withQuery)).toBe(
      "/admin?ui_mode=embedded_admin&platform=sub2api&embedded_admin_auth_popup=1",
    );
  });

  it("does not flag an ordinary URL as a popup return", () => {
    expect(
      isAdminAuthPopupReturn(new URLSearchParams("ui_mode=embedded_admin")),
    ).toBe(false);
    expect(
      isAdminAuthPopupReturn(
        new URLSearchParams("embedded_admin_auth_popup=0"),
      ),
    ).toBe(false);
  });
});
