import type { ReactNode } from "react";
import type { AuthUser } from "./types";

export function SessionBoundary({ loading, authenticated, user, error, retry, pending, login, children }: {
  loading: boolean; authenticated: boolean; user: AuthUser | null; error: string | null;
  retry: () => void; pending: ReactNode; login: ReactNode; children: ReactNode;
}) {
  if (!authenticated || !user) return loading ? pending : login;
  const blocked = loading || error !== null;
  // Token rotation keeps this key. A different principal or authorization
  // scope remounts all data/forms, including their in-memory submission keys.
  const scope = JSON.stringify([user.id, user.role, user.platform, user.platformUserId]);
  return (
    <div key={scope} aria-busy={loading}>
      {loading ? pending : error ? (
        <div role="alert">
          <p>{error} 身份尚未确认，已暂停操作。</p>
          <button onClick={retry}>重新核对登录状态</button>
        </div>
      ) : null}
      <div hidden={blocked} inert={blocked}>{children}</div>
    </div>
  );
}
