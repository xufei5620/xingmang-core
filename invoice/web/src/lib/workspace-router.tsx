import { createContext, useCallback, useContext, useMemo, type ReactNode } from "react";
import { Link as RouterLink, NavLink as RouterNavLink, Navigate as RouterNavigate,
  useLocation as useHostLocation, useNavigate as useHostNavigate,
  type LinkProps, type NavLinkProps, type NavigateProps, type NavigateFunction, type NavigateOptions, type To } from "react-router";
import { isAdminNavItemVisible, type AdminNavItemKey, type NativeAdminMode } from "./admin-scope";
export { Route, Routes } from "react-router";
export type { NativeAdminMode } from "./admin-scope";

const pageKeys: Record<string, AdminNavItemKey> = {
  "/admin": "review", "/admin/payment-candidates": "payment-candidates",
  "/admin/eligibility-freezes": "eligibility-freezes", "/admin/accounts/ledger": "account-ledger",
  "/admin/refund-cases": "refund-cases", "/admin/settings": "settings", "/admin/source-health": "source-health",
};
export function adminWorkspacePath(raw: string | null, mode: NativeAdminMode): string {
  const fallback = mode === "global" ? "/admin/settings" : "/admin";
  if (!raw || !raw.startsWith("/admin") || raw.includes("\\")) return fallback;
  const [pathname = "", search = ""] = raw.split("?");
  if (!pageKeys[pathname] || !isAdminNavItemVisible(pageKeys[pathname]!, mode)) return fallback;
  return pathname + (new URLSearchParams(search).get("view") === "issued" && pathname === "/admin" ? "?view=issued" : "");
}
type NativeContext = { mode: NativeAdminMode; path: string; toHost: (to: To) => To };
const NativeContext = createContext<NativeContext | null>(null);
export const useNativeAdmin = () => useContext(NativeContext);
export function useAdminPlatform() { const native = useNativeAdmin(); return native?.mode && native.mode !== "global" ? native.mode : null; }
export function NativeAdminRouter({ mode, children }: { mode: NativeAdminMode; children: ReactNode }) {
  const host = useHostLocation();
  const value = useMemo<NativeContext>(() => ({ mode,
    path: adminWorkspacePath(new URLSearchParams(host.search).get("invoice_path"), mode),
    toHost: to => {
      const target = typeof to === "string" ? to : `${to.pathname ?? "/admin"}${to.search ?? ""}`;
      const params = new URLSearchParams(host.search);
      params.set("invoice_path", adminWorkspacePath(target, mode));
      return { pathname: host.pathname, search: `?${params}`, hash: host.hash };
    },
  }), [mode, host.pathname, host.search, host.hash]);
  return <NativeContext.Provider value={value}>{children}</NativeContext.Provider>;
}
export function useLocation() {
  const host = useHostLocation(); const native = useNativeAdmin();
  if (!native) return host;
  const [pathname = "/admin", query] = native.path.split("?");
  return { ...host, pathname, search: query ? `?${query}` : "", hash: "" };
}
export function useNavigate(): NavigateFunction {
  const navigate = useHostNavigate(); const native = useNativeAdmin();
  return useCallback(((to: To | number, options?: NavigateOptions) =>
    typeof to === "number" ? navigate(to) : navigate(native ? native.toHost(to) : to, options)) as NavigateFunction, [navigate, native]);
}
export function Link(props: LinkProps) { const native = useNativeAdmin(); return <RouterLink {...props} to={native ? native.toHost(props.to) : props.to} />; }
export function Navigate(props: NavigateProps) { const native = useNativeAdmin(); return <RouterNavigate {...props} to={native ? native.toHost(props.to) : props.to} />; }
export function NavLink(props: NavLinkProps) {
  const native = useNativeAdmin();
  if (!native) return <RouterNavLink {...props} />;
  const target = typeof props.to === "string" ? props.to : `${props.to.pathname ?? ""}${props.to.search ?? ""}`;
  const active = native.path === target;
  const state = { isActive: active, isPending: false, isTransitioning: false };
  const { className, style, children, end: _end, caseSensitive: _caseSensitive, ...linkProps } = props;
  return <RouterLink {...linkProps} to={native.toHost(props.to)} aria-current={active ? "page" : undefined}
    style={typeof style === "function" ? style(state) : style}
    className={typeof className === "function" ? className(state) : className}>
    {typeof children === "function" ? children(state) : children}
  </RouterLink>;
}
