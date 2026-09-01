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

// XM-INV-PLATFORM-SCOPE (CR-0003): resolves the embedded view's effective
// platform scope. A platform-password session's own login platform is
// authoritative once known -- it is what the backend actually enforces
// server-side on every /api/v1/user/* endpoint, so the frontend must agree
// with it rather than trust a URL param the embedder could get wrong (or
// simply not bother setting, now that the server enforces the boundary on
// its own). The `platform` URL param (parseEmbeddedPlatform) remains a
// fallback for a session with no platform -- an OIDC administrator embed, or
// the brief window before the session finishes loading.
export function resolveEmbeddedPlatform(
  sessionPlatform: SourceType | null | undefined,
  urlPlatform: SourceType | null,
): SourceType | null {
  return sessionPlatform ?? urlPlatform;
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

// The exact literal production_auth.go's maskedEmailName returns for a
// platform-password account with no email on file -- the only case where
// the backend truly has nothing more specific to show. Comparing against it
// (rather than always preferring the platform+ID form whenever a session
// carries a platform) keeps this forward-compatible with a real captured
// username ever landing in displayName without also having an email.
const GENERIC_DISPLAY_NAME_FALLBACK = "用户";

export interface AccountIdentityInput {
  user: Pick<AuthUser, "displayName" | "email" | "platform" | "platformUserId">;
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
//     one account: prefer its email, then a real display name, then fall
//     back to "<platform label> · 用户 <platform user id>".
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
    if (user.displayName && user.displayName !== GENERIC_DISPLAY_NAME_FALLBACK)
      return user.displayName;
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
