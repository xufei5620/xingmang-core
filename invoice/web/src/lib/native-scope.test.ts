import { expect, it } from "vitest";
import { adminWorkspacePath } from "./workspace-router";
import { resolvePlatformSourceInstanceId } from "./admin-scope";
import { scopeBySource, accountIdentityLabel } from "./source-scope";

it.each(["https://example.test", "//example.test", "/admin/accounts/ledger", "/admin?view=issued"])("global view cannot turn %s into a per-platform workflow", path => {
  expect(adminWorkspacePath(path, "global")).toBe("/admin/settings");
});
it("allows only the matching native subpages and keeps issued history navigation", () => {
  expect(adminWorkspacePath("/admin/payment-candidates", "sub2api")).toBe("/admin");
  expect(adminWorkspacePath("/admin/payment-candidates", "newapi")).toBe("/admin/payment-candidates");
  expect(adminWorkspacePath("/admin?view=issued", "sub2api")).toBe("/admin?view=issued");
});
it("never chooses an arbitrary source when the health report is missing or ambiguous", () => {
  expect(resolvePlatformSourceInstanceId("sub2api", [])).toBeNull();
  expect(resolvePlatformSourceInstanceId("sub2api", [{ sourceType: "newapi", sourceInstanceId: "new" }])).toBeNull();
  expect(resolvePlatformSourceInstanceId("sub2api", [{ sourceType: "sub2api", sourceInstanceId: "a" }, { sourceType: "sub2api", sourceInstanceId: "b" }])).toBeNull();
  expect(resolvePlatformSourceInstanceId("sub2api", [{ sourceType: "sub2api", sourceInstanceId: "a" }, { sourceType: "sub2api", sourceInstanceId: "a" }])).toBe("a");
});
it("preserves customer platform separation and identifies username-only accounts", () => {
  const rows = [{ source: "sub2api" as const }, { source: "newapi" as const }];
  expect(scopeBySource(rows, "sub2api")).toEqual([rows[0]]);
  expect(scopeBySource(rows, "newapi")).toEqual([rows[1]]);
  expect(accountIdentityLabel({ user: { platform: "newapi", platformUserId: "42", username: "account", displayName: "用户", email: "" } })).toBe("account");
});
