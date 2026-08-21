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
import type { AuthSession, AuthUser } from "./types";

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

  const value = useMemo<AuthContextValue>(
    () => ({
      loading,
      user: session?.authenticated ? session.user : null,
      authenticated: session?.authenticated === true,
      error,
      stepUpRequired,
      refresh,
      login: () => navigateTopLevel(invoiceApi.loginURL(currentReturnTo())),
      logout: async () => {
        const logoutURL = await invoiceApi.logout();
        setSession({ authenticated: false });
        setStepUpRequired(false);
        if (logoutURL) navigateTopLevel(logoutURL);
      },
      stepUp: () =>
        navigateTopLevel(invoiceApi.adminStepUpURL(currentReturnTo())),
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
