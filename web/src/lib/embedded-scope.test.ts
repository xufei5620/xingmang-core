import { describe, expect, it } from "vitest";

import {
  accountIdentityLabel,
  appendEmbeddedParams,
  parseEmbeddedPlatform,
  resolveEmbeddedPlatform,
  scopeBySource,
} from "./embedded-scope";
import type { AuthUser, SourceAccount } from "../types";

describe("parseEmbeddedPlatform", () => {
  it("is always unscoped outside embedded mode, regardless of the param", () => {
    expect(
      parseEmbeddedPlatform(new URLSearchParams("platform=newapi"), false),
    ).toBeNull();
  });

  it("recognizes both valid platforms in embedded mode", () => {
    expect(
      parseEmbeddedPlatform(new URLSearchParams("platform=sub2api"), true),
    ).toBe("sub2api");
    expect(
      parseEmbeddedPlatform(new URLSearchParams("platform=newapi"), true),
    ).toBe("newapi");
  });

  it("treats a missing or unrecognized value as unscoped", () => {
    expect(parseEmbeddedPlatform(new URLSearchParams(""), true)).toBeNull();
    expect(
      parseEmbeddedPlatform(new URLSearchParams("platform=admin"), true),
    ).toBeNull();
    expect(
      parseEmbeddedPlatform(new URLSearchParams("platform="), true),
    ).toBeNull();
  });
});

describe("appendEmbeddedParams", () => {
  it("leaves the path untouched outside embedded mode", () => {
    expect(appendEmbeddedParams("/orders", false, "newapi")).toBe("/orders");
  });

  it("appends ui_mode alone when the view is unscoped", () => {
    expect(appendEmbeddedParams("/orders", true, null)).toBe(
      "/orders?ui_mode=embedded",
    );
  });

  it("appends platform after ui_mode when scoped", () => {
    expect(appendEmbeddedParams("/orders", true, "sub2api")).toBe(
      "/orders?ui_mode=embedded&platform=sub2api",
    );
  });

  it("uses & instead of ? when the path already carries a query string", () => {
    expect(
      appendEmbeddedParams("/records?request_id=abc", true, "newapi"),
    ).toBe("/records?request_id=abc&ui_mode=embedded&platform=newapi");
  });
});

describe("resolveEmbeddedPlatform", () => {
  it("prefers the session's platform over the URL param when both are set", () => {
    expect(resolveEmbeddedPlatform("sub2api", "newapi")).toBe("sub2api");
    expect(resolveEmbeddedPlatform("newapi", "sub2api")).toBe("newapi");
  });

  it("uses the session's platform even when the URL param is unscoped", () => {
    expect(resolveEmbeddedPlatform("sub2api", null)).toBe("sub2api");
  });

  it("falls back to the URL param only when the session has no platform", () => {
    expect(resolveEmbeddedPlatform(null, "newapi")).toBe("newapi");
    expect(resolveEmbeddedPlatform(undefined, "newapi")).toBe("newapi");
  });

  it("is unscoped when neither the session nor the URL param has a platform", () => {
    expect(resolveEmbeddedPlatform(null, null)).toBeNull();
    expect(resolveEmbeddedPlatform(undefined, null)).toBeNull();
  });
});

describe("scopeBySource", () => {
  const items = [
    { id: "1", source: "sub2api" as const },
    { id: "2", source: "newapi" as const },
    { id: "3", source: "sub2api" as const },
  ];

  it("returns every item unchanged when unscoped", () => {
    expect(scopeBySource(items, null)).toEqual(items);
  });

  it("keeps only the matching platform's items when scoped", () => {
    expect(scopeBySource(items, "sub2api")).toEqual([items[0], items[2]]);
    expect(scopeBySource(items, "newapi")).toEqual([items[1]]);
  });

  it("returns an empty list when nothing matches", () => {
    expect(scopeBySource([items[1]], "sub2api")).toEqual([]);
  });
});

describe("accountIdentityLabel", () => {
  const baseUser: Pick<
    AuthUser,
    "displayName" | "email" | "platform" | "platformUserId"
  > = {
    displayName: "用户",
    email: "",
    platform: null,
    platformUserId: null,
  };

  const boundAccounts: Pick<
    SourceAccount,
    "source" | "sourceLabel" | "externalUserIdMasked"
  >[] = [
    {
      source: "newapi",
      sourceLabel: "SoloV 模型平台",
      externalUserIdMasked: "n***8",
    },
  ];

  it("prefers the email on a platform-password session", () => {
    expect(
      accountIdentityLabel({
        user: {
          ...baseUser,
          platform: "newapi",
          platformUserId: "48",
          email: "person@example.com",
        },
        embeddedPlatform: null,
        sourceAccounts: [],
      }),
    ).toBe("person@example.com");
  });

  it("prefers a real display name over the platform+ID fallback", () => {
    expect(
      accountIdentityLabel({
        user: {
          ...baseUser,
          platform: "newapi",
          platformUserId: "48",
          displayName: "张三",
        },
        embeddedPlatform: null,
        sourceAccounts: [],
      }),
    ).toBe("张三");
  });

  it("falls back to '<platform> · 用户 <id>' when there is no email and no real name", () => {
    expect(
      accountIdentityLabel({
        user: { ...baseUser, platform: "newapi", platformUserId: "48" },
        embeddedPlatform: null,
        sourceAccounts: [],
      }),
    ).toBe("SoloV 模型平台 · 用户 48");
  });

  it("still produces a label when platformUserId is unexpectedly missing", () => {
    expect(
      accountIdentityLabel({
        user: { ...baseUser, platform: "sub2api", platformUserId: null },
        embeddedPlatform: null,
        sourceAccounts: [],
      }),
    ).toBe("SoloV API · 用户 ?");
  });

  it("shows the embedded platform's bound account for an OIDC session", () => {
    expect(
      accountIdentityLabel({
        user: { ...baseUser, displayName: "李四" },
        embeddedPlatform: "newapi",
        sourceAccounts: boundAccounts,
      }),
    ).toBe("SoloV 模型平台 · n***8");
  });

  it("falls back to display name/email when the OIDC user has no binding for the scoped platform", () => {
    expect(
      accountIdentityLabel({
        user: { ...baseUser, displayName: "李四", email: "li@example.com" },
        embeddedPlatform: "sub2api",
        sourceAccounts: boundAccounts,
      }),
    ).toBe("李四");
  });

  it("falls back to the generic label when an unscoped OIDC user has neither name nor email", () => {
    expect(
      accountIdentityLabel({
        user: { ...baseUser, displayName: "" },
        embeddedPlatform: null,
        sourceAccounts: [],
      }),
    ).toBe("已登录用户");
  });
});
