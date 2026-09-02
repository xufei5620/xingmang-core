import type { UserRole } from "../types";

export function shouldShowAdminReturn(
  adminWorkspace: boolean,
  role: UserRole | undefined,
  stepUpRequired: boolean,
) {
  return (
    !adminWorkspace &&
    (role === "admin" || (role === "user" && stepUpRequired))
  );
}

// XM-INV-HIDE-ADMIN-LOGIN: the login page's administrator-OIDC entry point
// (the "管理员登录" toggle and the "使用统一身份账号登录" button behind it)
// must only render for the admin area itself -- direct navigation to /admin
// or one of its sub-pages, or the console's /embed/admin/* entry points,
// which nginx always resolves to a /admin?ui_mode=embedded_admin... URL (see
// deploy/nginx/invoice.solov.cc.conf.template). It must never render on the
// user-facing login screen reached via /, /orders, /profiles or /records, or
// via the user embed's /embed/sub2api and /embed/newapi (which resolve to
// /?ui_mode=embedded&source=... -- not /admin). Business users authenticate
// with their platform password only; the admin OIDC login stays reachable
// exactly where it already was, just no longer advertised outside it.
//
// Matches the `pathname.startsWith("/admin")` convention App.tsx's own
// embeddedUserMode/DataProvider checks already use, so this stays consistent
// with how the rest of the app tells the admin area apart from the user one.
export function isAdminAreaPath(pathname: string): boolean {
  return pathname.startsWith("/admin");
}
