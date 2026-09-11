import { createContext, type ReactNode, useContext, useEffect, useMemo, useState } from "react";
import { invoiceApi } from "./lib/api";
import { httpInvoiceApi, retireInvoiceSession } from "./lib/http-api";
import { subscribeAuthFailures } from "./lib/api-contract";
import { createSessionRefresh, initialSessionState } from "./lib/auth-session";
import type { AuthUser } from "./types";

type AuthContextValue = {
  loading: boolean; user: AuthUser | null; authenticated: boolean; error: string | null;
  stepUpRequired: boolean; refresh: () => Promise<void>; logout: () => Promise<void>; stepUp: () => void;
};
const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children, staff = false, onStepUp, sessionRevision = 0 }: {
  children: ReactNode; staff?: boolean; onStepUp?: () => void; sessionRevision?: number;
}) {
  const [{ session, loading, error }, setSessionState] = useState(initialSessionState);
  const [stepUpRequired, setStepUpRequired] = useState(false);
  const controller = useMemo(() => createSessionRefresh(
    staff ? httpInvoiceApi.getStaffSession : invoiceApi.getSession,
    next => {
      setSessionState(next);
      if (!next.loading && !next.error) setStepUpRequired(next.session?.authenticated === true && next.session.adminStepUpRequired);
    },
  ), [staff]);
  useEffect(() => { void controller.refresh(); }, [controller, sessionRevision]);
  useEffect(() => subscribeAuthFailures(failure => {
    if (failure === "login") controller.invalidate();
    else setStepUpRequired(true);
  }), [controller]);
  useEffect(() => () => { controller.retire(); retireInvoiceSession(); }, [controller]);
  const value = useMemo<AuthContextValue>(() => ({
    loading, user: session?.authenticated ? session.user : null,
    authenticated: session?.authenticated === true, error, stepUpRequired,
    refresh: controller.refresh,
    logout: async () => {
      // The host owns staff logout; never revoke an unrelated customer cookie.
      if (staff) return;
      controller.invalidate(); setStepUpRequired(false); await invoiceApi.logout();
    },
    stepUp: () => onStepUp?.(),
  }), [loading, session, error, stepUpRequired, controller, staff, onStepUp]);
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  const value = useContext(AuthContext);
  if (!value) throw new Error("AuthProvider is missing");
  return value;
}
