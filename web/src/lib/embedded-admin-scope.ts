import type { SourceType } from "../types";

// XM-INV-ADMIN-EMBED (CR-0005): the xingmang platform console embeds the
// invoice admin console per-platform via `?ui_mode=embedded_admin`
// (+ `platform=sub2api|newapi`, or `?scope=global` for the cross-platform
// governance tab). This mirrors lib/embedded-scope.ts's existing
// `?ui_mode=embedded` user-embed pattern, but is kept in its own module
// rather than extending that one: embedded-scope.ts is user-facing
// session/scoping code the concurrent XM-INV-PLATFORM-SCOPE branch is
// actively editing, and this task's brief asks admin-mode changes to stay
// out of it so the merge stays clean.
//
// Everything in this file is presentation/view scoping only -- per CR-0005,
// "这是视图约束而非安全边界" (a view constraint, not a security boundary).
// Real authorization is entirely the existing admin session/RBAC/IP-allowlist
// chain (RequireAdmin, the `require("admin", ...)` HTTP middleware); nothing
// here participates in it, and a malformed or hostile URL can only ever
// widen *what an already-authenticated admin's browser chrome shows them*,
// never bypass or narrow authentication.

export type EmbeddedAdminScope =
  | { kind: "platform"; platform: SourceType }
  | { kind: "global" }
  | null;

export function parseEmbeddedAdminMode(searchParams: URLSearchParams): boolean {
  return searchParams.get("ui_mode") === "embedded_admin";
}

// Resolves the `platform=`/`scope=global` pair. Only meaningful alongside
// embedded-admin mode (matching parseEmbeddedPlatform's relationship to
// embeddedUserMode). An embedded-admin URL with neither a recognized
// platform nor `scope=global` (hand-typed, or a future embedder bug) parses
// to `null`: the caller must treat that as "cannot scope yet", not as
// "standalone" -- see isAdminNavItemVisible's and the source-instance
// resolution's doc comments for what null means at each of those call sites.
export function parseEmbeddedAdminScope(
  searchParams: URLSearchParams,
  embeddedAdminMode: boolean,
): EmbeddedAdminScope {
  if (!embeddedAdminMode) return null;
  if (searchParams.get("scope") === "global") return { kind: "global" };
  const platform = searchParams.get("platform");
  if (platform === "sub2api" || platform === "newapi") return { kind: "platform", platform };
  return null;
}

// Mirrors embedded-scope.ts's appendEmbeddedParams: keeps `ui_mode=
// embedded_admin` (+ platform/scope) attached across in-app navigation
// between the admin sub-pages, the same way `ui_mode`/`platform` already
// survive for the user embed. Unlike the user embed's userRoute, standalone
// admin (embeddedAdminMode === false) must stay byte-for-byte unchanged --
// this is a no-op in that case.
export function appendEmbeddedAdminParams(
  path: string,
  embeddedAdminMode: boolean,
  scope: EmbeddedAdminScope,
): string {
  if (!embeddedAdminMode) return path;
  const separator = path.includes("?") ? "&" : "?";
  const scopeSuffix =
    scope?.kind === "platform"
      ? `&platform=${scope.platform}`
      : scope?.kind === "global"
        ? `&scope=global`
        : "";
  return `${path}${separator}ui_mode=embedded_admin${scopeSuffix}`;
}

// The admin nav's fixed set of destinations (adminNav in App.tsx), tagged so
// visibility can be decided without string-matching hrefs. "审核工作台" and
// "发票档案" are two nav entries for the same underlying page/queue (CR-0005
// treats "申请审核（含发票档案视图）" as one item) and share the "review" key.
export type AdminNavItemKey =
  | "review"
  | "payment-candidates"
  | "eligibility-freezes"
  | "refund-cases"
  | "source-health"
  | "settings"
  | "return-to-user";

// CR-0005 (b): platform mode renders 申请审核(+发票档案)/资格冻结/退款与红冲/
// 源健康, plus 支付候选 for NewAPI only (Sub2API structurally has no payment-
// candidate queue -- newapi-only server-side already, not merely hidden).
// global mode renders only 系统设置 and the (unfiltered, both-platform)
// source-health overview. "返回用户端" never applies inside an admin-only
// embed. Standalone (scope === null, including an unparseable embedded-admin
// URL -- see parseEmbeddedAdminScope) shows everything unchanged: CR-0005 (f)
// requires the standalone console to keep working exactly as it does today,
// and an unresolved scope inside an actual embed degrades to "show
// everything" rather than a blank nav.
export function isAdminNavItemVisible(key: AdminNavItemKey, scope: EmbeddedAdminScope): boolean {
  if (!scope) return true;
  if (key === "return-to-user") return false;
  if (scope.kind === "global") return key === "settings" || key === "source-health";
  if (key === "settings") return false;
  if (key === "payment-candidates") return scope.platform === "newapi";
  return true;
}

export interface SourceHealthPlatformRow {
  sourceType: SourceType;
  sourceInstanceId: string;
}

// Resolves one platform's source_instance_id from the admin source-health
// report -- the one admin endpoint every embedded admin session already
// loads that exposes a source_type -> source_instance_id mapping -- so
// platform-scoped list requests can filter server-side without ever
// hardcoding a UUID (CR-0005 (c) is explicit that they must not). All 5
// streams for a platform share its source_instance_id; the first match is
// authoritative. Returns null both while unloaded and if the platform
// genuinely has no configured source instance -- callers must treat null as
// "cannot scope this request yet" (send it unscoped, or wait), never as
// "no filter needed".
export function resolvePlatformSourceInstanceId(
  platform: SourceType,
  sourceHealthItems: readonly SourceHealthPlatformRow[],
): string | null {
  return (
    sourceHealthItems.find((item) => item.sourceType === platform)?.sourceInstanceId ?? null
  );
}

// --- Popup-based OIDC login / admin step-up (CR-0005 (d)) ------------------
//
// Keycloak refuses to be framed, and the security architecture requires
// sensitive authentication to happen at the top level -- but for an admin
// embed, "top level" cannot mean navigating _top like the existing user
// embed does: that would drag the whole hosting console page over to
// invoice.solov.cc instead of just this iframe. Embedded admin mode instead
// opens OIDC login/step-up in a real popup window (window.open, on the
// click that triggers login/step-up -- browsers require a direct user
// gesture for this or they block it as a popup).
//
// No pre-existing postMessage mechanism was found anywhere in this web app
// to extend for the popup's "I'm done" signal to its opener -- only
// aspirational text in docs/SECURITY-ARCHITECTURE.md and an unused
// EMBED_ALLOWED_PARENT_ORIGINS env var/doc entry, neither wired into any Go
// or TypeScript code. What follows is therefore a new, narrow, same-origin
// channel: a popup opened by this same page notifying *this page* (its
// window.opener, from the popup's own point of view) that OIDC finished.
// This is a different channel from the cross-origin one further down this
// file (this page notifying its *framing* console, via window.parent, of
// its content height) -- that one now exists too (XM-INVCON0's
// EmbeddedConsoleFrame is merged and listens for it), this one still
// doesn't need it: the popup and the page that opened it are always the
// same origin (invoice.solov.cc) regardless of what frames the iframe, so
// the only origin this channel ever needs to accept is this page's own --
// see isAdminAuthPopupCompleteMessage's callers in AuthProvider.tsx.

export const ADMIN_AUTH_POPUP_MESSAGE = {
  source: "invoice-admin-embed-auth",
  version: 1,
  type: "auth-complete",
} as const;

export function isAdminAuthPopupCompleteMessage(data: unknown): boolean {
  if (typeof data !== "object" || data === null) return false;
  const value = data as Record<string, unknown>;
  return (
    value.source === ADMIN_AUTH_POPUP_MESSAGE.source &&
    value.version === ADMIN_AUTH_POPUP_MESSAGE.version &&
    value.type === ADMIN_AUTH_POPUP_MESSAGE.type
  );
}

// Marks a `return_to` URL as "OIDC/step-up just finished in the popup that
// was sent here; notify the opener and close" rather than a real navigation
// target. The popup keeps the rest of the path/query it was opened for (so a
// popup a user reopens directly, or one whose opener has vanished, still
// lands somewhere coherent instead of a dead marker-only URL) -- the marker
// alone drives the notify-and-close effect in AuthProvider.
const ADMIN_AUTH_POPUP_RETURN_PARAM = "embedded_admin_auth_popup";

export function isAdminAuthPopupReturn(searchParams: URLSearchParams): boolean {
  return searchParams.get(ADMIN_AUTH_POPUP_RETURN_PARAM) === "1";
}

export function withAdminAuthPopupReturnParam(returnTo: string): string {
  const separator = returnTo.includes("?") ? "&" : "?";
  return `${returnTo}${separator}${ADMIN_AUTH_POPUP_RETURN_PARAM}=1`;
}

// --- Height sync to the hosting console -------------------------------
//
// The platform side (XM-INVCON0, merged) sizes the iframe from a message
// this page posts to its own window.parent -- the cross-origin channel the
// popup-auth handshake above deliberately isn't. Exactly one parent origin
// is ever valid to send this to: the same one this page's own
// frame-ancestors CSP names (deploy/nginx/invoice.solov.cc.conf.template's
// /admin location, and web/nginx.conf's matching @admin_spa block) -- never
// "*", and never anything computed from the page's own embedding context
// (there is no reliable way to read the framing origin from inside a
// cross-origin iframe, nor should there be).

export const XM_EMBED_CONSOLE_ORIGIN = "https://console.solov.cc";

export interface XmEmbedHeightMessage {
  type: "xm-embed";
  version: 1;
  kind: "height";
  height: number;
}

export function buildXmEmbedHeightMessage(height: number): XmEmbedHeightMessage {
  return { type: "xm-embed", version: 1, kind: "height", height };
}

// Height sync must run only for this admin embed, and only when this page
// is actually framed -- never in standalone /admin (nothing to size for)
// and never in the user embed (`?ui_mode=embedded`), which predates this
// contract and has no listener for it. `isFramed` is the caller's own
// `window.parent !== window` check, passed in rather than read here so this
// stays a plain, environment-free predicate to unit test.
export function shouldSyncEmbeddedAdminHeight(
  embeddedAdminMode: boolean,
  isFramed: boolean,
): boolean {
  return embeddedAdminMode && isFramed;
}
