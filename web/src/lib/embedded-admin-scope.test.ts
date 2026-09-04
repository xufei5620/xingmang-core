import { describe, expect, it } from "vitest";

import {
  ADMIN_ASSERTION_NEEDED_MAX_ATTEMPTS,
  ADMIN_ASSERTION_NEEDED_RETRY_DELAYS_MS,
  ADMIN_ASSERTION_NEEDED_STEADY_INTERVAL_MS,
  ADMIN_AUTH_POPUP_MESSAGE,
  appendEmbeddedAdminParams,
  buildXmEmbedAdminAssertionNeededMessage,
  buildXmEmbedHeightMessage,
  shouldPostEmbeddedAdminHeight,
  XM_EMBED_HEIGHT_DEAD_BAND_PX,
  isAdminAuthPopupCompleteMessage,
  isAdminAuthPopupReturn,
  isAdminNavItemVisible,
  nextAdminAssertionNeededDelayMs,
  parseEmbeddedAdminMode,
  parseEmbeddedAdminScope,
  parseXmEmbedAdminAssertionMessage,
  resolvePlatformSourceInstanceId,
  shouldExchangeAdminAssertion,
  measureEmbeddedAdminHeight,
  shouldRequestAdminAssertion,
  shouldScheduleNextAdminAssertionNeededAttempt,
  shouldSyncEmbeddedAdminHeight,
  withAdminAuthPopupReturnParam,
  XM_EMBED_CONSOLE_ORIGIN,
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
      "account-ledger",
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
    expect(isAdminNavItemVisible("account-ledger", scope)).toBe(true);
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
    expect(isAdminNavItemVisible("account-ledger", scope)).toBe(false);
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

describe("embedded admin height sync", () => {
  it("builds the exact contract the console listens for", () => {
    expect(buildXmEmbedHeightMessage(842)).toEqual({
      type: "xm-embed",
      version: 1,
      kind: "height",
      height: 842,
    });
  });

  it("always targets the one approved console origin, never a wildcard", () => {
    expect(XM_EMBED_CONSOLE_ORIGIN).toBe("https://console.solov.cc");
    expect(XM_EMBED_CONSOLE_ORIGIN).not.toBe("*");
  });

  it("syncs only when embedded-admin mode is on and the page is actually framed", () => {
    expect(shouldSyncEmbeddedAdminHeight(true, true)).toBe(true);
  });

  it("never syncs standalone admin, even if somehow framed", () => {
    expect(shouldSyncEmbeddedAdminHeight(false, true)).toBe(false);
  });

  it("never syncs the user embed (embeddedAdminMode is false there)", () => {
    // The user embed sets embeddedUserMode, not embeddedAdminMode -- from
    // this function's point of view that is indistinguishable from
    // standalone: embeddedAdminMode false either way.
    expect(shouldSyncEmbeddedAdminHeight(false, true)).toBe(false);
  });

  it("never syncs when not actually framed, even in embedded-admin mode", () => {
    expect(shouldSyncEmbeddedAdminHeight(true, false)).toBe(false);
  });
});

describe("parseXmEmbedAdminAssertionMessage", () => {
  const validAssertion = "aaaa.bbbb.cccc";

  it("accepts the exact console-assertion envelope", () => {
    expect(
      parseXmEmbedAdminAssertionMessage({
        type: "xm-embed",
        version: 1,
        kind: "admin-assertion",
        assertion: validAssertion,
      }),
    ).toBe(validAssertion);
  });

  it("ignores a malformed message instead of throwing", () => {
    for (const data of [
      null,
      undefined,
      "a string, not an object",
      42,
      [],
      {},
      { type: "xm-embed", version: 1, kind: "height", height: 10 },
      { type: "xm-embed", version: 2, kind: "admin-assertion", assertion: validAssertion },
      { type: "other", version: 1, kind: "admin-assertion", assertion: validAssertion },
      { type: "xm-embed", version: 1, kind: "admin-assertion" },
      { type: "xm-embed", version: 1, kind: "admin-assertion", assertion: 12345 },
      { type: "xm-embed", version: 1, kind: "admin-assertion", assertion: "" },
      { type: "xm-embed", version: 1, kind: "admin-assertion", assertion: "not-three-segments" },
      { type: "xm-embed", version: 1, kind: "admin-assertion", assertion: "a..c" },
      { type: "xm-embed", version: 1, kind: "admin-assertion", assertion: "a.b.c.d" },
      {
        type: "xm-embed",
        version: 1,
        kind: "admin-assertion",
        assertion: "a".repeat(9000),
      },
    ]) {
      expect(parseXmEmbedAdminAssertionMessage(data)).toBeNull();
    }
  });

  it("never confuses the height-sync message for an assertion", () => {
    expect(
      parseXmEmbedAdminAssertionMessage(buildXmEmbedHeightMessage(400)),
    ).toBeNull();
  });
});

describe("buildXmEmbedAdminAssertionNeededMessage", () => {
  it("builds the exact contract the console listens for", () => {
    expect(buildXmEmbedAdminAssertionNeededMessage()).toEqual({
      type: "xm-embed",
      version: 1,
      kind: "admin-assertion-needed",
    });
  });

  it("is never confused with the height or admin-assertion messages", () => {
    const needed = buildXmEmbedAdminAssertionNeededMessage();
    expect(parseXmEmbedAdminAssertionMessage(needed)).toBeNull();
    expect(needed).not.toEqual(buildXmEmbedHeightMessage(400));
  });
});

describe("nextAdminAssertionNeededDelayMs", () => {
  it("follows the widening backoff for the first four retries", () => {
    expect(nextAdminAssertionNeededDelayMs(0)).toBe(2000);
    expect(nextAdminAssertionNeededDelayMs(1)).toBe(4000);
    expect(nextAdminAssertionNeededDelayMs(2)).toBe(8000);
    expect(nextAdminAssertionNeededDelayMs(3)).toBe(16000);
    expect(ADMIN_ASSERTION_NEEDED_RETRY_DELAYS_MS).toEqual([2000, 4000, 8000, 16000]);
  });

  it("settles to the fixed steady interval after the backoff is exhausted", () => {
    expect(nextAdminAssertionNeededDelayMs(4)).toBe(ADMIN_ASSERTION_NEEDED_STEADY_INTERVAL_MS);
    expect(nextAdminAssertionNeededDelayMs(5)).toBe(ADMIN_ASSERTION_NEEDED_STEADY_INTERVAL_MS);
    expect(nextAdminAssertionNeededDelayMs(39)).toBe(ADMIN_ASSERTION_NEEDED_STEADY_INTERVAL_MS);
    expect(ADMIN_ASSERTION_NEEDED_STEADY_INTERVAL_MS).toBe(30000);
  });
});

describe("shouldScheduleNextAdminAssertionNeededAttempt", () => {
  it("allows scheduling below the named attempt cap", () => {
    expect(shouldScheduleNextAdminAssertionNeededAttempt(0)).toBe(true);
    expect(shouldScheduleNextAdminAssertionNeededAttempt(ADMIN_ASSERTION_NEEDED_MAX_ATTEMPTS - 1)).toBe(
      true,
    );
  });

  it("stops for good once the cap is reached, never exceeding it", () => {
    expect(shouldScheduleNextAdminAssertionNeededAttempt(ADMIN_ASSERTION_NEEDED_MAX_ATTEMPTS)).toBe(
      false,
    );
    expect(
      shouldScheduleNextAdminAssertionNeededAttempt(ADMIN_ASSERTION_NEEDED_MAX_ATTEMPTS + 1),
    ).toBe(false);
  });
});

describe("measureEmbeddedAdminHeight", () => {
  // XM-INV-EMBED-HEIGHT: the root element stops growing once the console has
  // sized the frame (the embedded layout pins it to min-height: 100vh), so a
  // measurement that reads it alone goes quiet exactly when a table starts
  // filling in. Taking the larger of the two boxes keeps tracking.
  it("takes the larger of the two scroll heights", () => {
    expect(
      measureEmbeddedAdminHeight({
        documentElement: { scrollHeight: 560 },
        body: { scrollHeight: 940 },
      }),
    ).toBe(940);
  });

  it("still uses the root element when it is the taller one", () => {
    expect(
      measureEmbeddedAdminHeight({
        documentElement: { scrollHeight: 1200 },
        body: { scrollHeight: 800 },
      }),
    ).toBe(1200);
  });

  it("ignores a missing or zero box rather than reporting it", () => {
    expect(
      measureEmbeddedAdminHeight({ documentElement: { scrollHeight: 700 }, body: null }),
    ).toBe(700);
    expect(
      measureEmbeddedAdminHeight({ documentElement: { scrollHeight: 0 }, body: { scrollHeight: 640 } }),
    ).toBe(640);
  });

  it("reports 0 when neither box is usable, so the caller posts nothing", () => {
    expect(measureEmbeddedAdminHeight({ documentElement: null, body: null })).toBe(0);
    expect(
      measureEmbeddedAdminHeight({
        documentElement: { scrollHeight: Number.NaN },
        body: { scrollHeight: 0 },
      }),
    ).toBe(0);
  });
});

describe("shouldRequestAdminAssertion", () => {
  it("posts only while framed, session-check resolved, and unauthenticated", () => {
    expect(shouldRequestAdminAssertion(true, false, false)).toBe(true);
  });

  it("never posts when not framed, even mid-login-card", () => {
    expect(shouldRequestAdminAssertion(false, false, false)).toBe(false);
  });

  it("never posts while the session check is still in flight", () => {
    expect(shouldRequestAdminAssertion(true, true, false)).toBe(false);
  });

  it("never posts once authenticated", () => {
    expect(shouldRequestAdminAssertion(true, false, true)).toBe(false);
  });

  // XM-INV-ASSERT-STEPUP: an authenticated session whose administrator
  // step-up has lapsed still needs a fresh assertion -- exchanging one issues
  // a session with a fresh mfa_at, which IS the step-up for this login path.
  // With the Keycloak step-up route closed there is no other way forward.
  it("posts again when an authenticated session needs administrator step-up", () => {
    expect(shouldRequestAdminAssertion(true, false, true, true)).toBe(true);
  });

  it("still withholds the step-up request while the session check is in flight", () => {
    expect(shouldRequestAdminAssertion(true, true, true, true)).toBe(false);
  });

  it("still withholds the step-up request when not framed", () => {
    expect(shouldRequestAdminAssertion(false, false, true, true)).toBe(false);
  });

  it("defaults the step-up flag to false so existing callers are unchanged", () => {
    expect(shouldRequestAdminAssertion(true, false, true)).toBe(false);
    expect(shouldRequestAdminAssertion(true, false, false)).toBe(true);
  });

  it("never posts when both loading and authenticated are true (an impossible but still-safe combination)", () => {
    expect(shouldRequestAdminAssertion(true, true, true)).toBe(false);
  });
});

describe("shouldExchangeAdminAssertion", () => {
  it("exchanges the first assertion seen", () => {
    expect(shouldExchangeAdminAssertion("aaaa.bbbb.cccc", null)).toBe(true);
  });

  it("refuses to re-exchange the exact same assertion (the ready + onLoad double-delivery case)", () => {
    expect(shouldExchangeAdminAssertion("aaaa.bbbb.cccc", "aaaa.bbbb.cccc")).toBe(false);
  });

  it("still exchanges a genuinely fresh assertion, even after a prior one already succeeded", () => {
    expect(shouldExchangeAdminAssertion("dddd.eeee.ffff", "aaaa.bbbb.cccc")).toBe(true);
  });
});

// XM-INV-EMBED-LOOP: production walked the frame upward 2px a round -- 650,
// 651, 653, 655 ... 761 and climbing -- because the embedded layout was
// `min-height: 100vh`, so the document could never be shorter than the frame
// the console had just sized from this page's own report, and the iframe's
// 1px borders made every measurement come back a little taller. The layout
// fix removes that coupling; the dead band makes any future one inert.
describe("shouldPostEmbeddedAdminHeight", () => {
  it("posts the first real measurement", () => {
    expect(shouldPostEmbeddedAdminHeight(700, 0)).toBe(true);
  });

  it("never posts a height of zero, even as the first one", () => {
    expect(shouldPostEmbeddedAdminHeight(0, 0)).toBe(false);
    expect(shouldPostEmbeddedAdminHeight(-1, 0)).toBe(false);
  });

  it("says nothing when the measurement did not move", () => {
    expect(shouldPostEmbeddedAdminHeight(700, 700)).toBe(false);
  });

  it("breaks the feedback loop instead of riding it", () => {
    // What production actually did: the page reported H, the console sized
    // the frame to H, and the next measurement came back H+2 -- the iframe's
    // own borders. Posting that started the next round, and the pair walked
    // upward 2px at a time (650, 651, 653, 655 ... 761) until the console's
    // clamp caught them.
    //
    // The measurement is a function of the frame, so refusing to post is
    // what ends it: the console never resizes, the next measurement is the
    // same H+2, and it is refused again. The loop stops on the first round
    // rather than converging slowly.
    const posted = 650;
    for (let round = 0; round < 50; round += 1) {
      expect(shouldPostEmbeddedAdminHeight(posted + 2, posted)).toBe(false);
    }
  });

  it("still reports real growth, in either direction", () => {
    expect(shouldPostEmbeddedAdminHeight(650 + XM_EMBED_HEIGHT_DEAD_BAND_PX, 650)).toBe(true);
    expect(shouldPostEmbeddedAdminHeight(1200, 650)).toBe(true);
    expect(shouldPostEmbeddedAdminHeight(480, 1200)).toBe(true);
  });
});
