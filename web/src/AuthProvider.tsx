import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react";

import { invoiceApi } from "./lib/api";
import { subscribeAuthFailures } from "./lib/api-contract";
import {
  ADMIN_AUTH_POPUP_MESSAGE,
  isAdminAuthPopupCompleteMessage,
  isAdminAuthPopupReturn,
  parseEmbeddedAdminMode,
  withAdminAuthPopupReturnParam,
} from "./lib/embedded-admin-scope";
import type { AuthSession, AuthUser } from "./types";

// Parsed once from the initial URL, same pattern as App.tsx's
// embeddedUserMode/embeddedPlatform (and deliberately not imported from
// App.tsx: App.tsx imports AuthProvider, so the reverse would be circular).
const initialAuthURL = new URL(window.location.href);
const embeddedAdminMode = parseEmbeddedAdminMode(initialAuthURL.searchParams);
const isAdminAuthPopupReturnPage = isAdminAuthPopupReturn(
  initialAuthURL.searchParams,
);

type AuthContextValue = {
  loading: boolean;
  user: AuthUser | null;
  authenticated: boolean;
  error: string | null;
  stepUpRequired: boolean;
  refresh: () => Promise<void>;
  login: () => void;
  logout: () => Promise<void>;
  stepUp: () => void;
};

const AuthContext = createContext<AuthContextValue | null>(null);

function currentReturnTo() {
  return `${window.location.pathname}${window.location.search}${window.location.hash}`;
}

function navigateTopLevel(url: string) {
  // OIDC must never render inside the embedded invoice iframe. `_top` keeps
  // the authorization response, IdP pages and their anti-clickjacking headers
  // in the top-level browsing context. Login/step-up buttons provide the user
  // activation required by sandboxed custom-menu iframes.
  const opened = window.open(url, "_top");
  if (!opened && window.self === window.top) window.location.assign(url);
}

// Embedded admin mode cannot navigate `_top`: unlike the user embed, this
// iframe is hosted inside a persistent operator console (not a single-page
// embed), so dragging the whole console tab over to invoice.solov.cc would
// be far more disruptive than merely leaving the embed. OIDC login/step-up
// instead opens as a real popup window on the same click that triggered it
// (window.open requires a direct user gesture or browsers block it as a
// popup). Returns false if the popup was blocked so the caller can surface
// that to the admin instead of silently doing nothing.
function openAdminAuthPopup(url: string): boolean {
  const width = 520;
  const height = 700;
  const left = Math.max(0, Math.round(window.screenX + (window.outerWidth - width) / 2));
  const top = Math.max(0, Math.round(window.screenY + (window.outerHeight - height) / 2));
  const popup = window.open(
    url,
    "invoice-admin-auth",
    `width=${width},height=${height},left=${left},top=${top}`,
  );
  return popup != null;
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [session, setSession] = useState<AuthSession | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [stepUpRequired, setStepUpRequired] = useState(false);

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      const next = await invoiceApi.getSession();
      setSession(next);
      setStepUpRequired(
        next.authenticated && next.adminStepUpRequired === true,
      );
      setError(null);
    } catch (cause) {
      setSession(null);
      setError(cause instanceof Error ? cause.message : "无法读取登录状态。");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  useEffect(
    () =>
      subscribeAuthFailures((failure) => {
        if (failure === "login") setSession({ authenticated: false });
        else setStepUpRequired(true);
      }),
    [],
  );

  // This window *is* the popup: OIDC/step-up just finished (returnPath sent
  // the browser back here, cookie already set), so notify whichever window
  // opened us and close instead of ever rendering the app. window.opener is
  // always this same page's own window regardless of what frames the iframe
  // that opened the popup -- see embedded-admin-scope.ts's module doc.
  useEffect(() => {
    if (!isAdminAuthPopupReturnPage) return;
    if (window.opener) {
      try {
        (window.opener as Window).postMessage(
          ADMIN_AUTH_POPUP_MESSAGE,
          window.location.origin,
        );
      } catch {
        // Opener gone (closed/navigated) or unreachable; nothing else to do.
      }
    }
    window.close();
  }, []);

  // The other side of the same handshake: this is the window that opened the
  // popup, listening for its "done" signal so it can re-read the session
  // that just changed underneath it. Only ever armed in embedded admin mode,
  // and never on the popup return page itself (which is closing, not
  // listening). event.origin is checked against this page's own origin --
  // the popup is always same-origin with the page that opened it.
  useEffect(() => {
    if (!embeddedAdminMode || isAdminAuthPopupReturnPage) return;
    const handleMessage = (event: MessageEvent) => {
      if (event.origin !== window.location.origin) return;
      if (!isAdminAuthPopupCompleteMessage(event.data)) return;
      void refresh();
    };
    window.addEventListener("message", handleMessage);
    return () => window.removeEventListener("message", handleMessage);
  }, [refresh]);

  const value = useMemo<AuthContextValue>(
    () => ({
      loading,
      user: session?.authenticated ? session.user : null,
      authenticated: session?.authenticated === true,
      error,
      stepUpRequired,
      refresh,
      login: () => {
        if (embeddedAdminMode) {
          const opened = openAdminAuthPopup(
            invoiceApi.loginURL(withAdminAuthPopupReturnParam(currentReturnTo())),
          );
          if (!opened) setError("登录弹窗被浏览器拦截，请允许本站点弹出窗口后重试。");
          return;
        }
        navigateTopLevel(invoiceApi.loginURL(currentReturnTo()));
      },
      logout: async () => {
        const logoutURL = await invoiceApi.logout();
        setSession({ authenticated: false });
        setStepUpRequired(false);
        if (logoutURL) navigateTopLevel(logoutURL);
      },
      stepUp: () => {
        if (embeddedAdminMode) {
          const opened = openAdminAuthPopup(
            invoiceApi.adminStepUpURL(withAdminAuthPopupReturnParam(currentReturnTo())),
          );
          if (!opened) setError("验证弹窗被浏览器拦截，请允许本站点弹出窗口后重试。");
          return;
        }
        navigateTopLevel(invoiceApi.adminStepUpURL(currentReturnTo()));
      },
    }),
    [error, loading, refresh, session, stepUpRequired],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  const value = useContext(AuthContext);
  if (!value) throw new Error("AuthProvider is missing");
  return value;
}
