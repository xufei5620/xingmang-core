import type { AuthSession } from "../types";

export interface SessionState {
  session: AuthSession | null;
  loading: boolean;
  error: string | null;
}
export const initialSessionState: SessionState = { session: null, loading: true, error: null };

export function createSessionRefresh(
  read: (isCurrent: () => boolean) => Promise<AuthSession>,
  publish: (state: SessionState) => void,
) {
  let state = initialSessionState;
  let revision = 0;
  const update = (next: SessionState) => { state = next; publish(state); };
  return {
    async refresh() {
      const current = ++revision;
      update({ ...state, loading: true });
      try {
        const session = await read(() => current === revision);
        if (current === revision) update({ session, loading: false, error: null });
      } catch (cause) {
        if (current === revision) update({ ...state, loading: false, error: cause instanceof Error ? cause.message : "无法读取登录状态。" });
      }
    },
    retire() { revision += 1; },
    invalidate() {
      revision += 1;
      update({ session: { authenticated: false }, loading: false, error: null });
    },
    fail(message: string) { revision += 1; update({ ...state, loading: false, error: message }); },
  };
}
