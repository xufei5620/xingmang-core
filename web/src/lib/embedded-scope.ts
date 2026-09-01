import type { AuthUser, SourceAccount, SourceType } from "../types";
import { sourceName } from "./source-labels";

// XM-INV-EMBED-SCOPE: a platform embeds the invoice center in an iframe via
// `?ui_mode=embedded`; this optional second parameter narrows that embedded
// view to a single platform's data. An invoice_user can legitimately hold
// bound accounts on both platforms (see the multi-platform identity claim
// path), so without this the embedded iframe could show one platform's
// site-embedded users their New API spending mixed into a Sub2API view (or
// vice versa) -- confusing even though the underlying data is correctly
// scoped to that one signed-in user.

// Any other value (including an empty string) is treated as "unscoped",
// matching the existing ui_mode handling below.
export function parseEmbeddedPlatform(
  searchParams: URLSearchParams,
  embeddedUserMode: boolean,
): SourceType | null {
  if (!embeddedUserMode) return null;
  const raw = searchParams.get("platform");
  return raw === "sub2api" || raw === "newapi" ? raw : null;
}

// Mirrors App.tsx's userRoute handling of `ui_mode`: appends `platform` the
// same way so it survives in-app navigation between the embedded routes.
export function appendEmbeddedParams(
  path: string,
  embeddedUserMode: boolean,
  embeddedPlatform: SourceType | null,
): string {
  if (!embeddedUserMode) return path;
  const separator = path.includes("?") ? "&" : "?";
  const platformSuffix = embeddedPlatform ? `&platform=${embeddedPlatform}` : "";
  return `${path}${separator}ui_mode=embedded${platformSuffix}`;
}

// Narrows a list of platform-tagged items (funding lots, eligibility
// summaries, source account rows) down to one platform once the embedded
// view is scoped; unscoped views (embeddedPlatform === null) are returned
// unchanged.
export function scopeBySource<T extends { source: SourceType }>(
  items: T[],
  embeddedPlatform: SourceType | null,
): T[] {
  return embeddedPlatform
    ? items.filter((item) => item.source === embeddedPlatform)
    : items;
}

export interface AccountIdentityInput {
  user: Pick<
    AuthUser,
    "displayName" | "email" | "platform" | "platformUserId" | "username"
  >;
  // The `platform` URL param scoping the embedded view (see
  // parseEmbeddedPlatform), independent of the session's own platform.
  embeddedPlatform: SourceType | null;
  sourceAccounts: Pick<
    SourceAccount,
    "source" | "sourceLabel" | "externalUserIdMasked"
  >[];
}

// Builds the "which account am I looking at" label for the embedded view's
// header. Priority:
//  1. A platform-password session (user.platform set) identifies exactly
//     one account: prefer its email, then the raw captured username, then
//     fall back to "<platform label> · 用户 <platform user id>". Unlike
//     `displayName` (which the backend always backfills with a generic "用户"
//     placeholder when it has nothing better), `username` is only ever the
//     real captured account name -- present means show it, absent means
//     fall through, with no string-literal guessing needed.
//  2. An OIDC session scoped by the embedded `platform` param: show that
//     platform's bound source account, if any.
//  3. Otherwise the existing display name / email, same as the rest of the
//     app.
export function accountIdentityLabel({
  user,
  embeddedPlatform,
  sourceAccounts,
}: AccountIdentityInput): string {
  if (user.platform) {
    if (user.email) return user.email;
    if (user.username) return user.username;
    return `${sourceName[user.platform]} · 用户 ${user.platformUserId ?? "?"}`;
  }
  if (embeddedPlatform) {
    const bound = sourceAccounts.find(
      (account) => account.source === embeddedPlatform,
    );
    if (bound) return `${bound.sourceLabel} · ${bound.externalUserIdMasked}`;
  }
  return user.displayName || user.email || "已登录用户";
}
